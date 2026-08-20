package controller

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	goruntime "runtime"
	"sync/atomic"
	"testing"
	"time"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8slabels "k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	aihubv1alpha1 "github.com/opendatahub-io/model-registry-operator/api/aihub/v1alpha1"
	"github.com/opendatahub-io/model-registry-operator/internal/controller/config"
	"github.com/opendatahub-io/odh-platform-utilities/pkg/deploy"
	platformlabels "github.com/opendatahub-io/odh-platform-utilities/pkg/metadata/labels"
)

// --- Unit tests for platformConfigMapToAIHub and isPlatformConfigMap ---

func TestPlatformConfigMapToAIHub(t *testing.T) {
	tests := []struct {
		name     string
		objName  string
		wantLen  int
		wantName string
	}{
		{
			name:     "matching ConfigMap name enqueues singleton",
			objName:  "odh-modelregistry-config",
			wantLen:  1,
			wantName: "default-aihub",
		},
		{
			name:    "non-matching ConfigMap name returns nil",
			objName: "some-other-configmap",
			wantLen: 0,
		},
		{
			name:    "old ConfigMap name returns nil",
			objName: "odh-aihub-config",
			wantLen: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obj := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      tt.objName,
					Namespace: "some-ns",
				},
			}
			reqs := platformConfigMapToAIHub(context.Background(), obj)
			if len(reqs) != tt.wantLen {
				t.Fatalf("got %d requests, want %d", len(reqs), tt.wantLen)
			}
			if tt.wantLen > 0 {
				if reqs[0].Name != tt.wantName {
					t.Errorf("request Name = %q, want %q", reqs[0].Name, tt.wantName)
				}
				if reqs[0].Namespace != "" {
					t.Errorf("request Namespace = %q, want empty (cluster-scoped)", reqs[0].Namespace)
				}
			}
		})
	}
}

func TestIsPlatformConfigMap(t *testing.T) {
	tests := []struct {
		name    string
		objName string
		want    bool
	}{
		{"matching name", "odh-modelregistry-config", true},
		{"non-matching name", "other-cm", false},
		{"old name", "odh-aihub-config", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obj := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: tt.objName},
			}
			if got := isPlatformConfigMap(obj); got != tt.want {
				t.Errorf("isPlatformConfigMap(%q) = %v, want %v", tt.objName, got, tt.want)
			}
		})
	}
}

// --- Manager-based envtest proving the ConfigMap watch triggers reconcile ---

func TestAIHubConfigMapWatch_Envtest(t *testing.T) {
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
	crdTmpDir := t.TempDir()
	for _, crdInfo := range []struct{ src, dst string }{
		{filepath.Join("..", "..", "config", "overlays", "aihub", "components.platform.opendatahub.io_aihubs.yaml"), "aihubs.yaml"},
		{filepath.Join("..", "..", "config", "overlays", "aihub", "aihub.opendatahub.io_catalogs.yaml"), "catalogs.yaml"},
	} {
		data, err := os.ReadFile(crdInfo.src)
		if err != nil {
			t.Fatalf("reading CRD %s: %v", crdInfo.src, err)
		}
		if err := os.WriteFile(filepath.Join(crdTmpDir, crdInfo.dst), data, 0o644); err != nil {
			t.Fatalf("writing CRD %s to temp dir: %v", crdInfo.dst, err)
		}
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

	// --- Build a manager mirroring cmd/aihub.go cache config ---
	deployerSelector := k8slabels.SelectorFromSet(map[string]string{
		platformlabels.PlatformPartOf: "aihub",
	})
	deployerCacheObj := cache.ByObject{Label: deployerSelector}

	// Set a logger so controller-runtime doesn't complain.
	ctrl.SetLogger(zap.New(zap.WriteTo(os.Stderr), zap.UseDevMode(true)))

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme: scheme,
		Cache: cache.Options{
			ByObject: map[client.Object]cache.ByObject{
				&appsv1.Deployment{}:         deployerCacheObj,
				&corev1.Service{}:            deployerCacheObj,
				&corev1.ServiceAccount{}:     deployerCacheObj,
				&corev1.ConfigMap{}:          deployerCacheObj,
				&rbacv1.Role{}:               deployerCacheObj,
				&rbacv1.RoleBinding{}:        deployerCacheObj,
				&rbacv1.ClusterRole{}:        deployerCacheObj,
				&rbacv1.ClusterRoleBinding{}: deployerCacheObj,
				&admissionregistrationv1.ValidatingWebhookConfiguration{}: deployerCacheObj,
				&admissionregistrationv1.MutatingWebhookConfiguration{}:   deployerCacheObj,
			},
		},
		// Disable metrics/health to avoid port conflicts.
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
	})
	if err != nil {
		t.Fatalf("creating manager: %v", err)
	}

	// --- Track reconcile invocations with an atomic counter ---
	// We use an unexported onReconcile hook field on AIHubReconciler.
	// This is test-only: the field is nil in production (see controller).
	var reconcileCount atomic.Int64

	r := &AIHubReconciler{
		Client:                mgr.GetClient(),
		Scheme:                mgr.GetScheme(),
		ManifestsTemplatePath: tmpDir,
		APIReader:             mgr.GetAPIReader(),
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
			client: mgr.GetClient(),
		},
		onReconcile: func() { reconcileCount.Add(1) },
	}

	if err := r.SetupWithManager(mgr); err != nil {
		t.Fatalf("SetupWithManager: %v", err)
	}

	// --- Start manager in background ---
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		if err := mgr.Start(ctx); err != nil {
			// Manager returns error on context cancel, which is expected.
			t.Logf("manager stopped: %v", err)
		}
	}()

	// Wait for cache sync.
	if !mgr.GetCache().WaitForCacheSync(ctx) {
		t.Fatal("cache sync failed")
	}

	// Use an uncached client for test setup to avoid label-scoped cache issues.
	directClient, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatalf("creating direct client: %v", err)
	}

	appNs := "watch-app-ns"
	regNs := "watch-reg-ns"

	// --- Create namespaces ---
	for _, ns := range []string{appNs, regNs} {
		nsObj := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}
		if err := directClient.Create(ctx, nsObj); err != nil && !apierrors.IsAlreadyExists(err) {
			t.Fatalf("creating namespace %s: %v", ns, err)
		}
	}

	// --- Create the singleton AIHub ---
	aihub := &aihubv1alpha1.AIHub{
		ObjectMeta: metav1.ObjectMeta{Name: "default-aihub"},
		Spec: aihubv1alpha1.AIHubSpec{
			ApplicationNamespace: appNs,
			InstancesNamespace:   regNs,
		},
	}
	if err := directClient.Create(ctx, aihub); err != nil {
		t.Fatalf("creating AIHub: %v", err)
	}

	// Wait for the initial reconcile to fire (creates child deployments).
	waitFor(t, 30*time.Second, 200*time.Millisecond, func() bool {
		return reconcileCount.Load() > 0
	}, "initial reconcile to fire")

	// --- Patch both child Deployments to Available ---
	patchDeploymentAvailable(t, ctx, directClient, appNs, childDeploymentName)
	patchDeploymentAvailable(t, ctx, directClient, appNs, catalogDeploymentName)

	// Wait for a reconcile that sees both children Available.
	waitFor(t, 30*time.Second, 200*time.Millisecond, func() bool {
		got := &aihubv1alpha1.AIHub{}
		if err := directClient.Get(ctx, types.NamespacedName{Name: "default-aihub"}, got); err != nil {
			return false
		}
		for _, c := range got.Status.Conditions {
			if c.Type == string(metav1.StatusReasonUnknown) {
				continue
			}
		}
		return got.Status.Phase == "Ready"
	}, "AIHub to reach Ready phase")

	// Record reconcile count before creating the ConfigMap.
	countBefore := reconcileCount.Load()

	// --- Create the platform-version ConfigMap ---
	platformCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      platformVersionConfigMap,
			Namespace: appNs,
		},
		Data: map[string]string{
			platformVersionConfigMapKey: "2.20.0",
		},
	}
	if err := directClient.Create(ctx, platformCM); err != nil {
		t.Fatalf("creating platform ConfigMap: %v", err)
	}

	// The watch should trigger a reconcile. Wait for it.
	waitFor(t, 30*time.Second, 200*time.Millisecond, func() bool {
		return reconcileCount.Load() > countBefore
	}, "reconcile triggered by ConfigMap creation")

	// Verify platform release version in status.
	waitFor(t, 10*time.Second, 200*time.Millisecond, func() bool {
		got := &aihubv1alpha1.AIHub{}
		if err := directClient.Get(ctx, types.NamespacedName{Name: "default-aihub"}, got); err != nil {
			return false
		}
		return got.GetReleaseStatus().GetPlatformRelease() == "2.20.0"
	}, "platform release to be 2.20.0")

	// --- Update the ConfigMap to a new version ---
	countBefore2 := reconcileCount.Load()

	if err := directClient.Get(ctx, types.NamespacedName{Name: platformVersionConfigMap, Namespace: appNs}, platformCM); err != nil {
		t.Fatalf("re-fetching platform ConfigMap: %v", err)
	}
	platformCM.Data[platformVersionConfigMapKey] = "2.21.0"
	if err := directClient.Update(ctx, platformCM); err != nil {
		t.Fatalf("updating platform ConfigMap: %v", err)
	}

	// Wait for the watch-triggered reconcile.
	waitFor(t, 30*time.Second, 200*time.Millisecond, func() bool {
		return reconcileCount.Load() > countBefore2
	}, "reconcile triggered by ConfigMap update")

	// Verify updated platform release version in status.
	waitFor(t, 10*time.Second, 200*time.Millisecond, func() bool {
		got := &aihubv1alpha1.AIHub{}
		if err := directClient.Get(ctx, types.NamespacedName{Name: "default-aihub"}, got); err != nil {
			return false
		}
		return got.GetReleaseStatus().GetPlatformRelease() == "2.21.0"
	}, "platform release to be 2.21.0")
}

// waitFor polls condition with the given timeout and interval, failing with msg if not met.
func waitFor(t *testing.T, timeout, interval time.Duration, condition func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(interval)
	}
	t.Fatalf("timed out waiting for: %s", msg)
}

// patchDeploymentAvailable waits for a Deployment to exist and patches its status to Available.
func patchDeploymentAvailable(t *testing.T, ctx context.Context, c client.Client, namespace, name string) {
	t.Helper()
	dep := &appsv1.Deployment{}
	waitFor(t, 30*time.Second, 200*time.Millisecond, func() bool {
		err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, dep)
		return err == nil
	}, fmt.Sprintf("Deployment %s/%s to exist", namespace, name))

	dep.Status.Conditions = []appsv1.DeploymentCondition{
		{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionTrue},
	}
	if err := c.Status().Update(ctx, dep); err != nil {
		t.Fatalf("patching Deployment %s/%s status: %v", namespace, name, err)
	}
}
