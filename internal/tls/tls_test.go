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

package tls

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	oapiconfig "github.com/openshift/api/config/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type getErrorClient struct {
	client.Client
	err      error
	getCalls int
}

func (c *getErrorClient) Get(context.Context, client.ObjectKey, client.Object, ...client.GetOption) error {
	c.getCalls++
	return c.err
}

func TestClassifyAdherenceFetchError(t *testing.T) {
	apiServerGR := schema.GroupResource{Group: "config.openshift.io", Resource: "apiservers"}

	tests := map[string]struct {
		err            error
		wantFailClosed bool
	}{
		"not found": {
			err:            apierrors.NewNotFound(apiServerGR, "cluster"),
			wantFailClosed: false,
		},
		"no resource match": {
			err: &apimeta.NoResourceMatchError{
				PartialResource: schema.GroupVersionResource{Group: "config.openshift.io", Resource: "apiservers"},
			},
			wantFailClosed: false,
		},
		"service unavailable": {
			err:            apierrors.NewServiceUnavailable("down for maintenance"),
			wantFailClosed: false,
		},
		"timeout": {
			err:            apierrors.NewTimeoutError("timed out", 1),
			wantFailClosed: false,
		},
		"server timeout": {
			err:            apierrors.NewServerTimeout(apiServerGR, "get", 1),
			wantFailClosed: false,
		},
		"too many requests": {
			err:            apierrors.NewTooManyRequests("slow down", 1),
			wantFailClosed: false,
		},
		"context deadline exceeded": {
			err:            context.DeadlineExceeded,
			wantFailClosed: false,
		},
		"forbidden": {
			err:            apierrors.NewForbidden(apiServerGR, "cluster", errors.New("no access")),
			wantFailClosed: true,
		},
		"unauthorized": {
			err:            apierrors.NewUnauthorized("no access"),
			wantFailClosed: true,
		},
		"unexpected error": {
			err:            errors.New("boom"),
			wantFailClosed: true,
		},
		"wrapped forbidden": {
			err: fmt.Errorf("failed to get APIServer %q: %w", "cluster",
				apierrors.NewForbidden(apiServerGR, "cluster", errors.New("no access"))),
			wantFailClosed: true,
		},
		"wrapped not found": {
			err: fmt.Errorf("failed to get APIServer %q: %w", "cluster",
				apierrors.NewNotFound(apiServerGR, "cluster")),
			wantFailClosed: false,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			failClosed, reason := classifyAdherenceFetchError(tt.err)
			if failClosed != tt.wantFailClosed {
				t.Errorf("classifyAdherenceFetchError(%v) failClosed = %v, want %v", tt.err, failClosed, tt.wantFailClosed)
			}
			if reason == "" {
				t.Errorf("classifyAdherenceFetchError(%v) returned empty reason", tt.err)
			}
		})
	}
}

func TestConfigureTLSFromClientDeniedReadFailsClosed(t *testing.T) {
	apiServerGR := schema.GroupResource{Group: "config.openshift.io", Resource: "apiservers"}
	for name, readErr := range map[string]error{
		"forbidden":    apierrors.NewForbidden(apiServerGR, "cluster", errors.New("no access")),
		"unauthorized": apierrors.NewUnauthorized("no access"),
	} {
		t.Run(name, func(t *testing.T) {
			bootstrapClient := &getErrorClient{err: readErr}
			result, err := configureTLSFromClient(context.Background(), bootstrapClient, logr.Discard())
			if err == nil {
				t.Fatal("configureTLSFromClient() error = nil, want authorization error")
			}
			if bootstrapClient.getCalls != 1 {
				t.Fatalf("APIServer Get calls = %d, want 1", bootstrapClient.getCalls)
			}
			if len(result.Opts) != 0 {
				t.Fatalf("TLS options = %d, want none on denied APIServer read", len(result.Opts))
			}
		})
	}
}

func TestConfigureTLSFromClientTransientReadUsesIntermediateFallback(t *testing.T) {
	bootstrapClient := &getErrorClient{err: apierrors.NewServiceUnavailable("temporarily unavailable")}
	result, err := configureTLSFromClient(context.Background(), bootstrapClient, logr.Discard())
	if err != nil {
		t.Fatalf("configureTLSFromClient() error = %v, want transient fallback", err)
	}
	if bootstrapClient.getCalls != 1 {
		t.Fatalf("APIServer Get calls = %d, want 1", bootstrapClient.getCalls)
	}
	if !reflect.DeepEqual(result.Profile, *oapiconfig.TLSProfiles[oapiconfig.TLSProfileIntermediateType]) {
		t.Errorf("Profile = %#v, want Intermediate", result.Profile)
	}
	if !result.AdherenceFetched || result.AdherencePolicy != oapiconfig.TLSAdherencePolicyNoOpinion {
		t.Errorf("adherence = (%q, fetched %v), want NoOpinion and fetched", result.AdherencePolicy, result.AdherenceFetched)
	}
}

func TestTLSOptionsFromProfile(t *testing.T) {
	tests := map[string]struct {
		profile  oapiconfig.TLSProfileSpec
		wantErr  string
		wantMin  uint16
		wantCiph []uint16
	}{
		"invalid minimum version": {
			profile: oapiconfig.TLSProfileSpec{MinTLSVersion: "invalid"},
			wantErr: `failed to resolve minimum TLS version "invalid": unsupported`,
		},
		"all custom ciphers unusable": {
			profile: oapiconfig.TLSProfileSpec{
				MinTLSVersion: oapiconfig.VersionTLS12,
				Ciphers:       []string{"TLS_AES_128_GCM_SHA256"},
			},
			wantErr: "failed to find usable cipher suites for TLS <= 1.2: all 1 ciphers in TLS profile are TLS 1.3-only or unimplemented by Go",
		},
		"mixed custom ciphers keep usable subset": {
			profile: oapiconfig.TLSProfileSpec{
				MinTLSVersion: oapiconfig.VersionTLS12,
				Ciphers: []string{
					"TLS_AES_128_GCM_SHA256",
					"TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256",
				},
			},
			wantMin:  tls.VersionTLS12,
			wantCiph: []uint16{tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256},
		},
		"Modern leaves TLS 1.3 ciphers Go-managed": {
			profile: *oapiconfig.TLSProfiles[oapiconfig.TLSProfileModernType],
			wantMin: tls.VersionTLS13,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			option, err := tlsOptionsFromProfile(tt.profile, logr.Discard())
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Fatalf("tlsOptionsFromProfile() error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("tlsOptionsFromProfile() error = %v", err)
			}

			config := &tls.Config{}
			option(config)
			if config.MinVersion != tt.wantMin {
				t.Errorf("MinVersion = %d, want %d", config.MinVersion, tt.wantMin)
			}
			if !reflect.DeepEqual(config.CipherSuites, tt.wantCiph) {
				t.Errorf("CipherSuites = %v, want %v", config.CipherSuites, tt.wantCiph)
			}
		})
	}
}

func TestMinTLSVersion(t *testing.T) {
	tests := map[oapiconfig.TLSProtocolVersion]uint16{
		"":                      tls.VersionTLS12,
		oapiconfig.VersionTLS10: tls.VersionTLS10,
		oapiconfig.VersionTLS11: tls.VersionTLS11,
		oapiconfig.VersionTLS12: tls.VersionTLS12,
		oapiconfig.VersionTLS13: tls.VersionTLS13,
	}

	for version, want := range tests {
		got, err := minTLSVersion(version)
		if err != nil {
			t.Errorf("minTLSVersion(%q) error = %v", version, err)
		} else if got != want {
			t.Errorf("minTLSVersion(%q) = %d, want %d", version, got, want)
		}
	}
}

func TestConfigureTLSFromClientUsesIntermediateForEmptyProfile(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := oapiconfig.Install(scheme); err != nil {
		t.Fatal(err)
	}
	for name, profile := range map[string]*oapiconfig.TLSSecurityProfile{
		"nil profile":   nil,
		"empty profile": {},
	} {
		t.Run(name, func(t *testing.T) {
			apiServer := &oapiconfig.APIServer{}
			apiServer.Name = "cluster"
			apiServer.Spec.TLSSecurityProfile = profile
			bootstrapClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(apiServer).Build()

			result, err := configureTLSFromClient(context.Background(), bootstrapClient, logr.Discard())
			if err != nil {
				t.Fatalf("configureTLSFromClient() error = %v", err)
			}
			if !reflect.DeepEqual(result.Profile, *oapiconfig.TLSProfiles[oapiconfig.TLSProfileIntermediateType]) {
				t.Errorf("Profile = %#v, want Intermediate", result.Profile)
			}
			if !result.AdherenceFetched || result.AdherencePolicy != "" {
				t.Errorf("adherence = (%q, fetched %v), want empty policy fetched", result.AdherencePolicy, result.AdherenceFetched)
			}
			if len(result.Opts) != 1 {
				t.Fatalf("TLS options = %d, want one profile option", len(result.Opts))
			}
			config := &tls.Config{}
			for _, option := range result.Opts {
				option(config)
			}
			if config.MinVersion != tls.VersionTLS12 || len(config.CipherSuites) == 0 {
				t.Errorf("effective TLS config = {MinVersion: %d, CipherSuites: %v}, want Intermediate config", config.MinVersion, config.CipherSuites)
			}
		})
	}
}

func TestConfigureTLSFromClientMalformedProfileFailsBeforeOptions(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := oapiconfig.Install(scheme); err != nil {
		t.Fatal(err)
	}
	apiServer := &oapiconfig.APIServer{}
	apiServer.Name = "cluster"
	apiServer.Spec.TLSSecurityProfile = &oapiconfig.TLSSecurityProfile{Type: oapiconfig.TLSProfileCustomType}
	bootstrapClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(apiServer).Build()

	result, err := configureTLSFromClient(context.Background(), bootstrapClient, logr.Discard())
	if err == nil {
		t.Fatal("configureTLSFromClient() error = nil, want malformed profile error")
	}
	if len(result.Opts) != 0 {
		t.Fatalf("TLS options = %d, want none for malformed profile", len(result.Opts))
	}
}

func TestConfigureTLSFromClientEmptyCipherListAdherence(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := oapiconfig.Install(scheme); err != nil {
		t.Fatal(err)
	}

	profile := oapiconfig.TLSProfileSpec{MinTLSVersion: oapiconfig.VersionTLS12}
	tests := map[string]struct {
		policy        oapiconfig.TLSAdherencePolicy
		wantStrictErr bool
	}{
		"Strict rejects empty cipher list": {
			policy:        oapiconfig.TLSAdherencePolicyStrictAllComponents,
			wantStrictErr: true,
		},
		"NoOpinion accepts empty cipher list": {},
		"unknown policy rejects empty cipher list": {
			policy:        oapiconfig.TLSAdherencePolicy("FuturePolicy"),
			wantStrictErr: true,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			apiServer := &oapiconfig.APIServer{}
			apiServer.Name = "cluster"
			apiServer.Spec.TLSAdherence = tt.policy
			apiServer.Spec.TLSSecurityProfile = &oapiconfig.TLSSecurityProfile{
				Type:   oapiconfig.TLSProfileCustomType,
				Custom: &oapiconfig.CustomTLSProfile{TLSProfileSpec: profile},
			}
			bootstrapClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(apiServer).Build()

			result, err := configureTLSFromClient(context.Background(), bootstrapClient, logr.Discard())
			if tt.wantStrictErr {
				if err == nil || !strings.Contains(err.Error(), "invalid strict APIServer TLS profile: TLS profile contains no cipher suites") {
					t.Fatalf("configureTLSFromClient() error = %v, want strict empty-cipher-list validation error", err)
				}
				if len(result.Opts) != 0 {
					t.Fatalf("TLS options = %d, want none after strict validation error", len(result.Opts))
				}
				return
			}

			if err != nil {
				t.Fatalf("configureTLSFromClient() error = %v, want accepted NoOpinion profile", err)
			}
			if len(result.Opts) != 1 {
				t.Fatalf("TLS options = %d, want one profile option", len(result.Opts))
			}
			config := &tls.Config{}
			result.Opts[0](config)
			if config.MinVersion != tls.VersionTLS12 || len(config.CipherSuites) != 0 {
				t.Errorf("effective TLS config = {MinVersion: %d, CipherSuites: %v}, want TLS 1.2 with unrestricted ciphers", config.MinVersion, config.CipherSuites)
			}
		})
	}
}

func TestResolveAdherencePolicy(t *testing.T) {
	apiServerGR := schema.GroupResource{Group: "config.openshift.io", Resource: "apiservers"}

	tests := map[string]struct {
		policy      oapiconfig.TLSAdherencePolicy
		err         error
		wantPolicy  oapiconfig.TLSAdherencePolicy
		wantFetched bool
		wantErr     bool
	}{
		"success": {
			policy:      oapiconfig.TLSAdherencePolicyStrictAllComponents,
			err:         nil,
			wantPolicy:  oapiconfig.TLSAdherencePolicyStrictAllComponents,
			wantFetched: true,
			wantErr:     false,
		},
		"not found": {
			err:         apierrors.NewNotFound(apiServerGR, "cluster"),
			wantPolicy:  oapiconfig.TLSAdherencePolicyNoOpinion,
			wantFetched: true,
			wantErr:     false,
		},
		"no resource match": {
			err: &apimeta.NoResourceMatchError{
				PartialResource: schema.GroupVersionResource{Group: "config.openshift.io", Resource: "apiservers"},
			},
			wantPolicy:  oapiconfig.TLSAdherencePolicyNoOpinion,
			wantFetched: true,
			wantErr:     false,
		},
		"transient": {
			err:         apierrors.NewServiceUnavailable("down for maintenance"),
			wantPolicy:  oapiconfig.TLSAdherencePolicyNoOpinion,
			wantFetched: true,
			wantErr:     false,
		},
		"forbidden": {
			err:         apierrors.NewForbidden(apiServerGR, "cluster", errors.New("no access")),
			wantPolicy:  oapiconfig.TLSAdherencePolicyNoOpinion,
			wantFetched: false,
			wantErr:     true,
		},
		"unexpected error": {
			err:         errors.New("boom"),
			wantPolicy:  oapiconfig.TLSAdherencePolicyNoOpinion,
			wantFetched: false,
			wantErr:     true,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			policy, fetched, err := resolveAdherencePolicy(tt.policy, tt.err, logr.Discard())
			if policy != tt.wantPolicy {
				t.Errorf("resolveAdherencePolicy(%v) policy = %v, want %v", tt.err, policy, tt.wantPolicy)
			}
			if fetched != tt.wantFetched {
				t.Errorf("resolveAdherencePolicy(%v) fetched = %v, want %v", tt.err, fetched, tt.wantFetched)
			}
			if (err != nil) != tt.wantErr {
				t.Errorf("resolveAdherencePolicy(%v) err = %v, wantErr %v", tt.err, err, tt.wantErr)
			}
		})
	}
}
