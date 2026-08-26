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
	"maps"
	"os"
	"path/filepath"
	"time"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	klog "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	aihubv1alpha1 "github.com/opendatahub-io/model-registry-operator/api/aihub/v1alpha1"
	catalogv1alpha1 "github.com/opendatahub-io/model-registry-operator/api/catalog/v1alpha1"
	"github.com/opendatahub-io/model-registry-operator/internal/controller/config"
	"github.com/opendatahub-io/odh-platform-utilities/api/common"
	"github.com/opendatahub-io/odh-platform-utilities/pkg/controller/conditions"
	"github.com/opendatahub-io/odh-platform-utilities/pkg/deploy"
	"github.com/opendatahub-io/odh-platform-utilities/pkg/render/kustomize"
)

const (
	aihubFinalizer          = "aihub.opendatahub.io/finalizer"
	aihubCRName             = "default-aihub"
	catalogCRName           = "catalog"
	childDeploymentName     = "model-registry-operator-controller-manager"
	catalogDeploymentName   = "catalog-controller-manager"
	childManagerContainer   = "manager"
	asyncUploadTemplateName = "jobs-async-upload-s3-to-oci-template"

	// ConditionModelRegistryReady tracks whether the child model-registry
	// operator Deployment is available.
	ConditionModelRegistryReady = "ModelRegistryReady"

	// ConditionCatalogReady tracks whether the catalog operator Deployment
	// is available.
	ConditionCatalogReady = "CatalogReady"

	// Platform version ConfigMap (created by the orchestrator in the
	// application namespace).
	platformVersionConfigMap    = "odh-modelregistry-config"
	platformVersionConfigMapKey = "platformVersion"

	// Fixed names of the catalog resources that are NOT owner-referenced to
	// the Catalog CR (so they are not garbage-collected with it) and must be
	// deleted explicitly when AIHub takes over Catalog finalization. Mirrors
	// CatalogReconciler.finalizeCatalog / cleanupKubeRBACProxyConfig, which
	// derive these from catalogResourceName ("model-catalog") via templates.
	catalogHTTPRouteName          = catalogResourceName
	catalogAuthDelegatorCRBName   = catalogResourceName + "-auth-delegator"
	catalogKubeRBACProxyConfigMap = catalogResourceName + "-kube-rbac-proxy-config"
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

	// onReconcile is a test-only hook invoked at the start of each Reconcile
	// call. It is nil in production and only set in manager-based tests to
	// observe reconcile invocations triggered by watches.
	onReconcile func()
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
	if r.onReconcile != nil {
		r.onReconcile()
	}
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
	if aihub.Name != aihubCRName {
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

	// Ensure the instances namespace exists before provisioning registries into it.
	// Skip when it collapses to the (platform-managed) applications namespace.
	//
	// Intentionally NO owner-reference is set on the namespace. AIHub is
	// cluster-scoped, so an owner-ref would be honored and would cascade-delete the
	// namespace — along with every ModelRegistry CR and catalog resource a user
	// created in it — when the AIHub CR is removed (e.g. turning modelregistry off
	// in the DSC). The in-tree opendatahub-operator component deliberately created
	// this namespace without owning it for the same reason; an orphaned empty
	// namespace after removal is preferable to destroying user data. CreateIfNotExists
	// also never mutates a pre-existing/shared namespace.
	if spec.InstancesNamespace != "" && spec.InstancesNamespace != spec.ApplicationNamespace {
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: spec.InstancesNamespace}}
		rm := ResourceManager{Client: r.Client}
		if _, err := rm.CreateIfNotExists(ctx, &corev1.Namespace{}, ns); err != nil {
			return ctrl.Result{}, fmt.Errorf("ensuring instances namespace %q: %w", spec.InstancesNamespace, err)
		}
	}

	var gatewayDomain string
	if spec.Gateway != nil {
		gatewayDomain = spec.Gateway.Domain
	}

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
				if err := stampChildOperatorDeployment(&resources[i], images, spec.InstancesNamespace, spec.ApplicationNamespace, gatewayDomain); err != nil {
					return ctrl.Result{}, fmt.Errorf("stamping child operator deployment %s: %w", name, err)
				}
			}
		}

		// Stamp the async-upload Template's JOB_IMAGE parameter with the
		// platform-pinned image when available.
		if kind == "Template" && resources[i].GetName() == asyncUploadTemplateName && images.AsyncUploadImage != "" {
			if err := stampAsyncUploadTemplate(&resources[i], images.AsyncUploadImage); err != nil {
				return ctrl.Result{}, fmt.Errorf("stamping async-upload template JOB_IMAGE: %w", err)
			}
		}
	}

	// 5.5. Delete any child Deployment whose live spec.selector is incompatible with
	// the rendered manifest. spec.selector is immutable, so upgrading from the legacy
	// single-operator install (which stamped extra platform labels into the selector)
	// requires recreating the Deployment rather than patching it.
	if pending, err := r.reconcileIncompatibleSelectors(ctx, resources); err != nil {
		condMgr.MarkFalse(string(common.ConditionTypeProvisioningSucceeded),
			conditions.WithReason("SelectorMigrationFailed"),
			conditions.WithMessage("%s", err.Error()))
		condMgr.MarkTrue(string(common.ConditionTypeDegraded),
			conditions.WithSeverity(common.ConditionSeverityError),
			conditions.WithReason("ProvisioningFailed"),
			conditions.WithMessage("%s", err.Error()))
		_ = r.updateStatus(ctx, aihub, condMgr)
		return ctrl.Result{}, fmt.Errorf("recreating child deployment with incompatible selector: %w", err)
	} else if pending {
		condMgr.MarkFalse(string(common.ConditionTypeProvisioningSucceeded),
			conditions.WithReason("RecreatingDeployment"),
			conditions.WithMessage("recreating child Deployment to migrate an immutable selector"))
		if sErr := r.updateStatus(ctx, aihub, condMgr); sErr != nil {
			return ctrl.Result{}, sErr
		}
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
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

	if gatewayDomain == "" {
		condMgr.MarkTrue(string(common.ConditionTypeDegraded),
			conditions.WithSeverity(common.ConditionSeverityInfo),
			conditions.WithReason("GatewayDomainUnavailable"),
			conditions.WithMessage("Data Science Gateway domain not yet available; external routing for model registry instances is disabled until spec.gateway.domain is set"))
	} else {
		condMgr.MarkFalse(string(common.ConditionTypeDegraded),
			conditions.WithSeverity(common.ConditionSeverityInfo),
			conditions.WithReason("NoDegradation"))
	}

	if sErr := r.updateStatus(ctx, aihub, condMgr); sErr != nil {
		return ctrl.Result{}, sErr
	}
	log.Info("AIHub reconciliation complete")
	return ctrl.Result{}, nil
}

// reconcileIncompatibleSelectors deletes any live child Deployment whose immutable
// spec.selector.matchLabels differs from the rendered manifest's selector. This
// covers upgrading from the legacy single-operator install, which stamped extra
// platform labels (app.kubernetes.io/part-of, app.opendatahub.io/model-registry-operator)
// into the selector that the current manifests no longer include. Since
// spec.selector is immutable, the only way to converge on the canonical selector
// is to delete the Deployment and let the Deployer recreate it on a later
// reconcile.
//
// Post-migration Deployments carry the manager's part-of label and are served
// from the cache, so the common case (steady state, nothing to migrate) costs
// no apiserver round trip. Legacy Deployments live outside that label-scoped
// cache, so a cached Get reports NotFound for them; on that miss we fall back
// to the uncached APIReader to find them, since skipping the check would leave
// the Deployer's apply to fail server-side.
//
// Returns pending=true when a delete was just issued or one is still propagating,
// signaling the caller to requeue rather than call the Deployer this round.
func (r *AIHubReconciler) reconcileIncompatibleSelectors(ctx context.Context, resources []unstructured.Unstructured) (pending bool, err error) {
	fallbackReader := r.APIReader
	if fallbackReader == nil {
		fallbackReader = r.Client
	}
	log := klog.FromContext(ctx)

	for i := range resources {
		if resources[i].GetKind() != "Deployment" {
			continue
		}

		desiredSelector, found, err := unstructured.NestedStringMap(resources[i].Object, "spec", "selector", "matchLabels")
		if err != nil {
			return false, fmt.Errorf("reading desired selector for %s: %w", resources[i].GetName(), err)
		}
		// No rendered matchLabels (absent, or expressed via matchExpressions):
		// nothing to compare, so never treat it as a mismatch.
		if !found || len(desiredSelector) == 0 {
			continue
		}

		key := types.NamespacedName{Namespace: resources[i].GetNamespace(), Name: resources[i].GetName()}
		live := &appsv1.Deployment{}
		if err := r.Get(ctx, key, live); err != nil {
			if !apierrors.IsNotFound(err) {
				return false, fmt.Errorf("getting live deployment %s: %w", key, err)
			}
			// Not in the cache (e.g. a legacy Deployment missing the part-of
			// label). Fall back to the uncached reader before concluding it
			// doesn't exist.
			if err := fallbackReader.Get(ctx, key, live); err != nil {
				if apierrors.IsNotFound(err) {
					continue
				}
				return false, fmt.Errorf("getting live deployment %s: %w", key, err)
			}
		}

		if live.DeletionTimestamp != nil {
			// Deletion already in progress from a prior reconcile; wait for it.
			pending = true
			continue
		}

		// A real Deployment always has spec.selector populated: it is required
		// and immutable at creation. A nil selector here means the object isn't
		// a genuine live Deployment (e.g. a test fixture) rather than a real
		// mismatch, so leave it alone.
		if live.Spec.Selector == nil {
			continue
		}

		if maps.Equal(live.Spec.Selector.MatchLabels, desiredSelector) {
			continue
		}

		log.Info("recreating child Deployment with incompatible immutable selector",
			"deployment", key, "liveSelector", live.Spec.Selector.MatchLabels, "desiredSelector", desiredSelector)
		// Bind the delete to the exact object observed above via a UID
		// precondition. Without this, a stale cache read (the manager cache can
		// lag the apiserver) could delete a Deployment the Deployer already
		// recreated. A precondition conflict means the observed object is
		// already gone; treat it as a no-op and let the next reconcile
		// re-evaluate.
		if err := r.Delete(ctx, live,
			client.PropagationPolicy(metav1.DeletePropagationBackground),
			client.Preconditions{UID: &live.UID},
		); err != nil && !apierrors.IsNotFound(err) && !apierrors.IsConflict(err) {
			return false, fmt.Errorf("deleting deployment %s to migrate selector: %w", key, err)
		}
		pending = true
	}

	return pending, nil
}

// cleanupOnDelete performs ordered teardown before the AIHub finalizer is
// released. It deletes the singleton Catalog CR first and waits for it to be
// fully removed, so the catalog operator (when present) can finalize its
// operands before its own Deployment is GC'd by owner-reference cleanup. When
// no catalog operator ever existed the Catalog has no finalizer and
// disappears immediately. If the catalog operator existed, added its
// finalizer, and was then removed (e.g. GC'd) before the Catalog finished
// deleting, nothing is left to clear that finalizer — so this also detects
// that stuck case and has AIHub take over Catalog finalization itself
// (takeOverCatalogFinalization) rather than wait forever. Returns (true, nil)
// when cleanup is complete.
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
		// Re-fetch so the finalizer check below observes the server-set
		// DeletionTimestamp/finalizers rather than the pre-delete copy. If the
		// Catalog is already gone (no finalizer was ever added), there is
		// nothing to take over; fall through and report not-done anyway — the
		// next reconcile's Get at the top of this function will observe
		// NotFound and report done. This preserves a stable two-pass contract
		// for the deletion path regardless of how quickly the Catalog clears.
		if err := r.Get(ctx, key, cat); err != nil {
			if apierrors.IsNotFound(err) {
				return false, nil
			}
			return false, fmt.Errorf("getting Catalog after delete: %w", err)
		}
	}

	// The Catalog is deleting but still present. If it is blocked only on the
	// catalog operator's own finalizer and that operator is no longer running
	// to remove it, take over finalization here to avoid a permanent teardown
	// deadlock.
	if controllerutil.ContainsFinalizer(cat, catalogFinalizer) {
		canFinalize, err := r.catalogOperatorCanFinalize(ctx, aihub.Spec.ApplicationNamespace)
		if err != nil {
			return false, err
		}
		if !canFinalize {
			if err := r.takeOverCatalogFinalization(ctx, aihub, cat); err != nil {
				return false, err
			}
		}
	}

	// Still present (deletion in progress / finalizer pending) → not done.
	return false, nil
}

// catalogOperatorCanFinalize reports whether the catalog-controller-manager
// Deployment is present and Available, i.e. whether the CatalogReconciler is
// running and can be trusted to remove its own finalizer from the Catalog CR.
// Used only from the AIHub delete branch to decide whether AIHub must take
// over Catalog finalization itself.
func (r *AIHubReconciler) catalogOperatorCanFinalize(ctx context.Context, applicationNamespace string) (bool, error) {
	dep := &appsv1.Deployment{}
	key := types.NamespacedName{Namespace: applicationNamespace, Name: catalogDeploymentName}
	if err := r.Get(ctx, key, dep); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil // Operator gone; it cannot finalize.
		}
		return false, fmt.Errorf("getting catalog operator deployment %s: %w", key, err)
	}
	return isDeploymentAvailable(dep), nil
}

// takeOverCatalogFinalization performs the cleanup normally done by
// CatalogReconciler.finalizeCatalog and then removes the stuck catalog
// finalizer, so the Catalog CR (and the operands still owner-referenced to
// it) can finish being garbage-collected. Only called once the catalog
// operator has been confirmed absent/unavailable, so this never races a
// live operator; every action here is idempotent with finalizeCatalog
// (NotFound/NoMatch-tolerant deletes, no-op RemoveFinalizer), so re-running
// it on a later requeue is safe.
func (r *AIHubReconciler) takeOverCatalogFinalization(ctx context.Context, aihub *aihubv1alpha1.AIHub, cat *catalogv1alpha1.Catalog) error {
	log := klog.FromContext(ctx)
	log.Info("catalog operator unavailable, taking over Catalog finalization",
		"namespace", cat.Namespace, "name", cat.Name)

	instancesNs := cat.Namespace
	appNs := aihub.Spec.ApplicationNamespace

	// The gateway HTTPRoute and the kube-rbac-proxy auth-delegator
	// ClusterRoleBinding are not owner-referenced to the Catalog CR (see
	// CatalogReconciler.finalizeCatalog/cleanupKubeRBACProxyConfig), so they
	// must be deleted explicitly. The shared "allow-gateway-httproutes"
	// ReferenceGrant is intentionally left alone, matching finalizeCatalog.
	httpRoute := &gatewayapiv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: catalogHTTPRouteName, Namespace: appNs},
	}
	if err := r.deleteIgnoringMissing(ctx, httpRoute); err != nil {
		return fmt.Errorf("deleting catalog HTTPRoute: %w", err)
	}

	crb := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: catalogAuthDelegatorCRBName},
	}
	if err := r.deleteIgnoringMissing(ctx, crb); err != nil {
		return fmt.Errorf("deleting catalog auth-delegator ClusterRoleBinding: %w", err)
	}

	// The kube-rbac-proxy config ConfigMap is owner-referenced to the Catalog
	// (so it is also GC'd once the finalizer clears below), but it is deleted
	// explicitly here too for parity with finalizeCatalog.
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: catalogKubeRBACProxyConfigMap, Namespace: instancesNs},
	}
	if err := r.deleteIgnoringMissing(ctx, cm); err != nil {
		return fmt.Errorf("deleting catalog kube-rbac-proxy ConfigMap: %w", err)
	}

	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		latest := &catalogv1alpha1.Catalog{}
		if err := r.Get(ctx, types.NamespacedName{Namespace: cat.Namespace, Name: cat.Name}, latest); err != nil {
			if apierrors.IsNotFound(err) {
				return nil
			}
			return err
		}
		if !controllerutil.RemoveFinalizer(latest, catalogFinalizer) {
			return nil // Already removed.
		}
		log.Info("removing stuck catalog finalizer", "namespace", latest.Namespace, "name", latest.Name)
		return r.Update(ctx, latest)
	})
}

// deleteIgnoringMissing deletes obj, tolerating NotFound and NoMatch (e.g. the
// gateway-api CRD not installed in a given cluster/test environment). Mirrors
// CatalogReconciler.deleteFromTemplate's error handling.
func (r *AIHubReconciler) deleteIgnoringMissing(ctx context.Context, obj client.Object) error {
	if err := r.Delete(ctx, obj); err != nil && !apierrors.IsNotFound(err) && !apimeta.IsNoMatchError(err) {
		return err
	}
	return nil
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
		// Still record the fallback releases (and the platform version below)
		// so the release status is not left empty on metadata load failure.
		releases = append([]common.ComponentRelease(nil), fallbackReleases...)
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
	// A dedicated cache watches the platform-version ConfigMap
	// (odh-modelregistry-config) created by the orchestrator. This is separate
	// from the manager's main cache, which is label-scoped for ConfigMaps
	// (part-of=aihub) and must continue to serve the Owns(&ConfigMap{})
	// informer for the deployer-managed ConfigMaps. The dedicated cache uses a
	// field selector on metadata.name so it watches only the single platform CM.
	cmCache, err := cache.New(mgr.GetConfig(), cache.Options{
		Scheme: mgr.GetScheme(),
		ByObject: map[client.Object]cache.ByObject{
			&corev1.ConfigMap{}: {Field: fields.OneTermEqualSelector("metadata.name", platformVersionConfigMap)},
		},
	})
	if err != nil {
		return fmt.Errorf("creating platform ConfigMap cache: %w", err)
	}
	if err := mgr.Add(cmCache); err != nil {
		return fmt.Errorf("adding platform ConfigMap cache to manager: %w", err)
	}

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
		WatchesRawSource(source.Kind(cmCache, &corev1.ConfigMap{},
			handler.TypedEnqueueRequestsFromMapFunc(platformConfigMapToAIHub),
			predicate.NewTypedPredicateFuncs(isPlatformConfigMap),
		)).
		Complete(r)
}

// platformConfigMapToAIHub maps a platform ConfigMap event to a reconcile
// request for the singleton AIHub CR (name "default-aihub", cluster-scoped).
func platformConfigMapToAIHub(_ context.Context, obj *corev1.ConfigMap) []reconcile.Request {
	if obj.GetName() != platformVersionConfigMap {
		return nil
	}
	return []reconcile.Request{{NamespacedName: types.NamespacedName{Name: aihubCRName}}}
}

// isPlatformConfigMap is a predicate filter that accepts only the platform
// version ConfigMap by name.
func isPlatformConfigMap(obj *corev1.ConfigMap) bool {
	return obj.GetName() == platformVersionConfigMap
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
// to set the operator image, upsert operand env vars, set REGISTRIES_NAMESPACE,
// stamp HTTPROUTE_NAMESPACE so the child creates HTTPRoutes in the applications
// namespace, and optionally stamp GATEWAY_DOMAIN when non-empty.
func stampChildOperatorDeployment(u *unstructured.Unstructured, images ChildImages, registriesNs, httpRouteNamespace, gatewayDomain string) error {
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

		// Upsert HTTPROUTE_NAMESPACE so the child creates HTTPRoutes in the
		// applications namespace instead of the RHOAI-specific built-in default.
		upsertEnv(c, corev1.EnvVar{Name: config.HTTPRouteNamespaceEnv, Value: httpRouteNamespace})

		// Upsert GATEWAY_DOMAIN only when the platform has provided a domain.
		if gatewayDomain != "" {
			upsertEnv(c, corev1.EnvVar{Name: config.GatewayDomainEnv, Value: gatewayDomain})
		}

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

// stampAsyncUploadTemplate sets the JOB_IMAGE parameter on the async-upload
// OpenShift Template so async-upload jobs run the platform-pinned image
// instead of the floating template default. No-op when image is empty.
func stampAsyncUploadTemplate(u *unstructured.Unstructured, image string) error {
	params, found, err := unstructured.NestedSlice(u.Object, "parameters")
	if err != nil || !found {
		return err
	}
	for i := range params {
		p, ok := params[i].(map[string]any)
		if !ok {
			continue
		}
		if p["name"] == "JOB_IMAGE" {
			p["value"] = image
			params[i] = p
			return unstructured.SetNestedSlice(u.Object, params, "parameters")
		}
	}
	return nil
}
