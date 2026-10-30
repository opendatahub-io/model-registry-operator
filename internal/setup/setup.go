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
	"time"

	oapi "github.com/openshift/api"
	oapiconfig "github.com/openshift/api/config/v1"
	networking "istio.io/client-go/pkg/apis/networking/v1beta1"
	security "istio.io/client-go/pkg/apis/security/v1beta1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayapiv1beta1 "sigs.k8s.io/gateway-api/apis/v1beta1"

	aihubv1alpha1 "github.com/opendatahub-io/model-registry-operator/api/aihub/v1alpha1"
	catalogv1alpha1 "github.com/opendatahub-io/model-registry-operator/api/catalog/v1alpha1"
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
	utilruntime.Must(aihubv1alpha1.AddToScheme(scheme))
	utilruntime.Must(catalogv1alpha1.AddToScheme(scheme))
	//+kubebuilder:scaffold:scheme

	return scheme
}

// GetCapabilities detects cluster capabilities by querying the discovery API.
func GetCapabilities(cfg *rest.Config) (controller.ClusterCapabilities, error) {
	// Bound the discovery attempt so a hung connection cannot block startup
	// forever; ServerGroups does not take a context.
	discoveryCfg := rest.CopyConfig(cfg)
	discoveryCfg.Timeout = 10 * time.Second
	client, err := discovery.NewDiscoveryClientForConfig(discoveryCfg)
	if err != nil {
		return controller.ClusterCapabilities{}, err
	}
	return controller.DetectClusterCapabilities(client)
}
