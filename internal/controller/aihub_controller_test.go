package controller

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	aihubv1alpha1 "github.com/opendatahub-io/model-registry-operator/api/aihub/v1alpha1"
	catalogv1alpha1 "github.com/opendatahub-io/model-registry-operator/api/catalog/v1alpha1"
	"github.com/opendatahub-io/model-registry-operator/internal/controller/config"
	"github.com/opendatahub-io/odh-platform-utilities/api/common"
	"github.com/opendatahub-io/odh-platform-utilities/pkg/controller/conditions"
	"github.com/opendatahub-io/odh-platform-utilities/pkg/deploy"
	"github.com/opendatahub-io/odh-platform-utilities/pkg/render/kustomize"
)

// --- Pure tests (no cluster) ---

func TestStampChildOperatorDeployment_Full(t *testing.T) {
	u := makeDeploymentUnstructured(t, "test-deploy", "ns", "manager", "original-image:v1",
		[]corev1.EnvVar{
			{Name: config.RestImage, Value: "old-rest"},
			{Name: config.RegistriesNamespace, Value: "old-ns"},
		})

	images := ChildImages{
		OperatorImage: "new-operator:v2",
		OperandEnv: []corev1.EnvVar{
			{Name: config.RestImage, Value: "new-rest@sha256:abc"},
			{Name: config.PostgresImage, Value: "new-pg@sha256:def"},
		},
	}

	if err := stampChildOperatorDeployment(u, images, "my-reg-ns", ""); err != nil {
		t.Fatal(err)
	}

	deploy := deploymentFromUnstructured(t, u)
	c := findContainer(t, deploy, "manager")

	if c.Image != "new-operator:v2" {
		t.Errorf("Image = %q, want %q", c.Image, "new-operator:v2")
	}

	assertEnv(t, c, config.RestImage, "new-rest@sha256:abc")
	assertEnv(t, c, config.PostgresImage, "new-pg@sha256:def")
	assertEnv(t, c, config.RegistriesNamespace, "my-reg-ns")
}

func TestStampChildOperatorDeployment_EmptyOperatorImage(t *testing.T) {
	originalImage := "keep-me:v1"
	u := makeDeploymentUnstructured(t, "test-deploy", "ns", "manager", originalImage, nil)

	images := ChildImages{OperatorImage: ""}

	if err := stampChildOperatorDeployment(u, images, "reg-ns", ""); err != nil {
		t.Fatal(err)
	}

	deploy := deploymentFromUnstructured(t, u)
	c := findContainer(t, deploy, "manager")

	if c.Image != originalImage {
		t.Errorf("Image = %q, want %q (empty OperatorImage should leave image untouched)", c.Image, originalImage)
	}
	assertEnv(t, c, config.RegistriesNamespace, "reg-ns")
}

func TestStampChildOperatorDeployment_MissingContainer(t *testing.T) {
	u := makeDeploymentUnstructured(t, "test-deploy", "ns", "not-manager", "img:v1", nil)
	err := stampChildOperatorDeployment(u, ChildImages{}, "ns", "")
	if err == nil {
		t.Fatal("expected error for missing manager container")
	}
}

// TestRender_ModelRegistryOverlay renders the real kustomize overlay via the hack
// script and verifies the stamped output.
func TestRender_ModelRegistryOverlay(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available, skipping render test")
	}

	tmpDir := t.TempDir()
	repoRoot := filepath.Join("..", "..")
	hackScript := filepath.Join(repoRoot, "hack", "get_aihub_manifests.sh")
	cmd := exec.Command("bash", hackScript, tmpDir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("hack script failed: %v\n%s", err, out)
	}

	renderPath := filepath.Join(tmpDir, "modelregistry", "overlays", "odh")

	resources, err := kustomize.Render(renderPath, nil, kustomize.WithNamespace("test-app-ns"))
	if err != nil {
		t.Fatalf("kustomize.Render failed: %v", err)
	}

	// Strip cluster-scoped namespaces.
	for i := range resources {
		if clusterScopedKinds[resources[i].GetKind()] {
			resources[i].SetNamespace("")
		}
	}

	images := ChildImages{
		OperatorImage: "stamped-image@sha256:111",
		OperandEnv: []corev1.EnvVar{
			{Name: config.RestImage, Value: "stamped-rest@sha256:222"},
		},
	}

	var mrDeployFound, catalogDeployFound bool
	var crdCount int
	for i := range resources {
		kind := resources[i].GetKind()

		// Check cluster-scoped kinds have empty namespace.
		if clusterScopedKinds[kind] && resources[i].GetNamespace() != "" {
			t.Errorf("cluster-scoped %s %q has non-empty namespace %q",
				kind, resources[i].GetName(), resources[i].GetNamespace())
		}

		if kind == "CustomResourceDefinition" {
			crdCount++
		}

		if kind == "Deployment" {
			name := resources[i].GetName()
			if name == childDeploymentName || name == catalogDeploymentName {
				if err := stampChildOperatorDeployment(&resources[i], images, "my-reg-ns", ""); err != nil {
					t.Fatalf("stampChildOperatorDeployment(%s): %v", name, err)
				}
				deploy := deploymentFromUnstructured(t, &resources[i])
				c := findContainer(t, deploy, "manager")
				if c.Image != images.OperatorImage {
					t.Errorf("%s: stamped Image = %q, want %q", name, c.Image, images.OperatorImage)
				}
				assertEnv(t, c, config.RegistriesNamespace, "my-reg-ns")
				if name == childDeploymentName {
					assertEnv(t, c, config.RestImage, "stamped-rest@sha256:222")
					mrDeployFound = true
				} else {
					catalogDeployFound = true
				}
			}
		}
	}

	if !mrDeployFound {
		t.Error("MR operator Deployment not found in rendered resources")
	}
	if !catalogDeployFound {
		t.Error("catalog operator Deployment not found in rendered resources")
	}
	if crdCount < 1 {
		t.Errorf("expected at least 1 CRD, got %d", crdCount)
	}
}

// --- Mock deployer for reconcile tests ---

type mockDeployer struct {
	calls []deploy.DeployInput
	err   error
}

func (m *mockDeployer) Deploy(_ context.Context, in deploy.DeployInput) error {
	m.calls = append(m.calls, in)
	return m.err
}

// --- Helpers to build a test scheme and fake client ---

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{
		clientgoscheme.AddToScheme,
		apiextensionsv1.AddToScheme,
		admissionregistrationv1.AddToScheme,
		aihubv1alpha1.AddToScheme,
		catalogv1alpha1.AddToScheme,
	} {
		if err := add(s); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

// assembleManifests runs the hack script into a temp dir and returns the path.
func assembleManifests(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available, skipping reconcile test")
	}
	tmpDir := t.TempDir()
	repoRoot := filepath.Join("..", "..")
	hackScript := filepath.Join(repoRoot, "hack", "get_aihub_manifests.sh")
	cmd := exec.Command("bash", hackScript, tmpDir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("hack script failed: %v\n%s", err, out)
	}
	return tmpDir
}

// --- Fake-client reconcile test ---

func TestAIHubReconciler_Reconcile(t *testing.T) {
	tmpDir := assembleManifests(t)
	s := testScheme(t)

	appNs := "app-ns"
	regNs := "reg-ns"

	aihub := &aihubv1alpha1.AIHub{
		ObjectMeta: metav1.ObjectMeta{
			Name: "default",
		},
		Spec: aihubv1alpha1.AIHubSpec{
			ApplicationNamespace: appNs,
			InstancesNamespace:   regNs,
		},
	}

	// Seed the namespaces so the fake client accepts namespaced creates.
	appNsObj := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: appNs}}
	regNsObj := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: regNs}}

	fakeClient := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(aihub, appNsObj, regNsObj).
		WithStatusSubresource(&aihubv1alpha1.AIHub{}).
		Build()

	fakeEnvMap := map[string]string{
		config.ModelRegistryOperatorImage: "fake-op@sha256:aaa",
		config.RestImage:                  "fake-rest@sha256:bbb",
		config.PostgresImage:              "fake-pg@sha256:ccc",
	}

	mock := &mockDeployer{}
	reconciler := &AIHubReconciler{
		Client:                fakeClient,
		Scheme:                s,
		ManifestsTemplatePath: tmpDir,
		Getenv:                fakeGetenv(fakeEnvMap),
		Deployer:              mock,
		APIReader:             fakeClient,
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "default"}}
	ctx := context.Background()

	// First reconcile — the mock deployer does not actually create resources,
	// so the child Deployment Get will 404 → expect RequeueAfter > 0.
	result, err := reconciler.Reconcile(ctx, req)
	if err != nil {
		t.Fatalf("first Reconcile failed: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Error("expected RequeueAfter > 0 (child Deployment not created by mock)")
	}

	// Verify the mock deployer was called once.
	if len(mock.calls) != 1 {
		t.Fatalf("expected 1 deployer call, got %d", len(mock.calls))
	}

	// Verify the Owner is the AIHub CR.
	if mock.calls[0].Owner == nil || mock.calls[0].Owner.GetName() != "default" {
		t.Errorf("expected Owner name %q, got %v", "default", mock.calls[0].Owner)
	}

	// Verify the resources contain the child Deployments with stamped values.
	resources := mock.calls[0].Resources
	var mrDeployFound, catalogDeployFound bool
	var crdFound bool
	for _, res := range resources {
		kind := res.GetKind()

		// Every cluster-scoped kind must have empty namespace.
		if clusterScopedKinds[kind] && res.GetNamespace() != "" {
			t.Errorf("cluster-scoped %s %q has non-empty namespace %q",
				kind, res.GetName(), res.GetNamespace())
		}

		if kind == "CustomResourceDefinition" {
			crdFound = true
		}

		if kind == "Deployment" {
			name := res.GetName()
			if name == childDeploymentName || name == catalogDeploymentName {
				dep := deploymentFromUnstructured(t, &res)
				managerC := findContainer(t, dep, "manager")
				if managerC.Image != "fake-op@sha256:aaa" {
					t.Errorf("%s: manager image = %q, want %q", name, managerC.Image, "fake-op@sha256:aaa")
				}
				assertEnv(t, managerC, config.RegistriesNamespace, regNs)
				if name == childDeploymentName {
					assertEnv(t, managerC, config.RestImage, "fake-rest@sha256:bbb")
					mrDeployFound = true
				} else {
					catalogDeployFound = true
				}
			}
		}
	}
	if !mrDeployFound {
		t.Error("MR operator Deployment not found in deployer resources")
	}
	if !catalogDeployFound {
		t.Error("catalog operator Deployment not found in deployer resources")
	}
	if !crdFound {
		t.Error("expected at least 1 CustomResourceDefinition in deployer resources")
	}

	// Catalog CR must NOT exist yet — it is only created after both child
	// Deployments are Available, which the mock deployer does not simulate.
	catalogCR := &catalogv1alpha1.Catalog{}
	if err := fakeClient.Get(ctx, types.NamespacedName{
		Namespace: regNs,
		Name:      catalogCRName,
	}, catalogCR); !apierrors.IsNotFound(err) {
		t.Errorf("expected Catalog CR to not exist before children are Available, got err=%v", err)
	}

	// Verify status was written with Phase=NotReady (child deployment missing).
	got := &aihubv1alpha1.AIHub{}
	if err := fakeClient.Get(ctx, req.NamespacedName, got); err != nil {
		t.Fatal(err)
	}
	if got.Status.Phase != common.PhaseNotReady {
		t.Errorf("expected Phase=%q after first reconcile, got %q", common.PhaseNotReady, got.Status.Phase)
	}

	// Idempotency: second reconcile must not error.
	result2, err2 := reconciler.Reconcile(ctx, req)
	if err2 != nil {
		t.Fatalf("second Reconcile failed (idempotency): %v", err2)
	}
	if len(mock.calls) != 2 {
		t.Errorf("expected 2 deployer calls after second reconcile, got %d", len(mock.calls))
	}
	if result2.RequeueAfter == 0 {
		t.Error("expected RequeueAfter > 0 on second reconcile (child Deployment still not created)")
	}
}

// --- Finalizer tests ---

func TestAIHubReconciler_FinalizerAdded(t *testing.T) {
	tmpDir := assembleManifests(t)
	s := testScheme(t)

	aihub := &aihubv1alpha1.AIHub{
		ObjectMeta: metav1.ObjectMeta{Name: "default"},
		Spec: aihubv1alpha1.AIHubSpec{
			ApplicationNamespace: "app-ns",
			InstancesNamespace:   "reg-ns",
		},
	}
	appNsObj := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "app-ns"}}
	regNsObj := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "reg-ns"}}

	fakeClient := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(aihub, appNsObj, regNsObj).
		WithStatusSubresource(&aihubv1alpha1.AIHub{}).
		Build()

	reconciler := &AIHubReconciler{
		Client:                fakeClient,
		Scheme:                s,
		ManifestsTemplatePath: tmpDir,
		Getenv:                fakeGetenv(map[string]string{}),
		Deployer:              &mockDeployer{},
		APIReader:             fakeClient,
	}

	ctx := context.Background()
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "default"}}

	if _, err := reconciler.Reconcile(ctx, req); err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	// Verify the finalizer was added.
	got := &aihubv1alpha1.AIHub{}
	if err := fakeClient.Get(ctx, req.NamespacedName, got); err != nil {
		t.Fatal(err)
	}
	if !controllerutil.ContainsFinalizer(got, aihubFinalizer) {
		t.Errorf("expected finalizer %q on AIHub after first reconcile, got finalizers: %v", aihubFinalizer, got.Finalizers)
	}
}

func TestAIHubReconciler_DeletionCleanup(t *testing.T) {
	s := testScheme(t)

	regNs := "reg-ns"

	aihub := &aihubv1alpha1.AIHub{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "default",
			UID:        "test-aihub-uid",
			Finalizers: []string{aihubFinalizer},
		},
		Spec: aihubv1alpha1.AIHubSpec{
			ApplicationNamespace: "app-ns",
			InstancesNamespace:   regNs,
		},
	}

	catalog := &catalogv1alpha1.Catalog{
		ObjectMeta: metav1.ObjectMeta{
			Name:      catalogCRName,
			Namespace: regNs,
		},
	}
	catalog.SetGroupVersionKind(catalogv1alpha1.GroupVersion.WithKind("Catalog"))

	// Set the controller owner reference so IsControlledBy(cat, aihub) returns true.
	if err := controllerutil.SetControllerReference(aihub, catalog, s); err != nil {
		t.Fatalf("set owner ref: %v", err)
	}

	appNsObj := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "app-ns"}}
	regNsObj := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: regNs}}

	fakeClient := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(aihub, catalog, appNsObj, regNsObj).
		Build()

	reconciler := &AIHubReconciler{
		Client:                fakeClient,
		Scheme:                s,
		ManifestsTemplatePath: "", // deletion path returns before rendering
		Getenv:                fakeGetenv(map[string]string{}),
		Deployer:              &mockDeployer{},
		APIReader:             fakeClient,
	}

	ctx := context.Background()
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "default"}}

	// Delete the AIHub — fake client sets DeletionTimestamp and keeps the object
	// because of the finalizer.
	if err := fakeClient.Delete(ctx, aihub); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	// First reconcile: deletes the owned Catalog, then requeues (cleanup is not
	// done on the same pass — it always returns false after issuing the Delete).
	result, err := reconciler.Reconcile(ctx, req)
	if err != nil {
		t.Fatalf("first reconcile after delete failed: %v", err)
	}
	if result.RequeueAfter != 5*time.Second {
		t.Fatalf("first reconcile RequeueAfter = %v, want 5s (cleanup must not finish on the delete pass)", result.RequeueAfter)
	}

	catCheck := &catalogv1alpha1.Catalog{}
	catErr := fakeClient.Get(ctx, types.NamespacedName{Namespace: regNs, Name: catalogCRName}, catCheck)
	if !apierrors.IsNotFound(catErr) {
		t.Fatalf("expected Catalog to be gone after first reconcile, got err=%v", catErr)
	}

	if _, err := reconciler.Reconcile(ctx, req); err != nil {
		t.Fatalf("second reconcile after delete failed: %v", err)
	}

	got := &aihubv1alpha1.AIHub{}
	if err := fakeClient.Get(ctx, req.NamespacedName, got); !apierrors.IsNotFound(err) {
		t.Fatalf("expected AIHub to be removed once the finalizer cleared, got err=%v finalizers=%v", err, got.Finalizers)
	}
}

// --- Status and condition tests ---

func TestAIHubReconciler_StatusReady(t *testing.T) {
	tmpDir := assembleManifests(t)
	s := testScheme(t)

	appNs := "app-ns"
	regNs := "reg-ns"

	aihub := &aihubv1alpha1.AIHub{
		ObjectMeta: metav1.ObjectMeta{Name: "default"},
		Spec: aihubv1alpha1.AIHubSpec{
			ApplicationNamespace: appNs,
			InstancesNamespace:   regNs,
		},
	}

	// Seed child Deployments with Available=True.
	childDeploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      childDeploymentName,
			Namespace: appNs,
			Labels:    map[string]string{"app.kubernetes.io/part-of": "aihub"},
		},
		Status: appsv1.DeploymentStatus{
			Conditions: []appsv1.DeploymentCondition{
				{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionTrue},
			},
		},
	}
	catalogDeploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      catalogDeploymentName,
			Namespace: appNs,
			Labels:    map[string]string{"app.kubernetes.io/part-of": "aihub"},
		},
		Status: appsv1.DeploymentStatus{
			Conditions: []appsv1.DeploymentCondition{
				{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionTrue},
			},
		},
	}

	appNsObj := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: appNs}}
	regNsObj := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: regNs}}

	fakeClient := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(aihub, childDeploy, catalogDeploy, appNsObj, regNsObj).
		WithStatusSubresource(&aihubv1alpha1.AIHub{}).
		Build()

	mock := &mockDeployer{}
	reconciler := &AIHubReconciler{
		Client:                fakeClient,
		Scheme:                s,
		ManifestsTemplatePath: tmpDir,
		Getenv:                fakeGetenv(map[string]string{}),
		Deployer:              mock,
		APIReader:             fakeClient,
	}

	ctx := context.Background()
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "default"}}

	result, err := reconciler.Reconcile(ctx, req)
	if err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Errorf("expected no requeue, got RequeueAfter=%v", result.RequeueAfter)
	}

	got := &aihubv1alpha1.AIHub{}
	if err := fakeClient.Get(ctx, req.NamespacedName, got); err != nil {
		t.Fatal(err)
	}

	if got.Status.Phase != common.PhaseReady {
		t.Errorf("Phase = %q, want %q", got.Status.Phase, common.PhaseReady)
	}

	assertConditionStatus(t, got, string(common.ConditionTypeReady), metav1.ConditionTrue)
	assertConditionStatus(t, got, string(common.ConditionTypeProvisioningSucceeded), metav1.ConditionTrue)
	assertConditionStatus(t, got, ConditionModelRegistryReady, metav1.ConditionTrue)
	assertConditionStatus(t, got, ConditionCatalogReady, metav1.ConditionTrue)
}

func TestAIHubReconciler_StatusNotReady_CatalogMissing(t *testing.T) {
	tmpDir := assembleManifests(t)
	s := testScheme(t)

	appNs := "app-ns"
	regNs := "reg-ns"

	aihub := &aihubv1alpha1.AIHub{
		ObjectMeta: metav1.ObjectMeta{Name: "default"},
		Spec: aihubv1alpha1.AIHubSpec{
			ApplicationNamespace: appNs,
			InstancesNamespace:   regNs,
		},
	}

	// Seed only the MR Deployment with Available=True; catalog Deployment missing.
	childDeploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      childDeploymentName,
			Namespace: appNs,
			Labels:    map[string]string{"app.kubernetes.io/part-of": "aihub"},
		},
		Status: appsv1.DeploymentStatus{
			Conditions: []appsv1.DeploymentCondition{
				{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionTrue},
			},
		},
	}

	appNsObj := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: appNs}}
	regNsObj := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: regNs}}

	fakeClient := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(aihub, childDeploy, appNsObj, regNsObj).
		WithStatusSubresource(&aihubv1alpha1.AIHub{}).
		Build()

	reconciler := &AIHubReconciler{
		Client:                fakeClient,
		Scheme:                s,
		ManifestsTemplatePath: tmpDir,
		Getenv:                fakeGetenv(map[string]string{}),
		Deployer:              &mockDeployer{},
		APIReader:             fakeClient,
	}

	ctx := context.Background()
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "default"}}

	result, err := reconciler.Reconcile(ctx, req)
	if err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Error("expected RequeueAfter > 0 (catalog Deployment missing)")
	}

	got := &aihubv1alpha1.AIHub{}
	if err := fakeClient.Get(ctx, req.NamespacedName, got); err != nil {
		t.Fatal(err)
	}

	if got.Status.Phase != common.PhaseNotReady {
		t.Errorf("Phase = %q, want %q", got.Status.Phase, common.PhaseNotReady)
	}
	assertConditionStatus(t, got, string(common.ConditionTypeReady), metav1.ConditionFalse)
	assertConditionStatus(t, got, ConditionModelRegistryReady, metav1.ConditionTrue)
	assertConditionStatus(t, got, ConditionCatalogReady, metav1.ConditionFalse)
}

func TestAIHubReconciler_StatusNotReady_ChildMissing(t *testing.T) {
	tmpDir := assembleManifests(t)
	s := testScheme(t)

	appNs := "app-ns"
	regNs := "reg-ns"

	aihub := &aihubv1alpha1.AIHub{
		ObjectMeta: metav1.ObjectMeta{Name: "default"},
		Spec: aihubv1alpha1.AIHubSpec{
			ApplicationNamespace: appNs,
			InstancesNamespace:   regNs,
		},
	}

	appNsObj := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: appNs}}
	regNsObj := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: regNs}}

	fakeClient := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(aihub, appNsObj, regNsObj).
		WithStatusSubresource(&aihubv1alpha1.AIHub{}).
		Build()

	reconciler := &AIHubReconciler{
		Client:                fakeClient,
		Scheme:                s,
		ManifestsTemplatePath: tmpDir,
		Getenv:                fakeGetenv(map[string]string{}),
		Deployer:              &mockDeployer{},
		APIReader:             fakeClient,
	}

	ctx := context.Background()
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "default"}}

	result, err := reconciler.Reconcile(ctx, req)
	if err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Error("expected RequeueAfter > 0 (child Deployment missing)")
	}

	got := &aihubv1alpha1.AIHub{}
	if err := fakeClient.Get(ctx, req.NamespacedName, got); err != nil {
		t.Fatal(err)
	}

	if got.Status.Phase != common.PhaseNotReady {
		t.Errorf("Phase = %q, want %q", got.Status.Phase, common.PhaseNotReady)
	}
	assertConditionStatus(t, got, string(common.ConditionTypeReady), metav1.ConditionFalse)
}

func TestAIHubReconciler_PlatformVersionHandshake(t *testing.T) {
	tmpDir := assembleManifests(t)
	s := testScheme(t)

	appNs := "app-ns"
	regNs := "reg-ns"

	aihub := &aihubv1alpha1.AIHub{
		ObjectMeta: metav1.ObjectMeta{Name: "default"},
		Spec: aihubv1alpha1.AIHubSpec{
			ApplicationNamespace: appNs,
			InstancesNamespace:   regNs,
		},
	}

	childDeploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      childDeploymentName,
			Namespace: appNs,
			Labels:    map[string]string{"app.kubernetes.io/part-of": "aihub"},
		},
		Status: appsv1.DeploymentStatus{
			Conditions: []appsv1.DeploymentCondition{
				{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionTrue},
			},
		},
	}
	catalogDeploy2 := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      catalogDeploymentName,
			Namespace: appNs,
			Labels:    map[string]string{"app.kubernetes.io/part-of": "aihub"},
		},
		Status: appsv1.DeploymentStatus{
			Conditions: []appsv1.DeploymentCondition{
				{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionTrue},
			},
		},
	}

	platformCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      platformVersionConfigMap,
			Namespace: appNs,
		},
		Data: map[string]string{
			platformVersionConfigMapKey: "2.20.0",
		},
	}

	appNsObj := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: appNs}}
	regNsObj := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: regNs}}

	fakeClient := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(aihub, childDeploy, catalogDeploy2, platformCM, appNsObj, regNsObj).
		WithStatusSubresource(&aihubv1alpha1.AIHub{}).
		Build()

	mock := &mockDeployer{}
	reconciler := &AIHubReconciler{
		Client:                fakeClient,
		Scheme:                s,
		ManifestsTemplatePath: tmpDir,
		Getenv:                fakeGetenv(map[string]string{}),
		Deployer:              mock,
		APIReader:             fakeClient,
	}

	ctx := context.Background()
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "default"}}

	if _, err := reconciler.Reconcile(ctx, req); err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	got := &aihubv1alpha1.AIHub{}
	if err := fakeClient.Get(ctx, req.NamespacedName, got); err != nil {
		t.Fatal(err)
	}

	platformVersion := got.GetReleaseStatus().GetPlatformRelease()
	if platformVersion != "2.20.0" {
		t.Errorf("platform release = %q, want %q", platformVersion, "2.20.0")
	}

	// Also verify that component releases were loaded (from the hack script
	// which copies config/component_metadata.yaml into modelregistry/).
	if len(got.Status.Releases) < 2 {
		t.Errorf("expected at least 2 releases (component + platform), got %d: %+v",
			len(got.Status.Releases), got.Status.Releases)
	}
}

func TestAIHubReconciler_ReleasesWithoutPlatformCM(t *testing.T) {
	// When the platform ConfigMap is missing, releases should still contain
	// component entries but no platform entry.
	metadataDir := t.TempDir()
	writeMetadata(t, metadataDir, "modelregistry", `releases:
  - name: model-registry-operator
    version: v1.2.3
    repoUrl: https://github.com/opendatahub-io/model-registry-operator
`)

	s := testScheme(t)
	aihub := &aihubv1alpha1.AIHub{
		ObjectMeta: metav1.ObjectMeta{Name: "default"},
	}

	r := &AIHubReconciler{
		ManifestsTemplatePath: metadataDir,
		APIReader:             fake.NewClientBuilder().WithScheme(s).Build(),
	}

	r.setReleaseStatus(context.Background(), aihub)
	if len(aihub.Status.Releases) != 1 {
		t.Fatalf("expected 1 release, got %d: %+v", len(aihub.Status.Releases), aihub.Status.Releases)
	}
	if aihub.Status.Releases[0].Name != "model-registry-operator" {
		t.Errorf("unexpected release name: %s", aihub.Status.Releases[0].Name)
	}
	if aihub.GetReleaseStatus().GetPlatformRelease() != "" {
		t.Error("expected no platform release without ConfigMap")
	}
}

// --- Test helpers ---

func assertConditionStatus(t *testing.T, aihub *aihubv1alpha1.AIHub, condType string, expected metav1.ConditionStatus) {
	t.Helper()
	cond := conditions.FindStatusCondition(aihub, condType)
	if cond == nil {
		t.Errorf("condition %q not found", condType)
		return
	}
	if cond.Status != expected {
		t.Errorf("condition %q status = %q, want %q", condType, cond.Status, expected)
	}
}

func makeDeploymentUnstructured(t *testing.T, name, ns, containerName, image string, env []corev1.EnvVar) *unstructured.Unstructured {
	t.Helper()
	deploy := &appsv1.Deployment{
		TypeMeta: metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
		},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": name},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": name}},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: containerName, Image: image, Env: env},
					},
				},
			},
		},
	}
	obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(deploy)
	if err != nil {
		t.Fatal(err)
	}
	u := &unstructured.Unstructured{Object: obj}
	u.SetGroupVersionKind(appsv1.SchemeGroupVersion.WithKind("Deployment"))
	return u
}

func deploymentFromUnstructured(t *testing.T, u *unstructured.Unstructured) *appsv1.Deployment {
	t.Helper()
	d := &appsv1.Deployment{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, d); err != nil {
		t.Fatal(err)
	}
	return d
}

func findContainer(t *testing.T, d *appsv1.Deployment, name string) *corev1.Container {
	t.Helper()
	for i := range d.Spec.Template.Spec.Containers {
		if d.Spec.Template.Spec.Containers[i].Name == name {
			return &d.Spec.Template.Spec.Containers[i]
		}
	}
	t.Fatalf("container %q not found", name)
	return nil
}

func assertEnv(t *testing.T, c *corev1.Container, name, value string) {
	t.Helper()
	for _, e := range c.Env {
		if e.Name == name {
			if e.Value != value {
				t.Errorf("env %s = %q, want %q", name, e.Value, value)
			}
			return
		}
	}
	t.Errorf("env %s not found", name)
}

func assertNoEnv(t *testing.T, c *corev1.Container, name string) {
	t.Helper()
	for _, e := range c.Env {
		if e.Name == name {
			t.Errorf("env %s unexpectedly present with value %q", name, e.Value)
			return
		}
	}
}

func assertEnvEmpty(t *testing.T, c *corev1.Container, name string) {
	t.Helper()
	for _, e := range c.Env {
		if e.Name == name {
			if e.Value != "" {
				t.Errorf("env %s = %q, want empty (not stamped)", name, e.Value)
			}
			return
		}
	}
	// Not present at all — also acceptable.
}

func assertConditionReason(t *testing.T, aihub *aihubv1alpha1.AIHub, condType, expectedReason string) {
	t.Helper()
	cond := conditions.FindStatusCondition(aihub, condType)
	if cond == nil {
		t.Errorf("condition %q not found", condType)
		return
	}
	if cond.Reason != expectedReason {
		t.Errorf("condition %q reason = %q, want %q", condType, cond.Reason, expectedReason)
	}
}

// fakeGetenv is defined in aihub_images_test.go (same package).
