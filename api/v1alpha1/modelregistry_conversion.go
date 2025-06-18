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

	"github.com/opendatahub-io/model-registry-operator/internal/controller/config"
	apiconversion "k8s.io/apimachinery/pkg/conversion"
	"k8s.io/apimachinery/pkg/util/json"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/conversion"

	"github.com/opendatahub-io/model-registry-operator/api/v1beta1"
)

// ConvertTo converts this ModelRegistry (v1alpha1) to the Hub version (v1beta1).
func (src *ModelRegistry) ConvertTo(dstRaw conversion.Hub) error {
	dst, ok := dstRaw.(*v1beta1.ModelRegistry)
	if !ok {
		return fmt.Errorf("%T is not of type v1beta1.ModelRegistry", dstRaw)
	}
	log := ctrl.Log.WithName("conversion").WithValues("source", src.Namespace+"/"+src.Name)
	log.Info("converting ModelRegistry from spoke v1alpha1 to hub v1beta1")

	// Copy metadata
	dst.ObjectMeta = src.ObjectMeta

	// Replace istio config in v1alpha1 with oauth proxy config with defaults in v1beta1
	if src.Spec.Istio != nil {
		src.ReplaceIstioWithOAuthProxy()
	}

	// Copy status
	err := Convert_v1alpha1_ModelRegistryStatus_To_v1beta1_ModelRegistryStatus(&src.Status, &dst.Status, nil)
	if err != nil {
		return fmt.Errorf("failed to convert model registry v1alpha1 status to v1beta1: %w", err)
	}

	// Copy src spec to a map[string]interface{}
	srcSpec, err := json.Marshal(src.Spec)
	if err != nil {
		return fmt.Errorf("failed to marshal src spec v1alpha1: %w", err)
	}

	// read back srcSpec to dst.Spec, no other conversion is needed as unknown fields are discarded
	err = json.Unmarshal(srcSpec, &dst.Spec)
	if err != nil {
		return fmt.Errorf("failed to unmarshal dst spec v1beta1: %w", err)
	}

	return nil
}

// ConvertFrom converts the Hub version (v1beta1) to this ModelRegistry (v1alpha1).
func (dst *ModelRegistry) ConvertFrom(srcRaw conversion.Hub) error {
	src, ok := srcRaw.(*v1beta1.ModelRegistry)
	if !ok {
		return fmt.Errorf("%T is not of type v1beta1.ModelRegistry", srcRaw)
	}
	log := ctrl.Log.WithName("conversion").WithValues("source", src.Namespace+"/"+src.Name)
	log.Info("converting ModelRegistry from hub v1beta1 to spoke v1alpha1")

	// Copy metadata
	dst.ObjectMeta = src.ObjectMeta

	// Copy status
	err := Convert_v1beta1_ModelRegistryStatus_To_v1alpha1_ModelRegistryStatus(&src.Status, &dst.Status, nil)
	if err != nil {
		return fmt.Errorf("failed to convert model registry v1beta1 status to v1alpha1: %w", err)
	}

	// convert src spec to map[string]interface{}
	srcSpec, err := json.Marshal(src.Spec)
	if err != nil {
		return fmt.Errorf("failed to marshal src spec v1beta1: %w", err)
	}

	// read back srcSpec to dst.Spec, no other conversion is needed as unknown fields are discarded
	err = json.Unmarshal(srcSpec, &dst.Spec)
	if err != nil {
		return fmt.Errorf("failed to unmarshal dst spec v1alpha1: %w", err)
	}

	return nil
}

func Convert_v1alpha1_ModelRegistrySpec_To_v1beta1_ModelRegistrySpec(in *ModelRegistrySpec, out *v1beta1.ModelRegistrySpec, s apiconversion.Scope) error {
	if err := Convert_v1alpha1_RestSpec_To_v1beta1_RestSpec(&in.Rest, &out.Rest, s); err != nil {
		return err
	}
	if err := Convert_v1alpha1_GrpcSpec_To_v1beta1_GrpcSpec(&in.Grpc, &out.Grpc, s); err != nil {
		return err
	}
	if err := s.Convert(&in.Postgres, &out.Postgres); err != nil {
		return err
	}
	if err := s.Convert(&in.MySQL, &out.MySQL); err != nil {
		return err
	}
	if err := s.Convert(&in.EnableDatabaseUpgrade, &out.EnableDatabaseUpgrade); err != nil {
		return err
	}
	if err := s.Convert(&in.DowngradeDbSchemaVersion, &out.DowngradeDbSchemaVersion); err != nil {
		return err
	}
	if err := s.Convert(&in.OAuthProxy, &out.OAuthProxy); err != nil {
		return err
	}
	if in.Istio != nil && out.OAuthProxy == nil {
		// replace Istio configuration with OAuthProxy instead
		out.OAuthProxy = &v1beta1.OAuthProxyConfig{
			Port:         &httpsPort,
			ServiceRoute: config.RouteEnabled,
		}
	}
	return nil
}
