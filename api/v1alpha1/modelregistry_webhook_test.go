package v1alpha1_test

import (
	"fmt"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"reflect"
	"testing"

	"github.com/opendatahub-io/model-registry-operator/api/v1alpha1"
	"github.com/opendatahub-io/model-registry-operator/internal/controller/config"
)

var (
	certName       = "test-cert"
	tlsMode        = "SIMPLE"
	audience       = "test-audience"
	authProvider   = "test-auth-provider"
	authLabelKey   = "test-auth-labels"
	authLabelValue = "true"
	authLabel      = fmt.Sprintf("%s=%s", authLabelKey, authLabelValue)
	domain         = "example.com"
	controlPlane   = "test-smcp"
	istioIngress   = config.DefaultIstioIngressName
)

func TestValidateNamespace(t *testing.T) {
	tests := []struct {
		name                string
		registriesNamespace string
		registry            *v1alpha1.ModelRegistry
		wantErr             bool
	}{
		{"empty registries namespace", "",
			&v1alpha1.ModelRegistry{
				ObjectMeta: metav1.ObjectMeta{Namespace: "test-ns"},
				Spec: v1alpha1.ModelRegistrySpec{
					MySQL: &v1alpha1.MySQLConfig{},
				}},
			false},
		{"valid registries namespace", "test-ns",
			&v1alpha1.ModelRegistry{
				ObjectMeta: metav1.ObjectMeta{Namespace: "test-ns"},
				Spec: v1alpha1.ModelRegistrySpec{
					MySQL: &v1alpha1.MySQLConfig{},
				}},
			false},
		{"invalid registries namespace", "test-ns",
			&v1alpha1.ModelRegistry{
				ObjectMeta: metav1.ObjectMeta{Namespace: "not-test-ns"},
				Spec: v1alpha1.ModelRegistrySpec{
					MySQL: &v1alpha1.MySQLConfig{},
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
		mrSpec  *v1alpha1.ModelRegistry
		wantErr bool
	}{
		{
			name: "valid - mysql",
			mrSpec: &v1alpha1.ModelRegistry{Spec: v1alpha1.ModelRegistrySpec{
				MySQL: &v1alpha1.MySQLConfig{},
			}},
			wantErr: false,
		},
		{
			name: "valid - postgres",
			mrSpec: &v1alpha1.ModelRegistry{Spec: v1alpha1.ModelRegistrySpec{
				Postgres: &v1alpha1.PostgresConfig{},
			}},
			wantErr: false,
		},
		{
			name:    "invalid - missing databases",
			mrSpec:  &v1alpha1.ModelRegistry{Spec: v1alpha1.ModelRegistrySpec{}},
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

func TestValidateIstioConfig(t *testing.T) {
	defaultIstioIngress := config.DefaultIstioIngressName
	tests := []struct {
		name    string
		mrSpec  *v1alpha1.ModelRegistry
		wantErr bool
	}{
		{
			name: "invalid - istio missing authProvider",
			mrSpec: &v1alpha1.ModelRegistry{Spec: v1alpha1.ModelRegistrySpec{
				Istio: &v1alpha1.IstioConfig{},
			}},
			wantErr: true,
		},
		{
			name: "invalid - istio missing authConfigLabels",
			mrSpec: &v1alpha1.ModelRegistry{Spec: v1alpha1.ModelRegistrySpec{
				Istio: &v1alpha1.IstioConfig{AuthProvider: "istio"},
			}},
			wantErr: true,
		},
		{
			name: "invalid - istio gateway missing domain",
			mrSpec: &v1alpha1.ModelRegistry{Spec: v1alpha1.ModelRegistrySpec{
				Istio: &v1alpha1.IstioConfig{
					AuthProvider:     "istio",
					AuthConfigLabels: map[string]string{"auth": "enabled"},
					Gateway: &v1alpha1.GatewayConfig{
						IstioIngress: &defaultIstioIngress,
					},
				},
			}},
			wantErr: true,
		},
		{
			name: "invalid - istio gateway missing istioIngress",
			mrSpec: &v1alpha1.ModelRegistry{Spec: v1alpha1.ModelRegistrySpec{
				Istio: &v1alpha1.IstioConfig{
					AuthProvider:     "istio",
					AuthConfigLabels: map[string]string{"auth": "enabled"},
					Gateway:          &v1alpha1.GatewayConfig{},
				},
			}},
			wantErr: true,
		},
		{
			name: "invalid - istio gateway rest custom TLS missing credentials",
			mrSpec: &v1alpha1.ModelRegistry{Spec: v1alpha1.ModelRegistrySpec{
				Istio: &v1alpha1.IstioConfig{
					AuthProvider:     "istio",
					AuthConfigLabels: map[string]string{"auth": "enabled"},
					Gateway: &v1alpha1.GatewayConfig{
						Domain:       "test.com",
						IstioIngress: &defaultIstioIngress,
						Rest: v1alpha1.ServerConfig{
							TLS: &v1alpha1.TLSServerSettings{
								Mode: "SIMPLE",
							},
						},
					},
				},
			}},
			wantErr: true,
		},
		{
			name: "invalid - istio gateway grpc custom TLS missing credentials",
			mrSpec: &v1alpha1.ModelRegistry{Spec: v1alpha1.ModelRegistrySpec{
				Istio: &v1alpha1.IstioConfig{
					AuthProvider:     "istio",
					AuthConfigLabels: map[string]string{"auth": "enabled"},
					Gateway: &v1alpha1.GatewayConfig{
						Domain:       "test.com",
						IstioIngress: &defaultIstioIngress,
						Grpc: v1alpha1.ServerConfig{
							TLS: &v1alpha1.TLSServerSettings{
								Mode: "SIMPLE",
							},
						},
					},
				},
			}},
			wantErr: true,
		},
		{
			name: "valid - istio config",
			mrSpec: &v1alpha1.ModelRegistry{Spec: v1alpha1.ModelRegistrySpec{
				Istio: &v1alpha1.IstioConfig{
					AuthProvider:     "istio",
					AuthConfigLabels: map[string]string{"auth": "enabled"},
					Gateway: &v1alpha1.GatewayConfig{
						Domain:       "test.com",
						IstioIngress: &defaultIstioIngress,
					},
				},
			}},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, errList := tt.mrSpec.ValidateIstioConfig()
			if tt.wantErr {
				if len(errList) == 0 {
					t.Errorf("ValidateIstioConfig() error = %v, wantErr %v", errList, tt.wantErr)
				}
			} else {
				if len(errList) > 0 {
					t.Errorf("ValidateIstioConfig() error = %v, wantErr %v", errList, tt.wantErr)
				}
			}
		})
	}
}

func TestDefault(t *testing.T) {
	var httpPort int32 = v1alpha1.DefaultHttpPort

	tests := []struct {
		name       string
		mrSpec     *v1alpha1.ModelRegistry
		wantMrSpec *v1alpha1.ModelRegistry
	}{
		{
			name: "set default values",
			mrSpec: &v1alpha1.ModelRegistry{
				Spec: v1alpha1.ModelRegistrySpec{
					Rest:     v1alpha1.RestSpec{},
					Postgres: &v1alpha1.PostgresConfig{},
					MySQL:    &v1alpha1.MySQLConfig{},
					Istio: &v1alpha1.IstioConfig{
						Gateway: &v1alpha1.GatewayConfig{},
					},
				},
			},
			wantMrSpec: &v1alpha1.ModelRegistry{
				Spec: v1alpha1.ModelRegistrySpec{
					Rest: v1alpha1.RestSpec{
						ServiceRoute: config.RouteDisabled,
					},
					Postgres: nil,
					MySQL:    nil,
					Istio: &v1alpha1.IstioConfig{
						TlsMode: v1alpha1.DefaultTlsMode,
						Gateway: &v1alpha1.GatewayConfig{
							Rest: v1alpha1.ServerConfig{
								Port:         &httpPort,
								GatewayRoute: config.RouteEnabled,
							},
							Grpc: v1alpha1.ServerConfig{
								Port:         &httpPort,
								GatewayRoute: config.RouteEnabled,
							},
						},
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
		mrSpec     *v1alpha1.ModelRegistry
		wantMrSpec *v1alpha1.ModelRegistry
	}{
		{
			name: "cleanup runtime default values",
			mrSpec: &v1alpha1.ModelRegistry{
				Spec: v1alpha1.ModelRegistrySpec{
					Rest: v1alpha1.RestSpec{
						Resources: config.MlmdRestResourceRequirements.DeepCopy(),
						Image:     config.DefaultRestImage,
					},
					Grpc: v1alpha1.GrpcSpec{
						Resources: config.MlmdGRPCResourceRequirements.DeepCopy(),
						Image:     config.DefaultGrpcImage,
					},
					Istio: &v1alpha1.IstioConfig{
						Audiences:        []string{audience},
						AuthProvider:     authProvider,
						AuthConfigLabels: map[string]string{authLabelKey: authLabelValue},
						Gateway: &v1alpha1.GatewayConfig{
							Domain: domain,
							Rest: v1alpha1.ServerConfig{
								TLS: &v1alpha1.TLSServerSettings{
									Mode:           tlsMode,
									CredentialName: &certName,
								},
							},
							Grpc: v1alpha1.ServerConfig{
								TLS: &v1alpha1.TLSServerSettings{
									Mode:           tlsMode,
									CredentialName: &certName,
								},
							},
						},
					},
				},
			},
			wantMrSpec: &v1alpha1.ModelRegistry{
				Spec: v1alpha1.ModelRegistrySpec{
					Rest: v1alpha1.RestSpec{
						Resources: nil,
						Image:     "",
					},
					Grpc: v1alpha1.GrpcSpec{
						Resources: nil,
						Image:     "",
					},
					Istio: &v1alpha1.IstioConfig{
						Audiences:        []string{},
						AuthProvider:     "",
						AuthConfigLabels: map[string]string{},
						Gateway: &v1alpha1.GatewayConfig{
							Domain: "",
							Rest: v1alpha1.ServerConfig{
								TLS: &v1alpha1.TLSServerSettings{
									Mode:           tlsMode,
									CredentialName: nil,
								},
							},
							Grpc: v1alpha1.ServerConfig{
								TLS: &v1alpha1.TLSServerSettings{
									Mode:           tlsMode,
									CredentialName: nil,
								},
							},
						},
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
		mrSpec     *v1alpha1.ModelRegistry
		wantMrSpec *v1alpha1.ModelRegistry
	}{
		{
			name: "set runtime default values for istio config",
			mrSpec: &v1alpha1.ModelRegistry{
				Spec: v1alpha1.ModelRegistrySpec{
					Istio: &v1alpha1.IstioConfig{
						Gateway: &v1alpha1.GatewayConfig{
							Rest: v1alpha1.ServerConfig{
								TLS: &v1alpha1.TLSServerSettings{
									Mode: tlsMode,
								},
							},
							Grpc: v1alpha1.ServerConfig{
								TLS: &v1alpha1.TLSServerSettings{
									Mode: tlsMode,
								},
							},
						},
					},
				},
			},
			wantMrSpec: &v1alpha1.ModelRegistry{
				Spec: v1alpha1.ModelRegistrySpec{
					Grpc: v1alpha1.GrpcSpec{
						Resources: config.MlmdGRPCResourceRequirements.DeepCopy(),
						Image:     config.DefaultGrpcImage,
					},
					Rest: v1alpha1.RestSpec{
						Resources: config.MlmdRestResourceRequirements.DeepCopy(),
						Image:     config.DefaultRestImage,
					},
					Istio: &v1alpha1.IstioConfig{
						Audiences:        []string{audience},
						AuthProvider:     authProvider,
						AuthConfigLabels: map[string]string{authLabelKey: authLabelValue},
						Gateway: &v1alpha1.GatewayConfig{
							Domain:       domain,
							ControlPlane: &controlPlane,
							IstioIngress: &istioIngress,
							Rest: v1alpha1.ServerConfig{
								TLS: &v1alpha1.TLSServerSettings{
									Mode:           tlsMode,
									CredentialName: &certName,
								},
							},
							Grpc: v1alpha1.ServerConfig{
								TLS: &v1alpha1.TLSServerSettings{
									Mode:           tlsMode,
									CredentialName: &certName,
								},
							},
						},
					},
				},
			},
		},
		{
			name: "set runtime default values for oauth proxy config",
			mrSpec: &v1alpha1.ModelRegistry{
				ObjectMeta: metav1.ObjectMeta{Name: "default"},
				Spec: v1alpha1.ModelRegistrySpec{
					OAuthProxy: &v1alpha1.OAuthProxyConfig{},
				},
			},
			wantMrSpec: &v1alpha1.ModelRegistry{
				ObjectMeta: metav1.ObjectMeta{Name: "default"},
				Spec: v1alpha1.ModelRegistrySpec{
					Grpc: v1alpha1.GrpcSpec{
						Resources: config.MlmdGRPCResourceRequirements.DeepCopy(),
						Image:     config.DefaultGrpcImage,
					},
					Rest: v1alpha1.RestSpec{
						Resources: config.MlmdRestResourceRequirements.DeepCopy(),
						Image:     config.DefaultRestImage,
					},
					OAuthProxy: &v1alpha1.OAuthProxyConfig{
						TLSCertificateSecret: &v1alpha1.SecretKeyValue{
							Name: "default-oauth-proxy",
							Key:  "tls.crt",
						},
						TLSKeySecret: &v1alpha1.SecretKeyValue{
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

	config.SetDefaultAudiences([]string{audience})
	config.SetDefaultDomain(domain, k8sClient, false)
	config.SetDefaultControlPlane(controlPlane)
	config.SetDefaultCert(certName)
	config.SetDefaultAuthProvider(authProvider)
	config.SetDefaultAuthConfigLabels(authLabel)
}
