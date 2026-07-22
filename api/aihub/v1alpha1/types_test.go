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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/opendatahub-io/model-registry-operator/api/aihub/v1alpha1"
)

func TestGroupVersion(t *testing.T) {
	if v1alpha1.GroupVersion.Group != "components.platform.opendatahub.io" {
		t.Errorf("expected group components.platform.opendatahub.io, got %s", v1alpha1.GroupVersion.Group)
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
		&v1alpha1.AIHub{},
		&v1alpha1.AIHubList{},
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

func TestAIHubFields(t *testing.T) {
	ah := &v1alpha1.AIHub{
		ObjectMeta: metav1.ObjectMeta{
			Name: "default",
		},
		Spec: v1alpha1.AIHubSpec{
			ApplicationNamespace: "rhoai-model-registries",
		},
		Status: v1alpha1.AIHubStatus{
			Phase:              v1alpha1.PhaseReady,
			ObservedGeneration: 1,
			Conditions: []metav1.Condition{
				{Type: "Ready", Status: "True"},
			},
			Releases: []v1alpha1.ComponentRelease{
				{Name: "platform", Version: "2.20.0"},
				{Name: "model-registry-operator", RepoURL: "https://github.com/opendatahub-io/model-registry-operator", Version: "1.0.0"},
			},
		},
	}

	if ah.Spec.ApplicationNamespace != "rhoai-model-registries" {
		t.Errorf("unexpected spec.applicationNamespace: %s", ah.Spec.ApplicationNamespace)
	}
	if ah.Status.Phase != v1alpha1.PhaseReady {
		t.Errorf("unexpected phase: %s", ah.Status.Phase)
	}
	if ah.Status.ObservedGeneration != 1 {
		t.Errorf("unexpected observedGeneration: %d", ah.Status.ObservedGeneration)
	}
	if len(ah.Status.Conditions) != 1 {
		t.Errorf("expected 1 condition, got %d", len(ah.Status.Conditions))
	}
	if len(ah.Status.Releases) != 2 {
		t.Errorf("expected 2 releases, got %d", len(ah.Status.Releases))
	}
	if ah.Status.Releases[0].Name != "platform" || ah.Status.Releases[0].Version != "2.20.0" {
		t.Errorf("unexpected platform release: %+v", ah.Status.Releases[0])
	}
}

func TestAIHubDeepCopy(t *testing.T) {
	orig := &v1alpha1.AIHub{
		Spec: v1alpha1.AIHubSpec{
			ApplicationNamespace: "original-ns",
		},
		Status: v1alpha1.AIHubStatus{
			Phase:      v1alpha1.PhaseReady,
			Conditions: []metav1.Condition{{Type: "Ready", Status: "True"}},
			Releases:   []v1alpha1.ComponentRelease{{Name: "platform", Version: "1.0.0"}},
		},
	}

	cp := orig.DeepCopy()
	cp.Spec.ApplicationNamespace = "mutated-ns"
	cp.Status.Conditions[0].Status = "False"
	cp.Status.Releases[0].Version = "2.0.0"
	cp.Status.Phase = v1alpha1.PhaseNotReady

	if orig.Spec.ApplicationNamespace != "original-ns" {
		t.Error("DeepCopy did not produce independent spec")
	}
	if orig.Status.Conditions[0].Status != "True" {
		t.Error("DeepCopy did not produce independent status conditions")
	}
	if orig.Status.Releases[0].Version != "1.0.0" {
		t.Error("DeepCopy did not produce independent releases")
	}
	if orig.Status.Phase != v1alpha1.PhaseReady {
		t.Error("DeepCopy did not produce independent phase")
	}
}
