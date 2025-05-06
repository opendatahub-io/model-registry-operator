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

type ConfigMapKeyValue struct {
	// +kubebuilder:validation:Required
	// Kubernetes configmap name
	Name string `json:"name"`
	// +kubebuilder:validation:Required
	// Key name in configmap
	Key string `json:"key"`
}

type SecretKeyValue struct {
	// +kubebuilder:validation:Required
	// Kubernetes secret name
	Name string `json:"name"`
	// +kubebuilder:validation:Required
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
	// This parameter specifies the Kubernetes ConfigMap name and key containing SSL
	// certificate authority (CA) certificate(s).
	SSLRootCertificateConfigMap *ConfigMapKeyValue `json:"sslRootCertificateConfigMap,omitempty"`
	// This parameter specifies the Kubernetes Secret name and key containing SSL
	// certificate authority (CA) certificate(s).
	SSLRootCertificateSecret *SecretKeyValue `json:"sslRootCertificateSecret,omitempty"`
}

type MySQLConfig struct {
	//+kubebuilder:required
	// The hostname or IP address of the MYSQL server:
	// If unspecified, a connection to the local host is assumed.
	// Currently, a replicated MYSQL backend is not supported.
	Host string `json:"host"`

	//+kubebuilder:default=3306
	//+kubebuilder:validation:Minimum=1
	//+kubebuilder:validation:Maximum=65535
	// Port number to connect to at the server host.
	// The TCP Port number that the MYSQL server accepts connections on.
	// If unspecified, the default MYSQL port (3306) is used.
	Port *int32 `json:"port,omitempty"`

	//+kubebuilder:required
	// The MYSQL login id.
	Username string `json:"username"`

	// The password to use for `Username`. If empty, only MYSQL user ids that don't
	// have a password set are allowed to connect.
	PasswordSecret *SecretKeyValue `json:"passwordSecret,omitempty"`

	//+kubebuilder:required
	// The database to connect to. Must be specified.
	// After connecting to the MYSQL server, this database is created if not
	// already present unless SkipDBCreation is set.
	// All queries after Connect() are assumed to be for this database.
	Database string `json:"database"`

	//+kubebuilder:default=false
	// True if skipping database instance creation during ML Metadata
	// service initialization. By default, it is false.
	SkipDBCreation bool `json:"skipDBCreation,omitempty"`

	// This parameter specifies the Kubernetes Secret name and key of the client public key certificate.
	SSLCertificateSecret *SecretKeyValue `json:"sslCertificateSecret,omitempty"`
	// This parameter specifies the Kubernetes Secret name and key used for the
	// client private key.
	SSLKeySecret *SecretKeyValue `json:"sslKeySecret,omitempty"`
	// This parameter specifies the Kubernetes ConfigMap name and key containing
	// certificate authority (CA) certificate.
	SSLRootCertificateConfigMap *ConfigMapKeyValue `json:"sslRootCertificateConfigMap,omitempty"`
	// This parameter specifies the Kubernetes ConfigMap name containing
	// multiple certificate authority (CA) certificate(s) as keys.
	SSLRootCertificatesConfigMapName *string `json:"sslRootCertificatesConfigMapName,omitempty"`
	// This parameter specifies the Kubernetes Secret name and key containing
	// certificate authority (CA) certificate.
	SSLRootCertificateSecret *SecretKeyValue `json:"sslRootCertificateSecret,omitempty"`
	// This parameter specifies the Kubernetes Secret name containing
	// multiple certificate authority (CA) certificate(s) as keys.
	SSLRootCertificatesSecretName *string `json:"sslRootCertificatesSecretName,omitempty"`
	// This parameter specifies the list of permissible ciphers for SSL encryption.
	SSLCipher *string `json:"sslCipher,omitempty"`
	// If set, enable verification of the server certificate against the host
	// name used when connecting to the server.
	VerifyServerCert *bool `json:"verifyServerCert,omitempty"`
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

type TLSServerSettings struct {

	//+kubebuilder:default=SIMPLE
	//+kubebuilder:validation:Enum=SIMPLE;MUTUAL;ISTIO_MUTUAL;OPTIONAL_MUTUAL

	// The value of this field determines how TLS is enforced.
	// SIMPLE: Secure connections with standard TLS semantics. In this mode client certificate is not requested during handshake.
	//
	// MUTUAL: Secure connections to the downstream using mutual TLS by presenting server certificates for authentication. A client certificate will also be requested during the handshake and at least one valid certificate is required to be sent by the client.
	//
	// ISTIO_MUTUAL: Secure connections from the downstream using mutual TLS by presenting server certificates for authentication. Compared to Mutual mode, this mode uses certificates, representing gateway workload identity, generated automatically by Istio for mTLS authentication. When this mode is used, all other TLS fields should be empty.
	//
	// OPTIONAL_MUTUAL: Similar to MUTUAL mode, except that the client certificate is optional. Unlike SIMPLE mode, A client certificate will still be explicitly requested during handshake, but the client is not required to send a certificate. If a client certificate is presented, it will be validated. ca_certificates should be specified for validating client certificates.
	Mode string `json:"mode,omitempty"`

	// The name of the secret that holds the TLS certs including the CA certificates.
	// If not provided, it is set automatically using model registry operator env variable DEFAULT_CERT.
	// An Opaque secret should contain the following
	// keys and values: `tls.key: <privateKey>` and `tls.crt: <serverCert>` or
	// `key: <privateKey>` and `cert: <serverCert>`.
	// For mutual TLS, `cacert: <CACertificate>` and `crl: <CertificateRevocationList>`
	// can be provided in the same secret or a separate secret named `<secret>-cacert`.
	// A TLS secret for server certificates with an additional `tls.ocsp-staple` key
	// for specifying OCSP staple information, `ca.crt` key for CA certificates
	// and `ca.crl` for certificate revocation list is also supported.
	// Only one of server certificates and CA certificate
	// or credentialName can be specified.
	//+optional
	CredentialName *string `json:"credentialName,omitempty"`
}

type ServerConfig struct {
	//+kubebuilder:validation:Minimum=0
	//+kubebuilder:validation:Maximum=65535

	// Listen port for server connections, defaults to 80 without TLS and 443 when TLS settings are present.
	Port *int32 `json:"port,omitempty"`

	// Set of TLS related options that govern the server's behavior. Use
	// these options to control if all http requests should be redirected to
	// https, and the TLS modes to use.
	//+optional
	TLS *TLSServerSettings `json:"tls,omitempty"`

	//+kubebuilder:default:enabled
	//+kubebuilder:validation:Enum=disabled;enabled

	// Creates an OpenShift Route for Gateway Service when set to enabled (default).
	GatewayRoute string `json:"gatewayRoute,omitempty"`
}

type GatewayConfig struct {

	//+optional
	//+kubebuilder:validation:MaxLength=253
	//+kubebuilder:validation:Pattern=`^(([a-zA-Z0-9]|[a-zA-Z0-9][a-zA-Z0-9\-]{0,61}[a-zA-Z0-9])(\.([a-zA-Z0-9]|[a-zA-Z0-9][a-zA-Z0-9\-]{0,61}[a-zA-Z0-9]))*)?$`

	// Domain name for Gateway configuration.
	// Must follow DNS952 subdomain conventions.
	// If not provided, it is set automatically using model registry operator env variable DEFAULT_DOMAIN.
	// If the env variable is not set, it is set to the OpenShift `cluster` ingress domain in an OpenShift cluster.
	Domain string `json:"domain,omitempty"`

	// Value of label `istio` used to identify the Ingress Gateway
	IstioIngress *string `json:"istioIngress,omitempty"`

	// Maistra/OpenShift Servicemesh control plane name
	//+optional
	ControlPlane *string `json:"controlPlane,omitempty"`

	// Rest gateway server config
	Rest ServerConfig `json:"rest"`

	// gRPC  gateway server config
	Grpc ServerConfig `json:"grpc"`
}

type IstioConfig struct {

	// Authorino authentication provider name
	//
	// If missing, it is set using the operator environment property DEFAULT_AUTH_PROVIDER
	// Model registry will have an error status if the operator property is also missing
	AuthProvider string `json:"authProvider,omitempty"`

	// Authorino AuthConfig selector labels.
	//
	// If missing, it is set using the operator environment property DEFAULT_AUTH_CONFIG_LABELS
	//+optional
	AuthConfigLabels map[string]string `json:"authConfigLabels,omitempty"`

	//+kubebuilder:default=ISTIO_MUTUAL
	//+kubebuilder:Enum=DISABLE;SIMPLE;MUTUAL;ISTIO_MUTUAL

	// DestinationRule TLS mode. Defaults to ISTIO_MUTUAL.
	//
	// DISABLE: Do not setup a TLS connection to the upstream endpoint.
	//
	// SIMPLE: Originate a TLS connection to the upstream endpoint.
	//
	// MUTUAL: Secure connections to the upstream using mutual TLS by presenting
	// client certificates for authentication.
	//
	// ISTIO_MUTUAL: Secure connections to the upstream using mutual TLS by presenting
	// client certificates for authentication.
	// Compared to Mutual mode, this mode uses certificates generated
	// automatically by Istio for mTLS authentication. When this mode is
	// used, all other fields in `ClientTLSSettings` should be empty.
	TlsMode string `json:"tlsMode,omitempty"`

	// Optional Istio Gateway for registry services.
	// Gateway is not created if set to null (default).
	//+optional
	Gateway *GatewayConfig `json:"gateway,omitempty"`

	// Optional Authorino AuthConfig credential audiences. This depends on the cluster identity provider.
	// If not specified, operator will determine the cluster's audience using its own service account.
	//+optional
	Audiences []string `json:"audiences,omitempty"`
}

// +kubebuilder:validation:XValidation:rule="has(self.tlsCertificateSecret) == has(self.tlsKeySecret)",message="tlsCertificateSecret and tlsKeySecret MUST be set together"
type OAuthProxyConfig struct {
	//+kubebuilder:default=8443
	//+kubebuilder:validation:Minimum=0
	//+kubebuilder:validation:Maximum=65535

	// Listen port for OAuth Proxy connections, defaults to 8443
	Port *int32 `json:"port,omitempty"`

	// This parameter specifies the Kubernetes Secret name and key of the proxy public key certificate.
	// If this option is not set, the operator uses OpenShift Serving Certificate by default in OpenShift clusters.
	// In non-OpenShift clusters, a secret named `<modelregistry-name>-oauth-proxy` MUST be provided with data keys
	// `tls.crt` and `tls.key` for the certificate and secret key respectively.
	//+optional
	TLSCertificateSecret *SecretKeyValue `json:"tlsCertificateSecret,omitempty"`
	// This parameter specifies the optional Kubernetes Secret name and key used for the
	// proxy private key if `SSLCertificateSecret` is set.
	//+optional
	TLSKeySecret *SecretKeyValue `json:"tlsKeySecret,omitempty"`

	//+kubebuilder:validation:Enum=disabled;enabled
	//+kubebuilder:default=enabled

	// Create an OpenShift Route for REST proxy Service, enabled by default
	ServiceRoute string `json:"serviceRoute,omitempty"`

	//+optional
	//+kubebuilder:validation:MaxLength=253
	//+kubebuilder:validation:Pattern=`^(([a-zA-Z0-9]|[a-zA-Z0-9][a-zA-Z0-9\-]{0,61}[a-zA-Z0-9])(\.([a-zA-Z0-9]|[a-zA-Z0-9][a-zA-Z0-9\-]{0,61}[a-zA-Z0-9]))*)?$`

	// Domain name for Route configuration.
	// Must follow DNS952 subdomain conventions.
	// If not provided, it is set automatically using model registry operator env variable DEFAULT_DOMAIN.
	// If the env variable is not set, it is set to the OpenShift `cluster` ingress domain in an OpenShift cluster.
	Domain string `json:"domain,omitempty"`

	//+kubebuilder:default=443
	//+kubebuilder:validation:Minimum=0
	//+kubebuilder:validation:Maximum=65535

	// Listen port for OAuth Proxy Route connections, defaults to 443 in OpenShift router default configuration
	RoutePort *int32 `json:"routePort,omitempty"`

	// Optional image to support overriding the image deployed by the operator.
	//+optional
	Image string `json:"image,omitempty"`
}

// +kubebuilder:validation:XValidation:rule="!(has(self.istio) && has(self.oauthProxy))",message="Cannot use both istio and oauthProxy"
// ModelRegistrySpec defines the desired state of ModelRegistry.
// One of `postgres` or `mysql` database configurations MUST be provided!
type ModelRegistrySpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	//+kubebuilder:required

	// Configuration for REST endpoint
	Rest RestSpec `json:"rest"`

	//+kubebuilder:required

	// Configuration for gRPC endpoint
	Grpc GrpcSpec `json:"grpc"`

	// PostgreSQL configuration options
	//+optional
	Postgres *PostgresConfig `json:"postgres,omitempty"`

	// MySQL configuration options
	//+optional
	MySQL *MySQLConfig `json:"mysql,omitempty"`

	// Flag specifying database upgrade option. If set to true, it enables
	// database migration during initialization (Optional parameter)
	//+optional
	EnableDatabaseUpgrade *bool `json:"enable_database_upgrade,omitempty"`

	// Database downgrade schema version value. If set the database
	// schema version is downgraded to the set value during
	// initialization (Optional Parameter)
	//+optional
	DowngradeDbSchemaVersion *int64 `json:"downgrade_db_schema_version,omitempty"`

	// Istio servicemesh configuration options
	//+optional
	Istio *IstioConfig `json:"istio,omitempty"`

	// OpenShift OAuth proxy configuration options
	//+optional
	OAuthProxy *OAuthProxyConfig `json:"oauthProxy,omitempty"`
}

// ModelRegistryStatus defines the observed state of ModelRegistry
type ModelRegistryStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// Hosts where model registry services are available
	// NOTE: Gateway service names are different for gRPC and REST service routes
	Hosts []string `json:"hosts,omitempty"`

	// Formatted Host names separated by comma
	HostsStr string `json:"hostsStr,omitempty"`

	// SpecDefaults is a JSON string containing default spec values that were used for model registry deployment
	SpecDefaults string `json:"specDefaults,omitempty"`

	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:resource:shortName=mr
//+kubebuilder:subresource:status
//+kubebuilder:printcolumn:name="Available",type=string,JSONPath=`.status.conditions[?(@.type=="Available")].status`
//+kubebuilder:printcolumn:name="Istio",type=string,JSONPath=`.status.conditions[?(@.type=="IstioAvailable")].status`,priority=2
//+kubebuilder:printcolumn:name="Gateway",type=string,JSONPath=`.status.conditions[?(@.type=="GatewayAvailable")].status`,priority=2
//+kubebuilder:printcolumn:name="OAuthProxy",type=string,JSONPath=`.status.conditions[?(@.type=="OAuthProxyAvailable")].status`,priority=2
//+kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
//+kubebuilder:printcolumn:name="Hosts",type=string,JSONPath=`.status.hostsStr`,priority=2
//+kubebuilder:printcolumn:name="Status",type=string,JSONPath=`.status.conditions[?(@.type=="Available")].message`,priority=2

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
