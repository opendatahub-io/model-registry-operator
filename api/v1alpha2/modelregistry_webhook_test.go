package v1alpha2_test

import (
	"reflect"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/opendatahub-io/model-registry-operator/api/common"
	"github.com/opendatahub-io/model-registry-operator/api/v1alpha1"
	"github.com/opendatahub-io/model-registry-operator/api/v1alpha2"
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
		registry            *v1alpha1.ModelRegistry
		wantErr             bool
	}{
		{"empty registries namespace", "",
			&v1alpha1.ModelRegistry{
				ObjectMeta: metav1.ObjectMeta{Namespace: "test-ns"},
				Spec: v1alpha1.ModelRegistrySpec{
					MySQL: &common.MySQLConfig{},
				}},
			false},
		{"valid registries namespace", "test-ns",
			&v1alpha1.ModelRegistry{
				ObjectMeta: metav1.ObjectMeta{Namespace: "test-ns"},
				Spec: v1alpha1.ModelRegistrySpec{
					MySQL: &common.MySQLConfig{},
				}},
			false},
		{"invalid registries namespace", "test-ns",
			&v1alpha1.ModelRegistry{
				ObjectMeta: metav1.ObjectMeta{Namespace: "not-test-ns"},
				Spec: v1alpha1.ModelRegistrySpec{
					MySQL: &common.MySQLConfig{},
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
				MySQL: &common.MySQLConfig{},
			}},
			wantErr: false,
		},
		{
			name: "valid - postgres",
			mrSpec: &v1alpha1.ModelRegistry{Spec: v1alpha1.ModelRegistrySpec{
				Postgres: &common.PostgresConfig{},
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

func TestDefault(t *testing.T) {
	tests := []struct {
		name       string
		mrSpec     *v1alpha1.ModelRegistry
		wantMrSpec *v1alpha1.ModelRegistry
	}{
		{
			name: "set default values",
			mrSpec: &v1alpha1.ModelRegistry{
				Spec: v1alpha1.ModelRegistrySpec{
					Rest:     common.RestSpec{},
					Postgres: &common.PostgresConfig{},
					MySQL:    &common.MySQLConfig{},
				},
			},
			wantMrSpec: &v1alpha1.ModelRegistry{
				Spec: v1alpha1.ModelRegistrySpec{
					Rest: common.RestSpec{
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
		mrSpec     *v1alpha1.ModelRegistry
		wantMrSpec *v1alpha1.ModelRegistry
	}{
		{
			name: "cleanup runtime default values",
			mrSpec: &v1alpha1.ModelRegistry{
				Spec: v1alpha1.ModelRegistrySpec{
					Rest: common.RestSpec{
						Resources: config.MlmdRestResourceRequirements.DeepCopy(),
						Image:     config.DefaultRestImage,
					},
					Grpc: common.GrpcSpec{
						Resources: config.MlmdGRPCResourceRequirements.DeepCopy(),
						Image:     config.DefaultGrpcImage,
					},
				},
			},
			wantMrSpec: &v1alpha1.ModelRegistry{
				Spec: v1alpha1.ModelRegistrySpec{
					Rest: common.RestSpec{
						Resources: nil,
						Image:     "",
					},
					Grpc: common.GrpcSpec{
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
		mrSpec     *v1alpha1.ModelRegistry
		wantMrSpec *v1alpha1.ModelRegistry
	}{
		{
			name: "set runtime default values for oauth proxy config",
			mrSpec: &v1alpha1.ModelRegistry{
				ObjectMeta: metav1.ObjectMeta{Name: "default"},
				Spec: v1alpha1.ModelRegistrySpec{
					OAuthProxy: &common.OAuthProxyConfig{},
				},
			},
			wantMrSpec: &v1alpha1.ModelRegistry{
				ObjectMeta: metav1.ObjectMeta{Name: "default"},
				Spec: v1alpha1.ModelRegistrySpec{
					Grpc: common.GrpcSpec{
						Resources: config.MlmdGRPCResourceRequirements.DeepCopy(),
						Image:     config.DefaultGrpcImage,
					},
					Rest: common.RestSpec{
						Resources: config.MlmdRestResourceRequirements.DeepCopy(),
						Image:     config.DefaultRestImage,
					},
					OAuthProxy: &common.OAuthProxyConfig{
						TLSCertificateSecret: &common.SecretKeyValue{
							Name: "default-oauth-proxy",
							Key:  "tls.crt",
						},
						TLSKeySecret: &common.SecretKeyValue{
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
	config.SetDefaultCert(certName)
}

var _ = Describe("ModelRegistry Webhook", func() {
	var (
		obj    *v1alpha2.ModelRegistry
		oldObj *v1alpha1.ModelRegistry
	)

	BeforeEach(func() {
		obj = &v1alpha2.ModelRegistry{}
		oldObj = &v1alpha1.ModelRegistry{}
		Expect(oldObj).NotTo(BeNil(), "Expected oldObj to be initialized")
		Expect(obj).NotTo(BeNil(), "Expected obj to be initialized")
		// TODO (user): Add any setup logic common to all tests
	})

	AfterEach(func() {
		// TODO (user): Add any teardown logic common to all tests
	})

	Context("When creating ModelRegistry under Conversion Webhook", func() {
		// TODO (user): Add logic to convert the object to the desired version and verify the conversion
		It("Should convert model registry v1alpha1 to v1alpha2 correctly", func() {
			convertedObj := &v1alpha2.ModelRegistry{}
			Expect(oldObj.ConvertTo(convertedObj)).To(Succeed())
			Expect(convertedObj).ToNot(BeNil())
		})
	})

})
