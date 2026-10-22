/*
Copyright 2026.

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

package tls

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

var intermediateProxyArgs = []string{
	"--tls-min-version=VersionTLS12",
	"--tls-cipher-suites=TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256,TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256",
	"--tls-curve-preferences=4588,29,23,24",
}

func TestResolveProxyWithoutConfigAPI(t *testing.T) {
	// A nil reader proves vanilla Kubernetes does not attempt an APIServer read.
	got, err := resolveProxy(context.Background(), nil, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, intermediateProxyArgs) {
		t.Fatalf("proxy arguments = %q, want hardened Intermediate %q", got, intermediateProxyArgs)
	}
}

type proxyReader struct {
	client.Reader
	err  error
	keys []client.ObjectKey
}

func (r *proxyReader) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	r.keys = append(r.keys, key)
	if r.err != nil {
		return r.err
	}
	return r.Reader.Get(ctx, key, obj, opts...)
}

func proxyAPIServerReader(t *testing.T, policy configv1.TLSAdherencePolicy, profile *configv1.TLSSecurityProfile) *proxyReader {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := configv1.Install(scheme); err != nil {
		t.Fatal(err)
	}
	apiServer := &configv1.APIServer{}
	apiServer.Name = "cluster"
	apiServer.Spec.TLSAdherence = policy
	apiServer.Spec.TLSSecurityProfile = profile
	return &proxyReader{Reader: fake.NewClientBuilder().WithScheme(scheme).WithObjects(apiServer).Build()}
}

func customProxyProfile(version configv1.TLSProtocolVersion, ciphers []string, groups ...configv1.TLSGroup) *configv1.TLSSecurityProfile {
	return &configv1.TLSSecurityProfile{
		Type: configv1.TLSProfileCustomType,
		Custom: &configv1.CustomTLSProfile{TLSProfileSpec: configv1.TLSProfileSpec{
			MinTLSVersion: version,
			Ciphers:       ciphers,
			Groups:        groups,
		}},
	}
}

func TestResolveProxyAdherence(t *testing.T) {
	tests := []struct {
		name    string
		policy  configv1.TLSAdherencePolicy
		profile *configv1.TLSSecurityProfile
		want    []string
	}{
		{"NoOpinion ignores malformed profile", configv1.TLSAdherencePolicyNoOpinion, &configv1.TLSSecurityProfile{Type: configv1.TLSProfileCustomType}, intermediateProxyArgs},
		{"Legacy ignores Modern profile", configv1.TLSAdherencePolicyLegacyAdheringComponentsOnly, &configv1.TLSSecurityProfile{Type: configv1.TLSProfileModernType}, intermediateProxyArgs},
		{"Legacy ignores malformed profile", configv1.TLSAdherencePolicyLegacyAdheringComponentsOnly, &configv1.TLSSecurityProfile{Type: "invalid"}, intermediateProxyArgs},
		{"Strict nil profile defaults Intermediate", configv1.TLSAdherencePolicyStrictAllComponents, nil, intermediateProxyArgs},
		{"Strict empty profile defaults Intermediate", configv1.TLSAdherencePolicyStrictAllComponents, &configv1.TLSSecurityProfile{}, intermediateProxyArgs},
		{"Strict Intermediate", configv1.TLSAdherencePolicyStrictAllComponents, &configv1.TLSSecurityProfile{Type: configv1.TLSProfileIntermediateType}, intermediateProxyArgs},
		{"Strict Modern", configv1.TLSAdherencePolicyStrictAllComponents, &configv1.TLSSecurityProfile{Type: configv1.TLSProfileModernType}, []string{"--tls-min-version=VersionTLS13", "--tls-curve-preferences=4588,29,23,24"}},
		{"Strict Old falls back to Intermediate", configv1.TLSAdherencePolicyStrictAllComponents, &configv1.TLSSecurityProfile{Type: configv1.TLSProfileOldType}, intermediateProxyArgs},
		{"unknown policy honors Custom", "FuturePolicy", customProxyProfile(configv1.VersionTLS12, []string{"ECDHE-RSA-AES128-GCM-SHA256"}, configv1.TLSGroupSecP256r1), []string{"--tls-min-version=VersionTLS12", "--tls-cipher-suites=TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256", "--tls-curve-preferences=23"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reader := proxyAPIServerReader(t, tt.policy, tt.profile)
			got, err := resolveProxy(context.Background(), reader, true, false)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("proxy arguments = %q, want %q", got, tt.want)
			}
			if !reflect.DeepEqual(reader.keys, []client.ObjectKey{{Name: "cluster"}}) {
				t.Errorf("APIServer reads = %v, want one read of cluster", reader.keys)
			}
		})
	}
}

func TestResolveProxyCustomBelowMinimumFailsClosed(t *testing.T) {
	for _, version := range []configv1.TLSProtocolVersion{configv1.VersionTLS10, configv1.VersionTLS11} {
		reader := proxyAPIServerReader(t, configv1.TLSAdherencePolicyStrictAllComponents,
			customProxyProfile(version, []string{"ECDHE-RSA-AES128-GCM-SHA256"}))
		if got, err := resolveProxy(context.Background(), reader, true, false); err == nil {
			t.Errorf("%s: proxy arguments = %q, want error", version, got)
		}
	}
}

func TestResolveProxyReadErrors(t *testing.T) {
	resource := schema.GroupResource{Group: "config.openshift.io", Resource: "apiservers"}
	tests := []struct {
		name     string
		err      error
		fallback bool
	}{
		{"not found", apierrors.NewNotFound(resource, "cluster"), true},
		{"no resource match", &meta.NoResourceMatchError{PartialResource: schema.GroupVersionResource{Group: resource.Group, Resource: resource.Resource}}, true},
		{"no kind match", &meta.NoKindMatchError{GroupKind: schema.GroupKind{Group: resource.Group, Kind: "APIServer"}}, true},
		{"forbidden", apierrors.NewForbidden(resource, "cluster", errors.New("denied")), false},
		{"unauthorized", apierrors.NewUnauthorized("denied"), false},
		{"service unavailable", apierrors.NewServiceUnavailable("retry"), false},
		{"timeout", apierrors.NewTimeoutError("retry", 1), false},
		{"server timeout", apierrors.NewServerTimeout(resource, "get", 1), false},
		{"too many requests", apierrors.NewTooManyRequests("retry", 1), false},
		{"deadline", context.DeadlineExceeded, false},
		{"canceled", context.Canceled, false},
		{"unexpected", errors.New("unexpected"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reader := &proxyReader{err: tt.err}
			got, err := resolveProxy(context.Background(), reader, true, false)
			if tt.fallback {
				if err != nil || !reflect.DeepEqual(got, intermediateProxyArgs) {
					t.Fatalf("arguments, error = %q, %v; want Intermediate fallback", got, err)
				}
			} else if !errors.Is(err, tt.err) || got != nil {
				t.Fatalf("arguments, error = %q, %v; want nil and wrapped %v", got, err, tt.err)
			}
			if !reflect.DeepEqual(reader.keys, []client.ObjectKey{{Name: "cluster"}}) {
				t.Errorf("APIServer reads = %v, want one read of cluster", reader.keys)
			}
		})
	}
}

func TestResolveProxyPublicEntryPoint(t *testing.T) {
	reader := proxyAPIServerReader(t, configv1.TLSAdherencePolicyStrictAllComponents,
		customProxyProfile(configv1.VersionTLS12, []string{"ECDHE-RSA-AES128-GCM-SHA256"}, configv1.TLSGroupSecP256r1))
	got, err := ResolveProxy(context.Background(), reader, true)
	want := []string{"--tls-min-version=VersionTLS12", "--tls-cipher-suites=TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256", "--tls-curve-preferences=23"}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("arguments, error = %q, %v; want %q", got, err, want)
	}
}

func TestProxyArgsCipherAndGroupMapping(t *testing.T) {
	tests := []struct {
		name    string
		profile *configv1.TLSSecurityProfile
		fips    bool
		want    []string
	}{
		{"OpenSSL and IANA deduplicate and drop unusable ciphers", customProxyProfile(configv1.VersionTLS12, []string{"TLS_AES_128_GCM_SHA256", "invalid", "ECDHE-RSA-AES128-GCM-SHA256", "TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256", "ECDHE-ECDSA-AES256-GCM-SHA384"}), false, []string{"--tls-min-version=VersionTLS12", "--tls-cipher-suites=TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384"}},
		{"all supported groups preserve ordered uniqueness", customProxyProfile(configv1.VersionTLS12, []string{"ECDHE-RSA-AES128-GCM-SHA256"}, configv1.TLSGroupSecP521r1, configv1.TLSGroupSecP256r1, configv1.TLSGroupX25519, configv1.TLSGroupSecP384r1, configv1.TLSGroupX25519MLKEM768, configv1.TLSGroupSecP256r1, "unsupported"), false, []string{"--tls-min-version=VersionTLS12", "--tls-cipher-suites=TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256", "--tls-curve-preferences=25,23,29,24,4588"}},
		{"TLS13 omits cipher flag even with invalid ciphers", customProxyProfile(configv1.VersionTLS13, []string{"invalid"}, configv1.TLSGroupSecP384r1), false, []string{"--tls-min-version=VersionTLS13", "--tls-curve-preferences=24"}},
		{"TLS13 permits empty cipher list and omitted groups", customProxyProfile(configv1.VersionTLS13, nil), false, []string{"--tls-min-version=VersionTLS13"}},
		{"FIPS drops X25519 and hybrids", customProxyProfile(configv1.VersionTLS12, []string{"ECDHE-RSA-AES128-GCM-SHA256"}, configv1.TLSGroupX25519, configv1.TLSGroupX25519MLKEM768, configv1.TLSGroupSecP256r1, configv1.TLSGroupSecP384r1, configv1.TLSGroupSecP521r1, configv1.TLSGroupSecP256r1MLKEM768, configv1.TLSGroupSecP384r1MLKEM1024), true, []string{"--tls-min-version=VersionTLS12", "--tls-cipher-suites=TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256", "--tls-curve-preferences=23,24,25"}},
		{"FIPS Intermediate filters nonapproved ciphers and groups", nil, true, []string{"--tls-min-version=VersionTLS12", "--tls-cipher-suites=TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384", "--tls-curve-preferences=23,24"}},
		{"FIPS drops ChaCha20 but keeps approved AES", customProxyProfile(configv1.VersionTLS12, []string{"ECDHE-RSA-CHACHA20-POLY1305", "ECDHE-RSA-AES128-GCM-SHA256"}), true, []string{"--tls-min-version=VersionTLS12", "--tls-cipher-suites=TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := proxyArgs(context.Background(), tt.profile, tt.fips)
			if err != nil || !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("arguments, error = %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

func TestProxyArgsRejectsUnusableStrictProfiles(t *testing.T) {
	tests := []struct {
		name    string
		profile *configv1.TLSSecurityProfile
		fips    bool
		wantErr string
	}{
		{"missing custom spec", &configv1.TLSSecurityProfile{Type: configv1.TLSProfileCustomType}, false, "custom TLS profile"},
		{"unknown profile type", &configv1.TLSSecurityProfile{Type: "unknown"}, false, "unsupported TLS profile type"},
		{"Old minimum", &configv1.TLSSecurityProfile{Type: configv1.TLSProfileOldType}, false, "minimum TLS version"},
		{"TLS10", customProxyProfile(configv1.VersionTLS10, []string{"ECDHE-RSA-AES128-GCM-SHA256"}), false, "minimum TLS version"},
		{"TLS11", customProxyProfile(configv1.VersionTLS11, []string{"ECDHE-RSA-AES128-GCM-SHA256"}), false, "minimum TLS version"},
		{"invalid minimum", customProxyProfile("invalid", []string{"ECDHE-RSA-AES128-GCM-SHA256"}), false, "minimum version"},
		{"empty minimum", customProxyProfile("", []string{"ECDHE-RSA-AES128-GCM-SHA256"}), false, "minimum version"},
		{"empty TLS12 ciphers", customProxyProfile(configv1.VersionTLS12, nil), false, "no cipher suites"},
		{"TLS13 only ciphers with TLS12 minimum", customProxyProfile(configv1.VersionTLS12, []string{"TLS_AES_128_GCM_SHA256"}), false, "no cipher suites supported"},
		{"unimplemented ciphers", customProxyProfile(configv1.VersionTLS12, []string{"invalid", "DHE-RSA-AES128-GCM-SHA256"}), false, "no cipher suites supported"},
		{"unsupported groups", customProxyProfile(configv1.VersionTLS12, []string{"ECDHE-RSA-AES128-GCM-SHA256"}, "unsupported"), false, "no TLS groups supported"},
		{"FIPS disallowed groups", customProxyProfile(configv1.VersionTLS13, nil, configv1.TLSGroupX25519, configv1.TLSGroupX25519MLKEM768), true, "no TLS groups supported"},
		{"FIPS unsupported hybrids", customProxyProfile(configv1.VersionTLS13, nil, configv1.TLSGroupSecP256r1MLKEM768, configv1.TLSGroupSecP384r1MLKEM1024), true, "no TLS groups supported"},
		{"FIPS only disallowed ciphers", customProxyProfile(configv1.VersionTLS12, []string{"ECDHE-RSA-CHACHA20-POLY1305"}), true, "no cipher suites supported"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := proxyArgs(context.Background(), tt.profile, tt.fips)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) || got != nil {
				t.Fatalf("arguments, error = %q, %v; want nil and error containing %q", got, err, tt.wantErr)
			}
		})
	}
}

func TestResolveProxyRequiresHTTP2CompatibleTLS12Ciphers(t *testing.T) {
	// The proxy configures HTTP/2 at startup, which rejects an explicit TLS 1.2
	// cipher list without ECDHE RSA or ECDSA AES-128-GCM. TLS 1.3 has no such
	// list. A profile that cannot satisfy the requirement fails resolution.
	tests := []struct {
		name    string
		version configv1.TLSProtocolVersion
		ciphers []string
		want    []string
	}{
		{"RSA AES256 only", configv1.VersionTLS12, []string{"ECDHE-RSA-AES256-GCM-SHA384"}, nil},
		{"ECDSA AES256 only", configv1.VersionTLS12, []string{"ECDHE-ECDSA-AES256-GCM-SHA384"}, nil},
		{"ChaCha20 only", configv1.VersionTLS12, []string{"ECDHE-RSA-CHACHA20-POLY1305", "ECDHE-ECDSA-CHACHA20-POLY1305"}, nil},
		{"RSA AES128", configv1.VersionTLS12, []string{"ECDHE-RSA-AES128-GCM-SHA256"}, []string{"--tls-min-version=VersionTLS12", "--tls-cipher-suites=TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256"}},
		{"ECDSA AES128", configv1.VersionTLS12, []string{"ECDHE-ECDSA-AES128-GCM-SHA256"}, []string{"--tls-min-version=VersionTLS12", "--tls-cipher-suites=TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256"}},
		{"TLS13 AES256 only", configv1.VersionTLS13, []string{"ECDHE-RSA-AES256-GCM-SHA384"}, []string{"--tls-min-version=VersionTLS13"}},
		{"TLS13 ChaCha20 only", configv1.VersionTLS13, []string{"ECDHE-RSA-CHACHA20-POLY1305"}, []string{"--tls-min-version=VersionTLS13"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, policy := range []configv1.TLSAdherencePolicy{configv1.TLSAdherencePolicyStrictAllComponents, "FuturePolicy"} {
				reader := proxyAPIServerReader(t, policy, customProxyProfile(tt.version, tt.ciphers))
				got, err := resolveProxy(context.Background(), reader, true, false)
				if tt.want == nil {
					if err == nil || !strings.Contains(err.Error(), "HTTP/2") || got != nil {
						t.Fatalf("policy %q: arguments, error = %q, %v; want nil and HTTP/2 compatibility error", policy, got, err)
					}
				} else if err != nil || !reflect.DeepEqual(got, tt.want) {
					t.Fatalf("policy %q: arguments, error = %q, %v; want %q", policy, got, err, tt.want)
				}
			}
		})
	}
}
