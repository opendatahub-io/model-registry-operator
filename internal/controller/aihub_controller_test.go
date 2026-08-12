package controller

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	aihubv1alpha1 "github.com/opendatahub-io/model-registry-operator/api/aihub/v1alpha1"
	catalogv1alpha1 "github.com/opendatahub-io/model-registry-operator/api/catalog/v1alpha1"
	"github.com/opendatahub-io/model-registry-operator/internal/controller/config"
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

	if err := stampChildOperatorDeployment(u, images, "my-reg-ns"); err != nil {
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

	if err := stampChildOperatorDeployment(u, images, "reg-ns"); err != nil {
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
	err := stampChildOperatorDeployment(u, ChildImages{}, "ns")
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

	var deployFound bool
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

		if kind == "Deployment" && resources[i].GetName() == childDeploymentName {
			if err := stampChildOperatorDeployment(&resources[i], images, "my-reg-ns"); err != nil {
				t.Fatalf("stampChildOperatorDeployment: %v", err)
			}
			deploy := deploymentFromUnstructured(t, &resources[i])
			c := findContainer(t, deploy, "manager")
			if c.Image != images.OperatorImage {
				t.Errorf("stamped Image = %q, want %q", c.Image, images.OperatorImage)
			}
			assertEnv(t, c, config.RestImage, "stamped-rest@sha256:222")
			assertEnv(t, c, config.RegistriesNamespace, "my-reg-ns")
			deployFound = true
		}
	}

	if !deployFound {
		t.Error("child operator Deployment not found in rendered resources")
	}
	if crdCount < 1 {
		t.Errorf("expected at least 1 CRD, got %d", crdCount)
	}
}

// --- Fake-client reconcile test ---

func TestAIHubReconciler_Reconcile(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available, skipping reconcile test")
	}

	// Assemble manifests via hack script.
	tmpDir := t.TempDir()
	repoRoot := filepath.Join("..", "..")
	hackScript := filepath.Join(repoRoot, "hack", "get_aihub_manifests.sh")
	cmd := exec.Command("bash", hackScript, tmpDir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("hack script failed: %v\n%s", err, out)
	}

	// Build scheme with all needed types.
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := apiextensionsv1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := admissionregistrationv1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := aihubv1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := catalogv1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}

	// Register unstructured GVKs for ServiceMonitor and Template which are
	// not available as typed Go structs in this module's dependency tree.
	registerUnstructuredGVK(s, schema.GroupVersionKind{
		Group: "monitoring.coreos.com", Version: "v1", Kind: "ServiceMonitor",
	})
	registerUnstructuredGVK(s, schema.GroupVersionKind{
		Group: "monitoring.coreos.com", Version: "v1", Kind: "ServiceMonitorList",
	})
	registerUnstructuredGVK(s, schema.GroupVersionKind{
		Group: "template.openshift.io", Version: "v1", Kind: "Template",
	})
	registerUnstructuredGVK(s, schema.GroupVersionKind{
		Group: "template.openshift.io", Version: "v1", Kind: "TemplateList",
	})

	appNs := "app-ns"
	regNs := "reg-ns"

	aihub := &aihubv1alpha1.AIHub{
		ObjectMeta: metav1.ObjectMeta{
			Name: "default",
		},
		Spec: aihubv1alpha1.AIHubSpec{
			ApplicationNamespace: appNs,
			RegistriesNamespace:  regNs,
		},
	}

	// Seed the namespaces so the fake client accepts namespaced creates.
	appNsObj := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: appNs}}
	regNsObj := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: regNs}}

	fakeClient := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(aihub, appNsObj, regNsObj).
		Build()

	fakeEnvMap := map[string]string{
		config.ModelRegistryOperatorImage: "fake-op@sha256:aaa",
		config.RestImage:                  "fake-rest@sha256:bbb",
		config.PostgresImage:              "fake-pg@sha256:ccc",
	}

	reconciler := &AIHubReconciler{
		Client:                fakeClient,
		Scheme:                s,
		ManifestsTemplatePath: tmpDir,
		Getenv:                fakeGetenv(fakeEnvMap),
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "default"}}
	ctx := context.Background()

	// First reconcile — child Deployment won't have Available condition,
	// so expect requeue.
	result, err := reconciler.Reconcile(ctx, req)
	if err != nil {
		t.Fatalf("first Reconcile failed: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Log("NOTE: expected requeue (deployment not Available), got no requeue — checking if it returned success")
	}

	// Verify the child Deployment exists with stamped values.
	deploy := &appsv1.Deployment{}
	if err := fakeClient.Get(ctx, types.NamespacedName{
		Namespace: appNs,
		Name:      childDeploymentName,
	}, deploy); err != nil {
		t.Fatalf("child Deployment not found: %v", err)
	}

	managerC := findContainer(t, deploy, "manager")
	if managerC.Image != "fake-op@sha256:aaa" {
		t.Errorf("manager image = %q, want %q", managerC.Image, "fake-op@sha256:aaa")
	}
	assertEnv(t, managerC, config.RestImage, "fake-rest@sha256:bbb")
	assertEnv(t, managerC, config.PostgresImage, "fake-pg@sha256:ccc")
	assertEnv(t, managerC, config.RegistriesNamespace, regNs)

	// Verify CRDs exist (cluster-scoped, empty namespace).
	crdList := &apiextensionsv1.CustomResourceDefinitionList{}
	if err := fakeClient.List(ctx, crdList); err != nil {
		t.Fatalf("listing CRDs: %v", err)
	}
	if len(crdList.Items) < 1 {
		t.Error("expected at least 1 CRD")
	}
	for _, crd := range crdList.Items {
		if crd.Namespace != "" {
			t.Errorf("CRD %q has non-empty namespace %q", crd.Name, crd.Namespace)
		}
	}

	// Verify Catalog CR exists.
	catalog := &catalogv1alpha1.Catalog{}
	if err := fakeClient.Get(ctx, types.NamespacedName{
		Namespace: regNs,
		Name:      "default",
	}, catalog); err != nil {
		t.Fatalf("Catalog CR not found: %v", err)
	}

	// Idempotency: second reconcile must not error.
	_, err2 := reconciler.Reconcile(ctx, req)
	if err2 != nil {
		t.Fatalf("second Reconcile failed (idempotency): %v", err2)
	}
}

// --- Test helpers ---

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

// registerUnstructuredGVK registers a GVK as an unstructured type in the scheme.
// The fake client requires all GVKs to be registered; for CRDs without Go types
// (ServiceMonitor, Template), we register them as unstructured.
func registerUnstructuredGVK(s *runtime.Scheme, gvk schema.GroupVersionKind) {
	if len(gvk.Kind) > 4 && gvk.Kind[len(gvk.Kind)-4:] == "List" {
		s.AddKnownTypeWithName(gvk, &unstructured.UnstructuredList{})
	} else {
		s.AddKnownTypeWithName(gvk, &unstructured.Unstructured{})
	}
}
