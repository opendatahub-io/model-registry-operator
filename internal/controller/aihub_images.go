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

// operandImageEnvNames are the operand image env vars AIHub forwards, identity-mapped,
// to both child operator Deployments. Each name is identical on the AIHub container and
// on the child operator container, so forwarding is a straight passthrough.
var operandImageEnvNames = []string{
	config.RestImage,
	config.PostgresImage,
	config.KubeRBACProxyImage,
	config.CatalogDataImage,
	config.BenchmarkDataImage,
}

// ChildImages holds the images AIHub projects onto its child operator Deployments.
type ChildImages struct {
	// OperatorImage is the shared operator image stamped onto each child operator
	// Deployment's container image field. Empty means "leave the manifest default".
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
// pass os.Getenv in production. Operand images are an identity passthrough; the shared
// operator image is the only special case (it targets the child container image field).
func ResolveChildImages(getenv func(string) string) ChildImages {
	result := ChildImages{
		OperatorImage:    getenv(config.ModelRegistryOperatorImage),
		AsyncUploadImage: getenv(config.AsyncUploadImage),
	}
	for _, name := range operandImageEnvNames {
		if v := getenv(name); v != "" {
			result.OperandEnv = append(result.OperandEnv, corev1.EnvVar{Name: name, Value: v})
		}
	}
	return result
}
