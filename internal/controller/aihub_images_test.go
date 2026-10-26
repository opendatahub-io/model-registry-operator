package controller

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	imagev1 "github.com/openshift/api/image/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/opendatahub-io/model-registry-operator/api/catalog/v1alpha1"
	"github.com/opendatahub-io/model-registry-operator/internal/controller/config"
	"github.com/opendatahub-io/odh-platform-utilities/pkg/render/kustomize"
)

func TestAIHubCatalogImageStreamSources(t *testing.T) {
	for _, tc := range []struct {
		name, source, release string
	}{
		{name: "release digest", release: "registry.example/release/catalog@" + dataTestDigest1},
		{name: "development tag", release: "registry.example/release/catalog:latest"},
		{name: "configured channel", source: "registry.example/channels/catalog:updates", release: "registry.example/release/catalog@" + dataTestDigest1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			overlay := t.TempDir()
			if err := os.CopyFS(overlay, os.DirFS(filepath.Join("..", "..", "config", "overlays", "aihub"))); err != nil {
				t.Fatal(err)
			}
			paramsPath := filepath.Join(overlay, "params.env")
			paramsData, err := os.ReadFile(paramsPath)
			if err != nil {
				t.Fatal(err)
			}
			overrides := map[string]string{
				config.CatalogDataImageStreamSource: tc.source, config.BenchmarkDataImageStreamSource: tc.source,
				config.CatalogDataImage: tc.release, config.BenchmarkDataImage: tc.release,
			}
			lines := strings.Split(string(paramsData), "\n")
			for i, line := range lines {
				key, _, _ := strings.Cut(line, "=")
				if value, ok := overrides[key]; ok {
					lines[i] = key + "=" + value
				}
			}
			if err := os.WriteFile(paramsPath, []byte(strings.Join(lines, "\n")), 0o600); err != nil {
				t.Fatal(err)
			}
			resources, err := kustomize.Render(overlay, nil)
			if err != nil {
				t.Fatal(err)
			}
			var env map[string]string
			for i := range resources {
				if resources[i].GetKind() == "Deployment" && resources[i].GetName() == "aihub-controller-manager" {
					manager := findContainer(t, deploymentFromUnstructured(t, &resources[i]), "manager")
					env = make(map[string]string)
					for _, variable := range manager.Env {
						env[variable.Name] = variable.Value
					}
				}
			}
			if env == nil {
				t.Fatal("AIHub overlay did not render its manager Deployment")
			}
			for key, value := range overrides {
				if env[key] != value {
					t.Fatalf("rendered %s = %q, want %q", key, env[key], value)
				}
				t.Setenv(key, "")
			}
			images := ResolveChildImages(fakeGetenv(env))
			for _, name := range []string{childDeploymentName, catalogDeploymentName} {
				child := makeDeploymentUnstructured(t, name, "app-ns", "manager", "operator:v1", nil)
				if err := stampChildOperatorDeployment(child, images, "reg-ns", "app-ns", ""); err != nil {
					t.Fatal(err)
				}
				manager := findContainer(t, deploymentFromUnstructured(t, child), "manager")
				if tc.source != "" {
					for _, key := range []string{config.CatalogDataImageStreamSource, config.BenchmarkDataImageStreamSource} {
						assertEnv(t, manager, key, tc.source)
					}
				}
				if name == catalogDeploymentName {
					for _, variable := range manager.Env {
						t.Setenv(variable.Name, variable.Value)
					}
				}
			}

			stable := "stable"
			catalog := &catalogv1alpha1.Catalog{
				ObjectMeta: metav1.ObjectMeta{Name: catalogCRName, Namespace: "reg-ns", UID: "catalog"},
				Spec: catalogv1alpha1.CatalogSpec{
					CatalogDataImageStream: &stable, BenchmarkDataImageStream: &stable,
				},
			}
			r := newDataImageTestReconciler(t, catalog, true)
			params := r.buildCatalogParams(catalog, nil, nil, nil, "")
			if err := r.resolveCatalogDataImages(context.Background(), catalog, params); err != nil {
				t.Fatal(err)
			}
			streams := &imagev1.ImageStreamList{}
			if err := r.List(context.Background(), streams, client.InNamespace(catalog.Namespace)); err != nil {
				t.Fatal(err)
			}
			if len(streams.Items) != 1 || streams.Items[0].Name != CatalogDataImageStreamName {
				t.Fatalf("combined source must use one shared ImageStream, got %v", streams.Items)
			}
			source := tc.source
			if source == "" {
				source = tc.release
			}
			tags := streams.Items[0].Spec.Tags
			if len(tags) != 1 || tags[0].Name != "stable" || tags[0].From == nil || tags[0].From.Kind != "DockerImage" || tags[0].From.Name != source ||
				!tags[0].ImportPolicy.Scheduled || tags[0].ImportPolicy.ImportMode != imagev1.ImportModePreserveOriginal || tags[0].ReferencePolicy.Type != imagev1.SourceTagReferencePolicy {
				t.Fatalf("unexpected shared ImageStream configuration: %+v", tags)
			}
		})
	}
}

// fakeGetenv returns a getenv func backed by a map.
func fakeGetenv(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestResolveChildImages_FullEnv(t *testing.T) {
	env := map[string]string{
		config.ModelRegistryOperatorImage:     "registry.example/op@sha256:aaa",
		config.RestImage:                      "registry.example/rest@sha256:bbb",
		config.PostgresImage:                  "registry.example/pg@sha256:ccc",
		config.KubeRBACProxyImage:             "registry.example/krp@sha256:ddd",
		config.CatalogDataImage:               "registry.example/cat@sha256:eee",
		config.BenchmarkDataImage:             "registry.example/bench@sha256:fff",
		config.CatalogDataImageStreamSource:   "registry.example/catalog:latest",
		config.BenchmarkDataImageStreamSource: "registry.example/benchmarks:stable",
	}

	got := ResolveChildImages(fakeGetenv(env))

	if got.OperatorImage != env[config.ModelRegistryOperatorImage] {
		t.Fatalf("OperatorImage = %q, want %q", got.OperatorImage, env[config.ModelRegistryOperatorImage])
	}

	want := []corev1.EnvVar{
		{Name: config.RestImage, Value: env[config.RestImage]},
		{Name: config.PostgresImage, Value: env[config.PostgresImage]},
		{Name: config.KubeRBACProxyImage, Value: env[config.KubeRBACProxyImage]},
		{Name: config.CatalogDataImage, Value: env[config.CatalogDataImage]},
		{Name: config.BenchmarkDataImage, Value: env[config.BenchmarkDataImage]},
		{Name: config.CatalogDataImageStreamSource, Value: env[config.CatalogDataImageStreamSource]},
		{Name: config.BenchmarkDataImageStreamSource, Value: env[config.BenchmarkDataImageStreamSource]},
	}
	if len(got.OperandEnv) != len(want) {
		t.Fatalf("OperandEnv len = %d, want %d (%+v)", len(got.OperandEnv), len(want), got.OperandEnv)
	}
	for i := range want {
		if got.OperandEnv[i] != want[i] {
			t.Errorf("OperandEnv[%d] = %+v, want %+v", i, got.OperandEnv[i], want[i])
		}
	}
}

func TestResolveChildImages_EmptyEnv(t *testing.T) {
	got := ResolveChildImages(fakeGetenv(map[string]string{}))

	if got.OperatorImage != "" {
		t.Errorf("OperatorImage = %q, want empty", got.OperatorImage)
	}
	if len(got.OperandEnv) != 0 {
		t.Errorf("OperandEnv = %+v, want empty (unset AIHub env must leave child defaults untouched)", got.OperandEnv)
	}
}

func TestResolveChildImages_PartialEnv(t *testing.T) {
	env := map[string]string{
		config.RestImage: "registry.example/rest@sha256:bbb",
	}

	got := ResolveChildImages(fakeGetenv(env))

	if got.OperatorImage != "" {
		t.Errorf("OperatorImage = %q, want empty", got.OperatorImage)
	}
	if len(got.OperandEnv) != 1 {
		t.Fatalf("OperandEnv len = %d, want 1 (%+v)", len(got.OperandEnv), got.OperandEnv)
	}
	// identity mapping: the forwarded env var name must equal the source env name
	if got.OperandEnv[0].Name != config.RestImage {
		t.Errorf("OperandEnv[0].Name = %q, want %q (identity passthrough)", got.OperandEnv[0].Name, config.RestImage)
	}
	if got.OperandEnv[0].Value != env[config.RestImage] {
		t.Errorf("OperandEnv[0].Value = %q, want %q", got.OperandEnv[0].Value, env[config.RestImage])
	}
}
