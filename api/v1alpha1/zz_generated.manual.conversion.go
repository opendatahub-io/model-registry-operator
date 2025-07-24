package v1alpha1

import (
	"github.com/opendatahub-io/model-registry-operator/api/v1beta1"
	"k8s.io/apimachinery/pkg/conversion"
)

func Convert_v1beta1_ModelRegistrySpec_To_v1alpha1_ModelRegistrySpec(in *v1beta1.ModelRegistrySpec, out *ModelRegistrySpec, s conversion.Scope) error {
	// Manually convert all fields from in to out.
	if err := autoConvert_v1beta1_ModelRegistrySpec_To_v1alpha1_ModelRegistrySpec(in, out, s); err != nil {
		return err
	}
	// The Database field does not exist in v1alpha1, so it is ignored.
	return nil
}
