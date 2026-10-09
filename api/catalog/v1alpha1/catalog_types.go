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

// CatalogDatabaseVolume defines the storage configuration for the catalog database.
type CatalogDatabaseVolume struct {
	// SizeLimit is the storage size limit for the database's emptyDir volume.
	// +optional
	// +kubebuilder:validation:XValidation:rule="quantity(self).isGreaterThan(quantity('0'))",message="sizeLimit must be greater than zero"
	SizeLimit *resource.Quantity `json:"sizeLimit,omitempty"`
}

// CatalogDatabase defines storage configuration for the catalog database.
type CatalogDatabase struct {
	// Volume configures the emptyDir volume for the database.
	// +optional
	Volume CatalogDatabaseVolume `json:"volume,omitempty"`
}

// ProxyConfig defines outbound HTTP proxy settings for catalog components that
// make external requests.
type ProxyConfig struct {
	// HTTPProxy is the proxy URL for outbound HTTP requests (sets HTTP_PROXY).
	// +optional
	HTTPProxy string `json:"httpProxy,omitempty"`

	// HTTPSProxy is the proxy URL for outbound HTTPS requests (sets HTTPS_PROXY).
	// +optional
	HTTPSProxy string `json:"httpsProxy,omitempty"`

	// NoProxy is a comma-separated list of hosts/domains to exclude from proxying
	// (sets NO_PROXY).
	// +optional
	NoProxy string `json:"noProxy,omitempty"`
}

// CatalogSpec defines the desired state of Catalog.
type CatalogSpec struct {
	// Resources defines resource requirements for the catalog and PostgreSQL workloads.
	// +optional
	Resources CatalogResources `json:"resources,omitempty"`

	// Database configures storage for the catalog database.
	// +optional
	Database CatalogDatabase `json:"database,omitempty"`

	// CatalogDataImageStream independently selects the catalog data image.
	// Empty uses its current release default. "stable" follows the managed
	// ImageStream (OpenShift only); sha256:<64 lowercase hex> pins a digest in
	// its configured data repository. The benchmark field may select a different image.
	// AIHub creates the initial Catalog with both fields unset to use release defaults.
	// Selection/import failures make Catalog and AIHub unready. Successful
	// resolution starts activation; recovery requires an activation outcome for
	// the current attempt and both immutable images. Deployment scale-down is
	// temporary containment; runtime serving enforcement is a separate integration.
	// +optional
	// +kubebuilder:validation:MaxLength=128
	CatalogDataImageStream *string `json:"catalogDataImageStream,omitempty"`

	// BenchmarkDataImageStream independently selects the benchmark data image
	// using the same empty/stable/digest choices as CatalogDataImageStream.
	// Identical import sources share an ImageStream; separate sources are tracked
	// independently. Unset and empty values are equivalent.
	// +optional
	// +kubebuilder:validation:MaxLength=128
	BenchmarkDataImageStream *string `json:"benchmarkDataImageStream,omitempty"`

	// Proxy configures outbound HTTP proxy settings for the catalog. If unset,
	// the operator defaults to the cluster-wide proxy settings from the
	// OpenShift Proxy config (config.openshift.io/v1 Proxy "cluster"), if any.
	// Setting Proxy to any value, including an empty object, disables this
	// cluster-wide default entirely; only the fields set here are applied.
	// +optional
	Proxy *ProxyConfig `json:"proxy,omitempty"`
}

// CatalogStatus defines the observed state of Catalog.
type CatalogStatus struct {
	// Conditions represent the latest available observations of the Catalog's state.
	// Ready and Available require current activation evidence and workload health.
	// WorkloadAvailable tracks deployment health independently of data activation.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// ImageUpdate is the shared selection/import and activation contract.
	// +optional
	ImageUpdate *CatalogImageUpdateStatus `json:"imageUpdate,omitempty"`

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
