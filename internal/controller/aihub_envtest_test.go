package controller

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	goruntime "runtime"
	"slices"
	"strings"
	"testing"
	"time"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	aihubv1alpha1 "github.com/opendatahub-io/model-registry-operator/api/aihub/v1alpha1"
	catalogv1alpha1 "github.com/opendatahub-io/model-registry-operator/api/catalog/v1alpha1"
	"github.com/opendatahub-io/model-registry-operator/internal/controller/config"
	"github.com/opendatahub-io/odh-platform-utilities/api/common"
	"github.com/opendatahub-io/odh-platform-utilities/pkg/deploy"
	"github.com/opendatahub-io/odh-platform-utilities/pkg/render/kustomize"
)

// webhookCleanupDeployer wraps a ResourceDeployer and deletes the catalog
// ValidatingWebhookConfiguration after each deploy call. In envtest there is
// no backing webhook service, so the VWC with failurePolicy=Fail would block
// Catalog CR creation. This is a test-only workaround.
type webhookCleanupDeployer struct {
	inner  ResourceDeployer
	client client.Client
}

func (w *webhookCleanupDeployer) Deploy(ctx context.Context, input deploy.DeployInput) error {
	if err := w.inner.Deploy(ctx, input); err != nil {
		return err
	}
	// Delete the catalog VWC so the Catalog CR creation in the same reconcile
	// pass is not blocked by a webhook with no backing service.
	vwc := &admissionregistrationv1.ValidatingWebhookConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: "catalog-validating-webhook-configuration"},
	}
	_ = w.client.Delete(ctx, vwc) // ignore NotFound
	return nil
}

// TestAIHubReconcile_Envtest runs the AIHub reconciler against a real envtest
// apiserver to validate the full reconcile loop including SSA apply, CRD
// installation, Catalog CR creation with schema validation, status transitions,
// and finalizer-based teardown.
func TestAIHubReconcile_Envtest(t *testing.T) {
	// --- Skip guard: envtest binaries ---
	binAssetsDir := filepath.Join("..", "..", "bin", "k8s",
		fmt.Sprintf("1.35.0-%s-%s", goruntime.GOOS, goruntime.GOARCH))
	if v := os.Getenv("KUBEBUILDER_ASSETS"); v != "" {
		binAssetsDir = v
	}
	if _, err := os.Stat(filepath.Join(binAssetsDir, "kube-apiserver")); err != nil {
		t.Skipf("envtest binaries not available at %s: %v", binAssetsDir, err)
	}

	// --- Assemble manifests template ---
	tmpDir := assembleManifests(t)

	// --- Prepare scheme ---
	scheme := testScheme(t)

	// --- Copy the real AIHub CRD into a temp dir so envtest installs it ---
	aihubCRDPath := filepath.Join("..", "..", "config", "overlays", "aihub",
		"components.platform.opendatahub.io_aihubs.yaml")
	aihubCRDBytes, err := os.ReadFile(aihubCRDPath)
	if err != nil {
		t.Fatalf("reading AIHub CRD: %v", err)
	}
	crdTmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(crdTmpDir, "aihubs.yaml"), aihubCRDBytes, 0o644); err != nil {
		t.Fatalf("writing AIHub CRD to temp dir: %v", err)
	}

	// --- Start envtest ---
	useExisting := false
	testEnvLocal := &envtest.Environment{
		Scheme: scheme,
		CRDDirectoryPaths: []string{
			filepath.Join("..", "..", "config", "crd", "bases"),
			filepath.Join("testdata", "crd"),
			crdTmpDir,
		},
		ErrorIfCRDPathMissing: true,
		BinaryAssetsDirectory: binAssetsDir,
		UseExistingCluster:    &useExisting,
	}

	cfg, err := testEnvLocal.Start()
	if err != nil {
		t.Fatalf("starting envtest: %v", err)
	}
	defer func() {
		if err := testEnvLocal.Stop(); err != nil {
			t.Logf("warning: stopping envtest: %v", err)
		}
	}()

	k8sClient, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatalf("creating client: %v", err)
	}

	ctx := context.Background()
	appNs := "app-ns"
	regNs := "reg-ns"

	// --- Create namespaces ---
	for _, ns := range []string{appNs, regNs} {
		nsObj := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}
		if err := k8sClient.Create(ctx, nsObj); err != nil && !apierrors.IsAlreadyExists(err) {
			t.Fatalf("creating namespace %s: %v", ns, err)
		}
	}

	// --- Schema validation sub-tests ---
	t.Run("schema validation — CEL name rule", func(t *testing.T) {
		bad := &aihubv1alpha1.AIHub{
			ObjectMeta: metav1.ObjectMeta{Name: "not-default"},
			Spec: aihubv1alpha1.AIHubSpec{
				ApplicationNamespace: appNs,
				InstancesNamespace:   regNs,
			},
		}
		err := k8sClient.Create(ctx, bad)
		if err == nil {
			// Clean up and fail.
			_ = k8sClient.Delete(ctx, bad)
			t.Fatal("expected create of AIHub name='not-default' to be rejected by CEL rule, but it succeeded")
		}
		t.Logf("correctly rejected AIHub name='not-default': %v", err)
	})

	t.Run("schema validation — required fields", func(t *testing.T) {
		bad := &aihubv1alpha1.AIHub{
			ObjectMeta: metav1.ObjectMeta{Name: "default-aihub"},
			Spec:       aihubv1alpha1.AIHubSpec{},
		}
		err := k8sClient.Create(ctx, bad)
		if err == nil {
			_ = k8sClient.Delete(ctx, bad)
			t.Fatal("expected create of AIHub with empty namespaces to be rejected, but it succeeded")
		}
		t.Logf("correctly rejected AIHub with empty namespaces: %v", err)
	})

	t.Run("schema validation — gateway domain label too long", func(t *testing.T) {
		bad := &aihubv1alpha1.AIHub{
			ObjectMeta: metav1.ObjectMeta{Name: "default-aihub"},
			Spec: aihubv1alpha1.AIHubSpec{
				ApplicationNamespace: appNs,
				InstancesNamespace:   regNs,
				Gateway: &aihubv1alpha1.GatewaySpec{
					// A single DNS label of 64 chars exceeds the 63-char limit.
					Domain: strings.Repeat("a", 64) + ".example.com",
				},
			},
		}
		err := k8sClient.Create(ctx, bad)
		if err == nil {
			_ = k8sClient.Delete(ctx, bad)
			t.Fatal("expected create of AIHub with an over-long gateway domain label to be rejected by the pattern, but it succeeded")
		}
		if !strings.Contains(err.Error(), "spec.gateway.domain") {
			t.Fatalf("expected rejection to cite spec.gateway.domain, got: %v", err)
		}
		t.Logf("correctly rejected AIHub with over-long domain label: %v", err)
	})

	// --- Create the real singleton AIHub ---
	aihub := &aihubv1alpha1.AIHub{
		ObjectMeta: metav1.ObjectMeta{Name: "default-aihub"},
		Spec: aihubv1alpha1.AIHubSpec{
			ApplicationNamespace: appNs,
			InstancesNamespace:   regNs,
		},
	}
	if err := k8sClient.Create(ctx, aihub); err != nil {
		t.Fatalf("creating AIHub: %v", err)
	}

	// --- Build the reconciler with the REAL deployer ---
	r := &AIHubReconciler{
		Client:                k8sClient,
		Scheme:                scheme,
		ManifestsTemplatePath: tmpDir,
		APIReader:             k8sClient,
		HasServiceMonitorCRD:  true,
		Getenv: fakeGetenv(map[string]string{
			config.ModelRegistryOperatorImage: "fake-op@sha256:aaa",
			config.RestImage:                  "fake-rest@sha256:bbb",
			config.PostgresImage:              "fake-pg@sha256:ccc",
		}),
		Deployer: &webhookCleanupDeployer{
			inner: deploy.NewDeployer(
				deploy.WithFieldOwner("aihub"),
				deploy.WithApplyOrder(),
				deploy.WithCache(),
				deploy.WithMergeStrategy(
					schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"},
					deploy.MergeDeployments,
				),
				deploy.WithLegacyOwners(
					schema.GroupVersionKind{
						Group:   "components.platform.opendatahub.io",
						Version: "v1alpha1",
						Kind:    "ModelRegistry",
					},
				),
			),
			client: k8sClient,
		},
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "default-aihub"}}

	// --- Reconcile #1 ---
	result, err := r.Reconcile(ctx, req)
	if err != nil {
		t.Fatalf("reconcile #1 failed: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Error("reconcile #1: expected RequeueAfter > 0 (child Deployment not Available)")
	}

	// Assert MR child Deployment exists with stamped images.
	childDep := &appsv1.Deployment{}
	if err := k8sClient.Get(ctx, types.NamespacedName{
		Namespace: appNs, Name: childDeploymentName,
	}, childDep); err != nil {
		t.Fatalf("child Deployment %s not found: %v", childDeploymentName, err)
	}
	managerC := findContainer(t, childDep, childManagerContainer)
	if managerC.Image != "fake-op@sha256:aaa" {
		t.Errorf("manager image = %q, want %q", managerC.Image, "fake-op@sha256:aaa")
	}
	assertEnv(t, managerC, config.RestImage, "fake-rest@sha256:bbb")
	assertEnv(t, managerC, config.RegistriesNamespace, regNs)

	// Assert catalog child Deployment exists with stamped images.
	catalogDep := &appsv1.Deployment{}
	if err := k8sClient.Get(ctx, types.NamespacedName{
		Namespace: appNs, Name: catalogDeploymentName,
	}, catalogDep); err != nil {
		t.Fatalf("catalog Deployment %s not found: %v", catalogDeploymentName, err)
	}
	catalogManagerC := findContainer(t, catalogDep, childManagerContainer)
	if catalogManagerC.Image != "fake-op@sha256:aaa" {
		t.Errorf("catalog manager image = %q, want %q", catalogManagerC.Image, "fake-op@sha256:aaa")
	}
	assertEnv(t, catalogManagerC, config.RegistriesNamespace, regNs)

	// Assert the AIHub operator created and owns its own metrics ServiceMonitor
	// (RHOAIENG-88196: no longer shipped statically in the module bundle).
	sm := &unstructured.Unstructured{}
	sm.SetGroupVersionKind(schema.GroupVersionKind{Group: "monitoring.coreos.com", Version: "v1", Kind: "ServiceMonitor"})
	if err := k8sClient.Get(ctx, types.NamespacedName{
		Namespace: appNs, Name: aihubMetricsMonitorName,
	}, sm); err != nil {
		t.Fatalf("AIHub metrics ServiceMonitor %s not found: %v", aihubMetricsMonitorName, err)
	}
	if !metav1.IsControlledBy(sm, aihub) {
		t.Errorf("ServiceMonitor %s is not owned by the AIHub CR", aihubMetricsMonitorName)
	}
	wantServerName := aihubMetricsServiceName + "." + appNs + ".svc"
	endpoints, _, err := unstructured.NestedSlice(sm.Object, "spec", "endpoints")
	if err != nil || len(endpoints) != 1 {
		t.Fatalf("ServiceMonitor spec.endpoints: err=%v endpoints=%v", err, endpoints)
	}
	endpoint, _ := endpoints[0].(map[string]any)
	tlsConfig, _ := endpoint["tlsConfig"].(map[string]any)
	if serverName, _ := tlsConfig["serverName"].(string); serverName != wantServerName {
		t.Errorf("ServiceMonitor serverName = %q, want %q", serverName, wantServerName)
	}

	// Assert modelregistries CRD exists (CRDs applied).
	mrCRD := &apiextensionsv1.CustomResourceDefinition{}
	if err := k8sClient.Get(ctx, types.NamespacedName{
		Name: "modelregistries.modelregistry.opendatahub.io",
	}, mrCRD); err != nil {
		t.Fatalf("modelregistries CRD not found: %v", err)
	}

	// The Catalog CR is NOT created yet — both child Deployments must be
	// Available first (the catalog validating webhook needs a backing service).
	catalogCheck := &catalogv1alpha1.Catalog{}
	if err := k8sClient.Get(ctx, types.NamespacedName{
		Namespace: regNs, Name: catalogCRName,
	}, catalogCheck); !apierrors.IsNotFound(err) {
		t.Errorf("expected Catalog CR to not exist after reconcile #1, got err=%v", err)
	}

	// Assert finalizer present.
	got := &aihubv1alpha1.AIHub{}
	if err := k8sClient.Get(ctx, req.NamespacedName, got); err != nil {
		t.Fatal(err)
	}
	hasFinalizer := slices.Contains(got.Finalizers, aihubFinalizer)
	if !hasFinalizer {
		t.Errorf("AIHub missing finalizer %q after reconcile #1", aihubFinalizer)
	}

	// Assert Phase=NotReady, Ready=False.
	if got.Status.Phase != common.PhaseNotReady {
		t.Errorf("Phase = %q, want %q after reconcile #1", got.Status.Phase, common.PhaseNotReady)
	}
	assertConditionStatus(t, got, string(common.ConditionTypeReady), metav1.ConditionFalse)

	// --- Patch MR child Deployment to Available ---
	if err := k8sClient.Get(ctx, types.NamespacedName{
		Namespace: appNs, Name: childDeploymentName,
	}, childDep); err != nil {
		t.Fatalf("re-fetching child Deployment: %v", err)
	}
	childDep.Status.Conditions = []appsv1.DeploymentCondition{
		{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionTrue},
	}
	if err := k8sClient.Status().Update(ctx, childDep); err != nil {
		t.Fatalf("patching child Deployment status: %v", err)
	}

	// --- Reconcile #1b: MR Available but catalog not yet → still NotReady ---
	result1b, err := r.Reconcile(ctx, req)
	if err != nil {
		t.Fatalf("reconcile #1b failed: %v", err)
	}
	if result1b.RequeueAfter == 0 {
		t.Error("reconcile #1b: expected RequeueAfter > 0 (catalog Deployment not Available)")
	}
	got1b := &aihubv1alpha1.AIHub{}
	if err := k8sClient.Get(ctx, req.NamespacedName, got1b); err != nil {
		t.Fatal(err)
	}
	assertConditionStatus(t, got1b, ConditionModelRegistryReady, metav1.ConditionTrue)
	assertConditionStatus(t, got1b, ConditionCatalogReady, metav1.ConditionFalse)
	assertConditionStatus(t, got1b, string(common.ConditionTypeReady), metav1.ConditionFalse)

	// --- Patch catalog Deployment to Available ---
	if err := k8sClient.Get(ctx, types.NamespacedName{
		Namespace: appNs, Name: catalogDeploymentName,
	}, catalogDep); err != nil {
		t.Fatalf("re-fetching catalog Deployment: %v", err)
	}
	catalogDep.Status.Conditions = []appsv1.DeploymentCondition{
		{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionTrue},
	}
	if err := k8sClient.Status().Update(ctx, catalogDep); err != nil {
		t.Fatalf("patching catalog Deployment status: %v", err)
	}

	// --- Platform version handshake: create ConfigMap before reconcile #2 ---
	t.Run("platform version handshake", func(t *testing.T) {
		platformCM := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      platformVersionConfigMap,
				Namespace: appNs,
			},
			Data: map[string]string{
				platformVersionConfigMapKey: "2.20.0",
			},
		}
		if err := k8sClient.Create(ctx, platformCM); err != nil {
			t.Fatalf("creating platform ConfigMap: %v", err)
		}
	})

	// --- Reconcile #2 ---
	result2, err := r.Reconcile(ctx, req)
	if err != nil {
		t.Fatalf("reconcile #2 failed: %v", err)
	}
	if result2.RequeueAfter != 0 {
		t.Errorf("reconcile #2: expected RequeueAfter==0, got %v", result2.RequeueAfter)
	}

	got2 := &aihubv1alpha1.AIHub{}
	if err := k8sClient.Get(ctx, req.NamespacedName, got2); err != nil {
		t.Fatal(err)
	}
	if got2.Status.Phase != common.PhaseReady {
		t.Errorf("Phase = %q, want %q after reconcile #2", got2.Status.Phase, common.PhaseReady)
	}
	assertConditionStatus(t, got2, string(common.ConditionTypeReady), metav1.ConditionTrue)
	assertConditionStatus(t, got2, string(common.ConditionTypeProvisioningSucceeded), metav1.ConditionTrue)
	assertConditionStatus(t, got2, ConditionModelRegistryReady, metav1.ConditionTrue)
	assertConditionStatus(t, got2, ConditionCatalogReady, metav1.ConditionTrue)

	if got2.Status.ObservedGeneration != got2.Generation {
		t.Errorf("ObservedGeneration = %d, want %d (Generation)", got2.Status.ObservedGeneration, got2.Generation)
	}

	// Assert the Catalog CR "catalog" exists in reg-ns with a controller owner
	// reference Kind=AIHub. Created only after both children are Available
	// (reconcile #2). This also validates the Catalog CR content against the
	// real catalogs CRD schema (WI-6 item 3: "CRD schema validation for the
	// Catalog CR AIHub builds").
	catalog := &catalogv1alpha1.Catalog{}
	if err := k8sClient.Get(ctx, types.NamespacedName{
		Namespace: regNs, Name: catalogCRName,
	}, catalog); err != nil {
		t.Fatalf("Catalog CR not found after reconcile #2: %v", err)
	}
	var hasAIHubOwner bool
	for _, ref := range catalog.GetOwnerReferences() {
		if ref.Kind == "AIHub" && ref.Name == "default-aihub" &&
			ref.Controller != nil && *ref.Controller {
			hasAIHubOwner = true
			break
		}
	}
	if !hasAIHubOwner {
		t.Error("Catalog CR missing controller owner reference with Kind=AIHub")
	}

	platformVersion := got2.GetReleaseStatus().GetPlatformRelease()
	if platformVersion != "2.20.0" {
		t.Errorf("platform release = %q, want %q", platformVersion, "2.20.0")
	}
	if len(got2.Status.Releases) < 2 {
		t.Errorf("expected at least 2 releases (component + platform), got %d: %+v",
			len(got2.Status.Releases), got2.Status.Releases)
	}

	// --- Idempotency: Reconcile #3 ---
	t.Run("idempotency", func(t *testing.T) {
		result3, err := r.Reconcile(ctx, req)
		if err != nil {
			t.Fatalf("reconcile #3 (idempotency) failed: %v", err)
		}
		if result3.RequeueAfter != 0 {
			t.Errorf("reconcile #3: expected RequeueAfter==0, got %v", result3.RequeueAfter)
		}

		// Catalog CR still present.
		catCheck := &catalogv1alpha1.Catalog{}
		if err := k8sClient.Get(ctx, types.NamespacedName{
			Namespace: regNs, Name: catalogCRName,
		}, catCheck); err != nil {
			t.Errorf("Catalog CR gone after idempotent reconcile: %v", err)
		}

		// Child Deployment still present.
		depCheck := &appsv1.Deployment{}
		if err := k8sClient.Get(ctx, types.NamespacedName{
			Namespace: appNs, Name: childDeploymentName,
		}, depCheck); err != nil {
			t.Errorf("child Deployment gone after idempotent reconcile: %v", err)
		}
	})

	// --- Assert Degraded=True when no gateway domain ---
	// The pre-existing test creates an AIHub without spec.gateway, so after
	// the gateway domain plumbing, Degraded must be True/GatewayDomainUnavailable.
	assertConditionStatus(t, got2, string(common.ConditionTypeDegraded), metav1.ConditionTrue)
	assertConditionReason(t, got2, string(common.ConditionTypeDegraded), "GatewayDomainUnavailable")

	// --- Deletion / finalizer teardown ---
	t.Run("deletion/finalizer", func(t *testing.T) {
		// Simulate the real-world deadlock this test guards against
		// (RHOAIENG-88184): the catalog operator added its finalizer to the
		// Catalog CR at some point, then its Deployment was scaled to zero
		// replicas (e.g. during an upgrade) before the Catalog finished
		// deleting — mirroring the Jira reproduction, which scales the
		// operator down rather than deleting it. A scaled-to-zero Deployment
		// still reports Available=True, so nothing but the AvailableReplicas
		// count distinguishes it from a healthy operator. AIHub must take
		// over Catalog finalization itself rather than wait on it forever.
		catalogFresh := &catalogv1alpha1.Catalog{}
		if err := k8sClient.Get(ctx, types.NamespacedName{
			Namespace: regNs, Name: catalogCRName,
		}, catalogFresh); err != nil {
			t.Fatalf("re-fetching Catalog: %v", err)
		}
		if controllerutil.AddFinalizer(catalogFresh, catalogFinalizer) {
			if err := k8sClient.Update(ctx, catalogFresh); err != nil {
				t.Fatalf("adding catalogFinalizer to Catalog: %v", err)
			}
		}

		catalogOperatorDep := &appsv1.Deployment{}
		if err := k8sClient.Get(ctx, types.NamespacedName{
			Namespace: appNs, Name: catalogDeploymentName,
		}, catalogOperatorDep); err != nil {
			t.Fatalf("re-fetching catalog operator Deployment: %v", err)
		}
		zero := int32(0)
		catalogOperatorDep.Spec.Replicas = &zero
		if err := k8sClient.Update(ctx, catalogOperatorDep); err != nil {
			t.Fatalf("scaling catalog operator Deployment to zero: %v", err)
		}
		catalogOperatorDep.Status.AvailableReplicas = 0
		catalogOperatorDep.Status.Conditions = []appsv1.DeploymentCondition{
			{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionTrue},
		}
		if err := k8sClient.Status().Update(ctx, catalogOperatorDep); err != nil {
			t.Fatalf("patching scaled-down catalog operator Deployment status: %v", err)
		}

		// Re-fetch to get latest resourceVersion.
		fresh := &aihubv1alpha1.AIHub{}
		if err := k8sClient.Get(ctx, req.NamespacedName, fresh); err != nil {
			t.Fatalf("re-fetching AIHub: %v", err)
		}
		if err := k8sClient.Delete(ctx, fresh); err != nil {
			t.Fatalf("deleting AIHub: %v", err)
		}

		// Bounded reconcile loop to drain ordered teardown. With the catalog
		// operator scaled to zero and the Catalog CR's finalizer stuck, this
		// only completes if AIHub takes over Catalog finalization (see
		// cleanupOnDelete/takeOverCatalogFinalization).
		const maxIter = 10
		for i := range maxIter {
			_, err := r.Reconcile(ctx, req)
			if err != nil {
				t.Fatalf("reconcile during deletion (iter %d): %v", i, err)
			}
			// Check if AIHub is gone.
			check := &aihubv1alpha1.AIHub{}
			if err := k8sClient.Get(ctx, req.NamespacedName, check); apierrors.IsNotFound(err) {
				t.Logf("AIHub removed after %d reconcile iterations", i+1)
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatalf("AIHub not removed after %d reconcile iterations (catalog operator scaled to zero + stuck finalizer must not deadlock teardown)", maxIter)
	})
}

// TestAIHubGatewayDomain_Envtest verifies the gateway domain plumbing:
// (1) when spec.gateway.domain is set, GATEWAY_DOMAIN is stamped on both children;
// (2) when spec.gateway is absent, GATEWAY_DOMAIN is NOT stamped and Degraded is True.
func TestAIHubGatewayDomain_Envtest(t *testing.T) {
	// --- Skip guard: envtest binaries ---
	binAssetsDir := filepath.Join("..", "..", "bin", "k8s",
		fmt.Sprintf("1.35.0-%s-%s", goruntime.GOOS, goruntime.GOARCH))
	if v := os.Getenv("KUBEBUILDER_ASSETS"); v != "" {
		binAssetsDir = v
	}
	if _, err := os.Stat(filepath.Join(binAssetsDir, "kube-apiserver")); err != nil {
		t.Skipf("envtest binaries not available at %s: %v", binAssetsDir, err)
	}

	tmpDir := assembleManifests(t)
	scheme := testScheme(t)

	// Copy AIHub CRD.
	aihubCRDPath := filepath.Join("..", "..", "config", "overlays", "aihub",
		"components.platform.opendatahub.io_aihubs.yaml")
	aihubCRDBytes, err := os.ReadFile(aihubCRDPath)
	if err != nil {
		t.Fatalf("reading AIHub CRD: %v", err)
	}
	crdTmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(crdTmpDir, "aihubs.yaml"), aihubCRDBytes, 0o644); err != nil {
		t.Fatalf("writing AIHub CRD to temp dir: %v", err)
	}

	useExisting := false
	testEnvLocal := &envtest.Environment{
		Scheme: scheme,
		CRDDirectoryPaths: []string{
			filepath.Join("..", "..", "config", "crd", "bases"),
			filepath.Join("testdata", "crd"),
			crdTmpDir,
		},
		ErrorIfCRDPathMissing: true,
		BinaryAssetsDirectory: binAssetsDir,
		UseExistingCluster:    &useExisting,
	}

	cfg, err := testEnvLocal.Start()
	if err != nil {
		t.Fatalf("starting envtest: %v", err)
	}
	defer func() {
		if err := testEnvLocal.Stop(); err != nil {
			t.Logf("warning: stopping envtest: %v", err)
		}
	}()

	k8sClient, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatalf("creating client: %v", err)
	}

	ctx := context.Background()

	newReconciler := func(t *testing.T, k client.Client, tmpDir string) *AIHubReconciler {
		t.Helper()
		return &AIHubReconciler{
			Client:                k,
			Scheme:                scheme,
			ManifestsTemplatePath: tmpDir,
			APIReader:             k,
			Getenv: fakeGetenv(map[string]string{
				config.ModelRegistryOperatorImage: "fake-op@sha256:aaa",
				config.RestImage:                  "fake-rest@sha256:bbb",
				config.PostgresImage:              "fake-pg@sha256:ccc",
			}),
			Deployer: &webhookCleanupDeployer{
				inner: deploy.NewDeployer(
					deploy.WithFieldOwner("aihub"),
					deploy.WithApplyOrder(),
					deploy.WithCache(),
					deploy.WithMergeStrategy(
						schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"},
						deploy.MergeDeployments,
					),
					deploy.WithLegacyOwners(
						schema.GroupVersionKind{
							Group:   "components.platform.opendatahub.io",
							Version: "v1alpha1",
							Kind:    "ModelRegistry",
						},
					),
				),
				client: k,
			},
		}
	}

	// patchChildrenAvailable patches both child Deployments to Available.
	patchChildrenAvailable := func(t *testing.T, k client.Client, ns string) {
		t.Helper()
		for _, name := range []string{childDeploymentName, catalogDeploymentName} {
			dep := &appsv1.Deployment{}
			if err := k.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, dep); err != nil {
				t.Fatalf("getting %s: %v", name, err)
			}
			dep.Status.Conditions = []appsv1.DeploymentCondition{
				{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionTrue},
			}
			if err := k.Status().Update(ctx, dep); err != nil {
				t.Fatalf("patching %s status: %v", name, err)
			}
		}
	}

	t.Run("domain set", func(t *testing.T) {
		appNs := "gw-app-ns"
		regNs := "gw-reg-ns"
		for _, ns := range []string{appNs, regNs} {
			nsObj := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}
			if err := k8sClient.Create(ctx, nsObj); err != nil && !apierrors.IsAlreadyExists(err) {
				t.Fatalf("creating namespace %s: %v", ns, err)
			}
		}

		aihub := &aihubv1alpha1.AIHub{
			ObjectMeta: metav1.ObjectMeta{Name: "default-aihub"},
			Spec: aihubv1alpha1.AIHubSpec{
				ApplicationNamespace: appNs,
				InstancesNamespace:   regNs,
				Gateway:              &aihubv1alpha1.GatewaySpec{Domain: "apps.example.com"},
			},
		}
		if err := k8sClient.Create(ctx, aihub); err != nil {
			t.Fatalf("creating AIHub: %v", err)
		}
		defer func() {
			_ = k8sClient.Delete(ctx, aihub)
			// Drain finalizer.
			for range 10 {
				_, _ = newReconciler(t, k8sClient, tmpDir).Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "default-aihub"}})
				check := &aihubv1alpha1.AIHub{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: "default-aihub"}, check); apierrors.IsNotFound(err) {
					break
				}
				time.Sleep(100 * time.Millisecond)
			}
		}()

		r := newReconciler(t, k8sClient, tmpDir)
		req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "default-aihub"}}

		// Reconcile #1: children not Available yet.
		if _, err := r.Reconcile(ctx, req); err != nil {
			t.Fatalf("reconcile #1 failed: %v", err)
		}

		// Assert GATEWAY_DOMAIN on both children after first reconcile.
		for _, depName := range []string{childDeploymentName, catalogDeploymentName} {
			dep := &appsv1.Deployment{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: appNs, Name: depName}, dep); err != nil {
				t.Fatalf("%s not found: %v", depName, err)
			}
			c := findContainer(t, dep, childManagerContainer)
			assertEnv(t, c, config.GatewayDomainEnv, "apps.example.com")
		}

		// Patch children to Available and reconcile to Ready.
		patchChildrenAvailable(t, k8sClient, appNs)
		if _, err := r.Reconcile(ctx, req); err != nil {
			t.Fatalf("reconcile #2 failed: %v", err)
		}

		got := &aihubv1alpha1.AIHub{}
		if err := k8sClient.Get(ctx, req.NamespacedName, got); err != nil {
			t.Fatal(err)
		}
		if got.Status.Phase != common.PhaseReady {
			t.Errorf("Phase = %q, want %q", got.Status.Phase, common.PhaseReady)
		}
		assertConditionStatus(t, got, string(common.ConditionTypeDegraded), metav1.ConditionFalse)
	})

	t.Run("domain absent", func(t *testing.T) {
		appNs := "gw-absent-app-ns"
		regNs := "gw-absent-reg-ns"
		for _, ns := range []string{appNs, regNs} {
			nsObj := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}
			if err := k8sClient.Create(ctx, nsObj); err != nil && !apierrors.IsAlreadyExists(err) {
				t.Fatalf("creating namespace %s: %v", ns, err)
			}
		}

		aihub := &aihubv1alpha1.AIHub{
			ObjectMeta: metav1.ObjectMeta{Name: "default-aihub"},
			Spec: aihubv1alpha1.AIHubSpec{
				ApplicationNamespace: appNs,
				InstancesNamespace:   regNs,
			},
		}
		if err := k8sClient.Create(ctx, aihub); err != nil {
			t.Fatalf("creating AIHub: %v", err)
		}
		defer func() {
			_ = k8sClient.Delete(ctx, aihub)
			for range 10 {
				_, _ = newReconciler(t, k8sClient, tmpDir).Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "default-aihub"}})
				check := &aihubv1alpha1.AIHub{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: "default-aihub"}, check); apierrors.IsNotFound(err) {
					break
				}
				time.Sleep(100 * time.Millisecond)
			}
		}()

		r := newReconciler(t, k8sClient, tmpDir)
		req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "default-aihub"}}

		// Reconcile #1: children not Available yet.
		if _, err := r.Reconcile(ctx, req); err != nil {
			t.Fatalf("reconcile #1 failed: %v", err)
		}

		// Assert GATEWAY_DOMAIN is NOT stamped with a real domain on either child.
		// The kustomize-rendered template may include GATEWAY_DOMAIN="" as a
		// placeholder; that is fine — the child treats empty as "disabled".
		for _, depName := range []string{childDeploymentName, catalogDeploymentName} {
			dep := &appsv1.Deployment{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: appNs, Name: depName}, dep); err != nil {
				t.Fatalf("%s not found: %v", depName, err)
			}
			c := findContainer(t, dep, childManagerContainer)
			assertEnvEmpty(t, c, config.GatewayDomainEnv)
		}

		// Patch children to Available and reconcile to Ready.
		patchChildrenAvailable(t, k8sClient, appNs)
		if _, err := r.Reconcile(ctx, req); err != nil {
			t.Fatalf("reconcile #2 failed: %v", err)
		}

		got := &aihubv1alpha1.AIHub{}
		if err := k8sClient.Get(ctx, req.NamespacedName, got); err != nil {
			t.Fatal(err)
		}

		// Degraded=True with reason GatewayDomainUnavailable.
		assertConditionStatus(t, got, string(common.ConditionTypeDegraded), metav1.ConditionTrue)
		assertConditionReason(t, got, string(common.ConditionTypeDegraded), "GatewayDomainUnavailable")

		// Phase is still Ready — Degraded does not block readiness.
		if got.Status.Phase != common.PhaseReady {
			t.Errorf("Phase = %q, want %q (Degraded must not block readiness)", got.Status.Phase, common.PhaseReady)
		}
		assertConditionStatus(t, got, string(common.ConditionTypeReady), metav1.ConditionTrue)
	})
}

// TestAIHubNamespaceEnsure_Envtest verifies Gap 1: the controller creates the
// instances namespace when it differs from the applications namespace, and
// skips creation when they are equal.
func TestAIHubNamespaceEnsure_Envtest(t *testing.T) {
	// --- Skip guard: envtest binaries ---
	binAssetsDir := filepath.Join("..", "..", "bin", "k8s",
		fmt.Sprintf("1.35.0-%s-%s", goruntime.GOOS, goruntime.GOARCH))
	if v := os.Getenv("KUBEBUILDER_ASSETS"); v != "" {
		binAssetsDir = v
	}
	if _, err := os.Stat(filepath.Join(binAssetsDir, "kube-apiserver")); err != nil {
		t.Skipf("envtest binaries not available at %s: %v", binAssetsDir, err)
	}

	tmpDir := assembleManifests(t)
	scheme := testScheme(t)

	// Copy AIHub CRD.
	aihubCRDPath := filepath.Join("..", "..", "config", "overlays", "aihub",
		"components.platform.opendatahub.io_aihubs.yaml")
	aihubCRDBytes, err := os.ReadFile(aihubCRDPath)
	if err != nil {
		t.Fatalf("reading AIHub CRD: %v", err)
	}
	crdTmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(crdTmpDir, "aihubs.yaml"), aihubCRDBytes, 0o644); err != nil {
		t.Fatalf("writing AIHub CRD to temp dir: %v", err)
	}

	useExisting := false
	testEnvLocal := &envtest.Environment{
		Scheme: scheme,
		CRDDirectoryPaths: []string{
			filepath.Join("..", "..", "config", "crd", "bases"),
			filepath.Join("testdata", "crd"),
			crdTmpDir,
		},
		ErrorIfCRDPathMissing: true,
		BinaryAssetsDirectory: binAssetsDir,
		UseExistingCluster:    &useExisting,
	}

	cfg, err := testEnvLocal.Start()
	if err != nil {
		t.Fatalf("starting envtest: %v", err)
	}
	defer func() {
		if err := testEnvLocal.Stop(); err != nil {
			t.Logf("warning: stopping envtest: %v", err)
		}
	}()

	k8sClient, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatalf("creating client: %v", err)
	}

	ctx := context.Background()

	newReconciler := func(t *testing.T, k client.Client, dir string) *AIHubReconciler {
		t.Helper()
		return &AIHubReconciler{
			Client:                k,
			Scheme:                scheme,
			ManifestsTemplatePath: dir,
			APIReader:             k,
			Getenv: fakeGetenv(map[string]string{
				config.ModelRegistryOperatorImage: "fake-op@sha256:aaa",
				config.RestImage:                  "fake-rest@sha256:bbb",
				config.PostgresImage:              "fake-pg@sha256:ccc",
			}),
			Deployer: &webhookCleanupDeployer{
				inner: deploy.NewDeployer(
					deploy.WithFieldOwner("aihub"),
					deploy.WithApplyOrder(),
					deploy.WithCache(),
					deploy.WithMergeStrategy(
						schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"},
						deploy.MergeDeployments,
					),
					deploy.WithLegacyOwners(
						schema.GroupVersionKind{
							Group:   "components.platform.opendatahub.io",
							Version: "v1alpha1",
							Kind:    "ModelRegistry",
						},
					),
				),
				client: k,
			},
		}
	}

	patchChildrenAvailable := func(t *testing.T, k client.Client, ns string) {
		t.Helper()
		for _, name := range []string{childDeploymentName, catalogDeploymentName} {
			dep := &appsv1.Deployment{}
			if err := k.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, dep); err != nil {
				t.Fatalf("getting %s: %v", name, err)
			}
			dep.Status.Conditions = []appsv1.DeploymentCondition{
				{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionTrue},
			}
			if err := k.Status().Update(ctx, dep); err != nil {
				t.Fatalf("patching %s status: %v", name, err)
			}
		}
	}

	// Gap 1a: instances namespace differs from app namespace and does NOT
	// exist — the controller must create it.
	t.Run("creates instances namespace", func(t *testing.T) {
		appNs := "ns-ensure-app"
		regNs := "ns-ensure-reg" // NOT pre-created

		// Only create the app namespace.
		nsObj := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: appNs}}
		if err := k8sClient.Create(ctx, nsObj); err != nil && !apierrors.IsAlreadyExists(err) {
			t.Fatalf("creating app namespace: %v", err)
		}

		aihub := &aihubv1alpha1.AIHub{
			ObjectMeta: metav1.ObjectMeta{Name: "default-aihub"},
			Spec: aihubv1alpha1.AIHubSpec{
				ApplicationNamespace: appNs,
				InstancesNamespace:   regNs,
			},
		}
		if err := k8sClient.Create(ctx, aihub); err != nil {
			t.Fatalf("creating AIHub: %v", err)
		}
		defer func() {
			_ = k8sClient.Delete(ctx, aihub)
			for range 10 {
				_, _ = newReconciler(t, k8sClient, tmpDir).Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "default-aihub"}})
				check := &aihubv1alpha1.AIHub{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: "default-aihub"}, check); apierrors.IsNotFound(err) {
					break
				}
				time.Sleep(100 * time.Millisecond)
			}
		}()

		r := newReconciler(t, k8sClient, tmpDir)
		req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "default-aihub"}}

		// Reconcile #1 — should create the instances namespace.
		if _, err := r.Reconcile(ctx, req); err != nil {
			t.Fatalf("reconcile #1 failed: %v", err)
		}

		// Assert the instances namespace now exists.
		createdNs := &corev1.Namespace{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: regNs}, createdNs); err != nil {
			t.Fatalf("instances namespace %q not created: %v", regNs, err)
		}

		// Assert it has NO AIHub owner reference: an owner-ref would cascade-delete
		// the namespace (and all user data in it) when the AIHub CR is removed. The
		// namespace is intentionally created unowned, matching the old in-tree component.
		for _, ref := range createdNs.GetOwnerReferences() {
			if ref.Kind == "AIHub" {
				t.Errorf("instances namespace unexpectedly has an AIHub owner reference (%+v); it must be unowned to avoid cascade-deleting user data on removal", ref)
			}
		}

		// Patch children available and reconcile to Ready — Catalog CR must succeed.
		patchChildrenAvailable(t, k8sClient, appNs)
		if _, err := r.Reconcile(ctx, req); err != nil {
			t.Fatalf("reconcile #2 failed: %v", err)
		}

		catalog := &catalogv1alpha1.Catalog{}
		if err := k8sClient.Get(ctx, types.NamespacedName{
			Namespace: regNs, Name: catalogCRName,
		}, catalog); err != nil {
			t.Fatalf("Catalog CR not found after reconcile #2: %v", err)
		}
	})

	// Gap 1b: instances namespace == app namespace — controller must NOT error
	// and must NOT create a duplicate namespace.
	t.Run("same namespace no error", func(t *testing.T) {
		sameNs := "ns-ensure-same"

		nsObj := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: sameNs}}
		if err := k8sClient.Create(ctx, nsObj); err != nil && !apierrors.IsAlreadyExists(err) {
			t.Fatalf("creating namespace: %v", err)
		}

		aihub := &aihubv1alpha1.AIHub{
			ObjectMeta: metav1.ObjectMeta{Name: "default-aihub"},
			Spec: aihubv1alpha1.AIHubSpec{
				ApplicationNamespace: sameNs,
				InstancesNamespace:   sameNs,
			},
		}
		if err := k8sClient.Create(ctx, aihub); err != nil {
			t.Fatalf("creating AIHub: %v", err)
		}
		defer func() {
			_ = k8sClient.Delete(ctx, aihub)
			for range 10 {
				_, _ = newReconciler(t, k8sClient, tmpDir).Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "default-aihub"}})
				check := &aihubv1alpha1.AIHub{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: "default-aihub"}, check); apierrors.IsNotFound(err) {
					break
				}
				time.Sleep(100 * time.Millisecond)
			}
		}()

		r := newReconciler(t, k8sClient, tmpDir)
		req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "default-aihub"}}

		// Reconcile must not error.
		if _, err := r.Reconcile(ctx, req); err != nil {
			t.Fatalf("reconcile failed: %v", err)
		}
	})
}

// TestAIHubHTTPRouteNamespace_Envtest verifies Gap 3: HTTPROUTE_NAMESPACE is
// stamped on both child Deployments with the applications namespace value.
func TestAIHubHTTPRouteNamespace_Envtest(t *testing.T) {
	// --- Skip guard: envtest binaries ---
	binAssetsDir := filepath.Join("..", "..", "bin", "k8s",
		fmt.Sprintf("1.35.0-%s-%s", goruntime.GOOS, goruntime.GOARCH))
	if v := os.Getenv("KUBEBUILDER_ASSETS"); v != "" {
		binAssetsDir = v
	}
	if _, err := os.Stat(filepath.Join(binAssetsDir, "kube-apiserver")); err != nil {
		t.Skipf("envtest binaries not available at %s: %v", binAssetsDir, err)
	}

	tmpDir := assembleManifests(t)
	scheme := testScheme(t)

	// Copy AIHub CRD.
	aihubCRDPath := filepath.Join("..", "..", "config", "overlays", "aihub",
		"components.platform.opendatahub.io_aihubs.yaml")
	aihubCRDBytes, err := os.ReadFile(aihubCRDPath)
	if err != nil {
		t.Fatalf("reading AIHub CRD: %v", err)
	}
	crdTmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(crdTmpDir, "aihubs.yaml"), aihubCRDBytes, 0o644); err != nil {
		t.Fatalf("writing AIHub CRD to temp dir: %v", err)
	}

	useExisting := false
	testEnvLocal := &envtest.Environment{
		Scheme: scheme,
		CRDDirectoryPaths: []string{
			filepath.Join("..", "..", "config", "crd", "bases"),
			filepath.Join("testdata", "crd"),
			crdTmpDir,
		},
		ErrorIfCRDPathMissing: true,
		BinaryAssetsDirectory: binAssetsDir,
		UseExistingCluster:    &useExisting,
	}

	cfg, err := testEnvLocal.Start()
	if err != nil {
		t.Fatalf("starting envtest: %v", err)
	}
	defer func() {
		if err := testEnvLocal.Stop(); err != nil {
			t.Logf("warning: stopping envtest: %v", err)
		}
	}()

	k8sClient, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatalf("creating client: %v", err)
	}

	ctx := context.Background()
	appNs := "httproute-app-ns"
	regNs := "httproute-reg-ns"

	for _, ns := range []string{appNs, regNs} {
		nsObj := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}
		if err := k8sClient.Create(ctx, nsObj); err != nil && !apierrors.IsAlreadyExists(err) {
			t.Fatalf("creating namespace %s: %v", ns, err)
		}
	}

	aihub := &aihubv1alpha1.AIHub{
		ObjectMeta: metav1.ObjectMeta{Name: "default-aihub"},
		Spec: aihubv1alpha1.AIHubSpec{
			ApplicationNamespace: appNs,
			InstancesNamespace:   regNs,
		},
	}
	if err := k8sClient.Create(ctx, aihub); err != nil {
		t.Fatalf("creating AIHub: %v", err)
	}

	r := &AIHubReconciler{
		Client:                k8sClient,
		Scheme:                scheme,
		ManifestsTemplatePath: tmpDir,
		APIReader:             k8sClient,
		Getenv: fakeGetenv(map[string]string{
			config.ModelRegistryOperatorImage: "fake-op@sha256:aaa",
			config.RestImage:                  "fake-rest@sha256:bbb",
		}),
		Deployer: &webhookCleanupDeployer{
			inner: deploy.NewDeployer(
				deploy.WithFieldOwner("aihub"),
				deploy.WithApplyOrder(),
				deploy.WithCache(),
				deploy.WithMergeStrategy(
					schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"},
					deploy.MergeDeployments,
				),
				deploy.WithLegacyOwners(
					schema.GroupVersionKind{
						Group:   "components.platform.opendatahub.io",
						Version: "v1alpha1",
						Kind:    "ModelRegistry",
					},
				),
			),
			client: k8sClient,
		},
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "default-aihub"}}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	// Assert HTTPROUTE_NAMESPACE on both child Deployments.
	for _, depName := range []string{childDeploymentName, catalogDeploymentName} {
		dep := &appsv1.Deployment{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: appNs, Name: depName}, dep); err != nil {
			t.Fatalf("%s not found: %v", depName, err)
		}
		c := findContainer(t, dep, childManagerContainer)
		assertEnv(t, c, config.HTTPRouteNamespaceEnv, appNs)
	}
}

// TestAIHubLegacyOwnerAdoption_Envtest reproduces an upgrade from the old
// single model-registry-operator install, where the opendatahub-operator's
// ModelRegistry *component* deployed and owns the core operator's
// ServiceAccount and ClusterRole. When the AIHub operator later renders and
// applies the same resources with itself as owner, the stale ModelRegistry
// controller owner-ref must be stripped (via deploy.WithLegacyOwners) instead
// of colliding with AIHub's own controller ref ("Only one reference can have
// Controller set to true").
func TestAIHubLegacyOwnerAdoption_Envtest(t *testing.T) {
	// --- Skip guard: envtest binaries ---
	binAssetsDir := filepath.Join("..", "..", "bin", "k8s",
		fmt.Sprintf("1.35.0-%s-%s", goruntime.GOOS, goruntime.GOARCH))
	if v := os.Getenv("KUBEBUILDER_ASSETS"); v != "" {
		binAssetsDir = v
	}
	if _, err := os.Stat(filepath.Join(binAssetsDir, "kube-apiserver")); err != nil {
		t.Skipf("envtest binaries not available at %s: %v", binAssetsDir, err)
	}

	tmpDir := assembleManifests(t)
	scheme := testScheme(t)

	aihubCRDPath := filepath.Join("..", "..", "config", "overlays", "aihub",
		"components.platform.opendatahub.io_aihubs.yaml")
	aihubCRDBytes, err := os.ReadFile(aihubCRDPath)
	if err != nil {
		t.Fatalf("reading AIHub CRD: %v", err)
	}
	crdTmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(crdTmpDir, "aihubs.yaml"), aihubCRDBytes, 0o644); err != nil {
		t.Fatalf("writing AIHub CRD to temp dir: %v", err)
	}

	useExisting := false
	testEnvLocal := &envtest.Environment{
		Scheme: scheme,
		CRDDirectoryPaths: []string{
			filepath.Join("..", "..", "config", "crd", "bases"),
			filepath.Join("testdata", "crd"),
			crdTmpDir,
		},
		ErrorIfCRDPathMissing: true,
		BinaryAssetsDirectory: binAssetsDir,
		UseExistingCluster:    &useExisting,
	}

	cfg, err := testEnvLocal.Start()
	if err != nil {
		t.Fatalf("starting envtest: %v", err)
	}
	defer func() {
		if err := testEnvLocal.Stop(); err != nil {
			t.Logf("warning: stopping envtest: %v", err)
		}
	}()

	k8sClient, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatalf("creating client: %v", err)
	}

	ctx := context.Background()
	appNs := "legacy-owner-app-ns"
	regNs := "legacy-owner-reg-ns"

	for _, ns := range []string{appNs, regNs} {
		nsObj := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}
		if err := k8sClient.Create(ctx, nsObj); err != nil && !apierrors.IsAlreadyExists(err) {
			t.Fatalf("creating namespace %s: %v", ns, err)
		}
	}

	// Legacy owner-ref stamped by the pre-upgrade opendatahub-operator
	// ModelRegistry component, as controller.
	legacyOwnerRefs := []metav1.OwnerReference{
		{
			APIVersion:         "components.platform.opendatahub.io/v1alpha1",
			Kind:               "ModelRegistry",
			Name:               "default-modelregistry",
			UID:                "12345678-1234-1234-1234-1234567890ab",
			Controller:         new(true),
			BlockOwnerDeletion: new(true),
		},
	}

	// Pre-create the namespaced SA the core operator deploy targets, already
	// owned (as controller) by the legacy ModelRegistry component.
	legacySA := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:            childDeploymentName,
			Namespace:       appNs,
			OwnerReferences: legacyOwnerRefs,
		},
	}
	if err := k8sClient.Create(ctx, legacySA); err != nil {
		t.Fatalf("pre-creating legacy-owned ServiceAccount: %v", err)
	}

	// Pre-create the cluster-scoped ClusterRole the core operator deploy
	// targets, likewise owned by the legacy ModelRegistry component.
	legacyClusterRole := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "model-registry-operator-manager-role",
			OwnerReferences: legacyOwnerRefs,
		},
	}
	if err := k8sClient.Create(ctx, legacyClusterRole); err != nil {
		t.Fatalf("pre-creating legacy-owned ClusterRole: %v", err)
	}

	// Pre-create the cluster-scoped webhook configurations the core operator
	// deploy targets, likewise owned by the legacy ModelRegistry component.
	// These are the resources that actually collided in production: unlike
	// the SA/ClusterRole above, a legacy webhook config also lacks the
	// AIHub Deployer's part-of=aihub label, so a label-scoped cache Get
	// would miss it entirely (see TestAIHubLegacyOwnerAdoption_CacheMiss_Envtest).
	legacyMWC := &admissionregistrationv1.MutatingWebhookConfiguration{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "model-registry-operator-mutating-webhook-configuration",
			OwnerReferences: legacyOwnerRefs,
		},
		Webhooks: []admissionregistrationv1.MutatingWebhook{
			{
				Name: "mmodelregistry.opendatahub.io",
				ClientConfig: admissionregistrationv1.WebhookClientConfig{
					Service: &admissionregistrationv1.ServiceReference{
						Name:      "webhook-service",
						Namespace: appNs,
						Path:      new("/mutate-modelregistry-opendatahub-io-modelregistry"),
					},
				},
				SideEffects:             new(admissionregistrationv1.SideEffectClassNone),
				AdmissionReviewVersions: []string{"v1"},
			},
		},
	}
	if err := k8sClient.Create(ctx, legacyMWC); err != nil {
		t.Fatalf("pre-creating legacy-owned MutatingWebhookConfiguration: %v", err)
	}

	legacyVWC := &admissionregistrationv1.ValidatingWebhookConfiguration{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "model-registry-operator-validating-webhook-configuration",
			OwnerReferences: legacyOwnerRefs,
		},
		Webhooks: []admissionregistrationv1.ValidatingWebhook{
			{
				Name: "vmodelregistry.opendatahub.io",
				ClientConfig: admissionregistrationv1.WebhookClientConfig{
					Service: &admissionregistrationv1.ServiceReference{
						Name:      "webhook-service",
						Namespace: appNs,
						Path:      new("/validate-modelregistry-opendatahub-io-modelregistry"),
					},
				},
				SideEffects:             new(admissionregistrationv1.SideEffectClassNone),
				AdmissionReviewVersions: []string{"v1"},
			},
		},
	}
	if err := k8sClient.Create(ctx, legacyVWC); err != nil {
		t.Fatalf("pre-creating legacy-owned ValidatingWebhookConfiguration: %v", err)
	}

	aihub := &aihubv1alpha1.AIHub{
		ObjectMeta: metav1.ObjectMeta{Name: "default-aihub"},
		Spec: aihubv1alpha1.AIHubSpec{
			ApplicationNamespace: appNs,
			InstancesNamespace:   regNs,
		},
	}
	if err := k8sClient.Create(ctx, aihub); err != nil {
		t.Fatalf("creating AIHub: %v", err)
	}

	r := &AIHubReconciler{
		Client:                k8sClient,
		Scheme:                scheme,
		ManifestsTemplatePath: tmpDir,
		APIReader:             k8sClient,
		Getenv: fakeGetenv(map[string]string{
			config.ModelRegistryOperatorImage: "fake-op@sha256:aaa",
			config.RestImage:                  "fake-rest@sha256:bbb",
		}),
		Deployer: &webhookCleanupDeployer{
			inner: deploy.NewDeployer(
				deploy.WithFieldOwner("aihub"),
				deploy.WithApplyOrder(),
				deploy.WithCache(),
				deploy.WithMergeStrategy(
					schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"},
					deploy.MergeDeployments,
				),
				deploy.WithLegacyOwners(
					schema.GroupVersionKind{
						Group:   "components.platform.opendatahub.io",
						Version: "v1alpha1",
						Kind:    "ModelRegistry",
					},
				),
			),
			client: k8sClient,
		},
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "default-aihub"}}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("reconcile failed (legacy owner-ref should have been adopted, not collided with): %v", err)
	}

	assertSoleAIHubOwner := func(t *testing.T, refs []metav1.OwnerReference) {
		t.Helper()
		if len(refs) != 1 {
			t.Fatalf("expected exactly one owner reference, got %d: %+v", len(refs), refs)
		}
		ref := refs[0]
		if ref.Kind != "AIHub" || ref.Name != "default-aihub" {
			t.Fatalf("expected sole owner AIHub/default-aihub, got %s/%s", ref.Kind, ref.Name)
		}
		if ref.Controller == nil || !*ref.Controller {
			t.Fatalf("expected AIHub owner reference to be controller, got %+v", ref)
		}
	}

	sa := &corev1.ServiceAccount{}
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: appNs, Name: childDeploymentName}, sa); err != nil {
		t.Fatalf("getting adopted ServiceAccount: %v", err)
	}
	assertSoleAIHubOwner(t, sa.OwnerReferences)

	cr := &rbacv1.ClusterRole{}
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "model-registry-operator-manager-role"}, cr); err != nil {
		t.Fatalf("getting adopted ClusterRole: %v", err)
	}
	assertSoleAIHubOwner(t, cr.OwnerReferences)

	mwc := &admissionregistrationv1.MutatingWebhookConfiguration{}
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "model-registry-operator-mutating-webhook-configuration"}, mwc); err != nil {
		t.Fatalf("getting adopted MutatingWebhookConfiguration: %v", err)
	}
	assertSoleAIHubOwner(t, mwc.OwnerReferences)

	vwc := &admissionregistrationv1.ValidatingWebhookConfiguration{}
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "model-registry-operator-validating-webhook-configuration"}, vwc); err != nil {
		t.Fatalf("getting adopted ValidatingWebhookConfiguration: %v", err)
	}
	assertSoleAIHubOwner(t, vwc.OwnerReferences)
}

// TestAIHubSelectorMigration_Envtest verifies that a child Deployment left
// behind by the legacy single-operator install — whose immutable
// spec.selector carries extra platform labels the current manifests no
// longer render — is deleted and recreated with the canonical selector,
// and that a Deployment already on the canonical selector is left alone.
func TestAIHubSelectorMigration_Envtest(t *testing.T) {
	// --- Skip guard: envtest binaries ---
	binAssetsDir := filepath.Join("..", "..", "bin", "k8s",
		fmt.Sprintf("1.35.0-%s-%s", goruntime.GOOS, goruntime.GOARCH))
	if v := os.Getenv("KUBEBUILDER_ASSETS"); v != "" {
		binAssetsDir = v
	}
	if _, err := os.Stat(filepath.Join(binAssetsDir, "kube-apiserver")); err != nil {
		t.Skipf("envtest binaries not available at %s: %v", binAssetsDir, err)
	}

	tmpDir := assembleManifests(t)
	scheme := testScheme(t)

	aihubCRDPath := filepath.Join("..", "..", "config", "overlays", "aihub",
		"components.platform.opendatahub.io_aihubs.yaml")
	aihubCRDBytes, err := os.ReadFile(aihubCRDPath)
	if err != nil {
		t.Fatalf("reading AIHub CRD: %v", err)
	}
	crdTmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(crdTmpDir, "aihubs.yaml"), aihubCRDBytes, 0o644); err != nil {
		t.Fatalf("writing AIHub CRD to temp dir: %v", err)
	}

	useExisting := false
	testEnvLocal := &envtest.Environment{
		Scheme: scheme,
		CRDDirectoryPaths: []string{
			filepath.Join("..", "..", "config", "crd", "bases"),
			filepath.Join("testdata", "crd"),
			crdTmpDir,
		},
		ErrorIfCRDPathMissing: true,
		BinaryAssetsDirectory: binAssetsDir,
		UseExistingCluster:    &useExisting,
	}

	cfg, err := testEnvLocal.Start()
	if err != nil {
		t.Fatalf("starting envtest: %v", err)
	}
	defer func() {
		if err := testEnvLocal.Stop(); err != nil {
			t.Logf("warning: stopping envtest: %v", err)
		}
	}()

	k8sClient, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatalf("creating client: %v", err)
	}

	ctx := context.Background()
	appNs := "selector-migration-app-ns"
	regNs := "selector-migration-reg-ns"

	for _, ns := range []string{appNs, regNs} {
		nsObj := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}
		if err := k8sClient.Create(ctx, nsObj); err != nil && !apierrors.IsAlreadyExists(err) {
			t.Fatalf("creating namespace %s: %v", ns, err)
		}
	}

	// Derive the canonical selector from the same rendered manifests the
	// reconciler applies, so this test tracks config/overlays/odh instead of
	// duplicating a hardcoded label set that could drift.
	renderPath := filepath.Join(tmpDir, "modelregistry", "overlays", "odh")
	rendered, err := kustomize.Render(renderPath, nil, kustomize.WithNamespace(appNs))
	if err != nil {
		t.Fatalf("rendering model-registry manifests: %v", err)
	}
	var canonicalSelector map[string]string
	for i := range rendered {
		if rendered[i].GetKind() == "Deployment" && rendered[i].GetName() == childDeploymentName {
			canonicalSelector, _, err = unstructured.NestedStringMap(rendered[i].Object, "spec", "selector", "matchLabels")
			if err != nil {
				t.Fatalf("reading canonical selector: %v", err)
			}
		}
	}
	if len(canonicalSelector) == 0 {
		t.Fatal("could not derive canonical selector from rendered manifests")
	}

	// Legacy selector: the canonical labels plus the two platform labels the
	// pre-upgrade single-operator install stamped into the selector.
	legacySelector := map[string]string{
		"app.kubernetes.io/part-of":                  "model-registry-operator",
		"app.opendatahub.io/model-registry-operator": "true",
	}
	maps.Copy(legacySelector, canonicalSelector)

	legacyDep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      childDeploymentName,
			Namespace: appNs,
		},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: legacySelector},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: legacySelector},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: childManagerContainer, Image: "legacy-image:v1"},
					},
				},
			},
		},
	}
	if err := k8sClient.Create(ctx, legacyDep); err != nil {
		t.Fatalf("pre-creating legacy Deployment: %v", err)
	}
	legacyUID := legacyDep.UID

	aihub := &aihubv1alpha1.AIHub{
		ObjectMeta: metav1.ObjectMeta{Name: "default-aihub"},
		Spec: aihubv1alpha1.AIHubSpec{
			ApplicationNamespace: appNs,
			InstancesNamespace:   regNs,
		},
	}
	if err := k8sClient.Create(ctx, aihub); err != nil {
		t.Fatalf("creating AIHub: %v", err)
	}

	r := &AIHubReconciler{
		Client:                k8sClient,
		Scheme:                scheme,
		ManifestsTemplatePath: tmpDir,
		APIReader:             k8sClient,
		Getenv: fakeGetenv(map[string]string{
			config.ModelRegistryOperatorImage: "fake-op@sha256:aaa",
			config.RestImage:                  "fake-rest@sha256:bbb",
		}),
		Deployer: &webhookCleanupDeployer{
			inner: deploy.NewDeployer(
				deploy.WithFieldOwner("aihub"),
				deploy.WithApplyOrder(),
				deploy.WithCache(),
				deploy.WithMergeStrategy(
					schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"},
					deploy.MergeDeployments,
				),
				deploy.WithLegacyOwners(
					schema.GroupVersionKind{
						Group:   "components.platform.opendatahub.io",
						Version: "v1alpha1",
						Kind:    "ModelRegistry",
					},
				),
			),
			client: k8sClient,
		},
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "default-aihub"}}

	// --- Reconcile #1: detects the incompatible selector, deletes the
	// legacy Deployment, and requeues without erroring or calling Deploy. ---
	result, err := r.Reconcile(ctx, req)
	if err != nil {
		t.Fatalf("reconcile #1 failed (immutable selector should be migrated, not surfaced as an apply error): %v", err)
	}
	if result.RequeueAfter <= 0 {
		t.Errorf("reconcile #1: expected a short RequeueAfter while the Deployment is being recreated, got %v", result.RequeueAfter)
	}

	// --- Reconcile #2: the legacy Deployment is gone, so the Deployer
	// creates it fresh with the canonical selector. ---
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("reconcile #2 failed: %v", err)
	}

	migratedDep := &appsv1.Deployment{}
	depKey := types.NamespacedName{Namespace: appNs, Name: childDeploymentName}
	if err := k8sClient.Get(ctx, depKey, migratedDep); err != nil {
		t.Fatalf("getting migrated child Deployment: %v", err)
	}
	if migratedDep.UID == legacyUID {
		t.Fatal("expected the Deployment to be recreated (new UID), but UID is unchanged")
	}
	if migratedDep.Spec.Selector == nil || !maps.Equal(migratedDep.Spec.Selector.MatchLabels, canonicalSelector) {
		t.Fatalf("migrated selector = %v, want canonical selector %v", migratedDep.Spec.Selector, canonicalSelector)
	}
	migratedUID := migratedDep.UID

	// --- Reconcile #3: selector is already canonical, so the Deployment
	// must NOT be deleted/recreated again (delete only when necessary). ---
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("reconcile #3 failed: %v", err)
	}
	stableDep := &appsv1.Deployment{}
	if err := k8sClient.Get(ctx, depKey, stableDep); err != nil {
		t.Fatalf("getting Deployment after reconcile #3: %v", err)
	}
	if stableDep.UID != migratedUID {
		t.Fatalf("Deployment was recreated on an idempotent reconcile: UID changed from %s to %s", migratedUID, stableDep.UID)
	}
}
