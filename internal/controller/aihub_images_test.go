package controller

import (
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/opendatahub-io/model-registry-operator/internal/controller/config"
)

// fakeGetenv returns a getenv func backed by a map.
func fakeGetenv(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestResolveChildImages_FullEnv(t *testing.T) {
	env := map[string]string{
		config.ModelRegistryOperatorImage: "registry.example/op@sha256:aaa",
		config.RestImage:                  "registry.example/rest@sha256:bbb",
		config.PostgresImage:              "registry.example/pg@sha256:ccc",
		config.KubeRBACProxyImage:         "registry.example/krp@sha256:ddd",
		config.CatalogDataImage:           "registry.example/cat@sha256:eee",
		config.BenchmarkDataImage:         "registry.example/bench@sha256:fff",
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
