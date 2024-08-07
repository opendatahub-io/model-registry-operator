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

package v1alpha1

import (
	"fmt"
	"github.com/opendatahub-io/model-registry-operator/internal/controller/config"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// log is for logging in this package.
var modelregistrylog = logf.Log.WithName("modelregistry-resource")

// default ports
const (
	DEFAULT_TLS_MODE      = "ISTIO_MUTUAL"
	DEFAULT_HTTP_PORT     = 80
	DEFAULT_HTTPS_PORT    = 443
	DEFAULT_ISTIO_GATEWAY = "ingressgateway"
)

func (r *ModelRegistry) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(r).
		Complete()
}

// TODO(user): EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!

//+kubebuilder:webhook:path=/mutate-modelregistry-opendatahub-io-v1alpha1-modelregistry,mutating=true,failurePolicy=fail,sideEffects=None,groups=modelregistry.opendatahub.io,resources=modelregistries,verbs=create;update,versions=v1alpha1,name=mmodelregistry.opendatahub.io,admissionReviewVersions=v1

var (
	_         webhook.Defaulter = &ModelRegistry{}
	gateway                     = DEFAULT_ISTIO_GATEWAY
	httpPort  int32             = DEFAULT_HTTP_PORT
	httpsPort int32             = DEFAULT_HTTPS_PORT
)

// Default implements webhook.Defaulter so a webhook will be registered for the type
func (r *ModelRegistry) Default() {
	modelregistrylog.Info("default", "name", r.Name)

	if r.Spec.Grpc.Resources == nil {
		r.Spec.Grpc.Resources = config.MlmdGRPCResourceRequirements.DeepCopy()
	}
	if len(r.Spec.Grpc.Image) == 0 {
		r.Spec.Grpc.Image = config.GetStringConfigWithDefault(config.GrpcImage, config.DefaultGrpcImage)
	}

	if len(r.Spec.Rest.ServiceRoute) == 0 {
		r.Spec.Rest.ServiceRoute = config.RouteDisabled
	}
	if r.Spec.Rest.Resources == nil {
		r.Spec.Rest.Resources = config.MlmdRestResourceRequirements.DeepCopy()
	}
	if len(r.Spec.Rest.Image) == 0 {
		r.Spec.Rest.Image = config.GetStringConfigWithDefault(config.RestImage, config.DefaultRestImage)
	}

	// Fixes default database configs that get set for some reason in Kind cluster
	if r.Spec.Postgres != nil && len(r.Spec.Postgres.Host) == 0 && len(r.Spec.Postgres.HostAddress) == 0 {
		r.Spec.Postgres = nil
	}
	if r.Spec.MySQL != nil && len(r.Spec.MySQL.Host) == 0 {
		r.Spec.MySQL = nil
	}

	// istio defaults
	if r.Spec.Istio != nil {
		// set default TlsMode
		if len(r.Spec.Istio.TlsMode) == 0 {
			r.Spec.Istio.TlsMode = DEFAULT_TLS_MODE
		}
		if r.Spec.Istio.Gateway != nil {
			// set ingress gateway if not set
			if r.Spec.Istio.Gateway.IstioIngress == nil {
				r.Spec.Istio.Gateway.IstioIngress = &gateway
			}
			// set default gateway ports if needed
			if r.Spec.Istio.Gateway.Rest.Port == nil {
				if r.Spec.Istio.Gateway.Rest.TLS != nil {
					r.Spec.Istio.Gateway.Rest.Port = &httpsPort
				} else {
					r.Spec.Istio.Gateway.Rest.Port = &httpPort
				}
			}
			if r.Spec.Istio.Gateway.Grpc.Port == nil {
				if r.Spec.Istio.Gateway.Grpc.TLS != nil {
					r.Spec.Istio.Gateway.Grpc.Port = &httpsPort
				} else {
					r.Spec.Istio.Gateway.Grpc.Port = &httpPort
				}
			}
			// enable gateway routes by default
			if len(r.Spec.Istio.Gateway.Rest.GatewayRoute) == 0 {
				r.Spec.Istio.Gateway.Rest.GatewayRoute = config.RouteEnabled
			}
			if len(r.Spec.Istio.Gateway.Grpc.GatewayRoute) == 0 {
				r.Spec.Istio.Gateway.Grpc.GatewayRoute = config.RouteEnabled
			}
		}
	}
}

// TODO(user): change verbs to "verbs=create;update;delete" if you want to enable deletion validation.
//+kubebuilder:webhook:path=/validate-modelregistry-opendatahub-io-v1alpha1-modelregistry,mutating=false,failurePolicy=fail,sideEffects=None,groups=modelregistry.opendatahub.io,resources=modelregistries,verbs=create;update,versions=v1alpha1,name=vmodelregistry.opendatahub.io,admissionReviewVersions=v1

var _ webhook.Validator = &ModelRegistry{}

// ValidateCreate implements webhook.Validator so a webhook will be registered for the type
func (r *ModelRegistry) ValidateCreate() (admission.Warnings, error) {
	modelregistrylog.Info("validate create", "name", r.Name)

	return r.ValidateDatabase()
}

// ValidateUpdate implements webhook.Validator so a webhook will be registered for the type
func (r *ModelRegistry) ValidateUpdate(old runtime.Object) (admission.Warnings, error) {
	modelregistrylog.Info("validate update", "name", r.Name)

	return r.ValidateDatabase()
}

// ValidateDelete implements webhook.Validator so a webhook will be registered for the type
func (r *ModelRegistry) ValidateDelete() (admission.Warnings, error) {
	modelregistrylog.Info("validate delete", "name", r.Name)

	// TODO(user): fill in your validation logic upon object deletion.
	return nil, nil
}

// ValidateDatabase validates that at least one database config is present
func (r *ModelRegistry) ValidateDatabase() (admission.Warnings, error) {
	if r.Spec.Postgres == nil && r.Spec.MySQL == nil {
		return nil, fmt.Errorf("MUST set one of `postgres` or `mysql` database connection properties")
	}
	return nil, nil
}
