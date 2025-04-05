/*
Copyright 2023.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	errors2 "errors"
	"fmt"
	networkingv1 "k8s.io/api/networking/v1"
	"strings"
	"text/template"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"

	"github.com/banzaicloud/k8s-objectmatcher/patch"
	"github.com/go-logr/logr"
	modelregistryv1alpha1 "github.com/opendatahub-io/model-registry-operator/api/v1alpha1"
	"github.com/opendatahub-io/model-registry-operator/internal/controller/config"
	routev1 "github.com/openshift/api/route/v1"
	userv1 "github.com/openshift/api/user/v1"
	networking "istio.io/client-go/pkg/apis/networking/v1beta1"
	security "istio.io/client-go/pkg/apis/security/v1beta1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbac "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	pkgbuilder "sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	klog "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	modelRegistryFinalizer = "modelregistry.opendatahub.io/finalizer"
	DisplayNameAnnotation  = "openshift.io/display-name"
	DescriptionAnnotation  = "openshift.io/description"
)

// ModelRegistryReconciler reconciles a ModelRegistry object
type ModelRegistryReconciler struct {
	client.Client
	ClientSet           *kubernetes.Clientset
	Scheme              *runtime.Scheme
	Recorder            record.EventRecorder
	Log                 logr.Logger
	Template            *template.Template
	EnableWebhooks      bool
	IsOpenShift         bool
	HasIstio            bool
	CreateAuthResources bool
}

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the ModelRegistry object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.16.0/pkg/reconcile
func (r *ModelRegistryReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := klog.FromContext(ctx)

	modelRegistry := &modelregistryv1alpha1.ModelRegistry{}
	err := r.Get(ctx, req.NamespacedName, modelRegistry)
	if err != nil {
		if errors.IsNotFound(err) {
			// If the custom resource is not found then, it usually means that it was deleted or not created
			// In this way, we will stop the reconciliation
			log.Info("modelregistry resource not found. Ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		// Error reading the object - requeue the request.
		log.Error(err, "Failed to get modelregistry")
		return ctrl.Result{}, err
	}

	// Let's add a finalizer. Then, we can define some operations which should
	// occurs before the custom resource to be deleted.
	// More info: https://kubernetes.io/docs/concepts/overview/working-with-objects/finalizers
	if !controllerutil.ContainsFinalizer(modelRegistry, modelRegistryFinalizer) {
		log.Info("Adding Finalizer for ModelRegistry")
		if ok := controllerutil.AddFinalizer(modelRegistry, modelRegistryFinalizer); !ok {
			log.Error(err, "Failed to add finalizer into the custom resource")
			return ctrl.Result{Requeue: true}, nil
		}

		if err = r.Update(ctx, modelRegistry); err != nil {
			log.Error(err, "Failed to update custom resource to add finalizer")
			return ctrl.Result{}, err
		}
	}

	// Check if the modelRegistry instance is marked to be deleted, which is
	// indicated by the deletion timestamp being set.
	isMarkedToBeDeleted := modelRegistry.GetDeletionTimestamp() != nil
	if isMarkedToBeDeleted {
		if controllerutil.ContainsFinalizer(modelRegistry, modelRegistryFinalizer) {
			log.Info("Performing Finalizer Operations for modelRegistry before delete CR")

			// Let's add status "Degraded" to define that this resource has begun its process to be terminated.
			meta.SetStatusCondition(&modelRegistry.Status.Conditions, metav1.Condition{Type: ConditionTypeDegraded,
				Status: metav1.ConditionUnknown, Reason: "Finalizing",
				Message: fmt.Sprintf("Performing finalizer operations for the custom resource: %s ", modelRegistry.Name)})

			if err = r.Status().Update(ctx, modelRegistry); IgnoreDeletingErrors(err) != nil {
				var t *errors.StatusError
				switch {
				case errors2.As(err, &t):
					log.Error(err, "status error", "status", t.Status())
				}
				log.Error(err, "Failed to update modelRegistry status")
				return ctrl.Result{}, err
			}

			// Perform all operations required before remove the finalizer and allow
			// the Kubernetes API to remove the custom resource.
			if err = r.doFinalizerOperationsForModelRegistry(ctx, modelRegistry); err != nil {
				log.Error(err, "Failed to do finalizer operations for modelRegistry")
				return ctrl.Result{Requeue: true}, nil
			}

			// TODO(user): If you add operations to the doFinalizerOperationsForModelRegistry method
			// then you need to ensure that all worked fine before deleting and updating the Downgrade status
			// otherwise, you should requeue here.

			// Re-fetch the modelRegistry Custom Resource before update the status
			// so that we have the latest state of the resource on the cluster, and we will avoid
			// raise the issue "the object has been modified, please apply
			// your changes to the latest version and try again" which would re-trigger the reconciliation
			if err = r.Get(ctx, req.NamespacedName, modelRegistry); IgnoreDeletingErrors(err) != nil {
				log.Error(err, "Failed to re-fetch modelRegistry")
				return ctrl.Result{}, err
			}

			meta.SetStatusCondition(&modelRegistry.Status.Conditions, metav1.Condition{Type: ConditionTypeDegraded,
				Status: metav1.ConditionTrue, Reason: "Finalizing",
				Message: fmt.Sprintf("Finalizer operations for custom resource %s were successfully accomplished", modelRegistry.Name)})

			if err = r.Status().Update(ctx, modelRegistry); IgnoreDeletingErrors(err) != nil {
				log.Error(err, "Failed to update modelRegistry status")
				return ctrl.Result{}, err
			}

			log.Info("Removing Finalizer for modelRegistry after successfully perform the operations")
			if ok := controllerutil.RemoveFinalizer(modelRegistry, modelRegistryFinalizer); !ok {
				log.Error(err, "Failed to remove finalizer for modelRegistry")
				return ctrl.Result{Requeue: true}, nil
			}

			if err = r.Update(ctx, modelRegistry); IgnoreDeletingErrors(err) != nil {
				log.Error(err, "Failed to remove finalizer for modelRegistry")
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	// remember original spec
	originalSpec := modelRegistry.Spec.DeepCopy()
	// set runtime default properties in memory for reconciliation
	modelRegistry.RuntimeDefaults()

	// set defaults and validate if not using webhooks
	if !r.EnableWebhooks {
		modelRegistry.Default()
		if !isMarkedToBeDeleted {
			_, err = modelRegistry.ValidateRegistry()
			if err != nil {
				log.Error(err, "validate registry error")
				return ctrl.Result{}, err
			}
		}
	}

	params := &ModelRegistryParams{
		Name:         req.Name,
		Namespace:    req.Namespace,
		Spec:         &modelRegistry.Spec,
		OriginalSpec: originalSpec,
	}

	// update registry service
	result, err := r.updateRegistryResources(ctx, params, modelRegistry)
	if err != nil {
		log.Error(err, "service reconcile error")
		return r.handleReconcileErrors(ctx, modelRegistry, ctrl.Result{}, err)
	}
	log.Info("service reconciled", "status", result)
	r.logResultAsEvent(modelRegistry, result)

	// set custom resource status
	available := false
	if available, err = r.setRegistryStatus(ctx, req, params, result); err != nil {
		return r.handleReconcileErrors(ctx, modelRegistry, ctrl.Result{Requeue: true}, err)
	}
	log.Info("status reconciled")

	// requeue to update status
	return ctrl.Result{Requeue: (result != ResourceUnchanged) || !available}, nil
}

func IgnoreDeletingErrors(err error) error {
	if err == nil {
		return nil
	}
	if errors.IsNotFound(err) || errors.IsConflict(err) {
		return nil
	}
	return err
}

// SetupWithManager sets up the controller with the Manager.
func (r *ModelRegistryReconciler) SetupWithManager(mgr ctrl.Manager) error {
	builder := ctrl.NewControllerManagedBy(mgr).
		For(&modelregistryv1alpha1.ModelRegistry{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.ServiceAccount{}).
		Owns(&appsv1.Deployment{}).
		Owns(&rbac.Role{}).
		Owns(&networkingv1.NetworkPolicy{})
	if r.IsOpenShift {
		builder = builder.Owns(&rbac.RoleBinding{})

		labelsPredicate, err := predicate.LabelSelectorPredicate(metav1.LabelSelector{
			MatchLabels: map[string]string{
				"component":                    "model-registry",
				"app.kubernetes.io/created-by": "model-registry-operator",
			},
		})
		if err != nil {
			return err
		}
		builder = builder.Watches(
			&routev1.Route{},
			handler.EnqueueRequestsFromMapFunc(r.GetRegistryForRoute),
			pkgbuilder.WithPredicates(labelsPredicate))
		builder = builder.Watches(
			&rbac.ClusterRoleBinding{},
			handler.EnqueueRequestsFromMapFunc(r.GetRegistryForClusterRoleBinding),
			pkgbuilder.WithPredicates(labelsPredicate))
	}
	if r.HasIstio {
		if r.CreateAuthResources {
			builder = builder.Owns(CreateAuthConfig()).
				Owns(&security.AuthorizationPolicy{})
		}
		builder = builder.Owns(&networking.DestinationRule{}).
			Owns(&networking.Gateway{}).
			Owns(&networking.VirtualService{})
	}
	return builder.Complete(r)
}

// GetRegistryForRoute maps route name to model registry reconcile request
func (r *ModelRegistryReconciler) GetRegistryForRoute(ctx context.Context, object client.Object) []reconcile.Request {
	route := object.(*routev1.Route)

	labels := route.GetObjectMeta().GetLabels()
	name := labels["app"]
	if len(name) == 0 {
		logger := klog.FromContext(ctx)
		logger.Error(nil, "missing 'app' label in model registry route", "route", route.Name)
		return nil
	}

	namespace := labels["maistra.io/gateway-namespace"]
	if len(namespace) == 0 {
		// if label is not set, registry is in the same namespace as the route
		namespace = object.GetNamespace()
	}

	return []reconcile.Request{
		{NamespacedName: types.NamespacedName{Name: name, Namespace: namespace}},
	}
}

// GetRegistryForClusterRoleBinding maps role binding to model registry reconcile request
func (r *ModelRegistryReconciler) GetRegistryForClusterRoleBinding(ctx context.Context, object client.Object) []reconcile.Request {
	clusterRoleBinding := object.(*rbac.ClusterRoleBinding)

	logger := klog.FromContext(ctx)
	labels := clusterRoleBinding.GetObjectMeta().GetLabels()
	name := labels["app"]
	if len(name) == 0 {
		logger.Error(nil, "missing 'app' label in model registry clusterRoleBinding",
			"clusterRoleBinding", clusterRoleBinding.Name)
		return nil
	}

	namespace := labels["modelregistry.opendatahub.io/namespace"]
	if len(namespace) == 0 {
		logger.V(5).Info("missing 'modelregistry.opendatahub.io/namespace' label in model registry clusterRoleBinding",
			"clusterRoleBinding", clusterRoleBinding.Name)
		// ignore this cluster role binding
		return nil
	}

	return []reconcile.Request{
		{NamespacedName: types.NamespacedName{Name: name, Namespace: namespace}},
	}
}

// NOTE: There MUST be an empty newline at the end of this rbac permissions list, or role generation won't work!!!
// +kubebuilder:rbac:groups=modelregistry.opendatahub.io,resources=modelregistries,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=modelregistry.opendatahub.io,resources=modelregistries/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=modelregistry.opendatahub.io,resources=modelregistries/finalizers,verbs=update
// +kubebuilder:rbac:groups=core,resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=pods;pods/log,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=services;serviceaccounts,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=endpoints,verbs=get;list;watch
// +kubebuilder:rbac:groups=config.openshift.io,resources=ingresses,verbs=get;list;watch
// +kubebuilder:rbac:groups=route.openshift.io,resources=routes;routes/custom-host,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=user.openshift.io,resources=groups,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles;rolebindings;clusterrolebindings,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=authorino.kuadrant.io,resources=authconfigs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=security.istio.io,resources=authorizationpolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=networking.istio.io,resources=destinationrules,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=networking.istio.io,resources=gateways,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=networking.istio.io,resources=virtualservices,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=authentication.k8s.io,resources=tokenreviews,verbs=create
// +kubebuilder:rbac:groups=authorization.k8s.io,resources=subjectaccessreviews,verbs=create
// +kubebuilder:rbac:groups=networking.k8s.io,resources=networkpolicies,verbs=get;list;watch;create;update;patch;delete

func (r *ModelRegistryReconciler) updateRegistryResources(ctx context.Context, params *ModelRegistryParams, registry *modelregistryv1alpha1.ModelRegistry) (OperationResult, error) {
	var result, result2 OperationResult

	var err error
	result, err = r.createOrUpdateServiceAccount(ctx, params, registry, "serviceaccount.yaml.tmpl")
	if err != nil {
		return result, err
	}

	result2, err = r.createOrUpdateService(ctx, params, registry, "service.yaml.tmpl")
	if err != nil {
		return result2, err
	}
	if result2 != ResourceUnchanged {
		result = result2
	}

	result2, err = r.createOrUpdateDeployment(ctx, params, registry, "deployment.yaml.tmpl")
	if err != nil {
		return result2, err
	}
	if result2 != ResourceUnchanged {
		result = result2
	}

	result2, err = r.createOrUpdateRole(ctx, params, registry, "role.yaml.tmpl")
	if err != nil {
		return result2, err
	}
	if result2 != ResourceUnchanged {
		result = result2
	}

	if r.IsOpenShift {
		// create default group and role binding in OpenShift cluster
		result2, err = r.createOrUpdateGroup(ctx, params, registry, "group.yaml.tmpl")
		if err != nil {
			return result2, err
		}
		if result2 != ResourceUnchanged {
			result = result2
		}

		result2, err = r.createOrUpdateRoleBinding(ctx, params, registry, "role-binding.yaml.tmpl")
		if err != nil {
			return result2, err
		}
		if result2 != ResourceUnchanged {
			result = result2
		}

		// create simple openshift service route, if configured
		result2, err = r.createOrUpdateRoute(ctx, params, registry,
			"http-route.yaml.tmpl", registry.Spec.Rest.ServiceRoute)
		if err != nil {
			return result2, err
		}
		if result2 != ResourceUnchanged {
			result = result2
		}
	}

	if r.HasIstio {
		if registry.Spec.Istio != nil {
			result2, err = r.createOrUpdateIstioConfig(ctx, params, registry)
			if err != nil {
				return result2, err
			}
			if result2 != ResourceUnchanged {
				result = result2
			}
		} else {
			result2, err = r.deleteIstioConfig(ctx, params)
			if err != nil {
				return result2, err
			}
			if result2 != ResourceUnchanged {
				result = result2
			}
		}
	}

	// create or update oauth proxy config if enabled, delete if disabled
	result2, err = r.createOrUpdateOAuthConfig(ctx, params, registry)
	if err != nil {
		return result2, err
	}
	if result2 != ResourceUnchanged {
		result = result2
	}

	return result, nil
}

func (r *ModelRegistryReconciler) createOrUpdateIstioConfig(ctx context.Context, params *ModelRegistryParams, registry *modelregistryv1alpha1.ModelRegistry) (OperationResult, error) {
	var result, result2 OperationResult

	var err error
	result, err = r.createOrUpdateVirtualService(ctx, params, registry, "virtual-service.yaml.tmpl")
	if err != nil {
		return result, err
	}

	result2, err = r.createOrUpdateDestinationRule(ctx, params, registry, "destination-rule.yaml.tmpl")
	if err != nil {
		return result2, err
	}
	if result2 != ResourceUnchanged {
		result = result2
	}

	if r.CreateAuthResources {
		result2, err = r.createOrUpdateAuthorizationPolicy(ctx, params, registry, "authorino-authorization-policy.yaml.tmpl")
		if err != nil {
			return result2, err
		}
		if result2 != ResourceUnchanged {
			result = result2
		}

		result2, err = r.createOrUpdateAuthConfig(ctx, params, registry, "authconfig.yaml.tmpl")
		if err != nil {
			return result2, err
		}
		if result2 != ResourceUnchanged {
			result = result2
		}

	}

	if params.Spec.Istio.Gateway != nil {
		result2, err = r.createOrUpdateGateway(ctx, params, registry, "gateway.yaml.tmpl")
		if err != nil {
			return result2, err
		}
		if result2 != ResourceUnchanged {
			result = result2
		}

		if r.IsOpenShift {
			result2, err = r.createOrUpdateGatewayRoutes(ctx, params, registry, "gateway-route.yaml.tmpl")
			if err != nil {
				return result2, err
			}
			if result2 != ResourceUnchanged {
				result = result2
			}
		}
	} else {
		// remove gateway if it exists
		if err = r.deleteGatewayConfig(ctx, params); err != nil {
			return ResourceUpdated, err
		}
	}

	return result, nil
}

func (r *ModelRegistryReconciler) deleteIstioConfig(ctx context.Context, params *ModelRegistryParams) (OperationResult, error) {
	var err error

	objectMeta := metav1.ObjectMeta{Name: params.Name, Namespace: params.Namespace}
	virtualService := networking.VirtualService{ObjectMeta: objectMeta}
	if err = r.Client.Delete(ctx, &virtualService); client.IgnoreNotFound(err) != nil {
		return ResourceUpdated, err
	}

	destinationRule := networking.DestinationRule{ObjectMeta: objectMeta}
	if err = r.Client.Delete(ctx, &destinationRule); client.IgnoreNotFound(err) != nil {
		return ResourceUpdated, err
	}

	if r.CreateAuthResources {
		authorizationPolicy := security.AuthorizationPolicy{ObjectMeta: objectMeta}
		authorizationPolicy.Name = authorizationPolicy.Name + "-authorino"
		if err = r.Client.Delete(ctx, &authorizationPolicy); client.IgnoreNotFound(err) != nil {
			return ResourceUpdated, err
		}

		authConfig := CreateAuthConfig()
		authConfig.SetName(params.Name)
		authConfig.SetNamespace(params.Namespace)
		if err = r.Client.Delete(ctx, authConfig); client.IgnoreNotFound(err) != nil {
			return ResourceUpdated, err
		}
	}

	if err = r.deleteGatewayConfig(ctx, params); err != nil {
		return ResourceUpdated, err
	}

	return ResourceUnchanged, nil
}

func (r *ModelRegistryReconciler) createOrUpdateOAuthConfig(ctx context.Context, params *ModelRegistryParams,
	registry *modelregistryv1alpha1.ModelRegistry) (result OperationResult, err error) {

	result = ResourceUnchanged
	result2 := result

	// create oauth proxy resources
	if registry.Spec.OAuthProxy != nil {

		// create oauth proxy rolebinding
		result, err = r.createOrUpdateClusterRoleBinding(ctx, params, registry, "proxy-role-binding.yaml.tmpl")
		if err != nil {
			return result, err
		}

		// create oauth proxy service route if enabled, delete if disabled
		result2, err = r.createOrUpdateRoute(ctx, params, registry,
			"https-route.yaml.tmpl", registry.Spec.OAuthProxy.ServiceRoute)
		if err != nil {
			return result2, err
		}
		if result2 != ResourceUnchanged {
			result = result2
		}

		if registry.Spec.OAuthProxy.ServiceRoute == config.RouteEnabled {
			// create oauth proxy networkpolicy to ensure route is exposed
			result2, err = r.createOrUpdateNetworkPolicy(ctx, params, registry, "proxy-network-policy.yaml.tmpl")
			if err != nil {
				return result2, err
			}
			if result2 != ResourceUnchanged {
				result = result2
			}
		} else {
			// remove oauth proxy networkpolicy if it exists
			if err = r.deleteOAuthNetworkPolicy(ctx, params); err != nil {
				return result, err
			}
		}

	} else {
		// remove oauth proxy rolebinding if it exists
		if err = r.deleteOAuthClusterRoleBinding(ctx, params); err != nil {
			return result, err
		}
		// remove oauth proxy networkpolicy if it exists
		if err = r.deleteOAuthNetworkPolicy(ctx, params); err != nil {
			return result, err
		}
	}

	return result, nil
}

func (r *ModelRegistryReconciler) deleteGatewayConfig(ctx context.Context, params *ModelRegistryParams) error {
	gateway := networking.Gateway{ObjectMeta: metav1.ObjectMeta{Name: params.Name, Namespace: params.Namespace}}
	if err := r.Client.Delete(ctx, &gateway); client.IgnoreNotFound(err) != nil {
		return err
	}
	return r.deleteGatewayRoutes(ctx, params.Name)
}

func (r *ModelRegistryReconciler) deleteGatewayRoutes(ctx context.Context, name string) error {
	// delete all gateway routes
	labels := getRouteLabels(name)
	var list routev1.RouteList
	if err := r.Client.List(ctx, &list, labels); err != nil {
		return err
	}
	for _, route := range list.Items {
		if err := r.Client.Delete(ctx, &route); client.IgnoreNotFound(err) != nil {
			return err
		}
	}
	return nil
}

func getRouteLabels(name string) client.MatchingLabels {
	labels := client.MatchingLabels{
		"app":                     name,
		"component":               "model-registry",
		"maistra.io/gateway-name": name,
	}
	return labels
}

func (r *ModelRegistryReconciler) deleteOAuthClusterRoleBinding(ctx context.Context, params *ModelRegistryParams) error {
	roleBinding := rbac.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: params.Name + "-auth-delegator", Namespace: params.Namespace}}
	return client.IgnoreNotFound(r.Client.Delete(ctx, &roleBinding))
}

func (r *ModelRegistryReconciler) deleteOAuthNetworkPolicy(ctx context.Context, params *ModelRegistryParams) error {
	networkPolicy := networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: params.Name + "-https-route", Namespace: params.Namespace}}
	return client.IgnoreNotFound(r.Client.Delete(ctx, &networkPolicy))
}

func (r *ModelRegistryReconciler) createOrUpdateGatewayRoutes(ctx context.Context, params *ModelRegistryParams,
	registry *modelregistryv1alpha1.ModelRegistry, templateName string) (result OperationResult, err error) {
	result = ResourceUnchanged

	// get ingress gateway service
	var serviceList corev1.ServiceList
	labels := client.MatchingLabels{"istio": *params.Spec.Istio.Gateway.IstioIngress}
	if params.Spec.Istio.Gateway.ControlPlane != nil {
		labels["istio.io/rev"] = *params.Spec.Istio.Gateway.ControlPlane
	}
	if err = r.Client.List(ctx, &serviceList, labels); err != nil {
		return result, err
	}
	if len(serviceList.Items) != 1 {
		return result, fmt.Errorf("missing unique ingress gateway service with labels %s, found %d services",
			labels, len(serviceList.Items))
	}
	params.IngressService = &serviceList.Items[0]

	// create/update REST route
	result, err = r.handleGatewayRoute(ctx, params, templateName, "-rest",
		params.Spec.Istio.Gateway.Rest.GatewayRoute, params.Spec.Istio.Gateway.Rest.TLS)
	if err != nil {
		return result, err
	}

	// create/update gRPC route
	result2, err := r.handleGatewayRoute(ctx, params, templateName, "-grpc",
		params.Spec.Istio.Gateway.Grpc.GatewayRoute, params.Spec.Istio.Gateway.Grpc.TLS)
	if err != nil {
		return result2, err
	}
	if result2 != ResourceUnchanged {
		result = result2
	}

	return result, nil
}

func (r *ModelRegistryReconciler) handleGatewayRoute(ctx context.Context, params *ModelRegistryParams, templateName string,
	suffix string, gatewayRoute string, tls *modelregistryv1alpha1.TLSServerSettings) (result OperationResult, err error) {

	// route specific params
	params.Host = params.Name + suffix
	params.TLS = tls

	var route routev1.Route
	if err = r.Apply(params, templateName, &route); err != nil {
		return result, err
	}

	if gatewayRoute == config.RouteEnabled {
		result, err = r.createOrUpdate(ctx, &routev1.Route{}, &route)
		if err != nil {
			return result, err
		}
	} else {
		// delete the route if it exists
		if err = r.Client.Delete(ctx, &route); client.IgnoreNotFound(err) != nil {
			result = ResourceUpdated
			return result, err
		}
	}

	return result, nil
}

func (r *ModelRegistryReconciler) createOrUpdateGateway(ctx context.Context, params *ModelRegistryParams,
	registry *modelregistryv1alpha1.ModelRegistry, templateName string) (result OperationResult, err error) {

	result = ResourceUnchanged
	var gateway networking.Gateway
	if err = r.Apply(params, templateName, &gateway); err != nil {
		return result, err
	}
	if err = ctrl.SetControllerReference(registry, &gateway, r.Scheme); err != nil {
		return result, err
	}

	result, err = r.createOrUpdate(ctx, &networking.Gateway{}, &gateway)
	if err != nil {
		return result, err
	}

	return result, nil
}

func (r *ModelRegistryReconciler) createOrUpdateAuthConfig(ctx context.Context, params *ModelRegistryParams,
	registry *modelregistryv1alpha1.ModelRegistry, templateName string) (result OperationResult, err error) {
	// TODO: Audiences property was not being correctly updated to the default value in previous operator versions
	// Replace this with a conversion webhook in the future
	// Are AuthConfig audiences specified?
	if len(params.Spec.Istio.Audiences) == 0 {
		// use operator serviceaccount audiences by default
		params.Spec.Istio.Audiences = config.GetDefaultAudiences()
	}

	result = ResourceUnchanged
	authConfig := CreateAuthConfig()
	if err = r.Apply(params, templateName, authConfig); err != nil {
		return result, err
	}
	if err = ctrl.SetControllerReference(registry, authConfig, r.Scheme); err != nil {
		return result, err
	}

	// NOTE: AuthConfig CRD uses maps, which is not supported in k8s 3-way merge patch
	// use an Unstructured current object to force it to use a json merge patch instead
	current := CreateAuthConfig()
	result, err = r.createOrUpdate(ctx, current, authConfig)
	if err != nil {
		return result, err
	}
	return result, nil
}

func CreateAuthConfig() *unstructured.Unstructured {
	authConfig := unstructured.Unstructured{}
	authConfig.SetGroupVersionKind(schema.GroupVersionKind{Group: "authorino.kuadrant.io", Version: "v1beta3", Kind: "AuthConfig"})
	return &authConfig
}

func (r *ModelRegistryReconciler) createOrUpdateAuthorizationPolicy(ctx context.Context, params *ModelRegistryParams,
	registry *modelregistryv1alpha1.ModelRegistry, templateName string) (result OperationResult, err error) {
	result = ResourceUnchanged
	var authorizationPolicy security.AuthorizationPolicy
	if err = r.Apply(params, templateName, &authorizationPolicy); err != nil {
		return result, err
	}
	if err = ctrl.SetControllerReference(registry, &authorizationPolicy, r.Scheme); err != nil {
		return result, err
	}

	result, err = r.createOrUpdate(ctx, &security.AuthorizationPolicy{}, &authorizationPolicy)
	if err != nil {
		return result, err
	}
	return result, nil
}

func (r *ModelRegistryReconciler) createOrUpdateDestinationRule(ctx context.Context, params *ModelRegistryParams,
	registry *modelregistryv1alpha1.ModelRegistry, templateName string) (result OperationResult, err error) {
	result = ResourceUnchanged
	var destinationRule networking.DestinationRule
	if err = r.Apply(params, templateName, &destinationRule); err != nil {
		return result, err
	}
	if err = ctrl.SetControllerReference(registry, &destinationRule, r.Scheme); err != nil {
		return result, err
	}

	result, err = r.createOrUpdate(ctx, &networking.DestinationRule{}, &destinationRule)
	if err != nil {
		return result, err
	}
	return result, nil
}

func (r *ModelRegistryReconciler) createOrUpdateVirtualService(ctx context.Context, params *ModelRegistryParams,
	registry *modelregistryv1alpha1.ModelRegistry, templateName string) (result OperationResult, err error) {
	result = ResourceUnchanged
	var virtualService networking.VirtualService
	if err = r.Apply(params, templateName, &virtualService); err != nil {
		return result, err
	}
	if err = ctrl.SetControllerReference(registry, &virtualService, r.Scheme); err != nil {
		return result, err
	}

	result, err = r.createOrUpdate(ctx, &networking.VirtualService{}, &virtualService)
	if err != nil {
		return result, err
	}
	return result, nil
}

func (r *ModelRegistryReconciler) createOrUpdateRoleBinding(ctx context.Context, params *ModelRegistryParams,
	registry *modelregistryv1alpha1.ModelRegistry, templateName string) (result OperationResult, err error) {
	result = ResourceUnchanged
	var roleBinding rbac.RoleBinding
	if err = r.Apply(params, templateName, &roleBinding); err != nil {
		return result, err
	}
	if err = ctrl.SetControllerReference(registry, &roleBinding, r.Scheme); err != nil {
		return result, err
	}

	result, err = r.createOrUpdate(ctx, &rbac.RoleBinding{}, &roleBinding)
	if err != nil {
		return result, err
	}
	return result, nil
}

func (r *ModelRegistryReconciler) createOrUpdateClusterRoleBinding(ctx context.Context, params *ModelRegistryParams,
	registry *modelregistryv1alpha1.ModelRegistry, templateName string) (result OperationResult, err error) {
	result = ResourceUnchanged
	var roleBinding rbac.ClusterRoleBinding
	if err = r.Apply(params, templateName, &roleBinding); err != nil {
		return result, err
	}

	return r.createOrUpdate(ctx, &rbac.ClusterRoleBinding{}, &roleBinding)
}

func (r *ModelRegistryReconciler) createOrUpdateNetworkPolicy(ctx context.Context, params *ModelRegistryParams,
	registry *modelregistryv1alpha1.ModelRegistry, templateName string) (result OperationResult, err error) {
	result = ResourceUnchanged
	var networkPolicy networkingv1.NetworkPolicy
	if err = r.Apply(params, templateName, &networkPolicy); err != nil {
		return result, err
	}
	if err = ctrl.SetControllerReference(registry, &networkPolicy, r.Scheme); err != nil {
		return result, err
	}

	return r.createOrUpdate(ctx, &networkingv1.NetworkPolicy{}, &networkPolicy)
}

func (r *ModelRegistryReconciler) createOrUpdateRole(ctx context.Context, params *ModelRegistryParams,
	registry *modelregistryv1alpha1.ModelRegistry, templateName string) (result OperationResult, err error) {
	result = ResourceUnchanged
	var role rbac.Role
	if err = r.Apply(params, templateName, &role); err != nil {
		return result, err
	}
	if err = ctrl.SetControllerReference(registry, &role, r.Scheme); err != nil {
		return result, err
	}

	result, err = r.createOrUpdate(ctx, &rbac.Role{}, &role)
	if err != nil {
		return result, err
	}
	return result, nil
}

func (r *ModelRegistryReconciler) createOrUpdateGroup(ctx context.Context, params *ModelRegistryParams,
	_ *modelregistryv1alpha1.ModelRegistry, templateName string) (result OperationResult, err error) {
	result = ResourceUnchanged
	var group userv1.Group
	if err = r.Apply(params, templateName, &group); err != nil {
		return result, err
	}

	result, err = r.createOrUpdate(ctx, &userv1.Group{}, &group)
	if err != nil {
		return result, err
	}
	return result, nil
}

func (r *ModelRegistryReconciler) createOrUpdateDeployment(ctx context.Context, params *ModelRegistryParams,
	registry *modelregistryv1alpha1.ModelRegistry, templateName string) (result OperationResult, err error) {
	result = ResourceUnchanged
	var deployment appsv1.Deployment
	if err = r.Apply(params, templateName, &deployment); err != nil {
		return result, err
	}
	if err = ctrl.SetControllerReference(registry, &deployment, r.Scheme); err != nil {
		return result, err
	}

	result, err = r.createOrUpdate(ctx, &appsv1.Deployment{}, &deployment)
	if err != nil {
		return result, err
	}
	return result, nil
}

func (r *ModelRegistryReconciler) createOrUpdateRoute(ctx context.Context, params *ModelRegistryParams,
	registry *modelregistryv1alpha1.ModelRegistry, templateName string, serviceRoute string) (result OperationResult, err error) {
	result = ResourceUnchanged
	var route routev1.Route
	if err = r.Apply(params, templateName, &route); err != nil {
		return result, err
	}
	if err = ctrl.SetControllerReference(registry, &route, r.Scheme); err != nil {
		return result, err
	}

	if serviceRoute == config.RouteEnabled {
		if result, err = r.createOrUpdate(ctx, &routev1.Route{}, &route); err != nil {
			return result, err
		}
	} else {
		// delete the route if it exists
		if err = r.Client.Delete(ctx, &route); client.IgnoreNotFound(err) != nil {
			result = ResourceUpdated
			return result, err
		}
	}

	return result, nil
}

func (r *ModelRegistryReconciler) createOrUpdateService(ctx context.Context, params *ModelRegistryParams,
	registry *modelregistryv1alpha1.ModelRegistry, templateName string) (result OperationResult, err error) {
	result = ResourceUnchanged
	var service corev1.Service
	if err = r.Apply(params, templateName, &service); err != nil {
		return result, err
	}
	if err = ctrl.SetControllerReference(registry, &service, r.Scheme); err != nil {
		return result, err
	}

	// copy display name and description from MR to the Service
	if name, ok := registry.Annotations[DisplayNameAnnotation]; ok {
		if service.Annotations == nil {
			service.Annotations = make(map[string]string)
		}
		service.Annotations[DisplayNameAnnotation] = name
	}
	if description, ok := registry.Annotations[DescriptionAnnotation]; ok {
		if service.Annotations == nil {
			service.Annotations = make(map[string]string)
		}
		service.Annotations[DescriptionAnnotation] = description
	}

	if result, err = r.createOrUpdate(ctx, &corev1.Service{}, &service); err != nil {
		return result, err
	}
	return result, nil
}

func (r *ModelRegistryReconciler) createOrUpdateServiceAccount(ctx context.Context, params *ModelRegistryParams,
	registry *modelregistryv1alpha1.ModelRegistry, templateName string) (result OperationResult, err error) {
	result = ResourceUnchanged
	var sa corev1.ServiceAccount
	if err = r.Apply(params, templateName, &sa); err != nil {
		return result, err
	}
	if err = ctrl.SetControllerReference(registry, &sa, r.Scheme); err != nil {
		return result, err
	}

	if result, err = r.createOrUpdate(ctx, &corev1.ServiceAccount{}, &sa); err != nil {
		return result, err
	}
	return result, nil
}

//go:generate go-enum -type=OperationResult
type OperationResult int

const (
	// ResourceUnchanged means that the resource has not been changed.
	ResourceUnchanged OperationResult = iota
	// ResourceCreated means that a new resource is created.
	ResourceCreated
	// ResourceUpdated means that an existing resource is updated.
	ResourceUpdated
)

func (r *ModelRegistryReconciler) createOrUpdate(ctx context.Context, currObj client.Object, newObj client.Object) (OperationResult, error) {
	log := klog.FromContext(ctx)
	result := ResourceUnchanged

	key := client.ObjectKeyFromObject(newObj)
	gvk := newObj.GetObjectKind().GroupVersionKind()
	name := newObj.GetName()

	if err := r.Client.Get(ctx, key, currObj); err != nil {
		if client.IgnoreNotFound(err) == nil {
			// create object
			result = ResourceCreated
			log.Info("creating", "kind", gvk, "name", name)
			// save last applied config in annotation similar to kubectl apply
			if err := patch.DefaultAnnotator.SetLastAppliedAnnotation(newObj); err != nil {
				return result, err
			}
			return result, r.Client.Create(ctx, newObj)
		}
		// get error
		return result, err
	}

	// hack: envtest is missing typemeta for some reason, hence the ignores for apiVersion and kind!!!
	// create a patch by comparing objects
	patchResult, err := patch.DefaultPatchMaker.Calculate(currObj, newObj, patch.IgnoreStatusFields(),
		patch.IgnoreField("apiVersion"), patch.IgnoreField("kind"))
	if err != nil {
		return result, err
	}
	if !patchResult.IsEmpty() {
		// update object
		result = ResourceUpdated
		log.Info("updating", "kind", gvk, "name", name)
		// update last applied config in annotation
		if err := patch.DefaultAnnotator.SetLastAppliedAnnotation(newObj); err != nil {
			return result, err
		}
		// set metadata.resourceVersion if present
		if len(currObj.GetResourceVersion()) != 0 {
			newObj.SetResourceVersion(currObj.GetResourceVersion())
		}
		return result, r.Client.Update(ctx, newObj)
	}

	return result, nil
}

// finalizeMemcached will perform the required operations before delete the CR.
func (r *ModelRegistryReconciler) doFinalizerOperationsForModelRegistry(ctx context.Context, registry *modelregistryv1alpha1.ModelRegistry) error {
	// TODO(user): Add the cleanup steps that the operator
	// needs to do before the CR can be deleted. Examples
	// of finalizers include performing backups and deleting
	// resources that are not owned by this CR, like a PVC.

	// delete cross-namespace resources
	if err := r.Client.Delete(ctx, &userv1.Group{ObjectMeta: metav1.ObjectMeta{Name: registry.Name + "-users"}}); client.IgnoreNotFound(err) != nil {
		return err
	}
	if err := r.deleteGatewayRoutes(ctx, registry.Name); err != nil {
		return err
	}

	// Note: It is not recommended to use finalizers with the purpose of delete resources which are
	// created and managed in the reconciliation. These, such as the Deployment created on this reconcile,
	// are defined as depended on the custom resource. See that we use the method ctrl.SetControllerReference.
	// to set the ownerRef which means that the Deployment will be deleted by the Kubernetes API.
	// More info: https://kubernetes.io/docs/tasks/administer-cluster/use-cascading-deletion/

	// The following implementation will raise an event
	r.Recorder.Event(registry, "Warning", "Deleting",
		fmt.Sprintf("Custom Resource %s is being deleted from the namespace %s",
			registry.Name,
			registry.Namespace))

	return nil
}

// ModelRegistryParams is a wrapper for template parameters
type ModelRegistryParams struct {
	Name         string
	Namespace    string
	Spec         *modelregistryv1alpha1.ModelRegistrySpec
	OriginalSpec *modelregistryv1alpha1.ModelRegistrySpec

	// gateway route parameters
	Host           string
	IngressService *corev1.Service
	TLS            *modelregistryv1alpha1.TLSServerSettings
}

// Apply executes given template name with params
func (r *ModelRegistryReconciler) Apply(params *ModelRegistryParams, templateName string, object interface{}) error {
	builder := strings.Builder{}
	err := r.Template.ExecuteTemplate(&builder, templateName, params)
	if err != nil {
		return fmt.Errorf("error parsing templates %w", err)
	}
	err = yaml.UnmarshalStrict([]byte(builder.String()), object)
	if err != nil {
		return fmt.Errorf("error creating %T for model registry %s in namespace %s", object, params.Name, params.Namespace)
	}
	return nil
}

func (r *ModelRegistryReconciler) logResultAsEvent(registry *modelregistryv1alpha1.ModelRegistry, result OperationResult) {
	switch result {
	case ResourceCreated:
		r.Recorder.Event(registry, "Normal", "ServiceCreated",
			fmt.Sprintf("Created service for custom resource %s in namespace %s",
				registry.Name,
				registry.Namespace))
	case ResourceUpdated:
		r.Recorder.Event(registry, "Normal", "ServiceUpdated",
			fmt.Sprintf("Updated service for custom resource %s in namespace %s",
				registry.Name,
				registry.Namespace))
	case ResourceUnchanged:
		// ignore
	}
}

func (r *ModelRegistryReconciler) handleReconcileErrors(ctx context.Context, registry *modelregistryv1alpha1.ModelRegistry, result ctrl.Result, err error) (ctrl.Result, error) {
	if err != nil {
		// set Available condition to false, and message to err message
		meta.SetStatusCondition(&registry.Status.Conditions, metav1.Condition{Type: ConditionTypeAvailable,
			Status: metav1.ConditionFalse, Reason: ReasonDeploymentUnavailable,
			Message: fmt.Sprintf("unexpected reconcile error: %v", err)})
		if err1 := r.Status().Update(ctx, registry); err1 != nil {
			// log update error in operator logs and return original error
			klog.FromContext(ctx).Error(err1, "Failed to update modelRegistry status")
		}
	}
	return result, err
}
