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
	trueValue := true
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
			name: "valid - postgres auto-provisioning",
			mrSpec: &v1beta1.ModelRegistry{Spec: v1beta1.ModelRegistrySpec{
				Postgres: &v1beta1.PostgresConfig{
					GenerateDeployment: &trueValue,
				},
			}},
			wantErr: false,
		},
		{
			name: "invalid - postgres auto-provisioning with host",
			mrSpec: &v1beta1.ModelRegistry{Spec: v1beta1.ModelRegistrySpec{
				Postgres: &v1beta1.PostgresConfig{
					GenerateDeployment: &trueValue,
					Host:               "some-host",
				},
			}},
			wantErr: true,
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
	httpsPort := int32(8443)
	httpsRoutePort := int32(443)

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
		{
			name: "migrate oauth proxy to kube-rbac-proxy",
			mrSpec: &v1beta1.ModelRegistry{
				ObjectMeta: metav1.ObjectMeta{Name: "test-registry"},
				Spec: v1beta1.ModelRegistrySpec{
					Rest: v1beta1.RestSpec{},
					OAuthProxy: &v1beta1.OAuthProxyConfig{
						Port:         &httpsPort,
						RoutePort:    &httpsRoutePort,
						Domain:       "example.com",
						ServiceRoute: config.RouteEnabled,
					},
				},
			},
			wantMrSpec: &v1beta1.ModelRegistry{
				ObjectMeta: metav1.ObjectMeta{Name: "test-registry"},
				Spec: v1beta1.ModelRegistrySpec{
					Rest: v1beta1.RestSpec{
						ServiceRoute: config.RouteDisabled,
					},
					KubeRBACProxy: &v1beta1.KubeRBACProxyConfig{
						Port:         &httpsPort,
						RoutePort:    &httpsRoutePort,
						Domain:       "example.com",
						ServiceRoute: config.RouteEnabled,
						// Image is not copied here because it is set to the operator default
					},
					// OAuthProxy should be removed after migration
				},
			},
		},
		{
			name: "don't migrate when kube-rbac-proxy already exists",
			mrSpec: &v1beta1.ModelRegistry{
				ObjectMeta: metav1.ObjectMeta{Name: "test-registry"},
				Spec: v1beta1.ModelRegistrySpec{
					Rest: v1beta1.RestSpec{},
					KubeRBACProxy: &v1beta1.KubeRBACProxyConfig{
						Port:   &httpsPort,
						Domain: "different.com",
					},
				},
			},
			wantMrSpec: &v1beta1.ModelRegistry{
				ObjectMeta: metav1.ObjectMeta{Name: "test-registry"},
				Spec: v1beta1.ModelRegistrySpec{
					Rest: v1beta1.RestSpec{
						ServiceRoute: config.RouteDisabled,
					},
					// KubeRBACProxy should remain unchanged
					KubeRBACProxy: &v1beta1.KubeRBACProxyConfig{
						Port:         &httpsPort,
						Domain:       "different.com",
						ServiceRoute: config.RouteEnabled,
					},
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
						Resources: config.ModelRegistryRestResourceRequirements.DeepCopy(),
						Image:     config.DefaultRestImage,
					},
				},
			},
			wantMrSpec: &v1beta1.ModelRegistry{
				Spec: v1beta1.ModelRegistrySpec{
					Rest: v1beta1.RestSpec{
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
					Rest: v1beta1.RestSpec{
						Resources: config.ModelRegistryRestResourceRequirements.DeepCopy(),
						Image:     config.DefaultRestImage,
					},
					// No runtime defaults for oauth proxy config
					OAuthProxy: &v1beta1.OAuthProxyConfig{},
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
