package controller

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	goruntime "runtime"
	"testing"
	"time"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	aihubv1alpha1 "github.com/opendatahub-io/model-registry-operator/api/aihub/v1alpha1"
	catalogv1alpha1 "github.com/opendatahub-io/model-registry-operator/api/catalog/v1alpha1"
	"github.com/opendatahub-io/model-registry-operator/internal/controller/config"
	"github.com/opendatahub-io/odh-platform-utilities/api/common"
	"github.com/opendatahub-io/odh-platform-utilities/pkg/deploy"
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
			ObjectMeta: metav1.ObjectMeta{Name: "default"},
			Spec:       aihubv1alpha1.AIHubSpec{},
		}
		err := k8sClient.Create(ctx, bad)
		if err == nil {
			_ = k8sClient.Delete(ctx, bad)
			t.Fatal("expected create of AIHub with empty namespaces to be rejected, but it succeeded")
		}
		t.Logf("correctly rejected AIHub with empty namespaces: %v", err)
	})

	// --- Create the real singleton AIHub ---
	aihub := &aihubv1alpha1.AIHub{
		ObjectMeta: metav1.ObjectMeta{Name: "default"},
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
			),
			client: k8sClient,
		},
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "default"}}

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
	hasFinalizer := false
	for _, f := range got.Finalizers {
		if f == aihubFinalizer {
			hasFinalizer = true
			break
		}
	}
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
		if ref.Kind == "AIHub" && ref.Name == "default" &&
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

	// --- Deletion / finalizer teardown ---
	t.Run("deletion/finalizer", func(t *testing.T) {
		// Re-fetch to get latest resourceVersion.
		fresh := &aihubv1alpha1.AIHub{}
		if err := k8sClient.Get(ctx, req.NamespacedName, fresh); err != nil {
			t.Fatalf("re-fetching AIHub: %v", err)
		}
		if err := k8sClient.Delete(ctx, fresh); err != nil {
			t.Fatalf("deleting AIHub: %v", err)
		}

		// Bounded reconcile loop to drain ordered teardown.
		const maxIter = 10
		for i := 0; i < maxIter; i++ {
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
		t.Fatalf("AIHub not removed after %d reconcile iterations", maxIter)
	})
}
