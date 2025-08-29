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
	"crypto/rand"
	"embed"
	"encoding/base64"
	"fmt"
	"strings"
	"text/template"

	"k8s.io/apimachinery/pkg/api/validation"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/spf13/viper"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	klog "sigs.k8s.io/controller-runtime/pkg/log"
)

//go:embed templates/*.yaml.tmpl
//go:embed templates/oauth-proxy/*.yaml.tmpl
//go:embed templates/catalog/*.yaml.tmpl
var templateFS embed.FS

const (
	GrpcImage               = "GRPC_IMAGE"
	RestImage               = "REST_IMAGE"
	OAuthProxyImage         = "OAUTH_PROXY_IMAGE"
	CatalogDataImage        = "CATALOG_DATA_IMAGE"
	DefaultGrpcImage        = "quay.io/opendatahub/mlmd-grpc-server:latest"
	DefaultRestImage        = "quay.io/opendatahub/model-registry:latest"
	DefaultOAuthProxyImage  = "quay.io/openshift/origin-oauth-proxy:latest"
	DefaultCatalogDataImage = "quay.io/opendatahub/odh-model-metadata-collection:latest"
	RouteDisabled           = "disabled"
	RouteEnabled            = "enabled"
	DefaultIstioIngressName = "ingressgateway"

	// config env variables
	RegistriesNamespace = "REGISTRIES_NAMESPACE"
	EnableWebhooks      = "ENABLE_WEBHOOKS"
	DefaultDomain       = "DEFAULT_DOMAIN"
	EnableModelCatalog  = "ENABLE_MODEL_CATALOG"
)

var (
	defaultDomain              = ""
	defaultRegistriesNamespace = ""

	// Default ResourceRequirements
	MlmdRestResourceRequirements = createResourceRequirement(resource.MustParse("100m"), resource.MustParse("256Mi"), resource.MustParse("0m"), resource.MustParse("256Mi"))
	MlmdGRPCResourceRequirements = createResourceRequirement(resource.MustParse("100m"), resource.MustParse("256Mi"), resource.MustParse("0m"), resource.MustParse("256Mi"))
)

func init() {
	// init viper for config env variables
	viper.AutomaticEnv()
}

func createResourceRequirement(RequestsCPU resource.Quantity, RequestsMemory resource.Quantity, LimitsCPU resource.Quantity, LimitsMemory resource.Quantity) v1.ResourceRequirements {
	return v1.ResourceRequirements{
		Requests: v1.ResourceList{
			"cpu":    RequestsCPU,
			"memory": RequestsMemory,
		},
		Limits: v1.ResourceList{
			"cpu":    LimitsCPU,
			"memory": LimitsMemory,
		},
	}
}

func GetStringConfigWithDefault(configName, value string) string {
	if !viper.IsSet(configName) || len(viper.GetString(configName)) == 0 {
		return value
	}
	return viper.GetString(configName)
}

func ParseTemplates() (*template.Template, error) {
	tmpl := (&template.Template{}).Funcs(template.FuncMap{
		"randBytes": randBytes,
	})
	tmpl, err := tmpl.ParseFS(templateFS,
		"templates/*.yaml.tmpl",
		"templates/oauth-proxy/*.yaml.tmpl",
		"templates/catalog/*.yaml.tmpl",
	)
	if err != nil {
		return nil, err
	}
	return tmpl, err
}

func randBytes(n int) string {
	buf := make([]byte, n)
	rand.Read(buf)
	return base64.StdEncoding.EncodeToString(buf)
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
		defaultDomain = ingress.Spec.Domain
	}
	return defaultDomain
}
