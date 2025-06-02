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

package v1alpha2

import (
	"github.com/opendatahub-io/model-registry-operator/api/common"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/conversion"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// ModelRegistrySpec defines the desired state of ModelRegistry.
// One of `postgres` or `mysql` database configurations MUST be provided!
type ModelRegistrySpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	//+kubebuilder:required

	// Configuration for REST endpoint
	Rest common.RestSpec `json:"rest"`

	//+kubebuilder:required

	// Configuration for gRPC endpoint
	Grpc common.GrpcSpec `json:"grpc"`

	// PostgreSQL configuration options
	//+optional
	Postgres *common.PostgresConfig `json:"postgres,omitempty"`

	// MySQL configuration options
	//+optional
	MySQL *common.MySQLConfig `json:"mysql,omitempty"`

	// Flag specifying database upgrade option. If set to true, it enables
	// database migration during initialization (Optional parameter)
	//+optional
	EnableDatabaseUpgrade *bool `json:"enable_database_upgrade,omitempty"`

	// Database downgrade schema version value. If set the database
	// schema version is downgraded to the set value during
	// initialization (Optional Parameter)
	//+optional
	DowngradeDbSchemaVersion *int64 `json:"downgrade_db_schema_version,omitempty"`

	// OpenShift OAuth proxy configuration options
	//+optional
	OAuthProxy *common.OAuthProxyConfig `json:"oauthProxy,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:resource:shortName=mr
//+kubebuilder:subresource:status
//+kubebuilder:storageversion
//+kubebuilder:printcolumn:name="Available",type=string,JSONPath=`.status.conditions[?(@.type=="Available")].status`
//+kubebuilder:printcolumn:name="OAuthProxy",type=string,JSONPath=`.status.conditions[?(@.type=="OAuthProxyAvailable")].status`,priority=2
//+kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
//+kubebuilder:printcolumn:name="Hosts",type=string,JSONPath=`.status.hostsStr`,priority=2
//+kubebuilder:printcolumn:name="Status",type=string,JSONPath=`.status.conditions[?(@.type=="Available")].message`,priority=2

// ModelRegistry is the Schema for the modelregistries API
type ModelRegistry struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ModelRegistrySpec          `json:"spec,omitempty"`
	Status common.ModelRegistryStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// ModelRegistryList contains a list of ModelRegistry
type ModelRegistryList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ModelRegistry `json:"items"`
}

var _ conversion.Hub = (*ModelRegistry)(nil)

// Hub marks this type as a conversion hub.
func (*ModelRegistry) Hub() {}
