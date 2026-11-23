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

package controller

import (
	corev1 "k8s.io/api/core/v1"

	"github.com/opendatahub-io/model-registry-operator/internal/controller/config"
)

// operandImageEnvNames are the operand image env vars AIHub forwards to both child
// operator Deployments. The optional combined catalog image overrides the two data
// images and their sources; other values are forwarded under the same name.
var operandImageEnvNames = []string{
	config.RestImage,
	config.PostgresImage,
	config.KubeRBACProxyImage,
	config.CatalogDataImage,
	config.BenchmarkDataImage,
	config.CatalogDataImageStreamSource,
	config.BenchmarkDataImageStreamSource,
}

// ChildImages holds the images AIHub projects onto its child operator Deployments.
type ChildImages struct {
	// OperatorImage is the shared operator image stamped onto each child operator
	// Deployment's container image and self-image environment variable. Empty
	// uses the rendered manifest's container image for both.
	OperatorImage string

	// OperandEnv are the env vars stamped on both child operator Deployments.
	// Env vars whose source value is unset/empty are omitted so the child's own
	// default remains untouched.
	OperandEnv []corev1.EnvVar

	// AsyncUploadImage is the platform-pinned image for the async-upload
	// OpenShift Template's JOB_IMAGE parameter. It is NOT a container env
	// var — it targets the Template directly. Empty means "leave the
	// template's floating default untouched".
	AsyncUploadImage string
}

// ResolveChildImages reads AIHub's own environment via getenv and projects the images
// and env vars to forward to the child operators. getenv is injected for testability;
// pass os.Getenv in production. The combined catalog image is supplied through the
// platform's RelatedImages injection. Without it, the independent data image and
// source settings are preserved, including ODH's Quay defaults.
func ResolveChildImages(getenv func(string) string) ChildImages {
	result := ChildImages{
		OperatorImage:    getenv(config.ModelRegistryOperatorImage),
		AsyncUploadImage: getenv(config.AsyncUploadImage),
	}
	catalogImage := getenv(config.AIHubCatalogImage)
	for _, name := range operandImageEnvNames {
		value := getenv(name)
		if catalogImage != "" {
			switch name {
			case config.CatalogDataImage, config.BenchmarkDataImage,
				config.CatalogDataImageStreamSource, config.BenchmarkDataImageStreamSource:
				value = catalogImage
			}
		}
		if value != "" {
			result.OperandEnv = append(result.OperandEnv, corev1.EnvVar{Name: name, Value: value})
		}
	}
	return result
}
