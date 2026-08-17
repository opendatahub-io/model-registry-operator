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
	"fmt"
	"os"
	"path/filepath"
	"time"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	klog "sigs.k8s.io/controller-runtime/pkg/log"

	aihubv1alpha1 "github.com/opendatahub-io/model-registry-operator/api/aihub/v1alpha1"
	catalogv1alpha1 "github.com/opendatahub-io/model-registry-operator/api/catalog/v1alpha1"
	"github.com/opendatahub-io/model-registry-operator/internal/controller/config"
	"github.com/opendatahub-io/odh-platform-utilities/api/common"
	"github.com/opendatahub-io/odh-platform-utilities/pkg/controller/conditions"
	"github.com/opendatahub-io/odh-platform-utilities/pkg/deploy"
	"github.com/opendatahub-io/odh-platform-utilities/pkg/render/kustomize"
)

const (
	aihubFinalizer        = "aihub.opendatahub.io/finalizer"
	catalogCRName         = "catalog"
	childDeploymentName   = "model-registry-operator-controller-manager"
	catalogDeploymentName = "catalog-controller-manager"
	childManagerContainer = "manager"

	// ConditionModelRegistryReady tracks whether the child model-registry
	// operator Deployment is available.
	ConditionModelRegistryReady = "ModelRegistryReady"

	// ConditionCatalogReady tracks whether the catalog operator Deployment
	// is available.
	ConditionCatalogReady = "CatalogReady"

	// Platform version ConfigMap (created by the orchestrator in the
	// application namespace).
	platformVersionConfigMap    = "odh-aihub-config"
	platformVersionConfigMapKey = "platformVersion"
)

// clusterScopedKinds lists the kinds whose metadata.namespace must be cleared
// after kustomize rendering (the namespace transformer may stamp it incorrectly).
var clusterScopedKinds = map[string]bool{
	"CustomResourceDefinition":       true,
	"ClusterRole":                    true,
	"ClusterRoleBinding":             true,
	"ValidatingWebhookConfiguration": true,
	"MutatingWebhookConfiguration":   true,
	"Namespace":                      true,
}

// ResourceDeployer applies rendered resources to the cluster. Backed by
// odh-platform-utilities pkg/deploy.Deployer in production; mocked in tests.
type ResourceDeployer interface {
	Deploy(ctx context.Context, input deploy.DeployInput) error
}

type AIHubReconciler struct {
	client.Client
	Scheme                *runtime.Scheme
	ManifestsTemplatePath string
	Getenv                func(string) string
	Deployer              ResourceDeployer
	// APIReader is an uncached client.Reader for reading objects that live
	// outside the label-scoped manager cache (e.g. the platform version
	// ConfigMap created by the orchestrator).
	APIReader client.Reader
}

// newAIHubConditionManager creates a conditions.Manager for the AIHub CR.
// The happy condition is Ready; ProvisioningSucceeded,
// ConditionModelRegistryReady and ConditionCatalogReady are dependents.
// Degraded is set independently and is NOT registered as a dependent
// (matching the kserve precedent).
func newAIHubConditionManager(aihub *aihubv1alpha1.AIHub) *conditions.Manager {
	return conditions.NewManager(aihub,
		string(common.ConditionTypeReady),
		string(common.ConditionTypeProvisioningSucceeded),
		ConditionModelRegistryReady,
		ConditionCatalogReady,
	)
}

func (r *AIHubReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := klog.FromContext(ctx)

	// 1. Get the AIHub singleton.
	aihub := &aihubv1alpha1.AIHub{}
	if err := r.Get(ctx, req.NamespacedName, aihub); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// 2. Singleton guard.
	if aihub.Name != "default" {
		log.Info("ignoring non-singleton AIHub", "name", aihub.Name)
		return ctrl.Result{}, nil
	}

	// Handle deletion: run ordered cleanup, then release the finalizer.
	if !aihub.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(aihub, aihubFinalizer) {
			done, err := r.cleanupOnDelete(ctx, aihub)
			if err != nil {
				return ctrl.Result{}, err
			}
			if !done {
				// Dependent (Catalog) not fully removed yet; wait.
				return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
			}
			controllerutil.RemoveFinalizer(aihub, aihubFinalizer)
			if err := r.Update(ctx, aihub); err != nil {
				return ctrl.Result{}, fmt.Errorf("removing finalizer: %w", err)
			}
		}
		return ctrl.Result{}, nil
	}

	// Ensure the finalizer is present before provisioning.
	if controllerutil.AddFinalizer(aihub, aihubFinalizer) {
		if err := r.Update(ctx, aihub); err != nil {
			return ctrl.Result{}, fmt.Errorf("adding finalizer: %w", err)
		}
	}

	spec := aihub.Spec
	log.Info("reconciling AIHub", "applicationNamespace", spec.ApplicationNamespace, "instancesNamespace", spec.InstancesNamespace)

	condMgr := newAIHubConditionManager(aihub)

	// 3. Resolve child images from environment.
	getenv := r.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	images := ResolveChildImages(getenv)

	// 4. Render the model-registry child operator manifests.
	renderPath := filepath.Join(r.ManifestsTemplatePath, "modelregistry", "overlays", "odh")
	resources, err := kustomize.Render(renderPath, nil, kustomize.WithNamespace(spec.ApplicationNamespace))
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("rendering model-registry manifests: %w", err)
	}

	// 5. Stamp the child operator Deployment and strip cluster-scoped namespaces.
	for i := range resources {
		kind := resources[i].GetKind()

		// Clear namespace on cluster-scoped kinds.
		if clusterScopedKinds[kind] {
			resources[i].SetNamespace("")
		}

		// Stamp the child operator Deployments.
		if kind == "Deployment" {
			name := resources[i].GetName()
			if name == childDeploymentName || name == catalogDeploymentName {
				if err := stampChildOperatorDeployment(&resources[i], images, spec.InstancesNamespace); err != nil {
					return ctrl.Result{}, fmt.Errorf("stamping child operator deployment %s: %w", name, err)
				}
			}
		}
	}

	// 6. Apply all rendered resources via the Deployer (SSA, CRD-first ordering).
	if err := r.Deployer.Deploy(ctx, deploy.DeployInput{
		Client:    r.Client,
		Owner:     aihub,
		Resources: resources,
	}); err != nil {
		condMgr.MarkFalse(string(common.ConditionTypeProvisioningSucceeded),
			conditions.WithReason("DeployFailed"),
			conditions.WithMessage("%s", err.Error()))
		condMgr.MarkTrue(string(common.ConditionTypeDegraded),
			conditions.WithSeverity(common.ConditionSeverityError),
			conditions.WithReason("ProvisioningFailed"),
			conditions.WithMessage("%s", err.Error()))
		// Write status before returning the error.
		_ = r.updateStatus(ctx, aihub, condMgr)
		return ctrl.Result{}, fmt.Errorf("deploying child operator resources: %w", err)
	}

	condMgr.MarkTrue(string(common.ConditionTypeProvisioningSucceeded),
		conditions.WithReason("AllResourcesApplied"))

	// 7. Check child Deployment availability (model-registry operator).
	ready, requeue, err := r.checkChildDeploymentReady(ctx, spec.ApplicationNamespace, childDeploymentName, condMgr, ConditionModelRegistryReady)
	if err != nil {
		return ctrl.Result{}, err
	}
	if requeue {
		if sErr := r.updateStatus(ctx, aihub, condMgr); sErr != nil {
			return ctrl.Result{}, sErr
		}
		return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
	}
	if ready {
		condMgr.MarkTrue(ConditionModelRegistryReady,
			conditions.WithReason("AllDeploymentsAvailable"))
	}

	// 8. Check child Deployment availability (catalog operator).
	ready, requeue, err = r.checkChildDeploymentReady(ctx, spec.ApplicationNamespace, catalogDeploymentName, condMgr, ConditionCatalogReady)
	if err != nil {
		return ctrl.Result{}, err
	}
	if requeue {
		if sErr := r.updateStatus(ctx, aihub, condMgr); sErr != nil {
			return ctrl.Result{}, sErr
		}
		return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
	}
	if ready {
		condMgr.MarkTrue(ConditionCatalogReady,
			conditions.WithReason("AllDeploymentsAvailable"))
	}

	// 9. Create the singleton Catalog CR if absent.
	// The catalog operator (and its validating webhook with failurePolicy=Fail)
	// must be Available before the Catalog CR is created, otherwise the webhook
	// has no backing endpoints and rejects the create.
	newCatalog := &catalogv1alpha1.Catalog{
		ObjectMeta: metav1.ObjectMeta{
			Name:      catalogCRName,
			Namespace: spec.InstancesNamespace,
		},
	}
	newCatalog.SetGroupVersionKind(catalogv1alpha1.GroupVersion.WithKind("Catalog"))
	if err := ctrl.SetControllerReference(aihub, newCatalog, r.Scheme); err != nil {
		return ctrl.Result{}, fmt.Errorf("setting Catalog owner reference: %w", err)
	}
	rm := ResourceManager{Client: r.Client}
	currCatalog := &catalogv1alpha1.Catalog{}
	if _, err := rm.CreateIfNotExists(ctx, currCatalog, newCatalog); err != nil {
		return ctrl.Result{}, fmt.Errorf("ensuring Catalog CR: %w", err)
	}

	condMgr.MarkFalse(string(common.ConditionTypeDegraded),
		conditions.WithSeverity(common.ConditionSeverityInfo),
		conditions.WithReason("NoDegradation"))

	if sErr := r.updateStatus(ctx, aihub, condMgr); sErr != nil {
		return ctrl.Result{}, sErr
	}
	log.Info("AIHub reconciliation complete")
	return ctrl.Result{}, nil
}

// cleanupOnDelete performs ordered teardown before the AIHub finalizer is
// released. It deletes the singleton Catalog CR first and waits for it to be
// fully removed, so the catalog operator (when present) can finalize its
// operands before its own Deployment is GC'd by owner-reference cleanup.
// When no catalog operator exists the Catalog has no finalizer and disappears
// immediately. Returns (true, nil) when cleanup is complete.
func (r *AIHubReconciler) cleanupOnDelete(ctx context.Context, aihub *aihubv1alpha1.AIHub) (bool, error) {
	log := klog.FromContext(ctx)

	cat := &catalogv1alpha1.Catalog{}
	key := types.NamespacedName{Namespace: aihub.Spec.InstancesNamespace, Name: catalogCRName}
	err := r.Get(ctx, key, cat)
	if apierrors.IsNotFound(err) {
		return true, nil // Catalog gone → cleanup complete.
	}
	if err != nil {
		return false, fmt.Errorf("getting Catalog for cleanup: %w", err)
	}

	// Only delete a Catalog this AIHub owns. A user-controlled
	// InstancesNamespace must not let the controller destroy foreign resources.
	if !metav1.IsControlledBy(cat, aihub) {
		log.Info("Catalog is not owned by this AIHub, skipping deletion",
			"namespace", key.Namespace, "name", key.Name)
		return true, nil
	}

	if cat.DeletionTimestamp.IsZero() {
		log.Info("deleting Catalog CR during AIHub teardown", "namespace", key.Namespace, "name", key.Name)
		if err := r.Delete(ctx, cat); err != nil && !apierrors.IsNotFound(err) {
			return false, fmt.Errorf("deleting Catalog: %w", err)
		}
	}
	// Still present (deletion in progress / finalizer pending) → not done.
	return false, nil
}

// updateStatus sets release info, sorts conditions, derives phase from
// condition happiness, stamps ObservedGeneration, and persists status with a
// conflict retry (mirroring the kserve-module updateStatus pattern).
func (r *AIHubReconciler) updateStatus(ctx context.Context, aihub *aihubv1alpha1.AIHub, condMgr *conditions.Manager) error {
	r.setReleaseStatus(ctx, aihub)
	condMgr.Sort()

	if condMgr.IsHappy() {
		aihub.Status.Phase = common.PhaseReady
	} else {
		aihub.Status.Phase = common.PhaseNotReady
	}

	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		latest := &aihubv1alpha1.AIHub{}
		if err := r.Get(ctx, types.NamespacedName{Name: aihub.Name}, latest); err != nil {
			if apierrors.IsNotFound(err) {
				ctrl.LoggerFrom(ctx).Info("CR deleted, skipping status update")
				return nil
			}
			return err
		}
		latest.Status = aihub.Status
		latest.Status.ObservedGeneration = aihub.Generation
		return r.Status().Update(ctx, latest)
	})
}

// setReleaseStatus loads component releases from manifest metadata files and
// appends the platform version (if available) to the release list.
func (r *AIHubReconciler) setReleaseStatus(ctx context.Context, aihub *aihubv1alpha1.AIHub) {
	releases, err := loadComponentReleases(r.ManifestsTemplatePath,
		[]string{"modelregistry", "catalog"})
	if err != nil {
		ctrl.Log.Error(err, "failed to load component releases")
		return
	}

	if v := r.getPlatformVersion(ctx, aihub.Spec.ApplicationNamespace); v != "" {
		releases = append(releases, common.ComponentRelease{
			Name:    common.ReleasePlatform,
			Version: v,
		})
	}

	aihub.SetReleaseStatus(common.ComponentReleaseStatus{Releases: releases})
}

// getPlatformVersion reads the platform version from the orchestrator-created
// ConfigMap using the uncached API reader. The manager cache is label-scoped
// for ConfigMaps, so a cached Get would miss this platform-created ConfigMap.
func (r *AIHubReconciler) getPlatformVersion(ctx context.Context, applicationNamespace string) string {
	if r.APIReader == nil {
		return ""
	}
	cm := &corev1.ConfigMap{}
	key := types.NamespacedName{Name: platformVersionConfigMap, Namespace: applicationNamespace}
	if err := r.APIReader.Get(ctx, key, cm); err != nil {
		if apierrors.IsNotFound(err) {
			ctrl.LoggerFrom(ctx).V(1).Info("platform ConfigMap not found", "configmap", key)
			return ""
		}
		ctrl.LoggerFrom(ctx).Error(err, "reading platform ConfigMap failed", "configmap", key)
		return ""
	}
	return cm.Data[platformVersionConfigMapKey]
}

func (r *AIHubReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// NOTE: The platform-version ConfigMap (odh-aihub-config) is read on-demand
	// via the uncached APIReader in getPlatformVersion. Adding a Watch for it is
	// deferred because the manager cache is label-scoped for ConfigMaps, so an
	// Owns/Watches informer would not receive events for this platform-created
	// ConfigMap. A future follow-up can add a direct informer or periodic
	// re-sync trigger.
	return ctrl.NewControllerManagedBy(mgr).
		For(&aihubv1alpha1.AIHub{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.ServiceAccount{}).
		Owns(&corev1.ConfigMap{}).
		Owns(&rbacv1.Role{}).
		Owns(&rbacv1.RoleBinding{}).
		Owns(&rbacv1.ClusterRole{}).
		Owns(&rbacv1.ClusterRoleBinding{}).
		Owns(&admissionregistrationv1.ValidatingWebhookConfiguration{}).
		Owns(&admissionregistrationv1.MutatingWebhookConfiguration{}).
		Owns(&catalogv1alpha1.Catalog{}).
		Complete(r)
}

// checkChildDeploymentReady checks if the named Deployment exists and is
// Available. Returns (true, false, nil) when Available, (false, true, nil)
// when not found or not yet Available (caller should requeue), and
// (false, false, err) on unexpected errors.
func (r *AIHubReconciler) checkChildDeploymentReady(ctx context.Context, namespace, name string, condMgr *conditions.Manager, conditionType string) (ready, requeue bool, err error) {
	log := klog.FromContext(ctx)
	dep := &appsv1.Deployment{}
	key := types.NamespacedName{Namespace: namespace, Name: name}
	if err := r.Get(ctx, key, dep); err != nil {
		if !apierrors.IsNotFound(err) {
			return false, false, fmt.Errorf("getting child deployment %s: %w", key, err)
		}
		log.Info("child deployment not yet available, requeuing", "deployment", name)
		condMgr.MarkFalse(conditionType,
			conditions.WithReason("ChildDeploymentNotReady"),
			conditions.WithMessage("child deployment %s not found", name))
		condMgr.MarkFalse(string(common.ConditionTypeDegraded),
			conditions.WithSeverity(common.ConditionSeverityInfo),
			conditions.WithReason("NoDegradation"))
		return false, true, nil
	}
	if !isDeploymentAvailable(dep) {
		log.Info("child deployment not yet Available, requeuing", "deployment", name)
		condMgr.MarkFalse(conditionType,
			conditions.WithReason("ChildDeploymentNotReady"),
			conditions.WithMessage("child deployment %s not yet Available", name))
		condMgr.MarkFalse(string(common.ConditionTypeDegraded),
			conditions.WithSeverity(common.ConditionSeverityInfo),
			conditions.WithReason("NoDegradation"))
		return false, true, nil
	}
	return true, false, nil
}

// isDeploymentAvailable returns true if the Deployment has condition Available == True.
func isDeploymentAvailable(d *appsv1.Deployment) bool {
	for _, c := range d.Status.Conditions {
		if c.Type == appsv1.DeploymentAvailable {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// stampChildOperatorDeployment mutates the rendered child operator Deployment unstructured
// to set the operator image, upsert operand env vars, and set REGISTRIES_NAMESPACE.
func stampChildOperatorDeployment(u *unstructured.Unstructured, images ChildImages, registriesNs string) error {
	// Convert to typed Deployment.
	dep := &appsv1.Deployment{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, dep); err != nil {
		return fmt.Errorf("converting unstructured to Deployment: %w", err)
	}

	// Find the manager container.
	var found bool
	for i := range dep.Spec.Template.Spec.Containers {
		c := &dep.Spec.Template.Spec.Containers[i]
		if c.Name != childManagerContainer {
			continue
		}
		found = true

		// Set operator image if provided.
		if images.OperatorImage != "" {
			c.Image = images.OperatorImage
		}

		// Upsert operand env vars.
		for _, env := range images.OperandEnv {
			upsertEnv(c, env)
		}

		// Upsert REGISTRIES_NAMESPACE.
		upsertEnv(c, corev1.EnvVar{Name: config.RegistriesNamespace, Value: registriesNs})

		break
	}
	if !found {
		return fmt.Errorf("container %q not found in Deployment %s", childManagerContainer, dep.Name)
	}

	// Convert back to unstructured.
	obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(dep)
	if err != nil {
		return fmt.Errorf("converting Deployment back to unstructured: %w", err)
	}
	u.Object = obj
	return nil
}

// upsertEnv sets an env var on the container, replacing it if it already exists.
func upsertEnv(c *corev1.Container, env corev1.EnvVar) {
	for i := range c.Env {
		if c.Env[i].Name == env.Name {
			c.Env[i] = env
			return
		}
	}
	c.Env = append(c.Env, env)
}
