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

package webhook

import (
	"context"
	"fmt"
	"net/http"

	"github.com/go-logr/logr"
	catalogv1alpha1 "github.com/opendatahub-io/model-registry-operator/api/catalog/v1alpha1"
	admissionv1 "k8s.io/api/admission/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// SetupCatalogWebhookWithManager registers the catalog validating webhook with the manager.
func SetupCatalogWebhookWithManager(mgr ctrl.Manager) error {
	mgr.GetWebhookServer().Register("/validate-aihub-opendatahub-io-catalog", &admission.Webhook{
		Handler: &catalogValidator{
			Decoder: admission.NewDecoder(mgr.GetScheme()),
		},
	})
	return nil
}

type catalogValidator struct {
	Decoder admission.Decoder
}

var _ admission.Handler = &catalogValidator{}

func (v *catalogValidator) Handle(ctx context.Context, request admission.Request) admission.Response {
	if request.Kind.Group != catalogv1alpha1.GroupVersion.Group || request.Kind.Kind != "Catalog" {
		return admission.Errored(http.StatusBadRequest, fmt.Errorf("unsupported resource %s in namespace %s", request.Name, request.Namespace))
	}

	catalog := &catalogv1alpha1.Catalog{}
	if err := v.Decoder.Decode(request, catalog); err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}

	var warnings admission.Warnings
	var err error

	switch request.Operation {
	case admissionv1.Create:
		logr.FromContextOrDiscard(ctx).Info("validate create catalog", "name", catalog.Name, "namespace", catalog.Namespace)
		warnings, err = catalog.ValidateName(ctx)
	case admissionv1.Update, admissionv1.Delete, admissionv1.Connect:
		return admission.Allowed("")
	default:
		return admission.Errored(http.StatusBadRequest, fmt.Errorf("unknown operation %q", request.Operation))
	}

	if err != nil {
		return admission.Denied(err.Error()).WithWarnings(warnings...)
	}

	return admission.Allowed("").WithWarnings(warnings...)
}
