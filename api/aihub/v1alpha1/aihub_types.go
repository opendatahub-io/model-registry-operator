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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Phase represents the top-level lifecycle phase of a module.
// +kubebuilder:validation:Enum=Ready;"Not Ready";""
type Phase string

const (
	PhaseReady    Phase = "Ready"
	PhaseNotReady Phase = "Not Ready"
)

// ComponentRelease tracks the version of a component installed by the module.
type ComponentRelease struct {
	// Name identifies the component (e.g. "platform" for the version handshake).
	Name string `json:"name"`

	// RepoURL is the source repository for the component.
	// +optional
	RepoURL string `json:"repoUrl,omitempty"`

	// Version is the installed version string.
	// +optional
	Version string `json:"version,omitempty"`
}

// AIHubSpec defines the desired state of AIHub.
type AIHubSpec struct {
	// ApplicationNamespace is the namespace where model registries and the catalog are deployed.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`
	ApplicationNamespace string `json:"applicationNamespace"`
}

// AIHubStatus defines the observed state of AIHub.
type AIHubStatus struct {
	// Phase is the top-level lifecycle phase (Ready, Not Ready).
	// +optional
	Phase Phase `json:"phase,omitempty"`

	// ObservedGeneration is the most recent generation observed by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions represent the latest available observations of the AIHub's state.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// Releases tracks installed component versions. The entry with name "platform"
	// participates in the platform version handshake.
	// +optional
	Releases []ComponentRelease `json:"releases,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:resource:scope=Cluster
//+kubebuilder:subresource:status
//+kubebuilder:printcolumn:name="AppNamespace",type=string,JSONPath=`.spec.applicationNamespace`
//+kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
//+kubebuilder:validation:XValidation:rule="self.metadata.name == 'default'",message="Only the name 'default' is allowed"

// AIHub is the module CR that the platform creates to manage the AI Hub.
type AIHub struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   AIHubSpec   `json:"spec,omitempty"`
	Status AIHubStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// AIHubList contains a list of AIHub.
type AIHubList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AIHub `json:"items"`
}
