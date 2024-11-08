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
	"github.com/opendatahub-io/model-registry-operator/internal/controller/config"
)

const (
	resetSpecDefaultsAnnotation = "modelregistry.opendatahub.io/reset-spec-defaults"
)

type annHandlerFunc func(*ModelRegistry)

var annotationHandlers = map[string]annHandlerFunc{
	resetSpecDefaultsAnnotation: HandleResetDefaults,
}

// HandleAnnotations calls annotation handlers
func (r *ModelRegistry) HandleAnnotations() {
	annotations := r.GetAnnotations()
	if len(annotations) > 0 {
		for ann, handlerFunc := range annotationHandlers {
			_, found := annotations[ann]
			if found {
				modelregistrylog.Info("handle annotation", "name", r.Name, "annotation", ann)
				handlerFunc(r)
			}
		}
	}
}

// HandleResetDefaults resets operator default properties
func HandleResetDefaults(r *ModelRegistry) {
	// remove runtime default properties
	const emptyValue = ""
	r.Spec.Grpc.Image = emptyValue
	r.Spec.Grpc.Resources = nil
	r.Spec.Rest.Image = emptyValue
	r.Spec.Rest.Resources = nil

	// reset istio defaults
	if r.Spec.Istio != nil {
		r.Spec.Istio.Audiences = make([]string, 0)
		r.Spec.Istio.AuthProvider = emptyValue
		r.Spec.Istio.AuthConfigLabels = make(map[string]string)

		if r.Spec.Istio.Gateway != nil {
			r.Spec.Istio.Gateway.Domain = emptyValue

			// reset default cert, if set
			defaultCert := config.GetDefaultCert()
			if r.Spec.Istio.Gateway.Rest.TLS != nil && r.Spec.Istio.Gateway.Rest.TLS.Mode != DefaultTlsMode &&
				r.Spec.Istio.Gateway.Rest.TLS.CredentialName != nil && *r.Spec.Istio.Gateway.Rest.TLS.CredentialName == defaultCert {
				r.Spec.Istio.Gateway.Rest.TLS.CredentialName = nil
			}
			if r.Spec.Istio.Gateway.Grpc.TLS != nil && r.Spec.Istio.Gateway.Grpc.TLS.Mode != DefaultTlsMode &&
				r.Spec.Istio.Gateway.Grpc.TLS.CredentialName != nil && *r.Spec.Istio.Gateway.Grpc.TLS.CredentialName == defaultCert {
				r.Spec.Istio.Gateway.Grpc.TLS.CredentialName = nil
			}
		}
	}

	// remove annotation, since properties were reset
	annotations := r.GetAnnotations()
	delete(annotations, resetSpecDefaultsAnnotation)
	r.SetAnnotations(annotations)
}
