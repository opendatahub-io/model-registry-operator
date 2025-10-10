package controller

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"text/template"

	"github.com/banzaicloud/k8s-objectmatcher/patch"
	"github.com/go-logr/logr"
	"github.com/opendatahub-io/model-registry-operator/api/v1beta1"
	"github.com/opendatahub-io/model-registry-operator/internal/controller/config"
	routev1 "github.com/openshift/api/route/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbac "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	klog "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
	"sigs.k8s.io/yaml"
)

const modelCatalogName = "model-catalog"
const modelCatalogPostgresName = "model-catalog-postgres"

// ModelCatalogReconciler reconciles a single model catalog instance
type ModelCatalogReconciler struct {
	client.Client
	Scheme                *runtime.Scheme
	Recorder              record.EventRecorder
	Log                   logr.Logger
	Template              *template.Template
	IsOpenShift           bool
	TargetNamespace       string
	Enabled               bool
	SkipCatalogDBCreation bool

	// noDefaultSource is set after checking for the default source in the
	// user-managed sources configmap. When true, the default source is
	// assumed to not be present and no further attempts are made to remove
	// it.
	noDefaultSource bool

	// embedded utilities for shared functionality
	templateApplier *TemplateApplier
	resourceManager *ResourceManager
}

// ModelCatalogParams is a wrapper for template parameters
type ModelCatalogParams struct {
	Name      string
	Namespace string
	Component string
}

// Reconcile manages a single model catalog instance
func (r *ModelCatalogReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	// Note: We ignore req.Name and req.Namespace since all watched objects
	// are mapped to the same fixed reconcile request for deduplication.
	// This prevents reconcile storms from multiple objects trying to update
	// the same shared resources.

	// If disabled, just clean up any old resources.
	if !r.Enabled {
		return r.cleanupCatalogResources(ctx)
	}

	return r.ensureCatalogResources(ctx)
}

func (r *ModelCatalogReconciler) ensureCatalogResources(ctx context.Context) (ctrl.Result, error) {
	log := klog.FromContext(ctx)

	log.Info("Reconciling catalog")

	catalogParams := &ModelCatalogParams{
		Name:      modelCatalogName,
		Namespace: r.TargetNamespace,
		Component: modelCatalogName,
	}

	// Fetch the info from the platform's default-modelregistry CR to use an owner.
	crOwner, err := r.fetchDefaultModelRegistry(ctx)
	if client.IgnoreNotFound(err) != nil {
		log.Error(err, "unable to retrieve platform model registry CRD")
	}

	// Create or update ServiceAccount
	result, err := r.createOrUpdateServiceAccount(ctx, catalogParams, "catalog-serviceaccount.yaml.tmpl", crOwner)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Create or update the managed default sources ConfigMap
	result2, err := r.createOrUpdateConfigmap(ctx, catalogParams, "catalog-default-configmap.yaml.tmpl", crOwner)
	if err != nil {
		return ctrl.Result{}, err
	}
	if result2 != ResourceUnchanged {
		result = result2
	}

	// Create the user-managed sources ConfigMap if it doesn't exist
	result2, err = r.manageUserSourcesConfigmap(ctx, catalogParams, "catalog-configmap.yaml.tmpl")
	if err != nil {
		return ctrl.Result{}, err
	}
	if result2 != ResourceUnchanged {
		result = result2
	}

	// Create or update Deployment
	result2, deployment, err := r.createOrUpdateDeployment(ctx, catalogParams, "catalog-deployment.yaml.tmpl", crOwner)
	if err != nil {
		return ctrl.Result{}, err
	}
	if result2 != ResourceUnchanged {
		result = result2
	}
	if deployment == nil {
		return ctrl.Result{Requeue: true}, nil
	}

	// Use deployment as owner for the remaining resources
	deploymentOwner := &metav1.OwnerReference{
		APIVersion: deployment.APIVersion,
		Kind:       deployment.Kind,
		Name:       deployment.Name,
		UID:        deployment.UID,
	}

	// Create or update Service
	result2, err = r.createOrUpdateService(ctx, catalogParams, "catalog-service.yaml.tmpl", deploymentOwner)
	if err != nil {
		return ctrl.Result{}, err
	}
	if result2 != ResourceUnchanged {
		result = result2
	}

	// Create or update role
	result2, err = r.createOrUpdateRole(ctx, catalogParams, "catalog-role.yaml.tmpl", deploymentOwner)
	if err != nil {
		return ctrl.Result{}, err
	}
	if result2 != ResourceUnchanged {
		result = result2
	}

	// Create or update rolebinding
	result2, err = r.createOrUpdateRoleBinding(ctx, catalogParams, "catalog-rolebinding.yaml.tmpl", deploymentOwner)
	if err != nil {
		return ctrl.Result{}, err
	}
	if result2 != ResourceUnchanged {
		result = result2
	}

	postgresParams := &ModelCatalogParams{
		Name:      modelCatalogName,
		Namespace: r.TargetNamespace,
		Component: modelCatalogPostgresName,
	}

	// Create PostgreSQL resources only if not skipping DB creation
	if !r.SkipCatalogDBCreation {
		result2, err := r.createOrUpdateSecret(ctx, postgresParams, "catalog-postgres-secret.yaml.tmpl", crOwner)
		if err != nil {
			return ctrl.Result{}, err
		}
		if result2 != ResourceUnchanged {
			result = result2
		}

		// Create or update PostgreSQL PVC
		result2, err = r.createOrUpdatePostgresPVC(ctx, postgresParams, "catalog-postgres-pvc.yaml.tmpl", crOwner)
		if err != nil {
			return ctrl.Result{}, err
		}
		if result2 != ResourceUnchanged {
			result = result2
		}

		// Create or update PostgreSQL Deployment
		result2, postgresDeployment, err := r.createOrUpdateDeployment(ctx, postgresParams, "catalog-postgres-deployment.yaml.tmpl", crOwner)
		if err != nil {
			return ctrl.Result{}, err
		}
		if result2 != ResourceUnchanged {
			result = result2
		}
		if postgresDeployment == nil {
			return ctrl.Result{Requeue: true}, nil
		}

		// Use postgres deployment as owner for the remaining resources
		postgresDeploymentOwner := &metav1.OwnerReference{
			APIVersion: postgresDeployment.APIVersion,
			Kind:       postgresDeployment.Kind,
			Name:       postgresDeployment.Name,
			UID:        postgresDeployment.UID,
		}
		result2, err = r.createOrUpdateService(ctx, postgresParams, "catalog-postgres-service.yaml.tmpl", postgresDeploymentOwner)
		if err != nil {
			return ctrl.Result{}, err
		}
		if result2 != ResourceUnchanged {
			result = result2
		}
	} else {
		log.Info("Skipping catalog DB creation as configured")
	}

	if r.IsOpenShift {
		// Create or update Route
		result2, err = r.createOrUpdateRoute(ctx, catalogParams, "catalog-kube-rbac-proxy-https-route.yaml.tmpl", deploymentOwner)
		if err != nil {
			return ctrl.Result{}, err
		}
		if result2 != ResourceUnchanged {
			result = result2
		}

		// Create or update NetworkPolicy
		result2, err = r.createOrUpdateNetworkPolicy(ctx, catalogParams, "catalog-kube-rbac-proxy-network-policy.yaml.tmpl", deploymentOwner)
		if err != nil {
			return ctrl.Result{}, err
		}
		if result2 != ResourceUnchanged {
			result = result2
		}
	}

	// cleanup oauth proxy config as it's not used anymore
	result2, err = r.cleanupOAuthConfig(ctx, catalogParams)
	if err != nil {
		return ctrl.Result{}, err
	}
	if result2 != ResourceUnchanged {
		result = result2
	}

	// create or update kube-rbac-proxy config (catalog uses kube-rbac-proxy by default now)
	result2, err = r.createOrUpdateKubeRBACProxyConfig(ctx, catalogParams, crOwner)
	if err != nil {
		return ctrl.Result{}, err
	}
	if result2 != ResourceUnchanged {
		result = result2
	}

	// Use result to determine if we need to requeue
	if result != ResourceUnchanged {
		return ctrl.Result{Requeue: true}, nil
	}
	return ctrl.Result{}, nil
}

func (r *ModelCatalogReconciler) cleanupCatalogResources(ctx context.Context) (ctrl.Result, error) {
	catalogParams := &ModelCatalogParams{
		Name:      modelCatalogName,
		Namespace: r.TargetNamespace,
		Component: modelCatalogName,
	}

	// Delete the main resources - Kubernetes will automatically clean up owned resources
	// via garbage collection due to the owner references we've set up

	// Delete Deployment (this will cascade delete Service, Role, RoleBinding, Route, NetworkPolicy)
	result, err := r.deleteFromTemplate(ctx, catalogParams, "catalog-deployment.yaml.tmpl", &appsv1.Deployment{})
	if err != nil {
		return ctrl.Result{}, err
	}

	// Delete ServiceAccount (Deployment, ServiceAccount, and ClusterRoleBinding are owned by default-modelregistry)
	result2, err := r.deleteFromTemplate(ctx, catalogParams, "catalog-serviceaccount.yaml.tmpl", &corev1.ServiceAccount{})
	if err != nil {
		return ctrl.Result{}, err
	}
	if result2 != ResourceUnchanged {
		result = result2
	}

	// Delete OAuth Proxy Resources
	result2, err = r.cleanupOAuthConfig(ctx, catalogParams)
	if err != nil {
		return ctrl.Result{}, err
	}
	if result2 != ResourceUnchanged {
		result = result2
	}

	// Delete Kube-RBAC-Proxy Resources
	result2, err = r.cleanupKubeRBACProxyConfig(ctx, catalogParams)
	if err != nil {
		return ctrl.Result{}, err
	}
	if result2 != ResourceUnchanged {
		result = result2
	}

	postgresParams := &ModelCatalogParams{
		Name:      modelCatalogName,
		Namespace: r.TargetNamespace,
		Component: modelCatalogPostgresName,
	}

	// Delete PostgreSQL resources only if they were created (not skipping DB creation)
	if !r.SkipCatalogDBCreation {
		// Delete PostgreSQL Deployment
		result2, err = r.deleteFromTemplate(ctx, postgresParams, "catalog-postgres-deployment.yaml.tmpl", &appsv1.Deployment{})
		if err != nil {
			return ctrl.Result{}, err
		}
		if result2 != ResourceUnchanged {
			result = result2
		}

		// Delete PostgreSQL Service
		result2, err = r.deleteFromTemplate(ctx, postgresParams, "catalog-postgres-service.yaml.tmpl", &corev1.Service{})
		if err != nil {
			return ctrl.Result{}, err
		}
		if result2 != ResourceUnchanged {
			result = result2
		}

		// Delete PostgreSQL PVC
		result2, err = r.deleteFromTemplate(ctx, postgresParams, "catalog-postgres-pvc.yaml.tmpl", &corev1.PersistentVolumeClaim{})
		if err != nil {
			return ctrl.Result{}, err
		}
		if result2 != ResourceUnchanged {
			result = result2
		}

		// Delete PostgreSQL Secret last
		result2, err = r.deleteFromTemplate(ctx, postgresParams, "catalog-postgres-secret.yaml.tmpl", &corev1.Secret{})
		if err != nil {
			return ctrl.Result{}, err
		}
		if result2 != ResourceUnchanged {
			result = result2
		}
	}

	if r.IsOpenShift {
		// Delete OAuth Proxy Route (now uses kube-rbac-proxy templates)
		result2, err = r.deleteFromTemplate(ctx, catalogParams, "catalog-kube-rbac-proxy-https-route.yaml.tmpl", &routev1.Route{})
		if err != nil {
			return ctrl.Result{}, err
		}
		if result2 != ResourceUnchanged {
			result = result2
		}

		// Delete OAuth Proxy NetworkPolicy (now uses kube-rbac-proxy templates)
		result2, err = r.deleteFromTemplate(ctx, catalogParams, "catalog-kube-rbac-proxy-network-policy.yaml.tmpl", &networkingv1.NetworkPolicy{})
		if err != nil {
			return ctrl.Result{}, err
		}
		if result2 != ResourceUnchanged {
			result = result2
		}
	}

	// Note: We don't delete the sources configmap.

	// Use result to determine if we need to requeue
	if result != ResourceUnchanged {
		return ctrl.Result{Requeue: true}, nil
	}
	return ctrl.Result{}, nil
}

func (r *ModelCatalogReconciler) createOrUpdateDeployment(ctx context.Context, params *ModelCatalogParams, templateName string, owner *metav1.OwnerReference) (OperationResult, *appsv1.Deployment, error) {
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
		// Check if the error is due to immutable field conflicts
		if apierrors.IsForbidden(err) || (apierrors.IsInvalid(err) && strings.Contains(err.Error(), "field is immutable")) {
			log.Info("deleting deployment due to immutable field conflicts", "name", deployment.Name, "error", err.Error())

			// Get the existing deployment to delete it
			var existingDeployment appsv1.Deployment
			key := client.ObjectKeyFromObject(&deployment)
			if getErr := r.Client.Get(ctx, key, &existingDeployment); getErr != nil {
				return result, nil, getErr
			}

			// Delete the existing deployment
			if deleteErr := r.Client.Delete(ctx, &existingDeployment); deleteErr != nil {
				return result, nil, deleteErr
			}

			// Return to trigger recreation in next reconcile
			return ResourceUpdated, nil, nil
		}
		return result, nil, err
	}

	// Fetch the deployment to get the updated metadata (including UID)
	var actualDeployment appsv1.Deployment
	err = r.Client.Get(ctx, types.NamespacedName{
		Name:      deployment.Name,
		Namespace: deployment.Namespace,
	}, &actualDeployment)
	if err != nil {
		return result, nil, err
	}

	// Ensure APIVersion and Kind are set for proper owner references. Mostly for the tests.
	actualDeployment.APIVersion = "apps/v1"
	actualDeployment.Kind = "Deployment"

	return result, &actualDeployment, nil
}

func (r *ModelCatalogReconciler) createOrUpdateService(ctx context.Context, params *ModelCatalogParams, templateName string, owner *metav1.OwnerReference) (OperationResult, error) {
	result := ResourceUnchanged
	var service corev1.Service
	if err := r.Apply(params, templateName, &service); err != nil {
		return result, err
	}

	r.applyLabels(&service.ObjectMeta, params)
	r.applyOwnerReference(&service.ObjectMeta, owner)

	result, err := r.createOrUpdate(ctx, &corev1.Service{}, &service)
	if err != nil {
		return result, err
	}
	return result, nil
}

func (r *ModelCatalogReconciler) createOrUpdateRoute(ctx context.Context, params *ModelCatalogParams, templateName string, owner *metav1.OwnerReference) (OperationResult, error) {
	result := ResourceUnchanged
	var route routev1.Route
	if err := r.Apply(params, templateName, &route); err != nil {
		return result, err
	}

	r.applyLabels(&route.ObjectMeta, params)
	r.applyOwnerReference(&route.ObjectMeta, owner)

	result, err := r.createOrUpdate(ctx, &routev1.Route{}, &route)
	if err != nil {
		return result, err
	}
	return result, nil
}

func (r *ModelCatalogReconciler) createOrUpdateNetworkPolicy(ctx context.Context, params *ModelCatalogParams, templateName string, owner *metav1.OwnerReference) (OperationResult, error) {
	result := ResourceUnchanged
	var networkPolicy networkingv1.NetworkPolicy
	if err := r.Apply(params, templateName, &networkPolicy); err != nil {
		return result, err
	}

	r.applyLabels(&networkPolicy.ObjectMeta, params)
	r.applyOwnerReference(&networkPolicy.ObjectMeta, owner)

	result, err := r.createOrUpdate(ctx, &networkingv1.NetworkPolicy{}, &networkPolicy)
	if err != nil {
		return result, err
	}
	return result, nil
}

func (r *ModelCatalogReconciler) createOrUpdateConfigmap(ctx context.Context, params *ModelCatalogParams, templateName string, owner *metav1.OwnerReference) (OperationResult, error) {
	result := ResourceUnchanged
	var cm corev1.ConfigMap
	if err := r.Apply(params, templateName, &cm); err != nil {
		return result, err
	}

	r.applyLabels(&cm.ObjectMeta, params)
	r.applyOwnerReference(&cm.ObjectMeta, owner)

	result, err := r.createOrUpdate(ctx, &corev1.ConfigMap{}, &cm)
	if err != nil {
		return result, err
	}
	return result, nil
}

const sourcesFileName = "sources.yaml"

func (r *ModelCatalogReconciler) manageUserSourcesConfigmap(ctx context.Context, params *ModelCatalogParams, templateName string) (OperationResult, error) {
	log := klog.FromContext(ctx)

	// The sources configmap is created by this operator and it can be
	// changed to suit the site's needs. Before 3.0, the default configmap
	// contained an entry for the default catalog, which has now been moved
	// to the default sources configmap. The default catalog needs to be
	// removed from the user sources configmap if it's found.
	result := ResourceUnchanged
	var cm corev1.ConfigMap
	if err := r.Apply(params, templateName, &cm); err != nil {
		return result, err
	}

	r.applyLabels(&cm.ObjectMeta, params)

	var existing corev1.ConfigMap
	result, err := r.createIfNotExists(ctx, &existing, &cm)
	if err != nil {
		return result, err
	}

	if result == ResourceCreated {
		return result, nil
	}

	// If we've already checked for the default source this run then don't
	// check it again.
	if r.noDefaultSource {
		return result, nil
	}

	if existing.Data == nil {
		// Empty configmap, so nothing to do.
		return result, nil
	}

	existing.Data[sourcesFileName], err = r.removeDefaultSource(existing.Data[sourcesFileName])
	if err != nil {
		log.Error(err, "Unable to process sources configmap")
		return result, nil
	}

	if existing.Data[sourcesFileName] == "" {
		// Nothing to do.
		r.noDefaultSource = true
		return result, nil
	}

	result = ResourceUpdated
	log.Info("updating", "kind", existing.GetObjectKind().GroupVersionKind(), "name", existing.GetName())
	if err := patch.DefaultAnnotator.SetLastAppliedAnnotation(&existing); err != nil {
		return result, err
	}
	err = r.Client.Update(ctx, &existing)
	if err != nil {
		return result, err
	}

	r.noDefaultSource = true
	return result, nil
}

func (r *ModelCatalogReconciler) removeDefaultSource(doc string) (string, error) {
	type catalog struct {
		Name       string            `json:"name"`
		ID         string            `json:"id"`
		Type       string            `json:"type"`
		Enabled    *bool             `json:"enabled,omitempty"`
		Properties map[string]string `json:"properties,omitempty"`
	}
	var sources struct {
		Catalogs []catalog `json:"catalogs"`
	}

	err := yaml.UnmarshalStrict([]byte(doc), &sources)
	if err != nil {
		return "", err
	}

	originalLen := len(sources.Catalogs)
	sources.Catalogs = slices.DeleteFunc(sources.Catalogs, func(c catalog) bool {
		return c.ID == "default_catalog"
	})

	if len(sources.Catalogs) == originalLen {
		// Nothing to do
		return "", nil
	}

	buf, err := yaml.Marshal(sources)
	if err != nil {
		return "", err
	}

	return string(buf), nil
}

func (r *ModelCatalogReconciler) createOrUpdateServiceAccount(ctx context.Context, params *ModelCatalogParams, templateName string, owner *metav1.OwnerReference) (result OperationResult, err error) {
	result = ResourceUnchanged
	var sa corev1.ServiceAccount
	if err = r.Apply(params, templateName, &sa); err != nil {
		return result, err
	}

	r.applyLabels(&sa.ObjectMeta, params)
	r.applyOwnerReference(&sa.ObjectMeta, owner)

	if result, err = r.createOrUpdate(ctx, &corev1.ServiceAccount{}, &sa); err != nil {
		return result, err
	}
	return result, nil
}

func (r *ModelCatalogReconciler) createOrUpdateClusterRoleBinding(ctx context.Context, params *ModelCatalogParams, templateName string) (result OperationResult, err error) {
	result = ResourceUnchanged
	var roleBinding rbac.ClusterRoleBinding
	if err = r.Apply(params, templateName, &roleBinding); err != nil {
		return result, err
	}

	r.applyLabels(&roleBinding.ObjectMeta, params)
	// Note: ClusterRoleBinding is cluster-scoped and cannot have a namespaced owner reference

	return r.createOrUpdate(ctx, &rbac.ClusterRoleBinding{}, &roleBinding)
}

func (r *ModelCatalogReconciler) ensureSecretExists(ctx context.Context, params *ModelCatalogParams, templateName string, owner *metav1.OwnerReference) (OperationResult, error) {
	result := ResourceUnchanged
	var secret corev1.Secret
	if err := r.Apply(params, templateName, &secret); err != nil {
		return result, err
	}

	r.applyLabels(&secret.ObjectMeta, params)

	result, err := r.createIfNotExists(ctx, &corev1.Secret{}, &secret)
	if err != nil {
		return result, err
	}
	return result, nil
}

func (r *ModelCatalogReconciler) createOrUpdateRole(ctx context.Context, params *ModelCatalogParams, templateName string, owner *metav1.OwnerReference) (result OperationResult, err error) {
	result = ResourceUnchanged
	var role rbac.Role
	if err = r.Apply(params, templateName, &role); err != nil {
		return result, err
	}

	r.applyLabels(&role.ObjectMeta, params)
	r.applyOwnerReference(&role.ObjectMeta, owner)

	return r.createOrUpdate(ctx, &rbac.Role{}, &role)
}

func (r *ModelCatalogReconciler) createOrUpdateRoleBinding(ctx context.Context, params *ModelCatalogParams, templateName string, owner *metav1.OwnerReference) (result OperationResult, err error) {
	result = ResourceUnchanged
	var roleBinding rbac.RoleBinding
	if err = r.Apply(params, templateName, &roleBinding); err != nil {
		return result, err
	}

	r.applyLabels(&roleBinding.ObjectMeta, params)
	r.applyOwnerReference(&roleBinding.ObjectMeta, owner)

	return r.createOrUpdate(ctx, &rbac.RoleBinding{}, &roleBinding)
}

func (r *ModelCatalogReconciler) createOrUpdateSecret(ctx context.Context, params *ModelCatalogParams, templateName string, owner *metav1.OwnerReference) (OperationResult, error) {
	result := ResourceUnchanged
	var newSecret corev1.Secret
	if err := r.Apply(params, templateName, &newSecret); err != nil {
		return result, err
	}

	r.applyLabels(&newSecret.ObjectMeta, params)
	r.applyOwnerReference(&newSecret.ObjectMeta, owner)

	return r.createOrUpdate(ctx, &corev1.Secret{}, &newSecret)
}

func (r *ModelCatalogReconciler) cleanupOAuthConfig(ctx context.Context, params *ModelCatalogParams) (result OperationResult, err error) {
	result = ResourceUnchanged

	// delete oauth proxy cookie secret
	secret := corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      params.Name + "-oauth-cookie-secret",
			Namespace: params.Namespace,
		},
	}
	if err := r.Client.Delete(ctx, &secret); client.IgnoreNotFound(err) != nil {
		return result, err
	}

	return result, nil
}

func (r *ModelCatalogReconciler) createOrUpdateKubeRBACProxyConfig(ctx context.Context, params *ModelCatalogParams, owner *metav1.OwnerReference) (result OperationResult, err error) {
	result = ResourceUnchanged

	// create kube-rbac-proxy config
	result, err = r.createOrUpdateConfigmap(ctx, params, "catalog-kube-rbac-proxy-config.yaml.tmpl", owner)
	if err != nil {
		return result, err
	}

	// create kube-rbac-proxy rolebinding
	result2, err := r.createOrUpdateClusterRoleBinding(ctx, params, "catalog-kube-rbac-proxy-role-binding.yaml.tmpl")
	if err != nil {
		return result2, err
	}
	if result2 != ResourceUnchanged {
		result = result2
	}

	return result, nil
}

func (r *ModelCatalogReconciler) cleanupKubeRBACProxyConfig(ctx context.Context, params *ModelCatalogParams) (result OperationResult, err error) {
	result = ResourceUnchanged

	// delete kube-rbac-proxy config
	result, err = r.deleteFromTemplate(ctx, params, "catalog-kube-rbac-proxy-config.yaml.tmpl", &corev1.ConfigMap{})
	if err != nil {
		return result, err
	}

	// delete kube-rbac-proxy rolebinding
	result2, err := r.deleteFromTemplate(ctx, params, "catalog-kube-rbac-proxy-role-binding.yaml.tmpl", &rbac.ClusterRoleBinding{})
	if err != nil {
		return result2, err
	}
	if result2 != ResourceUnchanged {
		result = result2
	}

	return result, nil
}

func (r *ModelCatalogReconciler) createOrUpdatePostgresPVC(ctx context.Context, params *ModelCatalogParams, templateName string, owner *metav1.OwnerReference) (OperationResult, error) {
	result := ResourceUnchanged
	var newPVC corev1.PersistentVolumeClaim
	if err := r.Apply(params, templateName, &newPVC); err != nil {
		return result, err
	}

	r.applyLabels(&newPVC.ObjectMeta, params)
	r.applyOwnerReference(&newPVC.ObjectMeta, owner)

	// Check if PVC already exists
	var existingPVC corev1.PersistentVolumeClaim
	key := client.ObjectKeyFromObject(&newPVC)

	if err := r.Client.Get(ctx, key, &existingPVC); err != nil {
		if client.IgnoreNotFound(err) == nil {
			// PVC doesn't exist, create it
			result = ResourceCreated
			if err := patch.DefaultAnnotator.SetLastAppliedAnnotation(&newPVC); err != nil {
				return result, err
			}
			return result, r.Client.Create(ctx, &newPVC)
		}
		return result, err
	}

	// PVC exists, only update mutable fields
	updated := false

	// Update labels if they changed
	if !reflect.DeepEqual(existingPVC.Labels, newPVC.Labels) {
		existingPVC.Labels = newPVC.Labels
		updated = true
	}

	// Update owner references if they changed
	if !reflect.DeepEqual(existingPVC.OwnerReferences, newPVC.OwnerReferences) {
		existingPVC.OwnerReferences = newPVC.OwnerReferences
		updated = true
	}

	// Update storage size if it increased (storage can only be increased, not decreased)
	if newPVC.Spec.Resources.Requests.Storage().Cmp(*existingPVC.Spec.Resources.Requests.Storage()) > 0 {
		existingPVC.Spec.Resources.Requests = newPVC.Spec.Resources.Requests
		updated = true
	}

	if updated {
		result = ResourceUpdated
		if err := patch.DefaultAnnotator.SetLastAppliedAnnotation(&existingPVC); err != nil {
			return result, err
		}
		return result, r.Client.Update(ctx, &existingPVC)
	}

	return result, nil
}

// Apply executes given template name with params
func (r *ModelCatalogReconciler) Apply(params *ModelCatalogParams, templateName string, object any) error {
	// Ensure templateApplier is initialized
	if r.templateApplier == nil {
		r.templateApplier = &TemplateApplier{
			Template:    r.Template,
			IsOpenShift: r.IsOpenShift,
		}
	}

	// Create a default spec that's compatible with catalog templates
	var restPort int32 = 8080
	var oauthPort int32 = 8443
	var routePort int32 = 443

	defaultSpec := &v1beta1.ModelRegistrySpec{
		Rest: v1beta1.RestSpec{
			Port:      &restPort,
			Image:     config.GetStringConfigWithDefault(config.RestImage, config.DefaultRestImage),
			Resources: &config.CatalogServiceResourceRequirements,
		},
		// Use kube-rbac-proxy by default instead of oauth-proxy
		KubeRBACProxy: &v1beta1.KubeRBACProxyConfig{
			Port:      &oauthPort,
			RoutePort: &routePort,
			Image:     config.GetStringConfigWithDefault(config.KubeRBACProxyImage, config.DefaultKubeRBACProxyImage),
			Domain:    config.GetDefaultDomain(),
		},
	}

	catalogParams := struct {
		Name               string
		Namespace          string
		Spec               *v1beta1.ModelRegistrySpec
		CatalogDataImage   string
		BenchmarkDataImage string
		PostgresUser       string
		PostgresPassword   string
		PostgresDatabase   string
	}{
		Name:               params.Name,
		Namespace:          params.Namespace,
		Spec:               defaultSpec,
		CatalogDataImage:   config.GetStringConfigWithDefault(config.CatalogDataImage, config.DefaultCatalogDataImage),
		BenchmarkDataImage: config.GetStringConfigWithDefault(config.BenchmarkDataImage, config.DefaultBenchmarkDataImage),
		PostgresUser:       config.GetStringConfigWithDefault(config.CatalogPostgresUser, config.DefaultCatalogPostgresUser),
		PostgresPassword:   config.GetStringConfigWithDefault(config.CatalogPostgresPassword, config.DefaultCatalogPostgresPassword),
		PostgresDatabase:   config.GetStringConfigWithDefault(config.CatalogPostgresDatabase, config.DefaultCatalogPostgresDatabase),
	}

	return r.templateApplier.Apply(catalogParams, templateName, object)
}

func (r *ModelCatalogReconciler) createOrUpdate(ctx context.Context, currObj client.Object, newObj client.Object) (OperationResult, error) {
	// Ensure resourceManager is initialized
	if r.resourceManager == nil {
		r.resourceManager = &ResourceManager{Client: r.Client}
	}
	return r.resourceManager.CreateOrUpdate(ctx, currObj, newObj)
}

func (r *ModelCatalogReconciler) createIfNotExists(ctx context.Context, currObj client.Object, newObj client.Object) (OperationResult, error) {
	// Ensure resourceManager is initialized
	if r.resourceManager == nil {
		r.resourceManager = &ResourceManager{Client: r.Client}
	}
	return r.resourceManager.CreateIfNotExists(ctx, currObj, newObj)
}

func (r *ModelCatalogReconciler) deleteFromTemplate(ctx context.Context, params *ModelCatalogParams, templateName string, obj client.Object) (OperationResult, error) {
	if err := r.Apply(params, templateName, obj); err != nil {
		return ResourceUnchanged, err
	}
	err := r.Client.Delete(ctx, obj)
	if err != nil {
		return ResourceUnchanged, client.IgnoreNotFound(err)
	}
	return ResourceUpdated, nil
}

func (*ModelCatalogReconciler) applyLabels(meta *metav1.ObjectMeta, params *ModelCatalogParams) {
	if meta.Labels == nil {
		meta.Labels = map[string]string{}
	}
	meta.Labels["component"] = params.Component
	meta.Labels["app.kubernetes.io/created-by"] = "model-registry-operator"
}

// applyOwnerReference sets the owner reference using the provided owner metadata.
// This ensures that catalog resources are managed by the modelregistries.components.platform.opendatahub.io resource.
func (*ModelCatalogReconciler) applyOwnerReference(meta *metav1.ObjectMeta, owner *metav1.OwnerReference) {
	if owner != nil {
		// Set owner references (replace any existing ones for this resource type)
		meta.OwnerReferences = []metav1.OwnerReference{*owner}
	}
}

// fetchDefaultModelRegistry retrieves the default-modelregistry resource from modelregistries.components.platform.opendatahub.io.
// This resource is used as the owner reference for catalog deployment and service account resources.
// Returns the OwnerReference of the resource, or an error if not found.
func (r *ModelCatalogReconciler) fetchDefaultModelRegistry(ctx context.Context) (*metav1.OwnerReference, error) {
	modelRegistry := &unstructured.Unstructured{}
	modelRegistry.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "components.platform.opendatahub.io",
		Version: "v1alpha1",
		Kind:    "ModelRegistry",
	})

	err := r.Client.Get(ctx, types.NamespacedName{
		Name:      "default-modelregistry",
		Namespace: r.TargetNamespace,
	}, modelRegistry)
	if err != nil {
		return nil, err
	}

	return &metav1.OwnerReference{
		APIVersion: modelRegistry.GetAPIVersion(),
		Kind:       modelRegistry.GetKind(),
		Name:       modelRegistry.GetName(),
		UID:        modelRegistry.GetUID(),
	}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *ModelCatalogReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Initialize shared utilities
	r.templateApplier = &TemplateApplier{
		Template:    r.Template,
		IsOpenShift: r.IsOpenShift,
	}
	r.resourceManager = &ResourceManager{
		Client: r.Client,
	}

	labels, err := predicate.LabelSelectorPredicate(metav1.LabelSelector{
		MatchExpressions: []metav1.LabelSelectorRequirement{
			{
				Key:      "component",
				Operator: metav1.LabelSelectorOpIn,
				Values:   []string{modelCatalogName, modelCatalogPostgresName},
			},
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

	// only watch resources in the target namespace
	combinedPredicate := predicate.And(
		labels,
		predicate.NewPredicateFuncs(func(object client.Object) bool {
			return object.GetNamespace() == r.TargetNamespace
		}),
	)

	// Custom mapper that maps ALL watched objects to the same reconcile request
	// This enables workqueue deduplication to prevent reconcile storms
	mapToFixedCatalogRequest := handler.EnqueueRequestsFromMapFunc(
		func(ctx context.Context, obj client.Object) []reconcile.Request {
			return []reconcile.Request{{
				NamespacedName: types.NamespacedName{
					Name:      modelCatalogName,  // Always use "model-catalog"
					Namespace: r.TargetNamespace, // Always use target namespace
				},
			}}
		},
	)

	c, err := ctrl.NewControllerManagedBy(mgr).
		Named("modelcatalog").
		// All watched resources now map to the same reconcile request for deduplication
		Watches(&appsv1.Deployment{}, mapToFixedCatalogRequest, builder.WithPredicates(combinedPredicate)).
		Watches(&corev1.ConfigMap{}, mapToFixedCatalogRequest, builder.WithPredicates(combinedPredicate)).
		Watches(&corev1.Secret{}, mapToFixedCatalogRequest, builder.WithPredicates(combinedPredicate)).
		Watches(&corev1.ServiceAccount{}, mapToFixedCatalogRequest, builder.WithPredicates(combinedPredicate)).
		Watches(&corev1.Service{}, mapToFixedCatalogRequest, builder.WithPredicates(combinedPredicate)).
		Watches(&corev1.PersistentVolumeClaim{}, mapToFixedCatalogRequest, builder.WithPredicates(combinedPredicate)).
		Watches(&rbac.ClusterRoleBinding{}, mapToFixedCatalogRequest, builder.WithPredicates(labels)).
		Watches(&rbac.Role{}, mapToFixedCatalogRequest, builder.WithPredicates(combinedPredicate)).
		Watches(&rbac.RoleBinding{}, mapToFixedCatalogRequest, builder.WithPredicates(combinedPredicate)).
		Build(r)
	if err != nil {
		return err
	}

	// Enqueue a one-time event so Reconcile runs at startup
	ch := make(chan event.GenericEvent, 1)
	ch <- event.GenericEvent{Object: &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
		Name:      modelCatalogName,
		Namespace: r.TargetNamespace,
		Labels: map[string]string{
			"component":                    modelCatalogName,
			"app.kubernetes.io/created-by": "model-registry-operator",
		},
	}}} // object identity only; it need not exist
	return c.Watch(&source.Channel{Source: ch}, mapToFixedCatalogRequest)
}
