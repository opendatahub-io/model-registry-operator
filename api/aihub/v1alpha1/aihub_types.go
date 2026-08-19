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

	"github.com/opendatahub-io/odh-platform-utilities/api/common"
)

// Compile-time check: AIHub must implement common.PlatformObject so the
// orchestrator (ODH Operator) can read status, conditions, and releases
// through a uniform interface across all modules.
var _ common.PlatformObject = &AIHub{}

// GatewaySpec carries Data Science Gateway settings projected by the platform.
type GatewaySpec struct {
	// Domain is the Data Science Gateway wildcard domain used to build
	// per-instance HTTPRoute hostnames.
	// +optional
	Domain string `json:"domain,omitempty"`
}

// AIHubSpec defines the desired state of AIHub.
type AIHubSpec struct {
	// ApplicationNamespace is the namespace where the child operators (model registry
	// and catalog operators) run.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`
	ApplicationNamespace string `json:"applicationNamespace"`

	// InstancesNamespace is the namespace where model registry instances, the catalog
	// service, and the Catalog CR are deployed. It is sourced from the DSC field
	// spec.components.modelregistry.registriesNamespace and MAY equal ApplicationNamespace.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`
	InstancesNamespace string `json:"instancesNamespace"`

	// Gateway carries Data Science Gateway settings projected by the platform.
	// Optional: absent until the gateway domain is available.
	// +optional
	Gateway *GatewaySpec `json:"gateway,omitempty"`
}

// AIHubStatus defines the observed state of AIHub.
type AIHubStatus struct {
	common.Status                 `json:",inline"`
	common.ComponentReleaseStatus `json:",inline"`
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

	// +kubebuilder:validation:Required
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

// PlatformObject accessor methods.
// The orchestrator uses these to read/write module status generically:
//   - GetStatus: Phase, Conditions, ObservedGeneration
//   - Get/SetConditions: Ready, ProvisioningSucceeded, Degraded
//   - Get/SetReleaseStatus: deployed component versions (model-registry-operator, model-registry)

func (a *AIHub) GetStatus() *common.Status {
	return &a.Status.Status
}

func (a *AIHub) GetConditions() []common.Condition {
	return a.Status.Conditions
}

func (a *AIHub) SetConditions(conditions []common.Condition) {
	a.Status.Conditions = conditions
}

func (a *AIHub) GetReleaseStatus() *common.ComponentReleaseStatus {
	return &a.Status.ComponentReleaseStatus
}

func (a *AIHub) SetReleaseStatus(status common.ComponentReleaseStatus) {
	a.Status.ComponentReleaseStatus = status
}
