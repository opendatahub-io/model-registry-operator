package controller

import (
	"context"
	"fmt"
	"maps"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"text/template"
	"time"

	"github.com/banzaicloud/k8s-objectmatcher/patch"
	"github.com/go-logr/logr"
	catalogv1alpha1 "github.com/opendatahub-io/model-registry-operator/api/catalog/v1alpha1"
	"github.com/opendatahub-io/model-registry-operator/api/v1beta1"
	"github.com/opendatahub-io/model-registry-operator/internal/controller/config"
	"github.com/opendatahub-io/model-registry-operator/internal/utils"
	routev1 "github.com/openshift/api/route/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbac "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	klog "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayapiv1beta1 "sigs.k8s.io/gateway-api/apis/v1beta1"
	"sigs.k8s.io/yaml"
)

const catalogFinalizer = "catalog.aihub.opendatahub.io/finalizer"
const catalogSourceLabel = "opendatahub.io/catalog-source"
const sourcesFileName = "sources.yaml"

// catalogResourceName and catalogPostgresComponent are the fixed names given to the
// managed catalog resources. These match the names used by the legacy ModelCatalog
// controller so that the adopt-or-create migration strategy can find and re-parent
// pre-existing resources rather than creating a duplicate set alongside them. The
// Catalog CR itself is always named "catalog" (enforced by a validating webhook), but
// the CR name is intentionally decoupled from the managed resource names.
const catalogResourceName = "model-catalog"
const catalogPostgresComponent = "model-catalog-postgres"

// dnsLabelRegex matches valid Kubernetes DNS label names (RFC 1123).
var dnsLabelRegex = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// maxLabeledSourceNameLen is the longest a labeled source ConfigMap name can be. The
// discovered source is mounted as a volume named "labeled-<configmap-name>", and
// Kubernetes volume names are DNS labels limited to 63 characters.
const maxLabeledSourceNameLen = 63 - len("labeled-")

// LabeledSource represents a user-defined ConfigMap discovered via the catalogSourceLabel label.
type LabeledSource struct {
	Name string
}

// CatalogReconciler reconciles Catalog custom resources.
type CatalogReconciler struct {
	client.Client
	Scheme                *runtime.Scheme
	Recorder              events.EventRecorder
	Log                   logr.Logger
	Template              *template.Template
	Capabilities          ClusterCapabilities
	SkipCatalogDBCreation bool
	GatewayDomain         string
	GatewayName           string
	GatewayNamespace      string
	HTTPRouteNamespace    string

	templateApplier *TemplateApplier
	resourceManager *ResourceManager
}

// CatalogParams is a wrapper for template parameters
type CatalogParams struct {
	Name                    string
	Namespace               string
	Component               string
	PostgresImage           string
	PostgresResources       *corev1.ResourceRequirements
	DatabaseVolumeSizeLimit *resource.Quantity
	CatalogResources        *corev1.ResourceRequirements
	AdminGroups             []string
	LabeledSources          []LabeledSource
	GatewayDomain           string
	GatewayName             string
	GatewayNamespace        string
	HTTPRouteNamespace      string
	PostgresSecretHash      string
}

func (r *CatalogReconciler) createPostgresParams(catalog *catalogv1alpha1.Catalog) *CatalogParams {
	return &CatalogParams{
		Name:                    catalogResourceName,
		Namespace:               catalog.Namespace,
		Component:               catalogPostgresComponent,
		PostgresImage:           config.GetStringConfigWithDefault(config.PostgresImage, config.DefaultPostgresImage),
		PostgresResources:       catalog.Spec.Resources.Postgres,
		DatabaseVolumeSizeLimit: catalog.Spec.Database.Volume.SizeLimit,
	}
}

func (r *CatalogReconciler) buildCatalogParams(catalog *catalogv1alpha1.Catalog, adminGroups []string, labeledSources []LabeledSource) *CatalogParams {
	return &CatalogParams{
		Name:                    catalogResourceName,
		Namespace:               catalog.Namespace,
		Component:               catalogResourceName,
		PostgresResources:       catalog.Spec.Resources.Postgres,
		DatabaseVolumeSizeLimit: catalog.Spec.Database.Volume.SizeLimit,
		CatalogResources:        catalog.Spec.Resources.Catalog,
		AdminGroups:             adminGroups,
		LabeledSources:          labeledSources,
		GatewayDomain:           r.GatewayDomain,
		GatewayName:             r.GatewayName,
		GatewayNamespace:        r.GatewayNamespace,
		HTTPRouteNamespace:      r.HTTPRouteNamespace,
	}
}

// Reconcile manages a Catalog custom resource
func (r *CatalogReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := klog.FromContext(ctx)

	catalog := &catalogv1alpha1.Catalog{}
	if err := r.Get(ctx, req.NamespacedName, catalog); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if catalog.DeletionTimestamp != nil {
		if controllerutil.ContainsFinalizer(catalog, catalogFinalizer) {
			log.Info("Finalizing Catalog", "name", catalog.Name, "namespace", catalog.Namespace)
			if err := r.finalizeCatalog(ctx, catalog); err != nil {
				return ctrl.Result{}, err
			}
			controllerutil.RemoveFinalizer(catalog, catalogFinalizer)
			if err := r.Update(ctx, catalog); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	if !controllerutil.ContainsFinalizer(catalog, catalogFinalizer) {
		controllerutil.AddFinalizer(catalog, catalogFinalizer)
		if err := r.Update(ctx, catalog); err != nil {
			return ctrl.Result{}, err
		}
	}

	res, err := r.ensureCatalogResources(ctx, catalog)
	if err != nil {
		return res, err
	}

	condition, statusErr := r.updateStatus(ctx, catalog)
	if statusErr != nil {
		log.Error(statusErr, "Failed to update catalog status")
		return res, statusErr
	}

	if condition != nil {
		if condition.Reason == ReasonDeploymentCooldown {
			// Requeue after a fixed delay to avoid exponential backoff.
			return ctrl.Result{RequeueAfter: time.Second}, nil
		}
		if condition.Status != metav1.ConditionTrue {
			// Not yet available for another reason (e.g. deployment still rolling
			// out) - keep polling until it settles.
			return ctrl.Result{Requeue: true}, nil
		}
	}

	return res, nil
}

func (r *CatalogReconciler) finalizeCatalog(ctx context.Context, catalog *catalogv1alpha1.Catalog) error {
	catalogParams := r.buildCatalogParams(catalog, nil, nil)

	_, err := r.cleanupKubeRBACProxyConfig(ctx, catalogParams)
	if err != nil {
		return err
	}

	// Render the same template used to create this resource so the delete
	// targets the actual name/namespace the template produces, rather than a
	// name derived from catalogParams (which doesn't match: the HTTPRoute is
	// always named "model-catalog"). Key cleanup off existence rather than the
	// current gateway config, since the config may have changed (or been
	// unset) between creation and deletion.
	//
	// Note: the "allow-gateway-httproutes" ReferenceGrant is intentionally not
	// deleted here. It is shared across all HTTPRoutes in the namespace (both
	// Catalog's and ModelRegistry's), so removing it on Catalog deletion would
	// break other HTTPRoutes still relying on it. Mirrors the same rule for
	// ModelRegistry in deleteGatewayResources (modelregistry_gateway.go).
	if r.HTTPRouteNamespace != "" {
		if _, err := r.deleteFromTemplate(ctx, catalogParams, "catalog-gateway-httproute.yaml.tmpl", &gatewayapiv1.HTTPRoute{}); err != nil {
			return err
		}
	}

	return nil
}

func (r *CatalogReconciler) ensureCatalogResources(ctx context.Context, catalog *catalogv1alpha1.Catalog) (ctrl.Result, error) {
	log := klog.FromContext(ctx)

	log.Info("Reconciling catalog", "name", catalog.Name, "namespace", catalog.Namespace)

	var adminGroups []string
	if r.Capabilities.HasAuthAPI {
		var err error
		adminGroups, err = r.fetchAuthConfig(ctx)
		if err != nil {
			log.Error(err, "Failed to fetch auth config")
			return ctrl.Result{}, err
		}
	}

	labeledSources, err := r.discoverLabeledSources(ctx, catalog.Namespace)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to discover labeled catalog sources: %w", err)
	}

	catalogParams := r.buildCatalogParams(catalog, adminGroups, labeledSources)
	postgresParams := r.createPostgresParams(catalog)

	crOwner := metav1.NewControllerRef(catalog, catalogv1alpha1.GroupVersion.WithKind("Catalog"))

	result := ResourceUnchanged

	if !r.SkipCatalogDBCreation {
		res, pgSecret, err := r.createOrUpdatePostgresSecret(ctx, postgresParams, crOwner)
		if err != nil {
			return ctrl.Result{}, err
		}
		if res != ResourceUnchanged {
			result = res
		}
		if pgSecret != nil {
			hash := computeSecretDataHash(pgSecret.Data)
			catalogParams.PostgresSecretHash = hash
			postgresParams.PostgresSecretHash = hash
		}
	} else {
		log.Info("Skipping catalog DB creation as configured")
	}

	// Create or update ServiceAccount
	result2, err := r.createOrUpdateServiceAccount(ctx, catalogParams, "catalog-serviceaccount.yaml.tmpl", crOwner)
	if err != nil {
		return ctrl.Result{}, err
	}
	if result2 != ResourceUnchanged {
		result = result2
	}

	// Create or update the managed default sources ConfigMap
	result2, err = r.createOrUpdateConfigmap(ctx, catalogParams, "catalog-default-configmap.yaml.tmpl", crOwner)
	if err != nil {
		return ctrl.Result{}, err
	}
	if result2 != ResourceUnchanged {
		result = result2
	}

	// Delete legacy default sources ConfigMap if present
	var oldDefaultCM corev1.ConfigMap
	err = r.Get(ctx, types.NamespacedName{Name: "model-catalog-default-sources", Namespace: catalog.Namespace}, &oldDefaultCM)
	if client.IgnoreNotFound(err) != nil {
		log.Error(err, "failed to get legacy default sources ConfigMap")
	} else if err == nil && oldDefaultCM.Labels["app.kubernetes.io/created-by"] == "model-registry-operator" {
		if delErr := r.Delete(ctx, &oldDefaultCM); client.IgnoreNotFound(delErr) != nil {
			log.Error(delErr, "failed to delete legacy default sources ConfigMap")
		}
	}

	// Create user-managed sources ConfigMaps if they don't exist. noDefaultSource is
	// scoped to this single reconcile: once we've confirmed one of these ConfigMaps
	// needs no default-source stripping, skip re-checking the rest this pass. It must
	// NOT be stored on the reconciler (that would leak across Catalog CRs/namespaces
	// and races under concurrent reconciles).
	noDefaultSource := false
	for _, tmpl := range []string{
		"catalog-configmap.yaml.tmpl",
		"catalog-mcp-configmap.yaml.tmpl",
		"catalog-agent-configmap.yaml.tmpl",
	} {
		var done bool
		result2, done, err = r.manageUserSourcesConfigmap(ctx, catalogParams, tmpl, noDefaultSource)
		if err != nil {
			return ctrl.Result{}, err
		}
		noDefaultSource = done
		if result2 != ResourceUnchanged {
			result = result2
		}
	}

	// Create or update Deployment
	result2, _, err = r.createOrUpdateDeployment(ctx, catalogParams, "catalog-deployment.yaml.tmpl", crOwner)
	if err != nil {
		return ctrl.Result{}, err
	}
	if result2 != ResourceUnchanged {
		result = result2
	}

	// Create or update Service
	result2, err = r.createOrUpdateService(ctx, catalogParams, "catalog-service.yaml.tmpl", crOwner)
	if err != nil {
		return ctrl.Result{}, err
	}
	if result2 != ResourceUnchanged {
		result = result2
	}

	// Create or update role
	result2, err = r.createOrUpdateRole(ctx, catalogParams, "catalog-role.yaml.tmpl", crOwner)
	if err != nil {
		return ctrl.Result{}, err
	}
	if result2 != ResourceUnchanged {
		result = result2
	}

	// Create or update rolebinding
	result2, err = r.createOrUpdateRoleBinding(ctx, catalogParams, "catalog-rolebinding.yaml.tmpl", crOwner)
	if err != nil {
		return ctrl.Result{}, err
	}
	if result2 != ResourceUnchanged {
		result = result2
	}

	// Create or update admin role
	result2, err = r.createOrUpdateAdminRole(ctx, catalogParams, crOwner)
	if err != nil {
		log.Error(err, "Failed to create admin role")
		return ctrl.Result{}, err
	}
	if result2 != ResourceUnchanged {
		result = result2
	}

	// Create or update admin rolebinding
	result2, err = r.createOrUpdateAdminRoleBinding(ctx, catalogParams, crOwner)
	if err != nil {
		log.Error(err, "Failed to create admin rolebinding")
		return ctrl.Result{}, err
	}
	if result2 != ResourceUnchanged {
		result = result2
	}

	// Delete legacy postgres PVC if present
	var oldPVC corev1.PersistentVolumeClaim
	err = r.Get(ctx, types.NamespacedName{Name: catalogPostgresComponent, Namespace: catalog.Namespace}, &oldPVC)
	if client.IgnoreNotFound(err) != nil {
		log.Error(err, "failed to get legacy postgres PVC")
	} else if err == nil && oldPVC.Labels["app.kubernetes.io/created-by"] == "model-registry-operator" {
		if delErr := r.Delete(ctx, &oldPVC); client.IgnoreNotFound(delErr) != nil {
			log.Error(delErr, "failed to delete legacy postgres PVC")
		}
	}

	// Create PostgreSQL resources only if not skipping DB creation
	if !r.SkipCatalogDBCreation {
		result2, _, err := r.createOrUpdateDeployment(ctx, postgresParams, "catalog-postgres-deployment.yaml.tmpl", crOwner)
		if err != nil {
			return ctrl.Result{}, err
		}
		if result2 != ResourceUnchanged {
			result = result2
		}

		result2, err = r.createOrUpdateService(ctx, postgresParams, "catalog-postgres-service.yaml.tmpl", crOwner)
		if err != nil {
			return ctrl.Result{}, err
		}
		if result2 != ResourceUnchanged {
			result = result2
		}

		log.Info("Creating or updating postgres NetworkPolicy")
		result2, err = r.createOrUpdateNetworkPolicy(ctx, postgresParams, "catalog-postgres-network-policy.yaml.tmpl", crOwner)
		if err != nil {
			return ctrl.Result{}, err
		}
		if result2 != ResourceUnchanged {
			result = result2
		}
	} else {
		log.Info("Skipping catalog DB creation as configured")
	}

	if r.Capabilities.IsOpenShift {
		if r.GatewayDomain != "" {
			result2, err = r.ensureCatalogReferenceGrantExists(ctx, catalogParams)
			if err != nil {
				return ctrl.Result{}, err
			}
			if result2 != ResourceUnchanged {
				result = result2
			}

			result2, err = r.createOrUpdateCatalogHTTPRoute(ctx, catalogParams)
			if err != nil {
				return ctrl.Result{}, err
			}
			if result2 != ResourceUnchanged {
				result = result2
			}
		}

		result2, err = r.createOrUpdateRoute(ctx, catalogParams, "catalog-kube-rbac-proxy-https-route.yaml.tmpl", crOwner)
		if err != nil {
			return ctrl.Result{}, err
		}
		if result2 != ResourceUnchanged {
			result = result2
		}

		result2, err = r.createOrUpdateNetworkPolicy(ctx, catalogParams, "catalog-kube-rbac-proxy-network-policy.yaml.tmpl", crOwner)
		if err != nil {
			return ctrl.Result{}, err
		}
		if result2 != ResourceUnchanged {
			result = result2
		}
	}

	result2, err = r.cleanupOAuthConfig(ctx, catalogParams)
	if err != nil {
		return ctrl.Result{}, err
	}
	if result2 != ResourceUnchanged {
		result = result2
	}

	result2, err = r.createOrUpdateKubeRBACProxyConfig(ctx, catalogParams, crOwner)
	if err != nil {
		return ctrl.Result{}, err
	}
	if result2 != ResourceUnchanged {
		result = result2
	}

	if result != ResourceUnchanged {
		return ctrl.Result{Requeue: true}, nil
	}
	return ctrl.Result{}, nil
}

func (r *CatalogReconciler) updateStatus(ctx context.Context, catalog *catalogv1alpha1.Catalog) (*metav1.Condition, error) {
	catalog.Status.ObservedGeneration = catalog.Generation

	depKey := types.NamespacedName{
		Name:      catalogResourceName,
		Namespace: catalog.Namespace,
	}

	cond, err := r.checkDeploymentAvailability(ctx, depKey, catalogResourceName, catalogResourceName)
	if err != nil && !apierrors.IsNotFound(err) {
		return nil, err
	}
	if apierrors.IsNotFound(err) {
		cond = metav1.Condition{
			Type:    ConditionTypeAvailable,
			Status:  metav1.ConditionFalse,
			Reason:  ReasonDeploymentUnavailable,
			Message: "Deployment not found",
		}
	}

	apimeta.SetStatusCondition(&catalog.Status.Conditions, cond)
	return &cond, r.Status().Update(ctx, catalog)
}

func (r *CatalogReconciler) checkDeploymentAvailability(ctx context.Context, key client.ObjectKey, app string, podComponent string) (metav1.Condition, error) {
	deployment := &appsv1.Deployment{}
	if err := r.Get(ctx, key, deployment); err != nil {
		return metav1.Condition{}, err
	}

	condition := metav1.Condition{
		Type:   ConditionTypeAvailable,
		Status: metav1.ConditionFalse,
	}

	available := false
	var availableSince time.Time
	failed := false
	progressing := true
	for _, c := range deployment.Status.Conditions {
		switch c.Type {
		case appsv1.DeploymentAvailable:
			if !failed && progressing {
				available = c.Status == corev1.ConditionTrue
				if !available {
					condition.Message = c.Message
				} else {
					availableSince = c.LastTransitionTime.Time
				}
			}
		case appsv1.DeploymentProgressing:
			if c.Status == corev1.ConditionFalse && !failed {
				available = false
				progressing = false
				condition.Message = c.Message
			}
		case appsv1.DeploymentReplicaFailure:
			if c.Status == corev1.ConditionTrue {
				available = false
				failed = true
				condition.Message = c.Message
			}
		}
	}

	if !available {
		condition.Reason = ReasonDeploymentUnavailable
		condition.Message = fmt.Sprintf("Deployment is unavailable: %s", condition.Message)
	}

	if deployment.Status.UnavailableReplicas != 0 {
		condition = r.checkPodStatus(ctx, app, podComponent, key.Namespace, condition, deployment.Status.UnavailableReplicas)
	}

	if available {
		endpoints := &corev1.Endpoints{} //nolint:staticcheck
		if err := r.Get(ctx, key, endpoints); err != nil {
			condition.Status = metav1.ConditionFalse
			condition.Reason = ReasonDeploymentUnavailable
			condition.Message = fmt.Sprintf("Service endpoints not found: %v", err)
			return condition, nil
		}

		hasReadyEndpoints := false
		for _, subset := range endpoints.Subsets {
			if len(subset.Addresses) > 0 {
				hasReadyEndpoints = true
				break
			}
		}

		if !hasReadyEndpoints {
			condition.Status = metav1.ConditionFalse
			condition.Reason = ReasonDeploymentUnavailable
			condition.Message = "Service endpoints not ready - no ready addresses available"
			return condition, nil
		}

		if time.Since(availableSince) < deploymentDelay {
			condition.Status = metav1.ConditionFalse
			condition.Reason = ReasonDeploymentCooldown
			condition.Message = "Deployment only recently available"
			return condition, nil
		}

		condition.Status = metav1.ConditionTrue
		condition.Reason = ReasonDeploymentAvailable
		condition.Message = "Deployment is available"
	}

	return condition, nil
}

func (r *CatalogReconciler) checkPodStatus(ctx context.Context, app string, component string, namespace string, condition metav1.Condition, unavailableReplicas int32) metav1.Condition {
	condition.Status = metav1.ConditionFalse
	condition.Reason = ReasonDeploymentUnavailable

	var pods corev1.PodList
	if err := r.List(ctx, &pods, client.MatchingLabels{"app": app, "component": component}, client.InNamespace(namespace)); err != nil {
		r.Log.Error(err, "failed to list pods")
	}
	for _, p := range pods.Items {
		for _, s := range p.Status.ContainerStatuses {
			if !s.Ready && s.State.Waiting != nil && s.State.Waiting.Reason != containerCreatingReason {
				condition.Reason = ReasonConfigurationError
				condition.Message = fmt.Sprintf("container %s waiting: %s", s.Name, s.State.Waiting.Message)
				return condition
			}
		}
	}
	if condition.Message == "" {
		condition.Message = fmt.Sprintf("%d unavailable replicas", unavailableReplicas)
	}
	return condition
}

func (r *CatalogReconciler) createOrUpdateDeployment(ctx context.Context, params *CatalogParams, templateName string, owner *metav1.OwnerReference) (OperationResult, *appsv1.Deployment, error) {
	log := klog.FromContext(ctx)
	result := ResourceUnchanged
	var deployment appsv1.Deployment
	if err := r.Apply(params, templateName, &deployment); err != nil {
		return result, nil, err
	}

	r.applyLabels(&deployment.ObjectMeta, params)
	r.applyOwnerReference(&deployment.ObjectMeta, owner)

	result, err := r.createOrUpdate(ctx, &appsv1.Deployment{}, &deployment)
	if err != nil {
		if apierrors.IsForbidden(err) || (apierrors.IsInvalid(err) && strings.Contains(err.Error(), "field is immutable")) {
			log.Info("deleting deployment due to immutable field conflicts", "name", deployment.Name, "error", err.Error())

			var existingDeployment appsv1.Deployment
			key := client.ObjectKeyFromObject(&deployment)
			if getErr := r.Get(ctx, key, &existingDeployment); getErr != nil {
				return result, nil, getErr
			}

			if deleteErr := r.Delete(ctx, &existingDeployment); deleteErr != nil {
				return result, nil, deleteErr
			}

			// Don't recreate immediately: the cached client used by createOrUpdate
			// may not have observed the delete yet (informer watches lag writes),
			// which would make the immediate recreate attempt an Update against an
			// already-deleted object and fail with a spurious NotFound. Report the
			// change and let the next reconcile recreate the deployment once the
			// cache has caught up.
			return ResourceUpdated, nil, nil
		} else {
			return result, nil, fmt.Errorf("failed to create/update deployment %s: %w", deployment.Name, err)
		}
	}

	return result, &deployment, nil
}

func (r *CatalogReconciler) createOrUpdateService(ctx context.Context, params *CatalogParams, templateName string, owner *metav1.OwnerReference) (OperationResult, error) {
	var service corev1.Service
	if err := r.Apply(params, templateName, &service); err != nil {
		return ResourceUnchanged, err
	}

	r.applyLabels(&service.ObjectMeta, params)
	r.applyOwnerReference(&service.ObjectMeta, owner)

	return r.createOrUpdate(ctx, &corev1.Service{}, &service)
}

func (r *CatalogReconciler) ensureCatalogReferenceGrantExists(ctx context.Context, params *CatalogParams) (OperationResult, error) {
	var refGrant gatewayapiv1beta1.ReferenceGrant
	if err := r.Apply(params, "gateway-reference-grant.yaml.tmpl", &refGrant); err != nil {
		return ResourceUnchanged, err
	}
	return r.createIfNotExists(ctx, &gatewayapiv1beta1.ReferenceGrant{}, &refGrant)
}

func (r *CatalogReconciler) createOrUpdateCatalogHTTPRoute(ctx context.Context, params *CatalogParams) (OperationResult, error) {
	var httpRoute gatewayapiv1.HTTPRoute
	if err := r.Apply(params, "catalog-gateway-httproute.yaml.tmpl", &httpRoute); err != nil {
		return ResourceUnchanged, err
	}
	return r.createOrUpdate(ctx, &gatewayapiv1.HTTPRoute{}, &httpRoute)
}

func (r *CatalogReconciler) createOrUpdateRoute(ctx context.Context, params *CatalogParams, templateName string, owner *metav1.OwnerReference) (OperationResult, error) {
	var route routev1.Route
	if err := r.Apply(params, templateName, &route); err != nil {
		return ResourceUnchanged, err
	}

	r.applyLabels(&route.ObjectMeta, params)
	r.applyOwnerReference(&route.ObjectMeta, owner)

	return r.createOrUpdate(ctx, &routev1.Route{}, &route)
}

func (r *CatalogReconciler) createOrUpdateNetworkPolicy(ctx context.Context, params *CatalogParams, templateName string, owner *metav1.OwnerReference) (OperationResult, error) {
	var netPol networkingv1.NetworkPolicy
	if err := r.Apply(params, templateName, &netPol); err != nil {
		return ResourceUnchanged, err
	}

	r.applyLabels(&netPol.ObjectMeta, params)
	r.applyOwnerReference(&netPol.ObjectMeta, owner)

	return r.createOrUpdate(ctx, &networkingv1.NetworkPolicy{}, &netPol)
}

func (r *CatalogReconciler) createOrUpdateConfigmap(ctx context.Context, params *CatalogParams, templateName string, owner *metav1.OwnerReference) (OperationResult, error) {
	var cm corev1.ConfigMap
	if err := r.Apply(params, templateName, &cm); err != nil {
		return ResourceUnchanged, err
	}

	r.applyLabels(&cm.ObjectMeta, params)
	r.applyOwnerReference(&cm.ObjectMeta, owner)

	return r.createOrUpdate(ctx, &corev1.ConfigMap{}, &cm)
}

// manageUserSourcesConfigmap creates the given user sources ConfigMap if it doesn't
// exist, and otherwise strips the default catalog source from it if present.
// noDefaultSource lets the caller skip re-parsing once it's confirmed unnecessary for
// this reconcile; the returned bool reports whether that's now the case. On a parse
// error the returned bool is unchanged (not latched), so the next reconcile retries.
func (r *CatalogReconciler) manageUserSourcesConfigmap(ctx context.Context, params *CatalogParams, templateName string, noDefaultSource bool) (OperationResult, bool, error) {
	log := klog.FromContext(ctx)

	result := ResourceUnchanged
	var cm corev1.ConfigMap
	if err := r.Apply(params, templateName, &cm); err != nil {
		return result, noDefaultSource, err
	}

	r.applyLabels(&cm.ObjectMeta, params)

	var existing corev1.ConfigMap
	result, err := r.createIfNotExists(ctx, &existing, &cm)
	if err != nil {
		return result, noDefaultSource, err
	}

	if result == ResourceCreated {
		return result, noDefaultSource, nil
	}

	if noDefaultSource {
		return result, noDefaultSource, nil
	}

	if existing.Data == nil {
		return result, noDefaultSource, nil
	}

	stripped, err := r.removeDefaultSource(existing.Data[sourcesFileName])
	if err != nil {
		log.Error(err, "Unable to process sources configmap - user configmap may contain invalid catalog structure",
			"name", cm.Name, "namespace", cm.Namespace, "file", sourcesFileName)
		return result, noDefaultSource, nil
	}
	existing.Data[sourcesFileName] = stripped

	if existing.Data[sourcesFileName] == "" {
		return result, true, nil
	}

	patchResult, err := patch.DefaultAnnotator.GetOriginalConfiguration(&existing)
	if err != nil {
		return result, noDefaultSource, err
	}
	if patchResult == nil {
		if err := patch.DefaultAnnotator.SetLastAppliedAnnotation(&existing); err != nil {
			return result, noDefaultSource, err
		}
	}

	result, err = r.createOrUpdate(ctx, &corev1.ConfigMap{}, &existing)
	return result, true, err
}

func (r *CatalogReconciler) removeDefaultSource(doc string) (string, error) {
	type catalog struct {
		Name       string            `json:"name"`
		ID         string            `json:"id"`
		Type       string            `json:"type"`
		Enabled    *bool             `json:"enabled,omitempty"`
		Properties map[string]string `json:"properties,omitempty"`
		Labels     []string          `json:"labels,omitempty"`
	}
	var sources struct {
		Catalogs      []catalog `json:"catalogs,omitempty"`
		ModelCatalogs []catalog `json:"model_catalogs,omitempty"`
		McpCatalogs   []catalog `json:"mcp_catalogs,omitempty"`
		AgentCatalogs []catalog `json:"agent_catalogs,omitempty"`
		Labels        any       `json:"labels,omitempty"`
		NamedQueries  any       `json:"namedQueries,omitempty"`
	}

	err := yaml.UnmarshalStrict([]byte(doc), &sources)
	if err != nil {
		return "", err
	}

	originalCatalogsLen := len(sources.Catalogs)
	originalModelCatalogsLen := len(sources.ModelCatalogs)

	sources.Catalogs = slices.DeleteFunc(sources.Catalogs, func(c catalog) bool {
		return c.ID == "default_catalog"
	})
	sources.ModelCatalogs = slices.DeleteFunc(sources.ModelCatalogs, func(c catalog) bool {
		return c.ID == "default_catalog"
	})

	if len(sources.Catalogs) == originalCatalogsLen && len(sources.ModelCatalogs) == originalModelCatalogsLen {
		return "", nil
	}

	buf, err := yaml.Marshal(sources)
	if err != nil {
		return "", err
	}

	return string(buf), nil
}

func (r *CatalogReconciler) createOrUpdateServiceAccount(ctx context.Context, params *CatalogParams, templateName string, owner *metav1.OwnerReference) (result OperationResult, err error) {
	var sa corev1.ServiceAccount
	if err := r.Apply(params, templateName, &sa); err != nil {
		return ResourceUnchanged, err
	}

	r.applyLabels(&sa.ObjectMeta, params)
	r.applyOwnerReference(&sa.ObjectMeta, owner)

	return r.createOrUpdate(ctx, &corev1.ServiceAccount{}, &sa)
}

func (r *CatalogReconciler) createOrUpdateClusterRoleBinding(ctx context.Context, params *CatalogParams, templateName string) (result OperationResult, err error) {
	var crb rbac.ClusterRoleBinding
	if err := r.Apply(params, templateName, &crb); err != nil {
		return ResourceUnchanged, err
	}

	r.applyLabels(&crb.ObjectMeta, params)

	if crb.Labels == nil {
		crb.Labels = make(map[string]string)
	}
	crb.Labels["modelregistry.opendatahub.io/namespace"] = params.Namespace

	return r.createOrUpdate(ctx, &rbac.ClusterRoleBinding{}, &crb)
}

func (r *CatalogReconciler) createOrUpdateRole(ctx context.Context, params *CatalogParams, templateName string, owner *metav1.OwnerReference) (result OperationResult, err error) {
	var role rbac.Role
	if err := r.Apply(params, templateName, &role); err != nil {
		return ResourceUnchanged, err
	}

	r.applyLabels(&role.ObjectMeta, params)
	r.applyOwnerReference(&role.ObjectMeta, owner)

	return r.createOrUpdate(ctx, &rbac.Role{}, &role)
}

func (r *CatalogReconciler) createOrUpdateRoleBinding(ctx context.Context, params *CatalogParams, templateName string, owner *metav1.OwnerReference) (result OperationResult, err error) {
	var rb rbac.RoleBinding
	if err := r.Apply(params, templateName, &rb); err != nil {
		return ResourceUnchanged, err
	}

	r.applyLabels(&rb.ObjectMeta, params)
	r.applyOwnerReference(&rb.ObjectMeta, owner)

	return r.createOrUpdate(ctx, &rbac.RoleBinding{}, &rb)
}

func (r *CatalogReconciler) createOrUpdatePostgresSecret(ctx context.Context, params *CatalogParams, owner *metav1.OwnerReference) (OperationResult, *corev1.Secret, error) {
	log := klog.FromContext(ctx)
	result := ResourceUnchanged

	secretName := params.Name + "-postgres"

	existingSecret := &corev1.Secret{}
	err := r.Get(ctx, types.NamespacedName{
		Name:      secretName,
		Namespace: params.Namespace,
	}, existingSecret)

	if err == nil {
		log.V(1).Info("Postgres secret already exists, reconciling", "secret", secretName)

		needsUpdate := false

		if existingSecret.Data == nil {
			existingSecret.Data = make(map[string][]byte)
		}

		requiredKeys := map[string]string{
			"database-name": config.GetStringConfigWithDefault(config.CatalogPostgresDatabase, config.DefaultCatalogPostgresDatabase),
			"database-user": config.GetStringConfigWithDefault(config.CatalogPostgresUser, config.DefaultCatalogPostgresUser),
		}

		for key, defaultValue := range requiredKeys {
			if len(existingSecret.Data[key]) == 0 {
				log.Info("Adding missing key to existing secret", "secret", secretName, "key", key)
				existingSecret.Data[key] = []byte(defaultValue)
				needsUpdate = true
			}
		}

		if len(existingSecret.Data["database-password"]) == 0 {
			log.Info("Generating missing password for existing secret", "secret", secretName)
			password, err := utils.RandBytes(16)
			if err != nil {
				log.Error(err, "Failed to generate random password for secret", "secret", secretName)
				return result, nil, fmt.Errorf("failed to generate random password: %w", err)
			}
			existingSecret.Data["database-password"] = []byte(password)
			needsUpdate = true
		}

		if len(existingSecret.Data["database-salt"]) == 0 {
			log.Info("Generating missing salt for existing secret", "secret", secretName)
			salt, err := utils.RandBytes(16)
			if err != nil {
				log.Error(err, "Failed to generate random salt for secret", "secret", secretName)
				return result, nil, fmt.Errorf("failed to generate random salt: %w", err)
			}
			existingSecret.Data["database-salt"] = []byte(salt)
			needsUpdate = true
		}

		originalLabels := make(map[string]string, len(existingSecret.Labels))
		maps.Copy(originalLabels, existingSecret.Labels)
		r.applyLabels(&existingSecret.ObjectMeta, params)

		if !reflect.DeepEqual(originalLabels, existingSecret.Labels) {
			log.V(1).Info("Updating labels on existing secret", "secret", secretName)
			needsUpdate = true
		}

		hasOwnerRef := false
		for _, ref := range existingSecret.OwnerReferences {
			if ref.UID == owner.UID {
				hasOwnerRef = true
				break
			}
		}
		if !hasOwnerRef {
			log.V(1).Info("Adding owner reference to existing secret", "secret", secretName)
			r.applyOwnerReference(&existingSecret.ObjectMeta, owner)
			needsUpdate = true
		}

		if needsUpdate {
			if err := r.Update(ctx, existingSecret); err != nil {
				log.Error(err, "Failed to update existing secret", "secret", secretName)
				return result, nil, err
			}
			log.Info("Successfully reconciled existing secret", "secret", secretName)
			return ResourceUpdated, existingSecret, nil
		}

		return ResourceUnchanged, existingSecret, nil
	}

	if !apierrors.IsNotFound(err) {
		return result, nil, err
	}

	log.Info("Creating postgres secret with random password", "secret", secretName)

	password, err := utils.RandBytes(16)
	if err != nil {
		log.Error(err, "Failed to generate random password for new secret", "secret", secretName)
		return result, nil, fmt.Errorf("failed to generate random password: %w", err)
	}

	salt, err := utils.RandBytes(16)
	if err != nil {
		log.Error(err, "Failed to generate random salt for new secret", "secret", secretName)
		return result, nil, fmt.Errorf("failed to generate random salt: %w", err)
	}

	newSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: params.Namespace,
		},
		Data: map[string][]byte{
			"database-name":     []byte(config.GetStringConfigWithDefault(config.CatalogPostgresDatabase, config.DefaultCatalogPostgresDatabase)),
			"database-user":     []byte(config.GetStringConfigWithDefault(config.CatalogPostgresUser, config.DefaultCatalogPostgresUser)),
			"database-password": []byte(password),
			"database-salt":     []byte(salt),
		},
	}

	r.applyLabels(&newSecret.ObjectMeta, params)
	r.applyOwnerReference(&newSecret.ObjectMeta, owner)

	res, err := r.createOrUpdate(ctx, &corev1.Secret{}, newSecret)
	if err != nil {
		return res, nil, err
	}
	return res, newSecret, nil
}

// fetchAuthConfig retrieves admin groups from the cluster-scoped Auth CR.
// Returns a nil slice if the Auth CR is not found.
func (r *CatalogReconciler) fetchAuthConfig(ctx context.Context) ([]string, error) {
	authConfig := &unstructured.Unstructured{}
	authConfig.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "services.platform.opendatahub.io",
		Version: "v1alpha1",
		Kind:    "Auth",
	})

	err := r.Get(ctx, client.ObjectKey{
		Name: "auth",
		// Auth is cluster-scoped, so no namespace
	}, authConfig)
	if err != nil {
		if apierrors.IsNotFound(err) || apimeta.IsNoMatchError(err) {
			r.Log.Info("Auth CR not found, no admin groups configured")
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get Auth CR: %w", err)
	}

	adminGroups, found, err := unstructured.NestedStringSlice(authConfig.Object, "spec", "adminGroups")
	if err != nil {
		r.Log.Error(err, "Auth CR spec.adminGroups has an unexpected type, treating as empty")
		return nil, nil
	}
	if !found {
		r.Log.Info("No adminGroups found in auth CR spec")
		return nil, nil
	}

	r.Log.Info("Found admin groups from auth CR", "groups", adminGroups)
	return adminGroups, nil
}

func (r *CatalogReconciler) createOrUpdateAdminRole(ctx context.Context, params *CatalogParams, owner *metav1.OwnerReference) (OperationResult, error) {
	var role rbac.Role
	if err := r.Apply(params, "catalog-admin-role.yaml.tmpl", &role); err != nil {
		return ResourceUnchanged, err
	}

	r.applyLabels(&role.ObjectMeta, params)
	r.applyOwnerReference(&role.ObjectMeta, owner)

	return r.createOrUpdate(ctx, &rbac.Role{}, &role)
}

func (r *CatalogReconciler) createOrUpdateAdminRoleBinding(ctx context.Context, params *CatalogParams, owner *metav1.OwnerReference) (OperationResult, error) {
	if len(params.AdminGroups) == 0 {
		roleBindingName := fmt.Sprintf("%s-admin-binding", params.Name)
		key := types.NamespacedName{
			Name:      roleBindingName,
			Namespace: params.Namespace,
		}

		var existingRoleBinding rbac.RoleBinding
		err := r.Get(ctx, key, &existingRoleBinding)
		if err == nil {
			if deleteErr := r.Delete(ctx, &existingRoleBinding); deleteErr != nil {
				return ResourceUnchanged, fmt.Errorf("failed to delete admin RoleBinding: %w", deleteErr)
			}
			return ResourceUpdated, nil
		} else if !apierrors.IsNotFound(err) {
			return ResourceUnchanged, fmt.Errorf("failed to get admin RoleBinding: %w", err)
		}

		return ResourceUnchanged, nil
	}

	var rb rbac.RoleBinding
	if err := r.Apply(params, "catalog-admin-rolebinding.yaml.tmpl", &rb); err != nil {
		return ResourceUnchanged, err
	}

	r.applyLabels(&rb.ObjectMeta, params)
	r.applyOwnerReference(&rb.ObjectMeta, owner)

	return r.createOrUpdate(ctx, &rbac.RoleBinding{}, &rb)
}

func (r *CatalogReconciler) cleanupOAuthConfig(ctx context.Context, params *CatalogParams) (result OperationResult, err error) {
	result = ResourceUnchanged

	result2, err := r.deleteFromTemplate(ctx, params, "catalog-oauth-serviceaccount.yaml.tmpl", &corev1.ServiceAccount{})
	if err != nil {
		return result2, err
	}
	if result2 != ResourceUnchanged {
		result = result2
	}

	result2, err = r.deleteFromTemplate(ctx, params, "catalog-oauth-configmap.yaml.tmpl", &corev1.ConfigMap{})
	if err != nil {
		return result2, err
	}
	if result2 != ResourceUnchanged {
		result = result2
	}

	result2, err = r.deleteFromTemplate(ctx, params, "catalog-oauth-secret.yaml.tmpl", &corev1.Secret{})
	if err != nil {
		return result2, err
	}
	if result2 != ResourceUnchanged {
		result = result2
	}

	return result, nil
}

func (r *CatalogReconciler) createOrUpdateKubeRBACProxyConfig(ctx context.Context, params *CatalogParams, owner *metav1.OwnerReference) (result OperationResult, err error) {
	result = ResourceUnchanged

	result2, err := r.createOrUpdateConfigmap(ctx, params, "catalog-kube-rbac-proxy-config.yaml.tmpl", owner)
	if err != nil {
		return result2, err
	}
	if result2 != ResourceUnchanged {
		result = result2
	}

	result2, err = r.createOrUpdateClusterRoleBinding(ctx, params, "catalog-kube-rbac-proxy-role-binding.yaml.tmpl")
	if err != nil {
		return result2, err
	}
	if result2 != ResourceUnchanged {
		result = result2
	}

	return result, nil
}

func (r *CatalogReconciler) cleanupKubeRBACProxyConfig(ctx context.Context, params *CatalogParams) (result OperationResult, err error) {
	result = ResourceUnchanged

	result2, err := r.deleteFromTemplate(ctx, params, "catalog-kube-rbac-proxy-config.yaml.tmpl", &corev1.ConfigMap{})
	if err != nil {
		return result2, err
	}
	if result2 != ResourceUnchanged {
		result = result2
	}

	result2, err = r.deleteFromTemplate(ctx, params, "catalog-kube-rbac-proxy-role-binding.yaml.tmpl", &rbac.ClusterRoleBinding{})
	if err != nil {
		return result2, err
	}
	if result2 != ResourceUnchanged {
		result = result2
	}

	return result, nil
}

// Apply executes given template name with params
func (r *CatalogReconciler) Apply(params *CatalogParams, templateName string, object any) error {
	if r.templateApplier == nil {
		r.templateApplier = &TemplateApplier{
			Template:    r.Template,
			IsOpenShift: r.Capabilities.IsOpenShift,
		}
	}

	var restPort int32 = 8080
	var oauthPort int32 = 8443
	var routePort int32 = 443

	var catalogRestResources *corev1.ResourceRequirements
	if params.CatalogResources != nil {
		catalogRestResources = params.CatalogResources
	} else {
		catalogRestResources = &config.CatalogServiceResourceRequirements
	}

	defaultSpec := &v1beta1.ModelRegistrySpec{
		Rest: v1beta1.RestSpec{
			Port:      &restPort,
			Image:     config.GetStringConfigWithDefault(config.RestImage, config.DefaultRestImage),
			Resources: catalogRestResources,
		},
		KubeRBACProxy: &v1beta1.KubeRBACProxyConfig{
			Port:      &oauthPort,
			RoutePort: &routePort,
			Image:     config.GetStringConfigWithDefault(config.KubeRBACProxyImage, config.DefaultKubeRBACProxyImage),
			Domain:    config.GetDefaultDomain(),
		},
	}

	catalogParams := struct {
		Name                    string
		Namespace               string
		Spec                    *v1beta1.ModelRegistrySpec
		CatalogDataImage        string
		BenchmarkDataImage      string
		PostgresImage           string
		PostgresUser            string
		PostgresDatabase        string
		PostgresResources       *corev1.ResourceRequirements
		DatabaseVolumeSizeLimit *resource.Quantity
		AdminGroups             []string
		LabeledSources          []LabeledSource
		GatewayDomain           string
		GatewayName             string
		GatewayNamespace        string
		HTTPRouteNamespace      string
		PostgresSecretHash      string
	}{
		Name:                    params.Name,
		Namespace:               params.Namespace,
		Spec:                    defaultSpec,
		CatalogDataImage:        config.GetStringConfigWithDefault(config.CatalogDataImage, config.DefaultCatalogDataImage),
		BenchmarkDataImage:      config.GetStringConfigWithDefault(config.BenchmarkDataImage, config.DefaultBenchmarkDataImage),
		PostgresImage:           config.GetStringConfigWithDefault(config.PostgresImage, config.DefaultPostgresImage),
		PostgresUser:            config.GetStringConfigWithDefault(config.CatalogPostgresUser, config.DefaultCatalogPostgresUser),
		PostgresDatabase:        config.GetStringConfigWithDefault(config.CatalogPostgresDatabase, config.DefaultCatalogPostgresDatabase),
		PostgresResources:       params.PostgresResources,
		DatabaseVolumeSizeLimit: params.DatabaseVolumeSizeLimit,
		AdminGroups:             params.AdminGroups,
		LabeledSources:          params.LabeledSources,
		GatewayDomain:           params.GatewayDomain,
		GatewayName:             params.GatewayName,
		GatewayNamespace:        params.GatewayNamespace,
		HTTPRouteNamespace:      params.HTTPRouteNamespace,
		PostgresSecretHash:      params.PostgresSecretHash,
	}

	return r.templateApplier.Apply(catalogParams, templateName, object)
}

func (r *CatalogReconciler) createOrUpdate(ctx context.Context, currObj client.Object, newObj client.Object) (OperationResult, error) {
	if r.resourceManager == nil {
		r.resourceManager = &ResourceManager{Client: r.Client}
	}
	return r.resourceManager.CreateOrUpdate(ctx, currObj, newObj)
}

func (r *CatalogReconciler) createIfNotExists(ctx context.Context, currObj client.Object, newObj client.Object) (OperationResult, error) {
	if r.resourceManager == nil {
		r.resourceManager = &ResourceManager{Client: r.Client}
	}
	return r.resourceManager.CreateIfNotExists(ctx, currObj, newObj)
}

func (r *CatalogReconciler) deleteFromTemplate(ctx context.Context, params *CatalogParams, templateName string, obj client.Object) (OperationResult, error) {
	if r.Template != nil && r.Template.Lookup(templateName) == nil {
		return ResourceUnchanged, nil
	}
	if err := r.Apply(params, templateName, obj); err != nil {
		return ResourceUnchanged, err
	}
	if err := r.Delete(ctx, obj); err != nil {
		if apierrors.IsNotFound(err) || apimeta.IsNoMatchError(err) {
			return ResourceUnchanged, nil
		}
		return ResourceUnchanged, err
	}
	return ResourceUpdated, nil
}

func (*CatalogReconciler) applyLabels(meta *metav1.ObjectMeta, params *CatalogParams) {
	if meta.Labels == nil {
		meta.Labels = make(map[string]string)
	}

	meta.Labels["app"] = params.Name
	meta.Labels["component"] = params.Component
	meta.Labels["app.kubernetes.io/name"] = params.Name
	meta.Labels["app.kubernetes.io/instance"] = params.Name
	meta.Labels["app.kubernetes.io/component"] = params.Component
	meta.Labels["app.kubernetes.io/created-by"] = "model-registry-operator"
	meta.Labels["app.kubernetes.io/part-of"] = "model-catalog"
	meta.Labels["app.kubernetes.io/managed-by"] = "model-registry-operator"
}

func (*CatalogReconciler) applyOwnerReference(meta *metav1.ObjectMeta, owner *metav1.OwnerReference) {
	if owner != nil {
		meta.OwnerReferences = []metav1.OwnerReference{*owner}
	}
}

func (r *CatalogReconciler) discoverLabeledSources(ctx context.Context, namespace string) ([]LabeledSource, error) {
	log := klog.FromContext(ctx)
	var cmList corev1.ConfigMapList
	if err := r.List(ctx, &cmList,
		client.InNamespace(namespace),
		client.MatchingLabels{catalogSourceLabel: "true"},
	); err != nil {
		return nil, fmt.Errorf("failed to list configmaps with label %s: %w", catalogSourceLabel, err)
	}

	var sources []LabeledSource
	for _, cm := range cmList.Items {
		if cm.Data == nil {
			log.V(5).Info("Skipping labeled configmap without data", "name", cm.Name)
			continue
		}
		if _, ok := cm.Data[sourcesFileName]; !ok {
			log.V(5).Info("Skipping labeled configmap missing sources.yaml", "name", cm.Name)
			continue
		}

		if !dnsLabelRegex.MatchString(cm.Name) {
			log.Error(nil, "Skipping labeled configmap with invalid volume name (must be a valid DNS label: lowercase alphanumeric and hyphens only, no dots)",
				"name", cm.Name)
			continue
		}

		// The volume name is "labeled-<configmap-name>", so the configmap name must
		// be at most 63-len("labeled-") chars to stay within the Kubernetes 63-char
		// DNS label limit for volume names.
		if len(cm.Name) > maxLabeledSourceNameLen {
			log.Error(nil, "Skipping labeled configmap with name too long for a volume name (labeled-<name> must be at most 63 characters)",
				"name", cm.Name)
			continue
		}

		sources = append(sources, LabeledSource{Name: cm.Name})
	}

	slices.SortFunc(sources, func(a, b LabeledSource) int {
		return strings.Compare(a.Name, b.Name)
	})

	return sources, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *CatalogReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.templateApplier = &TemplateApplier{
		Template:    r.Template,
		IsOpenShift: r.Capabilities.IsOpenShift,
	}
	r.resourceManager = &ResourceManager{
		Client: r.Client,
	}

	b := ctrl.NewControllerManagedBy(mgr).
		Named("catalog").
		For(&catalogv1alpha1.Catalog{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.ServiceAccount{}).
		Owns(&corev1.ConfigMap{}).
		Owns(&corev1.Secret{}).
		Owns(&networkingv1.NetworkPolicy{}).
		Owns(&rbac.Role{}).
		Owns(&rbac.RoleBinding{})

	if r.Capabilities.IsOpenShift {
		b = b.Owns(&routev1.Route{})
	}

	catalogSourceLabels, err := predicate.LabelSelectorPredicate(metav1.LabelSelector{
		MatchLabels: map[string]string{catalogSourceLabel: "true"},
	})
	if err != nil {
		return err
	}

	labelsPredicate, err := predicate.LabelSelectorPredicate(metav1.LabelSelector{
		MatchExpressions: []metav1.LabelSelectorRequirement{
			{
				Key:      "app.kubernetes.io/created-by",
				Operator: metav1.LabelSelectorOpIn,
				Values:   []string{"model-registry-operator"},
			},
		},
	})
	if err != nil {
		return err
	}

	b = b.Watches(
		&corev1.ConfigMap{},
		handler.EnqueueRequestsFromMapFunc(r.getCatalogsForConfigMap),
		builder.WithPredicates(predicate.Or(catalogSourceLabels, labelsPredicate)),
	)

	b = b.Watches(
		&rbac.ClusterRoleBinding{},
		handler.EnqueueRequestsFromMapFunc(r.GetCatalogForClusterRoleBinding),
		builder.WithPredicates(labelsPredicate),
	)

	if r.Capabilities.HasAuthAPI {
		authGVK := schema.GroupVersionKind{
			Group:   "services.platform.opendatahub.io",
			Version: "v1alpha1",
			Kind:    "Auth",
		}
		authObj := &unstructured.Unstructured{}
		authObj.SetGroupVersionKind(authGVK)
		b = b.Watches(
			authObj,
			handler.EnqueueRequestsFromMapFunc(r.getCatalogsForAuth),
		)
	}

	return b.Complete(r)
}

func (r *CatalogReconciler) getCatalogsForConfigMap(ctx context.Context, object client.Object) []reconcile.Request {
	var list catalogv1alpha1.CatalogList
	if err := r.List(ctx, &list, client.InNamespace(object.GetNamespace())); err != nil {
		return nil
	}
	reqs := make([]reconcile.Request, len(list.Items))
	for i, item := range list.Items {
		reqs[i] = reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      item.Name,
				Namespace: item.Namespace,
			},
		}
	}
	return reqs
}

func (r *CatalogReconciler) GetCatalogForClusterRoleBinding(ctx context.Context, object client.Object) []reconcile.Request {
	clusterRoleBinding, ok := object.(*rbac.ClusterRoleBinding)
	if !ok {
		return nil
	}
	// The ClusterRoleBinding is cluster-scoped and named after the fixed legacy
	// resource name, not the Catalog CR name, so it can't be mapped back to a
	// Catalog CR by name. Use the namespace label to find the owning Catalog CR(s)
	// instead, mirroring getCatalogsForConfigMap.
	labels := clusterRoleBinding.GetObjectMeta().GetLabels()
	namespace := labels["modelregistry.opendatahub.io/namespace"]
	if len(namespace) == 0 {
		return nil
	}
	var list catalogv1alpha1.CatalogList
	if err := r.List(ctx, &list, client.InNamespace(namespace)); err != nil {
		return nil
	}
	reqs := make([]reconcile.Request, len(list.Items))
	for i, item := range list.Items {
		reqs[i] = reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      item.Name,
				Namespace: item.Namespace,
			},
		}
	}
	return reqs
}

func (r *CatalogReconciler) getCatalogsForAuth(ctx context.Context, object client.Object) []reconcile.Request {
	// The Auth CR is cluster-scoped, so it can't be tied to a single Catalog CR's
	// namespace. Enqueue every Catalog CR in the cluster.
	var list catalogv1alpha1.CatalogList
	if err := r.List(ctx, &list); err != nil {
		return nil
	}
	reqs := make([]reconcile.Request, len(list.Items))
	for i, item := range list.Items {
		reqs[i] = reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      item.Name,
				Namespace: item.Namespace,
			},
		}
	}
	return reqs
}
