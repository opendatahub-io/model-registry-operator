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
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// CatalogResources defines resource requirements for the catalog workloads.
type CatalogResources struct {
	// Catalog specifies resource requirements for the catalog container.
	// +optional
	Catalog *corev1.ResourceRequirements `json:"catalog,omitempty"`

	// Postgres specifies resource requirements for the PostgreSQL container.
	// +optional
	Postgres *corev1.ResourceRequirements `json:"postgres,omitempty"`
}

// CatalogDatabaseVolume defines the PVC configuration for the catalog database.
type CatalogDatabaseVolume struct {
	// SizeLimit is the storage size requested for the database PVC.
	// +optional
	SizeLimit *resource.Quantity `json:"sizeLimit,omitempty"`

	// StorageClassName is the name of the StorageClass to use for the database PVC.
	// If not set, the cluster default StorageClass is used.
	// +optional
	StorageClassName *string `json:"storageClassName,omitempty"`
}

// CatalogDatabase defines storage configuration for the catalog database.
type CatalogDatabase struct {
	// Volume configures the PVC for the database.
	// +optional
	Volume CatalogDatabaseVolume `json:"volume,omitempty"`
}

// CatalogSpec defines the desired state of Catalog.
type CatalogSpec struct {
	// Resources defines resource requirements for the catalog and PostgreSQL workloads.
	// +optional
	Resources CatalogResources `json:"resources,omitempty"`

	// Database configures storage for the catalog database.
	// +optional
	Database CatalogDatabase `json:"database,omitempty"`
}

// CatalogStatus defines the observed state of Catalog.
type CatalogStatus struct {
	// Conditions represent the latest available observations of the Catalog's state.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// ObservedGeneration is the most recent generation observed by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// Catalog is the Schema for the catalogs API.
type Catalog struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   CatalogSpec   `json:"spec,omitempty"`
	Status CatalogStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// CatalogList contains a list of Catalog.
type CatalogList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Catalog `json:"items"`
}
