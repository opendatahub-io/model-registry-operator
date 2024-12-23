package config_test

import (
	"fmt"
	"os"
	"testing"

	"github.com/go-logr/logr"
	"github.com/opendatahub-io/model-registry-operator/api/v1alpha1"
	"github.com/opendatahub-io/model-registry-operator/internal/controller"
	"github.com/opendatahub-io/model-registry-operator/internal/controller/config"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	rbac "k8s.io/api/rbac/v1"
)

func TestGetStringConfigWithDefault(t *testing.T) {
	tests := []struct {
		name       string
		configName string
		want       string
	}{
		{name: "test " + config.GrpcImage, configName: config.GrpcImage, want: "success1"},
		{name: "test " + config.RestImage, configName: config.RestImage, want: "success2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			os.Setenv(tt.configName, tt.want)
			if got := config.GetStringConfigWithDefault(tt.configName, "fail"); got != tt.want {
				t.Errorf("GetStringConfigWithDefault() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseTemplates(t *testing.T) {
	tests := []struct {
		name    string
		spec    v1alpha1.ModelRegistrySpec
		want    string
		wantErr bool
	}{
		{name: "role.yaml.tmpl"},
	}

	// parse all templates
	templates, err := config.ParseTemplates()
	if err != nil {
		t.Errorf("ParseTemplates() error = %v", err)
	}
	reconciler := controller.ModelRegistryReconciler{
		Log:         logr.Logger{},
		Template:    templates,
		IsOpenShift: true,
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := controller.ModelRegistryParams{
				Name:      "test",
				Namespace: "test-namespace",
				Spec:      &tt.spec,
			}
			var result rbac.Role
			err := reconciler.Apply(&params, tt.name, &result)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseTemplates() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if result.Name != fmt.Sprintf("registry-user-%s", params.Name) {
				t.Errorf("ParseTemplates() got = %v, want %v", result.Name, fmt.Sprintf("registry-user-%s", params.Name))
			}

			if result.Namespace != params.Namespace {
				t.Errorf("ParseTemplates() got = %v, want %v", result.Namespace, params.Namespace)
			}
		})
	}
}

func TestSetGetDefaultAudiences(t *testing.T) {
	tests := []struct {
		name      string
		audiences []string
	}{
		{name: "test1", audiences: []string{"audience1", "audience2"}},
		{name: "test2", audiences: []string{"audience3", "audience4"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config.SetDefaultAudiences(tt.audiences)
			if got := config.GetDefaultAudiences(); len(got) != len(tt.audiences) {
				t.Errorf("GetDefaultAudiences() = %v, want %v", got, tt.audiences)
			}
		})
	}
}

func TestSetGetDefaultAuthProvider(t *testing.T) {
	tests := []struct {
		name     string
		provider string
	}{
		{name: "test1", provider: "provider1"},
		{name: "test2", provider: "provider2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config.SetDefaultAuthProvider(tt.provider)
			if got := config.GetDefaultAuthProvider(); got != tt.provider {
				t.Errorf("GetDefaultAuthProvider() = %v, want %v", got, tt.provider)
			}
		})
	}
}

func TestSetGetDefaultAuthConfigLabels(t *testing.T) {
	tests := []struct {
		name   string
		labels string
	}{
		{name: "test1", labels: "label1"},
		{name: "test2", labels: "label2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config.SetDefaultAuthConfigLabels(tt.labels)
			if got := config.GetDefaultAuthConfigLabels(); len(got) != 1 {
				t.Errorf("GetDefaultAuthConfigLabels() = %v, want %v", got, tt.labels)
			}
		})
	}
}

func TestSetGetDefaultCert(t *testing.T) {
	tests := []struct {
		name string
		cert string
	}{
		{name: "test1", cert: "cert1"},
		{name: "test2", cert: "cert2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config.SetDefaultCert(tt.cert)
			if got := config.GetDefaultCert(); got != tt.cert {
				t.Errorf("GetDefaultCert() = %v, want %v", got, tt.cert)
			}
		})
	}
}

var _ = Describe("Defaults integration tests", func() {
	Describe("TestSetGetDefaultDomain", func() {
		It("Should return the set domain on openshift", func() {
			config.SetDefaultDomain("domain1", k8sClient, true)

			Expect(config.GetDefaultDomain()).To(Equal("domain1"))
		})

		It("Should return the set domain on non-openshift", func() {
			config.SetDefaultDomain("domain2", k8sClient, false)

			Expect(config.GetDefaultDomain()).To(Equal("domain2"))
		})

		It("Should return the domain from ingress when no domain is set", func() {
			config.SetDefaultDomain("", k8sClient, true)

			Expect(config.GetDefaultDomain()).To(Equal("domain3"))
		})
	})
})
