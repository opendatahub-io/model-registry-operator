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

package setup

import (
	"context"
	"crypto/tls"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	oapi "github.com/openshift/api"
	oapiconfig "github.com/openshift/api/config/v1"
	tlspkg "github.com/openshift/controller-runtime-common/pkg/tls"
	networking "istio.io/client-go/pkg/apis/networking/v1beta1"
	security "istio.io/client-go/pkg/apis/security/v1beta1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayapiv1beta1 "sigs.k8s.io/gateway-api/apis/v1beta1"

	modelregistryv1alpha1 "github.com/opendatahub-io/model-registry-operator/api/v1alpha1"
	modelregistryv1beta1 "github.com/opendatahub-io/model-registry-operator/api/v1beta1"
	"github.com/opendatahub-io/model-registry-operator/internal/controller"
	//+kubebuilder:scaffold:imports

	"k8s.io/client-go/discovery"
)

// NewScheme creates and returns a new runtime.Scheme with all types required
// by the operator registered.
func NewScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()

	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	// openshift scheme
	utilruntime.Must(oapi.Install(scheme))
	utilruntime.Must(oapiconfig.Install(scheme))
	// istio security scheme
	utilruntime.Must(security.AddToScheme(scheme))
	// istio networking scheme
	utilruntime.Must(networking.AddToScheme(scheme))
	// CRD scheme
	utilruntime.Must(apiextensionsv1.AddToScheme(scheme))
	// Gateway API scheme
	utilruntime.Must(gatewayapiv1.Install(scheme))
	utilruntime.Must(gatewayapiv1beta1.Install(scheme))

	utilruntime.Must(modelregistryv1alpha1.AddToScheme(scheme))
	utilruntime.Must(modelregistryv1beta1.AddToScheme(scheme))
	//+kubebuilder:scaffold:scheme

	return scheme
}

// GetCapabilities detects cluster capabilities by querying the discovery API.
func GetCapabilities() (controller.ClusterCapabilities, error) {
	cfg, err := ctrl.GetConfig()
	if err != nil {
		return controller.ClusterCapabilities{}, err
	}
	client, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return controller.ClusterCapabilities{}, err
	}
	return controller.DetectClusterCapabilities(client)
}

// TLSConfig holds the result of TLS profile configuration.
type TLSConfig struct {
	Opts             []func(*tls.Config)
	Profile          oapiconfig.TLSProfileSpec
	AdherencePolicy  oapiconfig.TLSAdherencePolicy
	AdherenceFetched bool
}

// ConfigureTLS reads the cluster TLS security profile (on OpenShift) and returns
// TLS options suitable for webhook and metrics servers.
func ConfigureTLS(scheme *runtime.Scheme, hasConfigAPI bool, log logr.Logger) (TLSConfig, error) {
	var result TLSConfig

	if hasConfigAPI {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		bootstrapClient, err := client.New(ctrl.GetConfigOrDie(), client.Options{Scheme: scheme})
		if err != nil {
			return TLSConfig{}, fmt.Errorf("unable to create bootstrap client for TLS profile: %w", err)
		}

		profile, err := tlspkg.FetchAPIServerTLSProfile(ctx, bootstrapClient)
		if err != nil {
			switch {
			case apierrors.IsServiceUnavailable(err),
				apierrors.IsTimeout(err),
				apierrors.IsTooManyRequests(err):
				log.Info("Transient API error reading TLS profile, using Intermediate fallback", "error", err)
			default:
				log.Error(err, "unable to fetch TLS profile, using defaults")
			}
			profile = *oapiconfig.TLSProfiles[oapiconfig.TLSProfileIntermediateType]
		}
		result.Profile = profile

		tlsConfigFn, unsupportedCiphers := tlspkg.NewTLSConfigFromProfile(profile)
		if len(unsupportedCiphers) > 0 {
			log.Info("some ciphers from TLS profile are not supported by Go", "unsupported", unsupportedCiphers)
		}
		result.Opts = append(result.Opts, tlsConfigFn)

		var adherenceErr error
		result.AdherencePolicy, adherenceErr = tlspkg.FetchAPIServerTLSAdherencePolicy(ctx, bootstrapClient)
		if adherenceErr != nil {
			log.Error(adherenceErr, "unable to fetch TLS adherence policy, watcher will retry")
		}
		result.AdherenceFetched = true
	}

	result.Opts = append(result.Opts, func(c *tls.Config) {
		c.NextProtos = []string{"h2", "http/1.1"}
	})

	return result, nil
}
