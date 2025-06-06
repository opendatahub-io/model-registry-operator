package config_test

import (
	"fmt"
	"github.com/opendatahub-io/model-registry-operator/api/v1beta1"
	"os"
	"testing"

	"github.com/go-logr/logr"
	"github.com/opendatahub-io/model-registry-operator/internal/controller"
	"github.com/opendatahub-io/model-registry-operator/internal/controller/config"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	rbac "k8s.io/api/rbac/v1"
)

func TestGetStringConfigWithDefault(t *testing.T) {
	tests := []struct {
		name         string
		configName   string
		defaultValue string
		want         string
	}{
		{name: "test " + config.GrpcImage, configName: config.GrpcImage, defaultValue: config.DefaultGrpcImage, want: "success1"},
		{name: "test " + config.RestImage, configName: config.RestImage, defaultValue: config.DefaultRestImage, want: "success2"},
		{name: "test " + config.OAuthProxyImage, configName: config.OAuthProxyImage, defaultValue: config.DefaultOAuthProxyImage, want: "success3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// test env variable not set or blank
			os.Setenv(tt.configName, "")
			if got := config.GetStringConfigWithDefault(tt.configName, tt.defaultValue); got != tt.defaultValue {
				t.Errorf("GetStringConfigWithDefault() = %v, want %v", got, tt.want)
			}
			// test env variable set
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
		spec    v1beta1.ModelRegistrySpec
		want    string
		wantErr bool
	}{
		{name: "role.yaml.tmpl"},
	}

	// parse all templates
	templates, err := config.ParseTemplates()
	if err != nil {
		t.Errorf("ParseTemplates() error = %v", err)
		t.FailNow() // exit this test, otherwise a panic error in Apply is not reported as a failure
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

func TestSetRegistriesNamespace(t *testing.T) {
	type args struct {
		namespace string
	}
	tests := []struct {
		name    string
		args    args
		wantErr bool
	}{
		{"empty namespace", args{""}, false},
		{"valid namespace", args{"valid-namespace"}, false},
		{"invalid namespace", args{"invalid//namespace"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := config.SetRegistriesNamespace(tt.args.namespace); (err != nil) != tt.wantErr {
				t.Errorf("SetRegistriesNamespace() error = %v, wantErr %v", err, tt.wantErr)
			}
			if res := config.GetRegistriesNamespace(); !tt.wantErr && res != tt.args.namespace {
				t.Errorf("GetRegistriesNamespace() expected %s, received %s", tt.args.namespace, res)
			}
			config.SetRegistriesNamespace("")
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
