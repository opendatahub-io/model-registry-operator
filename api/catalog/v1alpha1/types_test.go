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

package v1alpha1_test

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/opendatahub-io/model-registry-operator/api/catalog/v1alpha1"
)

func TestGroupVersion(t *testing.T) {
	if v1alpha1.GroupVersion.Group != "aihub.opendatahub.io" {
		t.Errorf("expected group aihub.opendatahub.io, got %s", v1alpha1.GroupVersion.Group)
	}
	if v1alpha1.GroupVersion.Version != "v1alpha1" {
		t.Errorf("expected version v1alpha1, got %s", v1alpha1.GroupVersion.Version)
	}
}

func TestAddToScheme(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme failed: %v", err)
	}

	for _, obj := range []runtime.Object{
		&v1alpha1.Catalog{},
		&v1alpha1.CatalogList{},
	} {
		gvks, _, err := scheme.ObjectKinds(obj)
		if err != nil {
			t.Errorf("type %T not registered: %v", obj, err)
			continue
		}
		if len(gvks) == 0 {
			t.Errorf("no GVKs for %T", obj)
		}
	}
}

func TestCatalogFields(t *testing.T) {
	cpu := resource.MustParse("100m")
	mem := resource.MustParse("256Mi")
	size := resource.MustParse("10Gi")
	sc := "standard"

	cat := &v1alpha1.Catalog{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "catalog",
			Namespace: "rhoai-model-registries",
		},
		Spec: v1alpha1.CatalogSpec{
			Resources: v1alpha1.CatalogResources{
				Catalog: &corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    cpu,
						corev1.ResourceMemory: mem,
					},
				},
				Postgres: &corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    cpu,
						corev1.ResourceMemory: mem,
					},
				},
			},
			Database: v1alpha1.CatalogDatabase{
				Volume: v1alpha1.CatalogDatabaseVolume{
					SizeLimit:        &size,
					StorageClassName: &sc,
				},
			},
		},
	}

	if cat.Spec.Resources.Catalog == nil {
		t.Error("catalog resources should not be nil")
	}
	if cat.Spec.Resources.Postgres == nil {
		t.Error("postgres resources should not be nil")
	}
	if cat.Spec.Database.Volume.SizeLimit == nil {
		t.Error("sizeLimit should not be nil")
	} else if cat.Spec.Database.Volume.SizeLimit.Cmp(size) != 0 {
		t.Errorf("unexpected sizeLimit: %v", cat.Spec.Database.Volume.SizeLimit)
	}
	if cat.Spec.Database.Volume.StorageClassName == nil || *cat.Spec.Database.Volume.StorageClassName != "standard" {
		t.Errorf("unexpected storageClassName: %v", cat.Spec.Database.Volume.StorageClassName)
	}
}

func TestCatalogDeepCopy(t *testing.T) {
	size := resource.MustParse("10Gi")
	sc := "standard"
	orig := &v1alpha1.Catalog{
		Spec: v1alpha1.CatalogSpec{
			Database: v1alpha1.CatalogDatabase{
				Volume: v1alpha1.CatalogDatabaseVolume{
					SizeLimit:        &size,
					StorageClassName: &sc,
				},
			},
		},
	}

	cp := orig.DeepCopy()
	newSize := resource.MustParse("20Gi")
	cp.Spec.Database.Volume.SizeLimit = &newSize
	newSC := "fast"
	cp.Spec.Database.Volume.StorageClassName = &newSC

	if orig.Spec.Database.Volume.SizeLimit.Cmp(size) != 0 {
		t.Error("DeepCopy did not produce independent SizeLimit")
	}
	if *orig.Spec.Database.Volume.StorageClassName != "standard" {
		t.Error("DeepCopy did not produce independent StorageClassName")
	}
}
