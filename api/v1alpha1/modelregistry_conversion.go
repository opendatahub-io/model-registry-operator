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
	"fmt"
	"log"

	"k8s.io/apimachinery/pkg/util/json"

	"sigs.k8s.io/controller-runtime/pkg/conversion"

	"github.com/opendatahub-io/model-registry-operator/api/v1alpha2"
)

// ConvertTo converts this ModelRegistry (v1alpha1) to the Hub version (v1alpha2).
func (src *ModelRegistry) ConvertTo(dstRaw conversion.Hub) error {
	dst := dstRaw.(*v1alpha2.ModelRegistry)
	log.Printf("ConvertTo: Converting ModelRegistry from Spoke version v1alpha1 to Hub version v1alpha2;"+
		"source: %s/%s, target: %s/%s", src.Namespace, src.Name, dst.Namespace, dst.Name)

	// Copy metadata
	dst.ObjectMeta = src.ObjectMeta

	// Copy src spec to a map[string]interface{}
	srcSpec, err := json.Marshal(src.Spec)
	if err != nil {
		return fmt.Errorf("failed to marshal src spec v1alpha1: %w", err)
	}

	// read back srcSpec to dst.Spec, no other conversion is needed as unknown fields are discarded
	err = json.Unmarshal(srcSpec, &dst.Spec)
	if err != nil {
		return fmt.Errorf("failed to unmarshal dst spec v1alpha2: %w", err)
	}

	return nil
}

// ConvertFrom converts the Hub version (v1alpha2) to this ModelRegistry (v1alpha1).
func (dst *ModelRegistry) ConvertFrom(srcRaw conversion.Hub) error {
	src := srcRaw.(*v1alpha2.ModelRegistry)
	log.Printf("ConvertFrom: Converting ModelRegistry from Hub version v1alpha2 to Spoke version v1alpha1;"+
		"source: %s/%s, target: %s/%s", src.Namespace, src.Name, dst.Namespace, dst.Name)

	// Copy metadata
	dst.ObjectMeta = src.ObjectMeta

	// convert src spec to map[string]interface{}
	srcSpec, err := json.Marshal(src.Spec)
	if err != nil {
		return fmt.Errorf("failed to marshal src spec v1alpha2: %w", err)
	}

	// read back srcSpec to dst.Spec, no other conversion is needed as unknown fields are discarded
	err = json.Unmarshal(srcSpec, &dst.Spec)
	if err != nil {
		return fmt.Errorf("failed to unmarshal dst spec v1alpha1: %w", err)
	}

	return nil
}
