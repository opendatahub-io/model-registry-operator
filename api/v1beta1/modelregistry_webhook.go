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

package v1beta1

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/opendatahub-io/model-registry-operator/internal/controller/config"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"
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

	tagSeparator = ":"
	emptyValue   = ""
)

var (
	_ webhook.Defaulter = &ModelRegistry{}
)

// Default implements webhook.Defaulter so a webhook will be registered for the type
func (r *ModelRegistry) Default() {
	modelregistrylog.Info("default", "name", r.Name, "status.specDefaults", r.Status.SpecDefaults)

	if len(r.Spec.Rest.ServiceRoute) == 0 {
		r.Spec.Rest.ServiceRoute = config.RouteDisabled
	}

	// Fixes default database configs that get set for some reason in Kind cluster
	// But don't remove postgres config if auto-provisioning is enabled
	if r.Spec.Postgres != nil && len(r.Spec.Postgres.Host) == 0 && len(r.Spec.Postgres.HostAddress) == 0 {
		// Check if auto-provisioning is enabled before removing the config
		isAutoProvisioning := r.Spec.Postgres.GenerateDeployment != nil && *r.Spec.Postgres.GenerateDeployment
		if !isAutoProvisioning {
			r.Spec.Postgres = nil
		}
	}
	if r.Spec.MySQL != nil && len(r.Spec.MySQL.Host) == 0 {
		r.Spec.MySQL = nil
	}

	// migrate oauth proxy to kube-rbac-proxy if oauth proxy is configured
	r.MigrateOAuthProxyToKubeRBACProxy()

	// enable kube-rbac-proxy route by default
	if r.Spec.KubeRBACProxy != nil && len(r.Spec.KubeRBACProxy.ServiceRoute) == 0 {
		r.Spec.KubeRBACProxy.ServiceRoute = config.RouteEnabled
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

	// kube-rbac-proxy defaults
	if r.Spec.KubeRBACProxy != nil {
		// set default cert and key if not provided
		if r.Spec.KubeRBACProxy.TLSCertificateSecret == nil {
			secretName := r.Name + "-kube-rbac-proxy"
			r.Spec.KubeRBACProxy.TLSCertificateSecret = &SecretKeyValue{
				Name: secretName,
				Key:  "tls.crt",
			}
			r.Spec.KubeRBACProxy.TLSKeySecret = &SecretKeyValue{
				Name: secretName,
				Key:  "tls.key",
			}
		}
		if len(r.Spec.KubeRBACProxy.Domain) == 0 {
			r.Spec.KubeRBACProxy.Domain = config.GetDefaultDomain()
		}
		if len(r.Spec.KubeRBACProxy.Image) == 0 {
			r.Spec.KubeRBACProxy.Image = config.GetStringConfigWithDefault(config.KubeRBACProxyImage, config.DefaultKubeRBACProxyImage)
		}
	}
}

// ValidateRegistry validates registry spec
func (r *ModelRegistry) ValidateRegistry() (warnings admission.Warnings, err error) {
	// set runtime defaults before validation, just like the reconcile loop
	r.RuntimeDefaults()

	errList := r.ValidateNamespace()
	warnings, errList2 := r.ValidateDatabase()

	// combine warnings and errors
	errList = slices.Concat(errList, errList2)
	if len(errList) != 0 {
		err = errors.NewInvalid(r.GroupVersionKind().GroupKind(), r.Name, errList)
	}
	return
}

// MigrateOAuthProxyToKubeRBACProxy automatically migrates existing OAuth proxy configurations to kube-rbac-proxy.
// This provides a seamless upgrade path similar to the Istio to OAuth proxy migration.
func (r *ModelRegistry) MigrateOAuthProxyToKubeRBACProxy() {
	// Only perform migration if OAuthProxy is configured but KubeRBACProxy is not
	if r.Spec.OAuthProxy != nil && r.Spec.KubeRBACProxy == nil {
		modelregistrylog.Info("migrating OAuthProxy configuration to KubeRBACProxy", "name", r.Name, "namespace", r.Namespace)

		// Create KubeRBACProxy config by copying OAuthProxy settings
		r.Spec.KubeRBACProxy = &KubeRBACProxyConfig{
			Port:                 r.Spec.OAuthProxy.Port,
			TLSCertificateSecret: r.Spec.OAuthProxy.TLSCertificateSecret,
			TLSKeySecret:         r.Spec.OAuthProxy.TLSKeySecret,
			ServiceRoute:         r.Spec.OAuthProxy.ServiceRoute,
			Domain:               r.Spec.OAuthProxy.Domain,
			RoutePort:            r.Spec.OAuthProxy.RoutePort,
			// Image is not copied here because it is set to the operator default
		}

		// MUST remove old oauth proxy config
		r.Spec.OAuthProxy = nil

		modelregistrylog.Info("successfully migrated OAuthProxy to KubeRBACProxy", "name", r.Name, "namespace", r.Namespace)
	}
}

func (r *ModelRegistry) ValidateNamespace() field.ErrorList {
	// make sure this instance's namespace matches registries namespace, if set
	registriesNamespace := config.GetRegistriesNamespace()
	namespace := r.Namespace
	if len(registriesNamespace) != 0 && namespace != registriesNamespace {
		return field.ErrorList{
			field.Invalid(field.NewPath("metadata").Child("namespace"), namespace, "namespace must be "+registriesNamespace),
		}
	}
	return nil
}

// ValidateDatabase validates that at least one database config is present
func (r *ModelRegistry) ValidateDatabase() (admission.Warnings, field.ErrorList) {
	hasPostgres := r.Spec.Postgres != nil
	hasMySQL := r.Spec.MySQL != nil
	hasAutoProvisioning := hasPostgres && r.Spec.Postgres.GenerateDeployment != nil && *r.Spec.Postgres.GenerateDeployment

	if !hasPostgres && !hasMySQL {
		return nil, field.ErrorList{
			field.Required(field.NewPath("spec").Child("postgres"), "required one of `postgres` or `mysql` database"),
			field.Required(field.NewPath("spec").Child("mysql"), "required one of `postgres` or `mysql` database"),
		}
	}

	if hasAutoProvisioning {
		// When auto-provisioning is enabled, host/hostAddress should not be set (they will be auto-generated)
		if len(r.Spec.Postgres.Host) > 0 || len(r.Spec.Postgres.HostAddress) > 0 {
			return nil, field.ErrorList{
				field.Invalid(field.NewPath("spec").Child("postgres").Child("host"), r.Spec.Postgres.Host, "host should not be set when auto-provisioning is enabled"),
			}
		}
		// Note: database and username CAN be set when auto-provisioning - they specify the desired values
	}

	return nil, nil
}

func (r *ModelRegistry) GetName() string {
	return r.Name
}

func (r *ModelRegistry) ValidateName(ctx context.Context, client client.Client) (warnings admission.Warnings, err error) {
	registries := &ModelRegistryList{}
	err = client.List(ctx, registries)
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
