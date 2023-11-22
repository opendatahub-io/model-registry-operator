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
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

type SecretKeyValue struct {
	// +kubebuilder:validation:Required
	// Kubernetes secret name
	Name string `json:"name"`
	// Key name in secret
	Key string `json:"key"`
}

type PostgresConfig struct {
	// Name of host to connect to.
	Host string `json:"host,omitempty"`
	// Numeric IP address of host to connect to. If this field is
	// provided, "host" field is ignored.
	HostAddress string `json:"hostAddress,omitempty"`

	//+kubebuilder:default=5432
	//+kubebuilder:validation:Minimum=1
	//+kubebuilder:validation:Maximum=65535
	// Port number to connect to at the server host.
	Port *int32 `json:"port,omitempty"`

	//+kubebuilder:required
	// PostgreSQL username to connect as.
	Username string `json:"username,omitempty"`

	// Password to be used if required by the PostgreSQL server.
	PasswordSecret *SecretKeyValue `json:"passwordSecret,omitempty"`

	//+kubebuilder:required
	// The database name.
	Database string `json:"database"`

	//+kubebuilder:default=false
	// True if skipping database instance creation during ML Metadata
	// service initialization. By default, it is false.
	SkipDBCreation bool `json:"skipDBCreation,omitempty"`

	//+kubebuilder:validation:Enum=disable;allow;prefer;require;verify-ca;verify-full
	//+kubebuilder:default=disable
	// PostgreSQL sslmode setup. Values can be disable, allow, prefer,
	// require, verify-ca, verify-full.
	SSLMode string `json:"sslMode,omitempty"`
	// This parameter specifies the Kubernetes Secret name and key of the client SSL certificate.
	SSLCertificateSecret *SecretKeyValue `json:"sslCertificateSecret,omitempty"`
	// This parameter specifies the Kubernetes Secret name and key used for the
	// client certificate SSL secret key.
	SSLKeySecret *SecretKeyValue `json:"sslKeySecret,omitempty"`
	// This parameter specifies the Kubernetes Secret name and key of the password for the SSL secret key
	// specified in sslKeySecret, allowing client certificate private keys
	// to be stored in encrypted form on disk even when interactive
	// passphrase input is not practical.
	SSLPasswordSecret *SecretKeyValue `json:"sslPasswordSecret,omitempty"`
	// This parameter specifies the Kubernetes Secret name and key containing SSL
	// certificate authority (CA) certificate(s).
	SSLRootCertificateSecret *SecretKeyValue `json:"sslRootCertificateSecret,omitempty"`
}

type RestSpec struct {
	//+kubebuilder:default=8080
	//+kubebuilder:validation:Minimum=1
	//+kubebuilder:validation:Maximum=65535

	// Listen port for REST connections, defaults to 8080.
	Port *int32 `json:"port,omitempty"`

	//+kubebuilder:validation:Enum=disabled;enabled
	//+kubebuilder:default=disabled
	// Create an OpenShift Route for REST Service
	ServiceRoute string `json:"serviceRoute,omitempty"`

	// Resource requirements
	//+optional
	Resources *v1.ResourceRequirements `json:"resources,omitempty"`

	// Optional image to support overriding the image deployed by the operator.
	//+optional
	Image string `json:"image,omitempty"`
}

type GrpcSpec struct {
	//+kubebuilder:default=9090
	//+kubebuilder:validation:Minimum=0
	//+kubebuilder:validation:Maximum=65535

	// Listen port for gRPC connections, defaults to 9090.
	Port *int32 `json:"port,omitempty"`

	// Resource requirements
	//+optional
	Resources *v1.ResourceRequirements `json:"resources,omitempty"`

	// Optional image to support overriding the image deployed by the operator.
	//+optional
	Image string `json:"image,omitempty"`
}

// ModelRegistrySpec defines the desired state of ModelRegistry
type ModelRegistrySpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	//+kubebuilder:required

	// Configuration for REST endpoint
	Rest RestSpec `json:"rest"`

	//+kubebuilder: required

	// Configuration for gRPC endpoint
	Grpc GrpcSpec `json:"grpc"`

	//+kubebuilder:required

	// PostgreSQL configuration options
	Postgres PostgresConfig `json:"postgres"`

	// Flag specifying database upgrade option. If set to true, it enables
	// database migration during initialization (Optional parameter)
	//+optional
	EnableDatabaseUpgrade *bool `json:"enable_database_upgrade,omitempty"`

	// Database downgrade schema version value. If set the database
	// schema version is downgraded to the set value during
	// initialization (Optional Parameter)
	//+optional
	DowngradeDbSchemaVersion *int64 `json:"downgrade_db_schema_version,omitempty"`
}

// ModelRegistryStatus defines the observed state of ModelRegistry
type ModelRegistryStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:resource:shortName=mr
//+kubebuilder:subresource:status

// ModelRegistry is the Schema for the modelregistries API
type ModelRegistry struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ModelRegistrySpec   `json:"spec,omitempty"`
	Status ModelRegistryStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// ModelRegistryList contains a list of ModelRegistry
type ModelRegistryList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ModelRegistry `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ModelRegistry{}, &ModelRegistryList{})
}
