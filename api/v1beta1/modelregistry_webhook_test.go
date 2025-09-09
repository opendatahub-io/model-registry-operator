package v1beta1_test

import (
	"reflect"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/opendatahub-io/model-registry-operator/api/v1beta1"
	"github.com/opendatahub-io/model-registry-operator/internal/controller/config"
)

var (
	certName = "test-cert"
	domain   = "example.com"
)

func TestValidateNamespace(t *testing.T) {
	tests := []struct {
		name                string
		registriesNamespace string
		registry            *v1beta1.ModelRegistry
		wantErr             bool
	}{
		{"empty registries namespace", "",
			&v1beta1.ModelRegistry{
				ObjectMeta: metav1.ObjectMeta{Namespace: "test-ns"},
				Spec: v1beta1.ModelRegistrySpec{
					MySQL: &v1beta1.MySQLConfig{},
				}},
			false},
		{"valid registries namespace", "test-ns",
			&v1beta1.ModelRegistry{
				ObjectMeta: metav1.ObjectMeta{Namespace: "test-ns"},
				Spec: v1beta1.ModelRegistrySpec{
					MySQL: &v1beta1.MySQLConfig{},
				}},
			false},
		{"invalid registries namespace", "test-ns",
			&v1beta1.ModelRegistry{
				ObjectMeta: metav1.ObjectMeta{Namespace: "not-test-ns"},
				Spec: v1beta1.ModelRegistrySpec{
					MySQL: &v1beta1.MySQLConfig{},
				}},
			true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config.SetRegistriesNamespace(tt.registriesNamespace)
			errList := tt.registry.ValidateNamespace()
			config.SetRegistriesNamespace("")
			if tt.wantErr {
				if len(errList) == 0 {
					t.Errorf("ValidateNamespace() error = %v, wantErr %v", errList, tt.wantErr)
				}
			} else {
				if len(errList) > 0 {
					t.Errorf("ValidateNamespace() error = %v, wantErr %v", errList, tt.wantErr)
				}
			}
		})
	}
}

func TestValidateDatabase(t *testing.T) {
	tests := []struct {
		name    string
		mrSpec  *v1beta1.ModelRegistry
		wantErr bool
	}{
		{
			name: "valid - mysql",
			mrSpec: &v1beta1.ModelRegistry{Spec: v1beta1.ModelRegistrySpec{
				MySQL: &v1beta1.MySQLConfig{},
			}},
			wantErr: false,
		},
		{
			name: "valid - postgres",
			mrSpec: &v1beta1.ModelRegistry{Spec: v1beta1.ModelRegistrySpec{
				Postgres: &v1beta1.PostgresConfig{},
			}},
			wantErr: false,
		},
		{
			name:    "invalid - missing databases",
			mrSpec:  &v1beta1.ModelRegistry{Spec: v1beta1.ModelRegistrySpec{}},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, errList := tt.mrSpec.ValidateDatabase()
			if tt.wantErr {
				if len(errList) == 0 {
					t.Errorf("ValidateDatabase() error = %v, wantErr %v", errList, tt.wantErr)
				}
			} else {
				if len(errList) > 0 {
					t.Errorf("ValidateDatabase() error = %v, wantErr %v", errList, tt.wantErr)
				}
			}
		})
	}
}

func TestDefault(t *testing.T) {
	tests := []struct {
		name       string
		mrSpec     *v1beta1.ModelRegistry
		wantMrSpec *v1beta1.ModelRegistry
	}{
		{
			name: "set default values",
			mrSpec: &v1beta1.ModelRegistry{
				Spec: v1beta1.ModelRegistrySpec{
					Rest:     v1beta1.RestSpec{},
					Postgres: &v1beta1.PostgresConfig{},
					MySQL:    &v1beta1.MySQLConfig{},
				},
			},
			wantMrSpec: &v1beta1.ModelRegistry{
				Spec: v1beta1.ModelRegistrySpec{
					Rest: v1beta1.RestSpec{
						ServiceRoute: config.RouteDisabled,
					},
					Postgres: nil,
					MySQL:    nil,
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.mrSpec.Default()
			if !reflect.DeepEqual(tt.mrSpec, tt.wantMrSpec) {
				t.Errorf("Default() = %v, want %v", tt.mrSpec, tt.wantMrSpec)
			}
		})
	}
}

func TestCleanupRuntimeDefaults(t *testing.T) {
	setupDefaults(t)

	tests := []struct {
		name       string
		mrSpec     *v1beta1.ModelRegistry
		wantMrSpec *v1beta1.ModelRegistry
	}{
		{
			name: "cleanup runtime default values",
			mrSpec: &v1beta1.ModelRegistry{
				Spec: v1beta1.ModelRegistrySpec{
					Rest: v1beta1.RestSpec{
						Resources: config.MlmdRestResourceRequirements.DeepCopy(),
						Image:     config.DefaultRestImage,
					},
					Grpc: v1beta1.GrpcSpec{
						Resources: config.MlmdGRPCResourceRequirements.DeepCopy(),
						Image:     config.DefaultGrpcImage,
					},
				},
			},
			wantMrSpec: &v1beta1.ModelRegistry{
				Spec: v1beta1.ModelRegistrySpec{
					Rest: v1beta1.RestSpec{
						Resources: nil,
						Image:     "",
					},
					Grpc: v1beta1.GrpcSpec{
						Resources: nil,
						Image:     "",
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.mrSpec.CleanupRuntimeDefaults()
			if !reflect.DeepEqual(tt.mrSpec, tt.wantMrSpec) {
				t.Errorf("CleanupRuntimeDefaults() = %v, want %v", tt.mrSpec, tt.wantMrSpec)
			}
		})
	}
}

func TestRuntimeDefaults(t *testing.T) {
	setupDefaults(t)

	tests := []struct {
		name       string
		mrSpec     *v1beta1.ModelRegistry
		wantMrSpec *v1beta1.ModelRegistry
	}{
		{
			name: "set runtime default values for oauth proxy config",
			mrSpec: &v1beta1.ModelRegistry{
				ObjectMeta: metav1.ObjectMeta{Name: "default"},
				Spec: v1beta1.ModelRegistrySpec{
					OAuthProxy: &v1beta1.OAuthProxyConfig{},
				},
			},
			wantMrSpec: &v1beta1.ModelRegistry{
				ObjectMeta: metav1.ObjectMeta{Name: "default"},
				Spec: v1beta1.ModelRegistrySpec{
					Grpc: v1beta1.GrpcSpec{
						Resources: config.MlmdGRPCResourceRequirements.DeepCopy(),
						Image:     config.DefaultGrpcImage,
					},
					Rest: v1beta1.RestSpec{
						Resources: config.MlmdRestResourceRequirements.DeepCopy(),
						Image:     config.DefaultRestImage,
					},
					OAuthProxy: &v1beta1.OAuthProxyConfig{
						TLSCertificateSecret: &v1beta1.SecretKeyValue{
							Name: "default-oauth-proxy",
							Key:  "tls.crt",
						},
						TLSKeySecret: &v1beta1.SecretKeyValue{
							Name: "default-oauth-proxy",
							Key:  "tls.key",
						},
						Domain: domain,
						Image:  config.DefaultOAuthProxyImage,
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.mrSpec.RuntimeDefaults()
			if !reflect.DeepEqual(tt.mrSpec, tt.wantMrSpec) {
				t.Errorf("RuntimeDefaults() = %v, want %v", tt.mrSpec, tt.wantMrSpec)
			}
		})
	}
}

func setupDefaults(t testing.TB) {
	t.Helper()

	config.SetDefaultDomain(domain, k8sClient, false)
}
