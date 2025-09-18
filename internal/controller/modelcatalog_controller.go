package controller

import (
	"context"
	"text/template"

	"github.com/go-logr/logr"
	"github.com/opendatahub-io/model-registry-operator/api/v1beta1"
	"github.com/opendatahub-io/model-registry-operator/internal/controller/config"
	routev1 "github.com/openshift/api/route/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbac "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const modelCatalogName = "model-catalog"

// ModelCatalogReconciler reconciles a single model catalog instance
type ModelCatalogReconciler struct {
	client.Client
	Scheme          *runtime.Scheme
	Recorder        record.EventRecorder
	Log             logr.Logger
	Template        *template.Template
	IsOpenShift     bool
	TargetNamespace string
	Enabled         bool

	// embedded utilities for shared functionality
	templateApplier *TemplateApplier
	resourceManager *ResourceManager
}

// ModelCatalogParams is a wrapper for template parameters
type ModelCatalogParams struct {
	Name      string
	Namespace string
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
	params := &ModelCatalogParams{
		Name:      modelCatalogName,
		Namespace: r.TargetNamespace,
	}

	// Create or update ServiceAccount
	result, err := r.createOrUpdateServiceAccount(ctx, params, "catalog-serviceaccount.yaml.tmpl")
	if err != nil {
		return ctrl.Result{}, err
	}

	// Create sources ConfigMap
	result2, err := r.ensureConfigMapExists(ctx, params, "catalog-configmap.yaml.tmpl")
	if err != nil {
		return ctrl.Result{}, err
	}
	if result2 != ResourceUnchanged {
		result = result2
	}

	// Create default catalog configmap
	result2, err = r.ensureConfigMapExists(ctx, params, "catalog-default-catalog-configmap.yaml.tmpl")
	if err != nil {
		return ctrl.Result{}, err
	}
	if result2 != ResourceUnchanged {
		result = result2
	}

	// Create or update Deployment
	result2, err = r.createOrUpdateDeployment(ctx, params, "catalog-deployment.yaml.tmpl")
	if err != nil {
		return ctrl.Result{}, err
	}
	if result2 != ResourceUnchanged {
		result = result2
	}

	// Create or update Service
	result2, err = r.createOrUpdateService(ctx, params, "catalog-service.yaml.tmpl")
	if err != nil {
		return ctrl.Result{}, err
	}
	if result2 != ResourceUnchanged {
		result = result2
	}

	// Create or update role
	result2, err = r.createOrUpdateRole(ctx, params, "catalog-role.yaml.tmpl")
	if err != nil {
		return ctrl.Result{}, err
	}
	if result2 != ResourceUnchanged {
		result = result2
	}

	// Create or update rolebinding
	result2, err = r.createOrUpdateRoleBinding(ctx, params, "catalog-rolebinding.yaml.tmpl")
	if err != nil {
		return ctrl.Result{}, err
	}
	if result2 != ResourceUnchanged {
		result = result2
	}

	if r.IsOpenShift {
		// Create or update Route
		result2, err = r.createOrUpdateRoute(ctx, params, "catalog-route.yaml.tmpl")
		if err != nil {
			return ctrl.Result{}, err
		}
		if result2 != ResourceUnchanged {
			result = result2
		}

		// Create or update NetworkPolicy
		result2, err = r.createOrUpdateNetworkPolicy(ctx, params, "catalog-network-policy.yaml.tmpl")
		if err != nil {
			return ctrl.Result{}, err
		}
		if result2 != ResourceUnchanged {
			result = result2
		}
	}

	// create or update oauth proxy config if enabled, delete if disabled
	result2, err = r.createOrUpdateOAuthConfig(ctx, params)
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
	params := &ModelCatalogParams{
		Name:      modelCatalogName,
		Namespace: r.TargetNamespace,
	}

	// Delete Deployment
	result, err := r.deleteFromTemplate(ctx, params, "catalog-deployment.yaml.tmpl", &appsv1.Deployment{})
	if err != nil {
		return ctrl.Result{}, err
	}

	// Delete Service
	result2, err := r.deleteFromTemplate(ctx, params, "catalog-service.yaml.tmpl", &corev1.Service{})
	if err != nil {
		return ctrl.Result{}, err
	}
	if result2 != ResourceUnchanged {
		result = result2
	}

	// Delete ServiceAccount
	result2, err = r.deleteFromTemplate(ctx, params, "catalog-serviceaccount.yaml.tmpl", &corev1.ServiceAccount{})
	if err != nil {
		return ctrl.Result{}, err
	}
	if result2 != ResourceUnchanged {
		result = result2
	}

	// Delete RoleBinding
	result2, err = r.deleteFromTemplate(ctx, params, "catalog-rolebinding.yaml.tmpl", &rbac.RoleBinding{})
	if err != nil {
		return ctrl.Result{}, err
	}
	if result2 != ResourceUnchanged {
		result = result2
	}

	// Delete Role
	result2, err = r.deleteFromTemplate(ctx, params, "catalog-role.yaml.tmpl", &rbac.Role{})
	if err != nil {
		return ctrl.Result{}, err
	}
	if result2 != ResourceUnchanged {
		result = result2
	}

	// Delete OAuth Proxy ClusterRoleBinding (cookie Secret is intentionally preserved)
	result2, err = r.deleteFromTemplate(ctx, params, "proxy-role-binding.yaml.tmpl", &rbac.ClusterRoleBinding{})
	if err != nil {
		return ctrl.Result{}, err
	}
	if result2 != ResourceUnchanged {
		result = result2
	}

	if r.IsOpenShift {
		// Delete Route
		result2, err = r.deleteFromTemplate(ctx, params, "catalog-route.yaml.tmpl", &routev1.Route{})
		if err != nil {
			return ctrl.Result{}, err
		}
		if result2 != ResourceUnchanged {
			result = result2
		}

		// Delete NetworkPolicy
		result2, err = r.deleteFromTemplate(ctx, params, "catalog-network-policy.yaml.tmpl", &networkingv1.NetworkPolicy{})
		if err != nil {
			return ctrl.Result{}, err
		}
		if result2 != ResourceUnchanged {
			result = result2
		}

		// Delete OAuth Proxy Route
		result2, err = r.deleteFromTemplate(ctx, params, "https-route.yaml.tmpl", &routev1.Route{})
		if err != nil {
			return ctrl.Result{}, err
		}
		if result2 != ResourceUnchanged {
			result = result2
		}

		// Delete OAuth Proxy NetworkPolicy
		result2, err = r.deleteFromTemplate(ctx, params, "proxy-network-policy.yaml.tmpl", &networkingv1.NetworkPolicy{})
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

func (r *ModelCatalogReconciler) createOrUpdateDeployment(ctx context.Context, params *ModelCatalogParams, templateName string) (OperationResult, error) {
	result := ResourceUnchanged
	var deployment appsv1.Deployment
	if err := r.Apply(params, templateName, &deployment); err != nil {
		return result, err
	}

	r.applyLabels(&deployment.ObjectMeta)

	result, err := r.createOrUpdate(ctx, &appsv1.Deployment{}, &deployment)
	if err != nil {
		return result, err
	}
	return result, nil
}

func (r *ModelCatalogReconciler) createOrUpdateService(ctx context.Context, params *ModelCatalogParams, templateName string) (OperationResult, error) {
	result := ResourceUnchanged
	var service corev1.Service
	if err := r.Apply(params, templateName, &service); err != nil {
		return result, err
	}

	r.applyLabels(&service.ObjectMeta)

	result, err := r.createOrUpdate(ctx, &corev1.Service{}, &service)
	if err != nil {
		return result, err
	}
	return result, nil
}

func (r *ModelCatalogReconciler) createOrUpdateRoute(ctx context.Context, params *ModelCatalogParams, templateName string) (OperationResult, error) {
	result := ResourceUnchanged
	var route routev1.Route
	if err := r.Apply(params, templateName, &route); err != nil {
		return result, err
	}

	r.applyLabels(&route.ObjectMeta)

	result, err := r.createOrUpdate(ctx, &routev1.Route{}, &route)
	if err != nil {
		return result, err
	}
	return result, nil
}

func (r *ModelCatalogReconciler) createOrUpdateNetworkPolicy(ctx context.Context, params *ModelCatalogParams, templateName string) (OperationResult, error) {
	result := ResourceUnchanged
	var networkPolicy networkingv1.NetworkPolicy
	if err := r.Apply(params, templateName, &networkPolicy); err != nil {
		return result, err
	}

	r.applyLabels(&networkPolicy.ObjectMeta)

	result, err := r.createOrUpdate(ctx, &networkingv1.NetworkPolicy{}, &networkPolicy)
	if err != nil {
		return result, err
	}
	return result, nil
}

func (r *ModelCatalogReconciler) ensureConfigMapExists(ctx context.Context, params *ModelCatalogParams, templateName string) (OperationResult, error) {
	result := ResourceUnchanged
	var cm corev1.ConfigMap
	if err := r.Apply(params, templateName, &cm); err != nil {
		return result, err
	}

	r.applyLabels(&cm.ObjectMeta)

	result, err := r.createIfNotExists(ctx, &corev1.ConfigMap{}, &cm)
	if err != nil {
		return result, err
	}
	return result, nil
}

func (r *ModelCatalogReconciler) createOrUpdateServiceAccount(ctx context.Context, params *ModelCatalogParams, templateName string) (result OperationResult, err error) {
	result = ResourceUnchanged
	var sa corev1.ServiceAccount
	if err = r.Apply(params, templateName, &sa); err != nil {
		return result, err
	}

	r.applyLabels(&sa.ObjectMeta)

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

	r.applyLabels(&roleBinding.ObjectMeta)

	return r.createOrUpdate(ctx, &rbac.ClusterRoleBinding{}, &roleBinding)
}

func (r *ModelCatalogReconciler) ensureSecretExists(ctx context.Context, params *ModelCatalogParams, templateName string) (OperationResult, error) {
	result := ResourceUnchanged
	var secret corev1.Secret
	if err := r.Apply(params, templateName, &secret); err != nil {
		return result, err
	}

	r.applyLabels(&secret.ObjectMeta)

	result, err := r.createIfNotExists(ctx, &corev1.Secret{}, &secret)
	if err != nil {
		return result, err
	}
	return result, nil
}

func (r *ModelCatalogReconciler) createOrUpdateRole(ctx context.Context, params *ModelCatalogParams, templateName string) (result OperationResult, err error) {
	result = ResourceUnchanged
	var role rbac.Role
	if err = r.Apply(params, templateName, &role); err != nil {
		return result, err
	}

	r.applyLabels(&role.ObjectMeta)

	return r.createOrUpdate(ctx, &rbac.Role{}, &role)
}

func (r *ModelCatalogReconciler) createOrUpdateRoleBinding(ctx context.Context, params *ModelCatalogParams, templateName string) (result OperationResult, err error) {
	result = ResourceUnchanged
	var roleBinding rbac.RoleBinding
	if err = r.Apply(params, templateName, &roleBinding); err != nil {
		return result, err
	}

	r.applyLabels(&roleBinding.ObjectMeta)

	return r.createOrUpdate(ctx, &rbac.RoleBinding{}, &roleBinding)
}

func (r *ModelCatalogReconciler) createOrUpdateOAuthConfig(ctx context.Context, params *ModelCatalogParams) (result OperationResult, err error) {
	result = ResourceUnchanged

	// create oauth proxy rolebinding
	result, err = r.createOrUpdateClusterRoleBinding(ctx, params, "proxy-role-binding.yaml.tmpl")
	if err != nil {
		return result, err
	}

	result2, err := r.ensureSecretExists(ctx, params, "proxy-cookie-secret.yaml.tmpl")
	if err != nil {
		return result2, err
	}
	if result2 != ResourceUnchanged {
		result = result2
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
			Port:  &restPort,
			Image: config.GetStringConfigWithDefault(config.RestImage, config.DefaultRestImage),
		},
		OAuthProxy: &v1beta1.OAuthProxyConfig{
			Port:      &oauthPort,
			RoutePort: &routePort,
			Image:     config.GetStringConfigWithDefault(config.OAuthProxyImage, config.DefaultOAuthProxyImage),
			Domain:    config.GetDefaultDomain(),
		},
	}

	catalogParams := struct {
		Name             string
		Namespace        string
		Spec             *v1beta1.ModelRegistrySpec
		CatalogDataImage string
	}{
		Name:             params.Name,
		Namespace:        params.Namespace,
		Spec:             defaultSpec,
		CatalogDataImage: config.GetStringConfigWithDefault(config.CatalogDataImage, config.DefaultCatalogDataImage),
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

func (*ModelCatalogReconciler) applyLabels(meta *metav1.ObjectMeta) {
	if meta.Labels == nil {
		meta.Labels = map[string]string{}
	}
	meta.Labels["component"] = modelCatalogName
	meta.Labels["app.kubernetes.io/created-by"] = "model-registry-operator"
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
		MatchLabels: map[string]string{
			"component":                    modelCatalogName,
			"app.kubernetes.io/created-by": "model-registry-operator",
		},
	})
	if err != nil {
		return err
	}

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
		Watches(&appsv1.Deployment{}, mapToFixedCatalogRequest, builder.WithPredicates(labels)).
		Watches(&corev1.ConfigMap{}, mapToFixedCatalogRequest, builder.WithPredicates(labels)).
		Watches(&corev1.Secret{}, mapToFixedCatalogRequest, builder.WithPredicates(labels)).
		Watches(&corev1.ServiceAccount{}, mapToFixedCatalogRequest, builder.WithPredicates(labels)).
		Watches(&corev1.Service{}, mapToFixedCatalogRequest, builder.WithPredicates(labels)).
		Watches(&rbac.ClusterRoleBinding{}, mapToFixedCatalogRequest, builder.WithPredicates(labels)).
		Watches(&rbac.Role{}, mapToFixedCatalogRequest, builder.WithPredicates(labels)).
		Watches(&rbac.RoleBinding{}, mapToFixedCatalogRequest, builder.WithPredicates(labels)).
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
