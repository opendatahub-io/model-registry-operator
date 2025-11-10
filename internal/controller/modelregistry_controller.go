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
	"strings"
	"text/template"

	networkingv1 "k8s.io/api/networking/v1"

	"k8s.io/client-go/kubernetes"

	"github.com/go-logr/logr"
	"github.com/opendatahub-io/model-registry-operator/api/v1beta1"
	"github.com/opendatahub-io/model-registry-operator/internal/controller/config"
	"github.com/opendatahub-io/model-registry-operator/internal/utils"
	routev1 "github.com/openshift/api/route/v1"
	userv1 "github.com/openshift/api/user/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbac "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
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
	ClientSet      *kubernetes.Clientset
	Scheme         *runtime.Scheme
	Recorder       record.EventRecorder
	Log            logr.Logger
	Template       *template.Template
	EnableWebhooks bool
	IsOpenShift    bool
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

	modelRegistry := &v1beta1.ModelRegistry{}
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

	// Migrate OAuth proxy to kube-rbac-proxy for existing registries
	// This handles registries created before the migration or updated outside the webhook
	if modelRegistry.Spec.OAuthProxy != nil {
		log.Info("Migrating OAuthProxy to KubeRBACProxy")
		modelRegistry.MigrateOAuthProxyToKubeRBACProxy()
		if err = r.Update(ctx, modelRegistry); err != nil {
			log.Error(err, "Failed to update modelRegistry after OAuth proxy migration")
			return ctrl.Result{}, err
		}
		// Requeue to process with the migrated configuration
		return ctrl.Result{Requeue: true}, nil
	}

	// remember original spec
	originalSpec := modelRegistry.Spec.DeepCopy()
	// set runtime default properties in memory for reconciliation
	modelRegistry.RuntimeDefaults()

	// set defaults and validate if not using webhooks
	if !r.EnableWebhooks {
		modelRegistry.Default()
		_, err = modelRegistry.ValidateRegistry()
		if err != nil {
			log.Error(err, "validate registry error")
			return ctrl.Result{}, err
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
		For(&v1beta1.ModelRegistry{}).
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
// +kubebuilder:rbac:groups=core,resources=configmaps,verbs=create;get;list;watch;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=secrets,verbs=create;get;list;watch;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=persistentvolumeclaims,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=config.openshift.io,resources=ingresses,verbs=get;list;watch
// +kubebuilder:rbac:groups=route.openshift.io,resources=routes;routes/custom-host,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=user.openshift.io,resources=groups,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles;rolebindings;clusterrolebindings,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=authentication.k8s.io,resources=tokenreviews,verbs=create
// +kubebuilder:rbac:groups=authorization.k8s.io,resources=subjectaccessreviews,verbs=create
// +kubebuilder:rbac:groups=networking.k8s.io,resources=networkpolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=migration.k8s.io,resources=storageversionmigrations,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apiextensions.k8s.io,resources=customresourcedefinitions,verbs=get;list;watch
// +kubebuilder:rbac:groups=components.platform.opendatahub.io,resources=modelregistries,verbs=get;list;watch

func (r *ModelRegistryReconciler) updateRegistryResources(ctx context.Context, params *ModelRegistryParams, registry *v1beta1.ModelRegistry) (OperationResult, error) {

	//log := klog.FromContext(ctx)

	var result, result2 OperationResult

	var err error

	if registry.Spec.Postgres != nil && registry.Spec.Postgres.GenerateDeployment != nil && *registry.Spec.Postgres.GenerateDeployment {
		result, err = r.createOrUpdatePostgres(ctx, params, registry)
		if err != nil {
			return result, err
		}
	}

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

	// create or update kube-rbac-proxy config if enabled, delete if disabled
	// This also handles cleanup of OAuth proxy resources since they share the same
	// ClusterRoleBinding, Route, and NetworkPolicy names
	result2, err = r.createOrUpdateKubeRBACProxyConfig(ctx, params, registry)
	if err != nil {
		return result2, err
	}
	if result2 != ResourceUnchanged {
		result = result2
	}

	// Clean up old catalog resources from before it was moved to ModelCatalogReconciler
	if err := r.deleteOldCatalogResources(ctx, params); err != nil {
		return result, err
	}

	return result, nil
}

func (r *ModelRegistryReconciler) createOrUpdatePostgres(ctx context.Context, params *ModelRegistryParams,
	registry *v1beta1.ModelRegistry) (result OperationResult, err error) {
	log := klog.FromContext(ctx)
	log.Info("Creating or updating postgres database")
	result = ResourceUnchanged
	var secret corev1.Secret
	secretName := params.Name + "-postgres-credentials"
	err = r.Get(ctx, types.NamespacedName{Name: secretName, Namespace: params.Namespace}, &secret)
	if err != nil {
		if errors.IsNotFound(err) {
			// Create the secret
			log.Info("Creating postgres secret", "secret", secretName)
			secret = corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      secretName,
					Namespace: params.Namespace,
				},
				StringData: map[string]string{
					"username": "modelregistry",
					"password": utils.RandBytes(16),
				},
			}
			if err = ctrl.SetControllerReference(registry, &secret, r.Scheme); err != nil {
				log.Error(err, "Failed to set controller reference on secret")
				return result, err
			}
			if result, err = r.createOrUpdate(ctx, &corev1.Secret{}, &secret); err != nil {
				log.Error(err, "Failed to create or update secret")
				return result, err
			}
		} else {
			log.Error(err, "Failed to get secret")
			return result, err
		}
	}

	log.Info("Creating or updating postgres PVC")
	var pvc corev1.PersistentVolumeClaim
	if err = r.Apply(params, "postgres-pvc.yaml.tmpl", &pvc); err != nil {
		log.Error(err, "Failed to apply postgres PVC template")
		return result, err
	}
	if err = ctrl.SetControllerReference(registry, &pvc, r.Scheme); err != nil {
		log.Error(err, "Failed to set controller reference on PVC")
		return result, err
	}
	if _, err = r.createOrUpdate(ctx, &corev1.PersistentVolumeClaim{}, &pvc); err != nil {
		log.Error(err, "Failed to create or update PVC")
		return result, err
	}

	log.Info("Creating or updating postgres deployment")
	var deployment appsv1.Deployment
	if err = r.Apply(params, "postgres-deployment.yaml.tmpl", &deployment); err != nil {
		log.Error(err, "Failed to apply postgres deployment template")
		return result, err
	}
	if err = ctrl.SetControllerReference(registry, &deployment, r.Scheme); err != nil {
		log.Error(err, "Failed to set controller reference on deployment")
		return result, err
	}
	if _, err = r.createOrUpdate(ctx, &appsv1.Deployment{}, &deployment); err != nil {
		// Check if the error is due to immutable field conflicts
		if errors.IsForbidden(err) || (errors.IsInvalid(err) && strings.Contains(err.Error(), "field is immutable")) {
			log.Info("deleting postgres deployment due to immutable field conflicts", "name", deployment.Name, "error", err.Error())

			// Get the existing deployment to delete it
			var existingDeployment appsv1.Deployment
			key := client.ObjectKeyFromObject(&deployment)
			if getErr := r.Client.Get(ctx, key, &existingDeployment); getErr != nil {
				log.Error(getErr, "Failed to get existing postgres deployment for deletion")
				return result, getErr
			}

			// Delete the existing deployment
			if deleteErr := r.Client.Delete(ctx, &existingDeployment); deleteErr != nil {
				log.Error(deleteErr, "Failed to delete existing postgres deployment")
				return result, deleteErr
			}

			// Return to trigger recreation in next reconcile
			log.Info("postgres deployment deleted, will be recreated in next reconcile")
			return ResourceUpdated, nil
		}
		log.Error(err, "Failed to create or update deployment")
		return result, err
	}

	log.Info("Creating or updating postgres service")
	var service corev1.Service
	if err = r.Apply(params, "postgres-service.yaml.tmpl", &service); err != nil {
		log.Error(err, "Failed to apply postgres service template")
		return result, err
	}
	if err = ctrl.SetControllerReference(registry, &service, r.Scheme); err != nil {
		log.Error(err, "Failed to set controller reference on service")
		return result, err
	}
	if _, err = r.createOrUpdate(ctx, &corev1.Service{}, &service); err != nil {
		log.Error(err, "Failed to create or update service")
		return result, err
	}

	// Update the spec in memory
	log.Info("Updating spec in memory with postgres details")
	port := int32(5432)
	registry.Spec.Postgres = &v1beta1.PostgresConfig{
		Host:     params.Name + "-postgres",
		Port:     &port,
		Username: "modelregistry",
		Database: "model_registry",
		PasswordSecret: &v1beta1.SecretKeyValue{
			Name: secretName,
			Key:  "password",
		},
	}

	return result, nil
}

func (r *ModelRegistryReconciler) createOrUpdateRoleBinding(ctx context.Context, params *ModelRegistryParams,
	registry *v1beta1.ModelRegistry, templateName string) (result OperationResult, err error) {
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
	_ *v1beta1.ModelRegistry, templateName string) (result OperationResult, err error) {
	result = ResourceUnchanged
	var roleBinding rbac.ClusterRoleBinding
	if err = r.Apply(params, templateName, &roleBinding); err != nil {
		return result, err
	}

	return r.createOrUpdate(ctx, &rbac.ClusterRoleBinding{}, &roleBinding)
}

func (r *ModelRegistryReconciler) createOrUpdateNetworkPolicy(ctx context.Context, params *ModelRegistryParams,
	registry *v1beta1.ModelRegistry, templateName string) (result OperationResult, err error) {
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
	registry *v1beta1.ModelRegistry, templateName string) (result OperationResult, err error) {
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
	_ *v1beta1.ModelRegistry, templateName string) (result OperationResult, err error) {
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
	registry *v1beta1.ModelRegistry, templateName string) (result OperationResult, err error) {
	log := klog.FromContext(ctx)
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
		// Check if the error is due to immutable field conflicts
		if errors.IsForbidden(err) || (errors.IsInvalid(err) && strings.Contains(err.Error(), "field is immutable")) {
			log.Info("deleting deployment due to immutable field conflicts", "name", deployment.Name, "error", err.Error())

			// Get the existing deployment to delete it
			var existingDeployment appsv1.Deployment
			key := client.ObjectKeyFromObject(&deployment)
			if getErr := r.Client.Get(ctx, key, &existingDeployment); getErr != nil {
				return result, getErr
			}

			// Delete the existing deployment
			if deleteErr := r.Client.Delete(ctx, &existingDeployment); deleteErr != nil {
				return result, deleteErr
			}

			// Return to trigger recreation in next reconcile
			return ResourceUpdated, nil
		}
		return result, err
	}
	return result, nil
}

func (r *ModelRegistryReconciler) createOrUpdateRoute(ctx context.Context, params *ModelRegistryParams,
	registry *v1beta1.ModelRegistry, templateName string, serviceRoute string) (result OperationResult, err error) {
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
	registry *v1beta1.ModelRegistry, templateName string) (result OperationResult, err error) {
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
	registry *v1beta1.ModelRegistry, templateName string) (result OperationResult, err error) {
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

func (r *ModelRegistryReconciler) ensureConfigMapExists(ctx context.Context, params *ModelRegistryParams, _ *v1beta1.ModelRegistry, templateName string) (OperationResult, error) {
	result := ResourceUnchanged
	var cm corev1.ConfigMap
	if err := r.Apply(params, templateName, &cm); err != nil {
		return result, err
	}

	result, err := r.createIfNotExists(ctx, &corev1.ConfigMap{}, &cm)
	if err != nil {
		return result, err
	}
	return result, nil
}

func (r *ModelRegistryReconciler) ensureSecretExists(ctx context.Context, params *ModelRegistryParams, _ *v1beta1.ModelRegistry, templateName string) (OperationResult, error) {
	result := ResourceUnchanged
	var secret corev1.Secret
	if err := r.Apply(params, templateName, &secret); err != nil {
		return result, err
	}

	result, err := r.createIfNotExists(ctx, &corev1.Secret{}, &secret)
	if err != nil {
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
	resourceManager := &ResourceManager{Client: r.Client}
	return resourceManager.CreateOrUpdate(ctx, currObj, newObj)
}

func (r *ModelRegistryReconciler) createIfNotExists(ctx context.Context, currObj client.Object, newObj client.Object) (OperationResult, error) {
	resourceManager := &ResourceManager{Client: r.Client}
	return resourceManager.CreateIfNotExists(ctx, currObj, newObj)
}

// finalizeMemcached will perform the required operations before delete the CR.
func (r *ModelRegistryReconciler) doFinalizerOperationsForModelRegistry(ctx context.Context, registry *v1beta1.ModelRegistry) error {
	// TODO(user): Add the cleanup steps that the operator
	// needs to do before the CR can be deleted. Examples
	// of finalizers include performing backups and deleting
	// resources that are not owned by this CR, like a PVC.

	// delete cross-namespace resources
	if err := r.Client.Delete(ctx, &userv1.Group{ObjectMeta: metav1.ObjectMeta{Name: registry.Name + "-users"}}); client.IgnoreNotFound(err) != nil {
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
	Spec         *v1beta1.ModelRegistrySpec
	OriginalSpec *v1beta1.ModelRegistrySpec

	// gateway route parameters
	Host           string
	IngressService *corev1.Service
	//TLS            *common.TLSServerSettings
}

// Apply executes given template name with params
func (r *ModelRegistryReconciler) Apply(params *ModelRegistryParams, templateName string, object any) error {
	templateApplier := &TemplateApplier{
		Template:    r.Template,
		IsOpenShift: r.IsOpenShift,
	}
	return templateApplier.Apply(params, templateName, object)
}

func (r *ModelRegistryReconciler) logResultAsEvent(registry *v1beta1.ModelRegistry, result OperationResult) {
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

func (r *ModelRegistryReconciler) handleReconcileErrors(ctx context.Context, registry *v1beta1.ModelRegistry, result ctrl.Result, err error) (ctrl.Result, error) {
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

// deleteOldCatalogResources removes old catalog resources that were created before the ModelCatalogReconciler.
func (r *ModelRegistryReconciler) deleteOldCatalogResources(ctx context.Context, params *ModelRegistryParams) error {
	log := klog.FromContext(ctx)

	deleteObject := func(name string, obj client.Object) error {
		nsName := client.ObjectKey{
			Namespace: params.Namespace,
			Name:      name,
		}
		err := r.Client.Get(ctx, nsName, obj)
		if err != nil {
			return client.IgnoreNotFound(err)
		}

		return r.Client.Delete(ctx, obj)
	}

	// Delete old catalog deployment
	if err := deleteObject(params.Name+"-catalog", &appsv1.Deployment{}); err != nil {
		log.V(1).Info("Failed to delete old catalog deployment", "error", err)
	}

	// Delete old catalog service
	if err := deleteObject(params.Name+"-catalog", &corev1.Service{}); err != nil {
		log.V(1).Info("Failed to delete old catalog service", "error", err)
	}

	if r.IsOpenShift {
		// Delete old catalog route
		if err := deleteObject(params.Name+"-catalog-https", &routev1.Route{}); err != nil {
			log.V(1).Info("Failed to delete old catalog route", "error", err)
		}

		// Delete old catalog network policy
		if err := deleteObject(params.Name+"-catalog-https-route", &networkingv1.NetworkPolicy{}); err != nil {
			log.V(1).Info("Failed to delete old catalog network policy", "error", err)
		}
	}

	return nil
}
