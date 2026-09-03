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

package config

import (
	"context"
	"embed"
	"encoding/base64"
	"fmt"
	"os"
	"regexp"
	"strings"
	"text/template"

	"github.com/opendatahub-io/model-registry-operator/internal/utils"
	"k8s.io/apimachinery/pkg/api/validation"

	configv1 "github.com/openshift/api/config/v1"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	klog "sigs.k8s.io/controller-runtime/pkg/log"
)

//go:embed templates/*.yaml.tmpl
//go:embed templates/kube-rbac-proxy/*.yaml.tmpl
//go:embed templates/catalog/*.yaml.tmpl
//go:embed templates/gateway/*.yaml.tmpl
var templateFS embed.FS

const (
	RestImage                  = "RELATED_IMAGE_ODH_MODEL_REGISTRY_IMAGE"
	OAuthProxyImage            = "OAUTH_PROXY_IMAGE"
	KubeRBACProxyImage         = "RELATED_IMAGE_ODH_KUBE_RBAC_PROXY_IMAGE"
	PostgresImage              = "RELATED_IMAGE_POSTGRESQL_16_IMAGE"
	CatalogDataImage           = "RELATED_IMAGE_ODH_MODEL_METADATA_COLLECTION_IMAGE"
	BenchmarkDataImage         = "RELATED_IMAGE_ODH_MODEL_PERFORMANCE_DATA_IMAGE"
	ModelRegistryOperatorImage = "RELATED_IMAGE_ODH_MODEL_REGISTRY_OPERATOR_IMAGE"
	AsyncUploadImage           = "RELATED_IMAGE_ODH_MODEL_REGISTRY_JOB_ASYNC_UPLOAD_IMAGE"
	DefaultAsyncUploadImage    = "quay.io/opendatahub/model-registry-job-async-upload:latest"
	DefaultRestImage           = "quay.io/opendatahub/model-registry:latest"
	DefaultOAuthProxyImage     = "quay.io/openshift/origin-oauth-proxy:latest"
	DefaultKubeRBACProxyImage  = "quay.io/openshift/origin-kube-rbac-proxy:latest"
	DefaultPostgresImage       = "quay.io/sclorg/postgresql-16-c10s:latest"
	DefaultCatalogDataImage    = "quay.io/opendatahub/odh-model-metadata-collection:latest"
	DefaultBenchmarkDataImage  = "quay.io/opendatahub/odh-model-metadata-collection:latest"
	RouteDisabled              = "disabled"
	RouteEnabled               = "enabled"
	DefaultIstioIngressName    = "ingressgateway"

	// config env variables
	RegistriesNamespace        = "REGISTRIES_NAMESPACE"
	EnableWebhooks             = "ENABLE_WEBHOOKS"
	DefaultDomain              = "DEFAULT_DOMAIN"
	SkipModelCatalogDBCreation = "SKIP_MODEL_CATALOG_DB_CREATION"

	// Data Science Gateway env variables and defaults
	GatewayDomainEnv          = "GATEWAY_DOMAIN"
	GatewayNameEnv            = "GATEWAY_NAME"
	GatewayNamespaceEnv       = "GATEWAY_NAMESPACE"
	HTTPRouteNamespaceEnv     = "HTTPROUTE_NAMESPACE"
	DefaultGatewayName        = "data-science-gateway"
	DefaultGatewayNamespace   = "openshift-ingress"
	DefaultHTTPRouteNamespace = "redhat-ods-applications"

	// PostgreSQL config env variables
	CatalogPostgresUser     = "CATALOG_POSTGRES_USER"
	CatalogPostgresDatabase = "CATALOG_POSTGRES_DATABASE"

	// Default PostgreSQL values
	DefaultCatalogPostgresUser     = "catalog_user"
	DefaultCatalogPostgresDatabase = "model_catalog"
	// Note: PostgreSQL password is generated securely using utils.RandBytes(16)
	// in createOrUpdatePostgresSecret() - no hardcoded default password is used
)

var (
	defaultDomain              = ""
	defaultRegistriesNamespace = ""

	// Default ResourceRequirements
	CatalogServiceResourceRequirements    = createResourceRequirement(resource.MustParse("100m"), resource.MustParse("256Mi"), resource.MustParse("0m"), resource.MustParse("256Mi"))
	ModelRegistryRestResourceRequirements = createResourceRequirement(resource.MustParse("100m"), resource.MustParse("256Mi"), resource.MustParse("0m"), resource.MustParse("256Mi"))
)

func createResourceRequirement(RequestsCPU resource.Quantity, RequestsMemory resource.Quantity, LimitsCPU resource.Quantity, LimitsMemory resource.Quantity) v1.ResourceRequirements {
	requests := v1.ResourceList{}
	if !RequestsCPU.IsZero() {
		requests["cpu"] = RequestsCPU
	}
	if !RequestsMemory.IsZero() {
		requests["memory"] = RequestsMemory
	}

	limits := v1.ResourceList{}
	if !LimitsCPU.IsZero() {
		limits["cpu"] = LimitsCPU
	}
	if !LimitsMemory.IsZero() {
		limits["memory"] = LimitsMemory
	}

	return v1.ResourceRequirements{
		Requests: requests,
		Limits:   limits,
	}
}

var sha256DigestRe = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

// imageTagRe matches a valid Docker image tag: an alphanumeric or underscore,
// followed by up to 127 chars of alphanumerics, underscores, periods, or dashes.
var imageTagRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)

// repoBase strips any tag or digest from an image reference, returning the
// "registry/repo" portion. It only treats a ':' after the last '/' as a tag
// separator so that "registry:port/repo" is handled correctly.
func repoBase(image string) string {
	if i := strings.IndexByte(image, '@'); i != -1 {
		image = image[:i]
	}
	if slash := strings.LastIndexByte(image, '/'); slash != -1 {
		if colon := strings.IndexByte(image[slash:], ':'); colon != -1 {
			image = image[:slash+colon]
		}
	}
	return image
}

// ResolveImage returns the image to use for an init container. When override is
// a well-formed sha256 digest it is pinned onto the trusted repository derived
// from the resolved default image (repoBase(default)@digest). When override is a
// well-formed image tag it is applied to the same trusted repository
// (repoBase(default):tag). Any invalid, empty, or nil override falls back to the
// full default image. The registry/repository is always operator-controlled.
func ResolveImage(override *string, envName, envDefault string) string {
	def := GetStringConfigWithDefault(envName, envDefault)
	if override != nil {
		v := strings.TrimSpace(*override)
		switch {
		case sha256DigestRe.MatchString(v):
			return repoBase(def) + "@" + v
		case imageTagRe.MatchString(v):
			return repoBase(def) + ":" + v
		}
	}
	return def
}

func GetStringConfigWithDefault(configName, value string) string {
	if v := os.Getenv(configName); v != "" {
		return v
	}
	return value
}

func GetBoolConfigWithDefault(configName string, defaultValue bool) bool {
	if v := os.Getenv(configName); v != "" {
		return v == "true"
	}
	return defaultValue
}

func ParseTemplates() (*template.Template, error) {
	tmpl := (&template.Template{}).Funcs(template.FuncMap{
		"b64enc":           b64enc,
		"quantityToString": utils.QuantityToString,
		"randBytes": func(n int) string {
			// Template function wrapper - panics on error as per template convention
			result, err := utils.RandBytes(n)
			if err != nil {
				panic(err)
			}
			return result
		},
	})
	tmpl, err := tmpl.ParseFS(templateFS,
		"templates/*.yaml.tmpl",
		"templates/kube-rbac-proxy/*.yaml.tmpl",
		"templates/catalog/*.yaml.tmpl",
		"templates/gateway/*.yaml.tmpl",
	)
	if err != nil {
		return nil, err
	}
	return tmpl, err
}

func b64enc(str string) string {
	return base64.StdEncoding.EncodeToString([]byte(str))
}

var (
	defaultClient      client.Client
	defaultIsOpenShift = false
)

func SetRegistriesNamespace(namespace string) error {
	namespace = strings.TrimSpace(namespace)
	if len(namespace) != 0 {
		errs := validation.ValidateNamespaceName(namespace, false)
		if len(errs) > 0 {
			return fmt.Errorf("invalid registries namespace %s: %v", namespace, errs)
		}
	}
	defaultRegistriesNamespace = namespace
	return nil
}

func GetRegistriesNamespace() string {
	return defaultRegistriesNamespace
}

func IsOpenShift() bool {
	return defaultIsOpenShift
}

func SetDefaultDomain(domain string, client client.Client, isOpenShift bool) {
	defaultDomain = domain
	defaultClient = client
	defaultIsOpenShift = isOpenShift
}

func GetDefaultDomain() string {
	if len(defaultDomain) == 0 && defaultIsOpenShift {
		ingress := configv1.Ingress{}
		namespacedName := types.NamespacedName{Name: "cluster"}
		err := defaultClient.Get(context.Background(), namespacedName, &ingress)
		if err != nil {
			klog.Log.Error(err, "error getting OpenShift domain name", fmt.Sprintf("%+v", ingress.GetObjectKind()), namespacedName)
			return ""
		}
		// try reading appsDomain if it is set
		if ingress.Spec.AppsDomain != "" {
			defaultDomain = ingress.Spec.AppsDomain
		} else {
			defaultDomain = ingress.Spec.Domain
		}
	}
	return defaultDomain
}

// ClusterProxy holds the effective cluster-wide proxy settings read from the
// OpenShift cluster Proxy object.
type ClusterProxy struct {
	HTTPProxy  string
	HTTPSProxy string
	NoProxy    string
}

// GetClusterProxy reads the OpenShift cluster Proxy object named "cluster" and
// returns its effective proxy settings (status, falling back to spec for any
// field left blank in status). It returns nil when not running on OpenShift,
// when the Proxy object doesn't exist, or when no proxy value is configured.
// Unlike GetDefaultDomain, the result is not cached: it is read fresh on every
// call, so it reflects the latest cluster Proxy state as of the next Catalog
// reconcile. There is no watch on the cluster Proxy object, so a change to it
// does not itself trigger a reconcile; it takes effect whenever the next
// reconcile happens to run for some other reason.
func GetClusterProxy() *ClusterProxy {
	if !defaultIsOpenShift {
		return nil
	}
	proxy := configv1.Proxy{}
	namespacedName := types.NamespacedName{Name: "cluster"}
	err := defaultClient.Get(context.Background(), namespacedName, &proxy)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			klog.Log.Error(err, "error getting OpenShift cluster proxy", "name", namespacedName)
		}
		return nil
	}
	cp := ClusterProxy{
		HTTPProxy:  firstNonEmpty(proxy.Status.HTTPProxy, proxy.Spec.HTTPProxy),
		HTTPSProxy: firstNonEmpty(proxy.Status.HTTPSProxy, proxy.Spec.HTTPSProxy),
		NoProxy:    firstNonEmpty(proxy.Status.NoProxy, proxy.Spec.NoProxy),
	}
	if cp.HTTPProxy == "" && cp.HTTPSProxy == "" && cp.NoProxy == "" {
		return nil
	}
	return &cp
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
