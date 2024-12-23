package v1alpha1_test

import (
	"reflect"
	"testing"

	"github.com/opendatahub-io/model-registry-operator/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestHandleAnnotations(t *testing.T) {
	tests := []struct {
		name            string
		annotations     map[string]string
		wantAnnotations map[string]string
		mrRestImage     string
		wantMrRestImage string
	}{
		{
			name: "no handler annotations",
			annotations: map[string]string{
				"modelregistry.opendatahub.io/other-annotation": "true",
			},
			wantAnnotations: map[string]string{
				"modelregistry.opendatahub.io/other-annotation": "true",
			},
			mrRestImage:     "test",
			wantMrRestImage: "test",
		},
		{
			name: "reset defaults",
			annotations: map[string]string{
				"modelregistry.opendatahub.io/reset-spec-defaults": "",
			},
			wantAnnotations: map[string]string{},
			mrRestImage:     "test",
			wantMrRestImage: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mr := &v1alpha1.ModelRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: tt.annotations,
				},
				Spec: v1alpha1.ModelRegistrySpec{
					Rest: v1alpha1.RestSpec{
						Image: tt.mrRestImage,
					},
				},
			}
			mr.HandleAnnotations()

			if !reflect.DeepEqual(mr.GetAnnotations(), tt.wantAnnotations) {
				t.Errorf("HandleAnnotations() = %v, want %v", mr.GetAnnotations(), tt.wantAnnotations)
			}

			if mr.Spec.Rest.Image != tt.wantMrRestImage {
				t.Errorf("HandleAnnotations() = %v, want %v", mr.Name, tt.wantMrRestImage)
			}
		})
	}
}
