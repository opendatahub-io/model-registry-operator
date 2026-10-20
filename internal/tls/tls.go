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
	"time"

	"github.com/go-logr/logr"
	frameworktls "github.com/opendatahub-io/odh-platform-utilities/framework/tls"
	oapiconfig "github.com/openshift/api/config/v1"
	tlspkg "github.com/openshift/controller-runtime-common/pkg/tls"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// TLSConfig holds the result of TLS profile configuration.
type TLSConfig struct {
	Opts             []func(*tls.Config)
	Profile          oapiconfig.TLSProfileSpec
	AdherencePolicy  oapiconfig.TLSAdherencePolicy
	AdherenceFetched bool
}

// ConfigureTLS reads the cluster TLS security profile (on OpenShift) and returns
// TLS options suitable for webhook and metrics servers.
func ConfigureTLS(cfg *rest.Config, scheme *runtime.Scheme, hasConfigAPI bool, log logr.Logger) (TLSConfig, error) {
	var result TLSConfig

	if hasConfigAPI {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		bootstrapCfg := rest.CopyConfig(cfg)
		bootstrapCfg.Timeout = 10 * time.Second
		bootstrapClient, err := client.New(bootstrapCfg, client.Options{Scheme: scheme})
		if err != nil {
			return TLSConfig{}, fmt.Errorf("unable to create bootstrap client for TLS profile: %w", err)
		}

		result, err = configureTLSFromClient(ctx, bootstrapClient, log)
		if err != nil {
			return TLSConfig{}, err
		}
	}

	result.Opts = append(result.Opts, func(c *tls.Config) {
		c.NextProtos = []string{"h2", "http/1.1"}
	})

	return result, nil
}

func configureTLSFromClient(ctx context.Context, bootstrapClient client.Client, log logr.Logger) (TLSConfig, error) {
	reader := &apiServerReader{Reader: bootstrapClient}
	loaded, err := frameworktls.LoadWithAdherence(ctx, reader)
	if err != nil {
		if reader.err != nil {
			_, _, err = resolveAdherencePolicy(oapiconfig.TLSAdherencePolicyNoOpinion, reader.err, log)
		}
		return TLSConfig{}, err
	}

	profile := loaded.Spec
	if reader.apiServer != nil {
		profile, err = tlspkg.GetTLSProfileSpec(reader.apiServer.Spec.TLSSecurityProfile)
		if err != nil {
			return TLSConfig{}, fmt.Errorf("unable to fetch TLS profile from APIServer %q: %w", tlspkg.APIServerName, err)
		}
	} else if reader.err != nil {
		if isTransientAPIError(reader.err) {
			log.Info("Transient API error reading TLS profile, using Intermediate fallback", "error", reader.err)
		} else {
			log.Error(reader.err, "unable to fetch TLS profile, using defaults")
		}
	}

	option, err := tlsOptionsFromProfile(profile, log)
	if err != nil {
		return TLSConfig{}, err
	}

	return TLSConfig{
		Opts:             []func(*tls.Config){option},
		Profile:          profile,
		AdherencePolicy:  loaded.AdherencePolicy,
		AdherenceFetched: true,
	}, nil
}

type apiServerReader struct {
	client.Reader
	apiServer *oapiconfig.APIServer
	err       error
}

func (r *apiServerReader) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	err := r.Reader.Get(ctx, key, obj, opts...)
	if apiServer, ok := obj.(*oapiconfig.APIServer); ok {
		r.err = err
		if err == nil {
			r.apiServer = apiServer
		}
	}
	return err
}

func tlsOptionsFromProfile(profile oapiconfig.TLSProfileSpec, log logr.Logger) (func(*tls.Config), error) {
	minVersion, err := minTLSVersion(profile.MinTLSVersion)
	if err != nil {
		return nil, err
	}

	tlsConfigFn, unsupportedCiphers := frameworktls.ConfigFromProfile(profile)
	if len(unsupportedCiphers) > 0 {
		log.Info("some ciphers from TLS profile are not supported by Go", "unsupported", unsupportedCiphers)
	}
	if minVersion == tls.VersionTLS13 {
		return tlsConfigFn, nil
	}

	scratch := &tls.Config{}
	tlsConfigFn(scratch)
	usable := usableCipherSuites(scratch.CipherSuites)
	if len(profile.Ciphers) > 0 && len(usable) == 0 {
		return nil, fmt.Errorf("failed to find usable cipher suites for TLS <= 1.2: all %d ciphers in TLS profile are TLS 1.3-only or unimplemented by Go", len(profile.Ciphers))
	}

	return func(config *tls.Config) {
		tlsConfigFn(config)
		if len(profile.Ciphers) > 0 {
			config.CipherSuites = usable
		}
	}, nil
}

func minTLSVersion(version oapiconfig.TLSProtocolVersion) (uint16, error) {
	switch version {
	case "":
		return tls.VersionTLS12, nil
	case oapiconfig.VersionTLS10:
		return tls.VersionTLS10, nil
	case oapiconfig.VersionTLS11:
		return tls.VersionTLS11, nil
	case oapiconfig.VersionTLS12:
		return tls.VersionTLS12, nil
	case oapiconfig.VersionTLS13:
		return tls.VersionTLS13, nil
	default:
		return 0, fmt.Errorf("failed to resolve minimum TLS version %q: unsupported", version)
	}
}

func usableCipherSuites(mapped []uint16) []uint16 {
	usable := make(map[uint16]struct{})
	for _, suite := range tls.CipherSuites() {
		usable[suite.ID] = struct{}{}
	}
	for _, suite := range tls.InsecureCipherSuites() {
		usable[suite.ID] = struct{}{}
	}

	filtered := make([]uint16, 0, len(mapped))
	for _, id := range mapped {
		if id >= 0x1300 && id <= 0x13ff {
			continue
		}
		if _, ok := usable[id]; ok {
			filtered = append(filtered, id)
		}
	}
	return filtered
}

// resolveAdherencePolicy decides the effective TLS adherence policy given the
// result of fetching it from the APIServer. On fail-closed errors it returns a
// wrapped error and fetched=false; on success or graceful/transient errors it
// returns fetched=true (logging graceful/transient errors and falling back to
// NoOpinion) so the watcher can be seeded and self-heal.
func resolveAdherencePolicy(policy oapiconfig.TLSAdherencePolicy, err error, log logr.Logger) (oapiconfig.TLSAdherencePolicy, bool, error) {
	if err == nil {
		return policy, true, nil
	}

	failClosed, reason := classifyAdherenceFetchError(err)
	if failClosed {
		return oapiconfig.TLSAdherencePolicyNoOpinion, false, fmt.Errorf("%s: %w", reason, err)
	}

	log.Info(reason, "error", err)
	return oapiconfig.TLSAdherencePolicyNoOpinion, true, nil
}

// classifyAdherenceFetchError decides how ConfigureTLS should react to an error
// returned while fetching the cluster APIServer. It returns failClosed=true when
// the operator must not continue (authorization / unexpected), and a human-readable
// reason for logging or error wrapping.
func classifyAdherenceFetchError(err error) (failClosed bool, reason string) {
	switch {
	case apierrors.IsNotFound(err), apimeta.IsNoMatchError(err):
		return false, "APIServer resource not found, continuing with default TLS adherence policy"
	case isTransientAPIError(err):
		return false, "transient API error reading APIServer TLS configuration, continuing with defaults; watcher will retry"
	case apierrors.IsForbidden(err):
		return true, "forbidden reading APIServer TLS configuration (check operator RBAC for the APIServer resource)"
	case apierrors.IsUnauthorized(err):
		return true, "unauthorized reading APIServer TLS configuration (check operator credentials for the APIServer resource)"
	default:
		return true, "unexpected error reading APIServer TLS configuration"
	}
}

// isTransientAPIError reports whether err is a transient API error likely to
// succeed on retry.
func isTransientAPIError(err error) bool {
	return apierrors.IsServiceUnavailable(err) ||
		apierrors.IsTimeout(err) ||
		apierrors.IsServerTimeout(err) ||
		apierrors.IsTooManyRequests(err) ||
		errors.Is(err, context.DeadlineExceeded)
}
