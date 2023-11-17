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
	"embed"
	"github.com/spf13/viper"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"text/template"
)

//go:embed templates/*.yaml.tmpl
var templateFS embed.FS

const (
	GrpcImage        = "GRPC_IMAGE"
	RestImage        = "REST_IMAGE"
	DefaultGrpcImage = "gcr.io/tfx-oss-public/ml_metadata_store_server:1.14.0"
	DefaultRestImage = "quay.io/opendatahub/model-registry:latest"
	RouteDisabled    = "disabled"
	RouteEnabled     = "enabled"
)

// Default ResourceRequirements
var (
	MlmdRestResourceRequirements = createResourceRequirement(resource.MustParse("100m"), resource.MustParse("256Mi"), resource.MustParse("100m"), resource.MustParse("256Mi"))
	MlmdGRPCResourceRequirements = createResourceRequirement(resource.MustParse("100m"), resource.MustParse("256Mi"), resource.MustParse("100m"), resource.MustParse("256Mi"))
)

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
	template, err := template.ParseFS(templateFS, "templates/*.yaml.tmpl")
	if err != nil {
		return nil, err
	}
	return template, err
}
