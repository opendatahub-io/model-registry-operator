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

	"github.com/opendatahub-io/model-registry-operator/internal/controller/config"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// ValidateName ensures the Catalog CR name is "catalog"
func (r *Catalog) ValidateName(ctx context.Context) (admission.Warnings, error) {
	errList := field.ErrorList{}

	if r.Name != "catalog" {
		errList = append(errList, field.Invalid(
			field.NewPath("metadata").Child("name"),
			r.Name,
			"catalog resource name must be 'catalog'",
		))
	}

	errList = append(errList, r.ValidateNamespace()...)

	if len(errList) > 0 {
		return nil, errors.NewInvalid(r.GroupVersionKind().GroupKind(), r.Name, errList)
	}
	return nil, nil
}

// ValidateNamespace ensures the Catalog CR is created in the configured registries
// namespace. The reconciler's caches (e.g. for ConfigMaps) are scoped to that
// namespace, so a Catalog CR created elsewhere would never be correctly reconciled.
func (r *Catalog) ValidateNamespace() field.ErrorList {
	registriesNamespace := config.GetRegistriesNamespace()
	namespace := r.Namespace
	if len(registriesNamespace) != 0 && namespace != registriesNamespace {
		return field.ErrorList{
			field.Invalid(field.NewPath("metadata").Child("namespace"), namespace, "namespace must be "+registriesNamespace),
		}
	}
	return nil
}
