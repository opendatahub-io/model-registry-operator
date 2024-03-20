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

type ServerTLSSettings struct {

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
	Mode string `json:"mode"`

	// REQUIRED if mode is `SIMPLE` or `MUTUAL`. The path to the file
	// holding the server-side TLS certificate to use.
	//+optional
	ServerCertificate *string `json:"serverCertificate,omitempty"`

	// REQUIRED if mode is `SIMPLE` or `MUTUAL`. The path to the file
	// holding the server's private key.
	//+optional
	PrivateKey *string `json:"privateKey,omitempty"`

	// REQUIRED if mode is `MUTUAL` or `OPTIONAL_MUTUAL`. The path to a file
	// containing certificate authority certificates to use in verifying a presented
	// client side certificate.
	//+optional
	CaCertificates *string `json:"caCertificates,omitempty"`

	// For gateways running on Kubernetes, the name of the secret that
	// holds the TLS certs including the CA certificates. Applicable
	// only on Kubernetes. An Opaque secret should contain the following
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

	// A list of alternate names to verify the subject identity in the
	// certificate presented by the client.
	SubjectAltNames []string `json:"subjectAltNames,omitempty"`

	// An optional list of base64-encoded SHA-256 hashes of the SPKIs of
	// authorized client certificates.
	// Note: When both verify_certificate_hash and verify_certificate_spki
	// are specified, a hash matching either value will result in the
	// certificate being accepted.
	VerifyCertificateSpki []string `json:"verifyCertificateSpki,omitempty"`

	// An optional list of hex-encoded SHA-256 hashes of the
	// authorized client certificates. Both simple and colon separated
	// formats are acceptable.
	// Note: When both verify_certificate_hash and verify_certificate_spki
	// are specified, a hash matching either value will result in the
	// certificate being accepted.
	VerifyCertificateHash []string `json:"verifyCertificateHash,omitempty"`

	//+kubebuilder:Enum=TLS_AUTO;TLSV1_0;TLSV1_1;TLSV1_2;TLSV1_3

	// Optional: Minimum TLS protocol version. By default, it is `TLSV1_2`.
	// TLS protocol versions below TLSV1_2 require setting compatible ciphers with the
	// `cipherSuites` setting as they no longer include compatible ciphers.
	//
	// TLS_AUTO: Automatically choose the optimal TLS version.
	//
	// TLSV1_0: TLS version 1.0
	//
	// TLSV1_1: TLS version 1.1
	//
	// TLSV1_2: TLS version 1.2
	//
	// TLSV1_3: TLS version 1.3
	//
	// Note: Using TLS protocol versions below TLSV1_2 has serious security risks.
	//+optional
	MinProtocolVersion *string `json:"minProtocolVersion,omitempty"`

	//+kubebuilder:Enum=TLS_AUTO;TLSV1_0;TLSV1_1;TLSV1_2;TLSV1_3

	// Optional: Maximum TLS protocol version.
	//
	// TLS_AUTO: Automatically choose the optimal TLS version.
	//
	// TLSV1_0: TLS version 1.0
	//
	// TLSV1_1: TLS version 1.1
	//
	// TLSV1_2: TLS version 1.2
	//
	// TLSV1_3: TLS version 1.3
	//
	//+optional
	MaxProtocolVersion *string `json:"maxProtocolVersion,omitempty"`

	// Optional: If specified, only support the specified cipher list.
	// Otherwise, default to the default cipher list supported by Envoy
	// as specified [here](https://www.envoyproxy.io/docs/envoy/latest/api-v3/extensions/transport_sockets/tls/v3/common.proto).
	// The supported list of ciphers are:
	// * `ECDHE-ECDSA-AES128-GCM-SHA256`
	// * `ECDHE-RSA-AES128-GCM-SHA256`
	// * `ECDHE-ECDSA-AES256-GCM-SHA384`
	// * `ECDHE-RSA-AES256-GCM-SHA384`
	// * `ECDHE-ECDSA-CHACHA20-POLY1305`
	// * `ECDHE-RSA-CHACHA20-POLY1305`
	// * `ECDHE-ECDSA-AES128-SHA`
	// * `ECDHE-RSA-AES128-SHA`
	// * `ECDHE-ECDSA-AES256-SHA`
	// * `ECDHE-RSA-AES256-SHA`
	// * `AES128-GCM-SHA256`
	// * `AES256-GCM-SHA384`
	// * `AES128-SHA`
	// * `AES256-SHA`
	// * `DES-CBC3-SHA`
	//+optional
	CipherSuites []string `json:"cipher_suites,omitempty"`
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
	TLS *ServerTLSSettings `json:"tls,omitempty"`
}

type GatewayConfig struct {
	//+kubebuilder:required

	// Domain name for Gateway configuration
	Domain string `json:"domain"`

	//+kubebuilder:required
	//+kubebuilder:default=ingressgateway

	// Value of label `istio` used to identify the Ingress Gateway
	IstioIngress string `json:"istioIngress"`

	// Maistra/OpenShift Servicemesh control plane name
	//+optional
	ControlPlane *string `json:"controlPlane,omitempty"`

	// Rest gateway server config
	Rest ServerConfig `json:"rest"`

	// Rest gateway server config
	Grpc ServerConfig `json:"grpc"`
}

type IstioConfig struct {
	//+kubebuilder:required

	// Authorino authentication provider name
	AuthProvider string `json:"authProvider"`

	// Authorino AuthConfig selector labels
	//+optional
	AuthConfigLabels map[string]string `json:"authConfigLabels,omitempty"`

	//+kubebuilder:required
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
