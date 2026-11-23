package controller

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	catalogv1alpha1 "github.com/opendatahub-io/model-registry-operator/api/catalog/v1alpha1"
	"github.com/opendatahub-io/model-registry-operator/internal/catalogcontent"
	"github.com/opendatahub-io/model-registry-operator/internal/controller/config"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestCatalogLegacyReleaseImageDefaults(t *testing.T) {
	const repository = "registry.example/catalog"
	for _, tc := range []struct {
		name, release string
		hasRuntime    bool
	}{
		{name: "release digest", release: repository + "@" + dataTestDigest1},
		{name: "release tag", release: repository + ":release"},
		{name: "published runtime preserved", release: repository + "@" + dataTestDigest1, hasRuntime: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(config.CatalogDataImage, tc.release)
			t.Setenv(config.BenchmarkDataImage, tc.release)
			t.Setenv(config.CatalogDataImageStreamSource, "")
			t.Setenv(config.BenchmarkDataImageStreamSource, "")
			catalog := &catalogv1alpha1.Catalog{ObjectMeta: metav1.ObjectMeta{
				Name: catalogCRName, Namespace: "legacy-test", UID: "legacy-catalog", Generation: 1,
			}}
			r := newDataImageTestReconciler(t, catalog, true)
			request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(catalog)}
			data, benchmarks := t.TempDir(), t.TempDir()
			for name, content := range map[string]string{
				"validated-models-catalog.yaml":      "models: []\n",
				"other-models-catalog.yaml":          "models: []\n",
				"redhat-mcp-servers-catalog.yaml":    "mcp_servers: []\n",
				"partner-mcp-servers-catalog.yaml":   "mcp_servers: []\n",
				"community-mcp-servers-catalog.yaml": "mcp_servers: []\n",
				"redhat-agents-catalog.yaml":         "agents: []\n",
			} {
				if err := os.WriteFile(filepath.Join(data, name), []byte(content), 0600); err != nil {
					t.Fatal(err)
				}
			}
			runtimeContent := "serving_runtimes: []\n"
			if tc.hasRuntime {
				runtimeContent = "serving_runtimes: [{name: published, versions: [{version: v1, image: registry/runtime:v1}]}]\n"
				if err := os.WriteFile(filepath.Join(data, "redhat-serving-runtimes-catalog.yaml"), []byte(runtimeContent), 0600); err != nil {
					t.Fatal(err)
				}
			}
			check := func(stage string, ready bool) {
				t.Helper()
				if _, err := r.Reconcile(t.Context(), request); err != nil {
					t.Fatal(err)
				}
				deployment := &appsv1.Deployment{}
				if err := r.Get(t.Context(), client.ObjectKey{Name: catalogResourceName, Namespace: catalog.Namespace}, deployment); err != nil {
					t.Fatal(err)
				}
				if deployment.Spec.Replicas == nil || *deployment.Spec.Replicas != 1 {
					t.Fatalf("%s: compatible release image was contained", stage)
				}
				shared := t.TempDir()
				command := deployment.Spec.Template.Spec.InitContainers[0].Command[2]
				command = strings.ReplaceAll(command, "/app/data/", data+"/")
				command = strings.ReplaceAll(command, "/shared-data/", shared+"/")
				if output, err := exec.Command("/bin/sh", "-c", command).CombinedOutput(); err != nil {
					t.Fatalf("%s: copy failed: %v: %s", stage, err, output)
				}
				sources := &corev1.ConfigMap{}
				if err := r.Get(t.Context(), client.ObjectKey{Name: "default-catalog-sources", Namespace: catalog.Namespace}, sources); err != nil {
					t.Fatal(err)
				}
				configPath := filepath.Join(t.TempDir(), "sources.yaml")
				if err := os.WriteFile(configPath, []byte(strings.ReplaceAll(sources.Data[sourcesFileName], "/shared-data/", shared+"/")), 0600); err != nil {
					t.Fatal(err)
				}
				if failure := catalogcontent.Validate(configPath, benchmarks); failure != nil {
					t.Fatalf("%s: identical release content failed validation: %v", stage, failure)
				}
				content, err := os.ReadFile(filepath.Join(shared, "redhat-serving-runtimes-catalog.yaml"))
				if err != nil || string(content) != runtimeContent {
					t.Fatalf("%s: runtime compatibility content = %q, error = %v", stage, content, err)
				}
				if ready {
					markDataImageWorkloadReady(t, r, catalog)
					markCatalogValidationReady(t, r, catalog)
					if _, err := r.Reconcile(t.Context(), request); err != nil {
						t.Fatal(err)
					}
					if err := r.Get(t.Context(), request.NamespacedName, catalog); err != nil {
						t.Fatal(err)
					}
					if catalog.Status.Activation.State != "Succeeded" {
						t.Fatalf("%s: activation failed: %+v", stage, catalog.Status.Activation)
					}
				}
			}
			check("release default", true)
		})
	}
}

func TestCatalogLegacyRuntimeImportScope(t *testing.T) {
	const release = "registry.example/catalog@" + dataTestDigest1
	for _, tc := range []struct {
		name, source, imported, manualSource string
		selection                            string
		compatible                           bool
	}{
		{name: "release default", compatible: true},
		{name: "explicit pin", selection: dataTestDigest1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(config.CatalogDataImage, release)
			t.Setenv(config.BenchmarkDataImage, release)
			t.Setenv(config.CatalogDataImageStreamSource, tc.source)
			t.Setenv(config.BenchmarkDataImageStreamSource, "")
			catalog := &catalogv1alpha1.Catalog{
				ObjectMeta: metav1.ObjectMeta{Name: catalogCRName, Namespace: "scope-test", UID: "catalog"},
				Spec:       catalogv1alpha1.CatalogSpec{CatalogDataImageStream: &tc.selection},
			}
			r := newDataImageTestReconciler(t, catalog, true)
			resolve := func() *CatalogParams {
				t.Helper()
				params := r.buildCatalogParams(catalog, nil, nil, nil, "")
				if err := r.resolveCatalogDataImages(t.Context(), catalog, params); err != nil {
					t.Fatal(err)
				}
				return params
			}
			params := resolve()

			if params.AllowLegacyRuntimeFile != tc.compatible {
				t.Fatalf("legacy runtime compatibility = %v, want %v", params.AllowLegacyRuntimeFile, tc.compatible)
			}
		})
	}
}

func TestCatalogLegacyCompatibilityRetriesFailedActivation(t *testing.T) {
	t.Setenv(config.CatalogDataImageStreamSource, dataTestRepository+":updates")
	t.Setenv(config.BenchmarkDataImageStreamSource, dataTestRepository+":updates")
	r, catalog := readyDataImageCatalog(t)
	if err := r.observeActivation(t.Context(), catalog, &catalogActivationFailure{Reason: "CatalogContentInvalid"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Status().Update(t.Context(), catalog); err != nil {
		t.Fatal(err)
	}
	previous := catalog.Status.Activation.DeepCopy()
	// Selecting the same content as the release default enables legacy runtime
	// compatibility and must retry validation for the failed content.
	t.Setenv(config.CatalogDataImage, previous.RequestedImages.Catalog)
	catalog.Spec.CatalogDataImageStream = nil
	catalog.Generation++
	if err := r.Update(t.Context(), catalog); err != nil {
		t.Fatal(err)
	}
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(catalog)}
	if _, err := r.Reconcile(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if err := r.Get(t.Context(), request.NamespacedName, catalog); err != nil {
		t.Fatal(err)
	}
	current := catalog.Status.Activation
	if current.RequestedImages != previous.RequestedImages || current.AttemptID == previous.AttemptID || current.State != "Pending" {
		t.Fatalf("compatibility change did not retry the same content: %+v", current)
	}
	markDataImageWorkloadReady(t, r, catalog)
	markCatalogValidationReady(t, r, catalog)
	if _, err := r.Reconcile(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if err := r.Get(t.Context(), request.NamespacedName, catalog); err != nil {
		t.Fatal(err)
	}
	if catalog.Status.Activation.State != "Succeeded" {
		t.Fatalf("compatible release failed to recover: %+v", catalog.Status.Activation)
	}
}
