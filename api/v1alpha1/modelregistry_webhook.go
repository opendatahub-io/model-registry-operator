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
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// log is for logging in this package.
var modelregistrylog = logf.Log.WithName("modelregistry-resource")

const (
	// default ports
	DefaultHttpPort  = 80
	DefaultHttpsPort = 443

	DefaultTlsMode      = IstioMutualTlsMode
	IstioMutualTlsMode  = "ISTIO_MUTUAL"
	DefaultIstioGateway = "ingressgateway"
)

func (r *ModelRegistry) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(r).
		Complete()
}

// TODO(user): EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!

//+kubebuilder:webhook:path=/mutate-modelregistry-opendatahub-io-v1alpha1-modelregistry,mutating=true,failurePolicy=fail,sideEffects=None,groups=modelregistry.opendatahub.io,resources=modelregistries,verbs=create;update,versions=v1alpha1,name=mmodelregistry.opendatahub.io,admissionReviewVersions=v1

var (
	_                   webhook.Defaulter = &ModelRegistry{}
	defaultIstioGateway                   = DefaultIstioGateway
	httpsPort           int32             = DefaultHttpsPort
	httpPort            int32             = DefaultHttpPort
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
			r.Spec.Istio.TlsMode = DefaultTlsMode
		}
		// set default audiences
		if len(r.Spec.Istio.Audiences) == 0 {
			r.Spec.Istio.Audiences = config.GetDefaultAudiences()
		}
		// set default authprovider
		if len(r.Spec.Istio.AuthProvider) == 0 {
			r.Spec.Istio.AuthProvider = config.GetDefaultAuthProvider()
		}
		// set default authconfig labels
		if len(r.Spec.Istio.AuthConfigLabels) == 0 {
			r.Spec.Istio.AuthConfigLabels = config.GetDefaultAuthConfigLabels()
		}

		if r.Spec.Istio.Gateway != nil {
			// set default domain
			if len(r.Spec.Istio.Gateway.Domain) == 0 {
				r.Spec.Istio.Gateway.Domain = config.GetDefaultDomain()
			}
			// set ingress gateway if not set
			if r.Spec.Istio.Gateway.IstioIngress == nil {
				r.Spec.Istio.Gateway.IstioIngress = &defaultIstioGateway
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

			// set default cert
			if r.Spec.Istio.Gateway.Rest.TLS != nil && r.Spec.Istio.Gateway.Rest.TLS.Mode != DefaultTlsMode &&
				(r.Spec.Istio.Gateway.Rest.TLS.CredentialName == nil || len(*r.Spec.Istio.Gateway.Rest.TLS.CredentialName) == 0) {
				cert := config.GetDefaultCert()
				r.Spec.Istio.Gateway.Rest.TLS.CredentialName = &cert
			}
			if r.Spec.Istio.Gateway.Grpc.TLS != nil && r.Spec.Istio.Gateway.Grpc.TLS.Mode != DefaultTlsMode &&
				(r.Spec.Istio.Gateway.Grpc.TLS.CredentialName == nil || len(*r.Spec.Istio.Gateway.Grpc.TLS.CredentialName) == 0) {
				cert := config.GetDefaultCert()
				r.Spec.Istio.Gateway.Grpc.TLS.CredentialName = &cert
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

	return r.ValidateRegistry()
}

// ValidateUpdate implements webhook.Validator so a webhook will be registered for the type
func (r *ModelRegistry) ValidateUpdate(_ runtime.Object) (admission.Warnings, error) {
	modelregistrylog.Info("validate update", "name", r.Name)

	return r.ValidateRegistry()
}

// ValidateRegistry validates registry spec
func (r *ModelRegistry) ValidateRegistry() (warnings admission.Warnings, err error) {
	warnings, errList := r.ValidateDatabase()
	warn, errList2 := r.ValidateIstioConfig()

	// combine warnings and errors
	warnings = append(warnings, warn...)
	errList = append(errList, errList2...)
	if len(errList) != 0 {
		err = errors.NewInvalid(r.GroupVersionKind().GroupKind(), r.Name, errList)
	}
	return
}

// ValidateDelete implements webhook.Validator so a webhook will be registered for the type
func (r *ModelRegistry) ValidateDelete() (admission.Warnings, error) {
	modelregistrylog.Info("validate delete", "name", r.Name)

	// TODO(user): fill in your validation logic upon object deletion.
	return nil, nil
}

// ValidateDatabase validates that at least one database config is present
func (r *ModelRegistry) ValidateDatabase() (admission.Warnings, field.ErrorList) {
	if r.Spec.Postgres == nil && r.Spec.MySQL == nil {
		return nil, field.ErrorList{
			field.Required(field.NewPath("spec").Child("postgres"), "required one of `postgres` or `mysql` database"),
			field.Required(field.NewPath("spec").Child("mysql"), "required one of `postgres` or `mysql` database"),
		}
	}
	return nil, nil
}

// ValidateIstioConfig validates the istio and gateway config
func (r *ModelRegistry) ValidateIstioConfig() (warnings admission.Warnings, err field.ErrorList) {
	istio := r.Spec.Istio
	if istio != nil {
		istioPath := field.NewPath("spec").Child("istio")
		if len(istio.AuthProvider) == 0 {
			err = append(err, field.Required(istioPath.Child("authProvider"),
				fmt.Sprintf("missing authProvider and operator environment variable %s", config.DefaultAuthProvider)))
		}
		if len(istio.AuthConfigLabels) == 0 {
			err = append(err, field.Required(istioPath.Child("authConfigLabels"),
				fmt.Sprintf("missing authConfigLabels and operator environment variable %s", config.DefaultAuthConfigLabels)))
		}

		// validate gateway
		gateway := istio.Gateway
		if gateway != nil {
			gatewayPath := istioPath.Child("gateway")
			if len(gateway.Domain) == 0 {
				err = append(err, field.Required(gatewayPath.Child("domain"),
					fmt.Sprintf("missing domain and operator environment variable %s", config.DefaultDomain)))
			}
			if gateway.Rest.TLS != nil && gateway.Rest.TLS.Mode != DefaultTlsMode &&
				(gateway.Rest.TLS.CredentialName == nil || len(*gateway.Rest.TLS.CredentialName) == 0) {
				err = append(err, field.Required(gatewayPath.Child("rest").Child("tls").Child("credentialName"),
					fmt.Sprintf("missing rest credentialName and operator environment variable %s", config.DefaultCert)))
			}
			if gateway.Grpc.TLS != nil && gateway.Grpc.TLS.Mode != DefaultTlsMode &&
				(gateway.Grpc.TLS.CredentialName == nil || len(*gateway.Grpc.TLS.CredentialName) == 0) {
				err = append(err, field.Required(gatewayPath.Child("grpc").Child("tls").Child("credentialName"),
					fmt.Sprintf("missing grpc credentialName and operator environment variable %s", config.DefaultCert)))
			}
		}
	}

	return
}
