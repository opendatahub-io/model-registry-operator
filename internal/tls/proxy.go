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
	"crypto/fips140"
	"errors"
	"fmt"
	"os"
	"strings"

	frameworktls "github.com/opendatahub-io/odh-platform-utilities/framework/tls"
	configv1 "github.com/openshift/api/config/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"sigs.k8s.io/controller-runtime/pkg/client"
	klog "sigs.k8s.io/controller-runtime/pkg/log"
)

// ResolveProxy returns kube-rbac-proxy TLS flags using a direct API reader.
// An absent APIServer API or a predefined cluster profile whose minimum version
// is below the proxy's TLS 1.2 floor falls back to the hardened Intermediate
// default; a failed APIServer read or any other profile the proxy cannot honor
// returns an error.
func ResolveProxy(ctx context.Context, reader client.Reader, hasConfigAPI bool) ([]string, error) {
	// Product Go builds also enable FIPS from the kernel setting.
	kernelFIPS, _ := os.ReadFile("/proc/sys/crypto/fips_enabled")
	fipsEnabled := fips140.Enabled() || strings.TrimSpace(string(kernelFIPS)) == "1"
	return resolveProxy(ctx, reader, hasConfigAPI, fipsEnabled)
}

// errProxyTLSBelowMinimum marks a profile whose minimum TLS version is below
// what kube-rbac-proxy supports.
var errProxyTLSBelowMinimum = errors.New("proxy minimum TLS version is below TLS 1.2")

func resolveProxy(ctx context.Context, reader client.Reader, hasConfigAPI, fipsEnabled bool) ([]string, error) {
	var profile *configv1.TLSSecurityProfile
	if hasConfigAPI {
		apiServer := &configv1.APIServer{}
		if err := reader.Get(ctx, client.ObjectKey{Name: "cluster"}, apiServer); err != nil {
			if !apierrors.IsNotFound(err) && !meta.IsNoMatchError(err) {
				return nil, fmt.Errorf("read APIServer cluster for proxy TLS configuration: %w", err)
			}
		} else if frameworktls.ShouldHonorClusterTLSProfile(apiServer.Spec.TLSAdherence) {
			profile = apiServer.Spec.TLSSecurityProfile
		}
	}
	args, err := proxyArgs(ctx, profile, fipsEnabled)
	// Only a predefined profile below TLS 1.2 falls back: Intermediate is
	// stricter than every such profile, so the swap can never weaken policy. A
	// Custom profile is an explicit admin choice and any other unusable profile
	// fails closed, keeping the last good Deployment.
	if errors.Is(err, errProxyTLSBelowMinimum) && profile != nil && profile.Custom == nil {
		klog.FromContext(ctx).Error(err, "cluster TLS profile is below kube-rbac-proxy's minimum; using Intermediate")
		return proxyArgs(ctx, nil, fipsEnabled)
	}
	return args, err
}

func proxyArgs(ctx context.Context, profile *configv1.TLSSecurityProfile, fipsEnabled bool) ([]string, error) {
	version, ciphers, err := frameworktls.FromProfileStrict(ctx, profile, frameworktls.FormatGo)
	if err != nil {
		return nil, fmt.Errorf("resolve proxy TLS profile: %w", err)
	}
	if version != string(configv1.VersionTLS12) && version != string(configv1.VersionTLS13) {
		if version == string(configv1.VersionTLS10) || version == string(configv1.VersionTLS11) {
			return nil, fmt.Errorf("%w: minimum TLS version %q is unsupported, require TLS 1.2 or TLS 1.3", errProxyTLSBelowMinimum, version)
		}
		return nil, fmt.Errorf("proxy minimum TLS version %q is unsupported: require TLS 1.2 or TLS 1.3", version)
	}

	args := []string{"--tls-min-version=" + version}
	if version == string(configv1.VersionTLS12) {
		usable := make([]string, 0)
		seen := make(map[string]bool)
		for cipher := range strings.SplitSeq(ciphers, ",") {
			if cipher == "" || seen[cipher] || fipsEnabled && !proxyFIPSCipher(cipher) {
				continue
			}
			seen[cipher] = true
			usable = append(usable, cipher)
		}
		if len(usable) == 0 {
			return nil, fmt.Errorf("proxy TLS profile contains no cipher suites supported in the current crypto mode")
		}
		// kube-rbac-proxy always configures HTTP/2, whose TLS 1.2 startup
		// validation requires one of these suites. Reject instead of adding
		// a cipher the strict profile did not request.
		if !seen["TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256"] && !seen["TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256"] {
			return nil, fmt.Errorf("proxy TLS 1.2 profile lacks an HTTP/2-required ECDHE RSA or ECDSA AES-128-GCM cipher suite")
		}
		args = append(args, "--tls-cipher-suites="+strings.Join(usable, ","))
	}

	// FromProfileStrict already validated profile shape; this accessor is safe.
	spec := frameworktls.ProfileSpecFromSecurityProfile(profile)
	curves := make([]string, 0, len(spec.Groups))
	seen := make(map[string]bool)
	for _, group := range spec.Groups {
		var curve string
		switch group {
		case configv1.TLSGroupSecP256r1:
			curve = "23"
		case configv1.TLSGroupSecP384r1:
			curve = "24"
		case configv1.TLSGroupSecP521r1:
			curve = "25"
		case configv1.TLSGroupX25519:
			if !fipsEnabled {
				curve = "29"
			}
		case configv1.TLSGroupX25519MLKEM768:
			if !fipsEnabled {
				curve = "4588"
			}
		}
		if curve != "" && !seen[curve] {
			seen[curve] = true
			curves = append(curves, curve)
		}
	}
	if len(spec.Groups) > 0 && len(curves) == 0 {
		return nil, fmt.Errorf("proxy TLS profile contains no TLS groups supported in the current crypto mode")
	}
	if len(curves) > 0 {
		args = append(args, "--tls-curve-preferences="+strings.Join(curves, ","))
	}
	return args, nil
}

func proxyFIPSCipher(cipher string) bool {
	// Restrict TLS 1.2 to ECDHE AES-GCM suites supported by the operand image.
	switch cipher {
	case "TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256", "TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256",
		"TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384", "TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384":
		return true
	default:
		return false
	}
}
