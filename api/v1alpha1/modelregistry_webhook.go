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
	"context"
	"fmt"
	"github.com/opendatahub-io/model-registry-operator/internal/controller/config"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"maps"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
	"slices"
	"strings"
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

	tagSeparator = ":"
	emptyValue   = ""
)

func (r *ModelRegistry) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(r).
		WithValidator(&modelRegistryValidator{Client: mgr.GetClient()}).
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
	modelregistrylog.Info("default", "name", r.Name, "status.specDefaults", r.Status.SpecDefaults)

	// handle annotation mutations
	r.HandleAnnotations()

	if len(r.Spec.Rest.ServiceRoute) == 0 {
		r.Spec.Rest.ServiceRoute = config.RouteDisabled
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

		if r.Spec.Istio.Gateway != nil {
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
		}
	}

	// handle runtime default properties for https://issues.redhat.com/browse/RHOAIENG-15033
	r.CleanupRuntimeDefaults()
}

// CleanupRuntimeDefaults removes runtime defaults. Usually on first reconcile, when specDefaults is empty,
// or for model registries reconciled by older operator versions before adding specDefaults support.
// It removes images if they are the same as the operator defaults (ignoring version tag),
// and it removes default runtime values that match default runtime properties set in the operator
// since they are redundant as custom property values.
func (r *ModelRegistry) CleanupRuntimeDefaults() {
	// if specDefaults hasn't been set for new MRs or all properties were set in a previous version
	if r.Status.SpecDefaults != "" && r.Status.SpecDefaults != "{}" {
		// model registry has custom values set for runtime properties
		return
	}

	// check grpc image against operator default grpc image repo
	if len(r.Spec.Grpc.Image) != 0 {
		defaultGrpcImage := config.GetStringConfigWithDefault(config.GrpcImage, config.DefaultGrpcImage)
		defaultGrpcImageRepo := strings.Split(defaultGrpcImage, tagSeparator)[0]

		grpcImageRepo := strings.Split(r.Spec.Grpc.Image, tagSeparator)[0]
		if grpcImageRepo == defaultGrpcImageRepo {
			modelregistrylog.V(4).Info("reset image", "grpc repo", grpcImageRepo)
			// remove image altogether as the MR repo matches operator repo,
			// so that future operator version upgrades don't have to handle a hardcoded default
			r.Spec.Grpc.Image = emptyValue

			// also reset resource requirements
			r.Spec.Grpc.Resources = nil
		}
	}

	// check rest image against operator default rest image repo
	if len(r.Spec.Rest.Image) != 0 {
		defaultRestImage := config.GetStringConfigWithDefault(config.RestImage, config.DefaultRestImage)
		defaultRestImageRepo := strings.Split(defaultRestImage, tagSeparator)[0]

		restImageRepo := strings.Split(r.Spec.Rest.Image, tagSeparator)[0]
		if restImageRepo == defaultRestImageRepo {
			modelregistrylog.V(4).Info("reset image", "rest repo", restImageRepo)
			// remove image altogether as the MR repo matches operator repo,
			// so that future operator version upgrades don't have to handle a hardcoded default
			r.Spec.Rest.Image = emptyValue

			// also reset resource requirements
			r.Spec.Rest.Resources = nil
		}
	}

	// reset istio defaults
	if r.Spec.Istio != nil {
		// reset default audiences
		if len(r.Spec.Istio.Audiences) != 0 && slices.Equal(r.Spec.Istio.Audiences, config.GetDefaultAudiences()) {
			r.Spec.Istio.Audiences = make([]string, 0)
		}
		// reset default authprovider
		if r.Spec.Istio.AuthProvider == config.GetDefaultAuthProvider() {
			r.Spec.Istio.AuthProvider = emptyValue
		}
		// reset default authconfig labels
		if len(r.Spec.Istio.AuthConfigLabels) != 0 && maps.Equal(r.Spec.Istio.AuthConfigLabels, config.GetDefaultAuthConfigLabels()) {
			r.Spec.Istio.AuthConfigLabels = make(map[string]string)
		}

		if r.Spec.Istio.Gateway != nil {
			// reset default domain
			if r.Spec.Istio.Gateway.Domain == config.GetDefaultDomain() {
				r.Spec.Istio.Gateway.Domain = emptyValue
			}

			// reset default cert
			if r.Spec.Istio.Gateway.Rest.TLS != nil && r.Spec.Istio.Gateway.Rest.TLS.Mode != DefaultTlsMode &&
				(r.Spec.Istio.Gateway.Rest.TLS.CredentialName != nil && *r.Spec.Istio.Gateway.Rest.TLS.CredentialName == config.GetDefaultCert()) {
				r.Spec.Istio.Gateway.Rest.TLS.CredentialName = nil
			}
			if r.Spec.Istio.Gateway.Grpc.TLS != nil && r.Spec.Istio.Gateway.Grpc.TLS.Mode != DefaultTlsMode &&
				(r.Spec.Istio.Gateway.Grpc.TLS.CredentialName != nil && *r.Spec.Istio.Gateway.Grpc.TLS.CredentialName == config.GetDefaultCert()) {
				r.Spec.Istio.Gateway.Grpc.TLS.CredentialName = nil
			}
		}
	}
}

// RuntimeDefaults sets default values from the operator environment, which could change at runtime.
func (r *ModelRegistry) RuntimeDefaults() {
	modelregistrylog.Info("runtime defaults", "name", r.Name)

	if r.Spec.Grpc.Resources == nil {
		r.Spec.Grpc.Resources = config.MlmdGRPCResourceRequirements.DeepCopy()
	}
	if len(r.Spec.Grpc.Image) == 0 {
		r.Spec.Grpc.Image = config.GetStringConfigWithDefault(config.GrpcImage, config.DefaultGrpcImage)
	}

	if r.Spec.Rest.Resources == nil {
		r.Spec.Rest.Resources = config.MlmdRestResourceRequirements.DeepCopy()
	}
	if len(r.Spec.Rest.Image) == 0 {
		r.Spec.Rest.Image = config.GetStringConfigWithDefault(config.RestImage, config.DefaultRestImage)
	}

	// istio defaults
	if r.Spec.Istio != nil {
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

// ValidateRegistry validates registry spec
func (r *ModelRegistry) ValidateRegistry() (warnings admission.Warnings, err error) {
	// set runtime defaults before validation, just like the reconcile loop
	r.RuntimeDefaults()

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

// TODO(user): change verbs to "verbs=create;update;delete" if you want to enable deletion validation.
//+kubebuilder:webhook:path=/validate-modelregistry-opendatahub-io-v1alpha1-modelregistry,mutating=false,failurePolicy=fail,sideEffects=None,groups=modelregistry.opendatahub.io,resources=modelregistries,verbs=create;update,versions=v1alpha1,name=vmodelregistry.opendatahub.io,admissionReviewVersions=v1

// NOTE: this type MUST not be exported, or kubebuilder thinks its a CRD and tried to generate code for it!!!
type modelRegistryValidator struct {
	client.Client
}

var _ webhook.CustomValidator = &modelRegistryValidator{}

// ValidateCreate validates the object on creation. The optional warnings will be added to the response as warning messages. Return an error if the object is invalid.
func (v *modelRegistryValidator) ValidateCreate(ctx context.Context, obj runtime.Object) (warnings admission.Warnings, err error) {
	r, err := v.toModelRegistry(obj)
	if err != nil {
		return
	}
	modelregistrylog.Info("validate create", "name", r.Name)

	// make sure registry name is unique in the cluster
	warnings, err = v.validateName(ctx, r)
	if err != nil {
		return
	}

	return r.ValidateRegistry()
}

func (v *modelRegistryValidator) toModelRegistry(obj runtime.Object) (*ModelRegistry, error) {
	r, ok := obj.(*ModelRegistry)
	if !ok {
		return nil, errors.NewBadRequest(fmt.Sprintf("unsupported type %s", obj.GetObjectKind().GroupVersionKind().String()))
	}
	return r, nil
}

func (v *modelRegistryValidator) validateName(ctx context.Context, r *ModelRegistry) (warnings admission.Warnings, err error) {
	registries := &ModelRegistryList{}
	err = v.Client.List(ctx, registries)
	if err != nil {
		return
	}

	for _, registry := range registries.Items {
		if registry.Name == r.Name {
			return warnings, errors.NewInvalid(r.GroupVersionKind().GroupKind(),
				r.Name, field.ErrorList{
					field.Duplicate(field.NewPath("metadata").Child("name"),
						fmt.Sprintf("registry named %s already exists in namespace %s",
							registry.Name, registry.Namespace)),
				})
		}
	}
	return
}

// ValidateUpdate validates the object on update. The optional warnings will be added to the response as warning messages. Return an error if the object is invalid.
func (v *modelRegistryValidator) ValidateUpdate(_ context.Context, _, newObj runtime.Object) (warnings admission.Warnings, err error) {
	r, err := v.toModelRegistry(newObj)
	if err != nil {
		return
	}
	modelregistrylog.Info("validate update", "name", r.Name)

	return r.ValidateRegistry()
}

// ValidateDelete validates the object on deletion. The optional warnings will be added to the response as warning messages. Return an error if the object is invalid.
func (v *modelRegistryValidator) ValidateDelete(_ context.Context, obj runtime.Object) (warnings admission.Warnings, err error) {
	r, err := v.toModelRegistry(obj)
	if err != nil {
		return
	}
	modelregistrylog.Info("validate delete", "name", r.Name)

	// TODO(user): fill in your validation logic upon object deletion.
	return nil, nil
}
