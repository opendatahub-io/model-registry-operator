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

package config_test

import (
	"testing"

	"github.com/opendatahub-io/model-registry-operator/internal/controller/config"
)

func ptr(s string) *string { return &s }

func TestResolveImage(t *testing.T) {
	const (
		unusedEnv = "TEST_RESOLVE_IMAGE_UNUSED_ENV"
		setEnv    = "TEST_RESOLVE_IMAGE_SET_ENV"
	)
	validDigest := "sha256:abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"

	tests := []struct {
		name       string
		override   *string
		envName    string
		envDefault string
		envSet     string // if non-empty, set envName to this value
		want       string
	}{
		{
			name:       "valid digest with quay default (tag)",
			override:   ptr(validDigest),
			envName:    unusedEnv,
			envDefault: config.DefaultCatalogDataImage,
			want:       "quay.io/opendatahub/odh-model-metadata-collection@" + validDigest,
		},
		{
			name:       "valid digest with registry.redhat.io default",
			override:   ptr(validDigest),
			envName:    unusedEnv,
			envDefault: "registry.redhat.io/rhoai/odh-model-metadata-collection-rhel9:v2.19",
			want:       "registry.redhat.io/rhoai/odh-model-metadata-collection-rhel9@" + validDigest,
		},
		{
			name:       "default already has a digest — override replaces it",
			override:   ptr(validDigest),
			envName:    unusedEnv,
			envDefault: "registry.redhat.io/rhoai/foo@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			want:       "registry.redhat.io/rhoai/foo@" + validDigest,
		},
		{
			name:       "registry with port in default — port preserved",
			override:   ptr(validDigest),
			envName:    unusedEnv,
			envDefault: "localhost:5000/repo:tag",
			want:       "localhost:5000/repo@" + validDigest,
		},
		{
			name:       "nil override returns full default",
			override:   nil,
			envName:    unusedEnv,
			envDefault: config.DefaultBenchmarkDataImage,
			want:       config.DefaultBenchmarkDataImage,
		},
		{
			name:       "empty string override returns full default",
			override:   ptr(""),
			envName:    unusedEnv,
			envDefault: config.DefaultCatalogDataImage,
			want:       config.DefaultCatalogDataImage,
		},
		{
			name:       "whitespace-only override returns full default",
			override:   ptr("   "),
			envName:    unusedEnv,
			envDefault: config.DefaultCatalogDataImage,
			want:       config.DefaultCatalogDataImage,
		},
		{
			name:       "uppercase hex is invalid — returns default",
			override:   ptr("sha256:ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789"),
			envName:    unusedEnv,
			envDefault: config.DefaultCatalogDataImage,
			want:       config.DefaultCatalogDataImage,
		},
		{
			name:       "wrong length (63 hex) — returns default",
			override:   ptr("sha256:abcdef0123456789abcdef0123456789abcdef0123456789abcdef012345678"),
			envName:    unusedEnv,
			envDefault: config.DefaultCatalogDataImage,
			want:       config.DefaultCatalogDataImage,
		},
		{
			name:       "missing sha256: prefix — now treated as valid tag",
			override:   ptr("abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"),
			envName:    unusedEnv,
			envDefault: config.DefaultCatalogDataImage,
			want:       "quay.io/opendatahub/odh-model-metadata-collection:abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
		},
		{
			name:       "valid digest with leading/trailing whitespace is accepted",
			override:   ptr("  " + validDigest + "  "),
			envName:    unusedEnv,
			envDefault: config.DefaultCatalogDataImage,
			want:       "quay.io/opendatahub/odh-model-metadata-collection@" + validDigest,
		},
		{
			name:       "env var overrides envDefault — valid digest pins onto env image repo",
			override:   ptr(validDigest),
			envName:    setEnv,
			envDefault: "should-not-be-used:latest",
			envSet:     "my-registry.example.com/custom/image:v1.0",
			want:       "my-registry.example.com/custom/image@" + validDigest,
		},
		{
			name:       "env var overrides envDefault — nil override returns env value",
			override:   nil,
			envName:    setEnv,
			envDefault: "should-not-be-used:latest",
			envSet:     "my-registry.example.com/custom/image:v1.0",
			want:       "my-registry.example.com/custom/image:v1.0",
		},
		// --- tag override tests ---
		{
			name:       "valid simple tag",
			override:   ptr("v3.5"),
			envName:    unusedEnv,
			envDefault: config.DefaultCatalogDataImage,
			want:       "quay.io/opendatahub/odh-model-metadata-collection:v3.5",
		},
		{
			name:       "tag with dots and dashes",
			override:   ptr("v3.5-data-20260101"),
			envName:    unusedEnv,
			envDefault: config.DefaultCatalogDataImage,
			want:       "quay.io/opendatahub/odh-model-metadata-collection:v3.5-data-20260101",
		},
		{
			name:       "tag latest",
			override:   ptr("latest"),
			envName:    unusedEnv,
			envDefault: config.DefaultCatalogDataImage,
			want:       "quay.io/opendatahub/odh-model-metadata-collection:latest",
		},
		{
			name:       "tag with underscore leading char",
			override:   ptr("_build"),
			envName:    unusedEnv,
			envDefault: config.DefaultCatalogDataImage,
			want:       "quay.io/opendatahub/odh-model-metadata-collection:_build",
		},
		{
			name:       "invalid tag — contains slash (security: cannot inject repo)",
			override:   ptr("evil/repo:tag"),
			envName:    unusedEnv,
			envDefault: config.DefaultCatalogDataImage,
			want:       config.DefaultCatalogDataImage,
		},
		{
			name:       "invalid tag — contains @",
			override:   ptr("x@sha256:abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"),
			envName:    unusedEnv,
			envDefault: config.DefaultCatalogDataImage,
			want:       config.DefaultCatalogDataImage,
		},
		{
			name:       "invalid tag — too long (129 chars)",
			override:   ptr("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
			envName:    unusedEnv,
			envDefault: config.DefaultCatalogDataImage,
			want:       config.DefaultCatalogDataImage,
		},
		{
			name:       "digest still takes precedence over tag branch",
			override:   ptr(validDigest),
			envName:    unusedEnv,
			envDefault: config.DefaultCatalogDataImage,
			want:       "quay.io/opendatahub/odh-model-metadata-collection@" + validDigest,
		},
		{
			name:       "default has tag — override tag replaces it",
			override:   ptr("v2"),
			envName:    unusedEnv,
			envDefault: "quay.io/opendatahub/foo:latest",
			want:       "quay.io/opendatahub/foo:v2",
		},
		{
			name:       "default has registry:port — tag override preserves port",
			override:   ptr("v2"),
			envName:    unusedEnv,
			envDefault: "localhost:5000/repo:tag",
			want:       "localhost:5000/repo:v2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.envSet != "" {
				t.Setenv(tt.envName, tt.envSet)
			}
			got := config.ResolveImage(tt.override, tt.envName, tt.envDefault)
			if got != tt.want {
				t.Errorf("ResolveImage() = %q, want %q", got, tt.want)
			}
		})
	}
}
