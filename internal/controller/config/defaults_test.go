package config_test

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	"github.com/opendatahub-io/model-registry-operator/api/v1beta1"
	"github.com/opendatahub-io/model-registry-operator/internal/controller"
	"github.com/opendatahub-io/model-registry-operator/internal/controller/config"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbac "k8s.io/api/rbac/v1"
)

func TestGetStringConfigWithDefault(t *testing.T) {
	tests := []struct {
		name         string
		configName   string
		defaultValue string
		want         string
	}{
		{name: "test " + config.RestImage, configName: config.RestImage, defaultValue: config.DefaultRestImage, want: "success1"},
		{name: "test " + config.OAuthProxyImage, configName: config.OAuthProxyImage, defaultValue: config.DefaultOAuthProxyImage, want: "success2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// test env variable not set or blank
			t.Setenv(tt.configName, "")
			if got := config.GetStringConfigWithDefault(tt.configName, tt.defaultValue); got != tt.defaultValue {
				t.Errorf("GetStringConfigWithDefault() = %v, want %v", got, tt.want)
			}
			// test env variable set
			t.Setenv(tt.configName, tt.want)
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

func TestKubeRBACProxyTemplates(t *testing.T) {
	// parse all templates
	templates, err := config.ParseTemplates()
	if err != nil {
		t.Errorf("ParseTemplates() error = %v", err)
		t.FailNow()
	}
	reconciler := controller.ModelRegistryReconciler{
		Log:         logr.Logger{},
		Template:    templates,
		IsOpenShift: true,
	}

	httpsPort := int32(8443)
	routePort := int32(443)

	params := controller.ModelRegistryParams{
		Name:      "test-registry",
		Namespace: "test-namespace",
		Spec: &v1beta1.ModelRegistrySpec{
			KubeRBACProxy: &v1beta1.KubeRBACProxyConfig{
				Port:      &httpsPort,
				RoutePort: &routePort,
				Image:     "quay.io/openshift/origin-kube-rbac-proxy:latest",
				Domain:    "example.com",
			},
		},
	}

	t.Run("kube-rbac-proxy-config.yaml.tmpl", func(t *testing.T) {
		var result corev1.ConfigMap
		err := reconciler.Apply(&params, "kube-rbac-proxy-config.yaml.tmpl", &result)
		if err != nil {
			t.Errorf("Apply() error = %v", err)
			return
		}

		if result.Name != "test-registry-kube-rbac-proxy-config" {
			t.Errorf("ConfigMap name = %v, want test-registry-kube-rbac-proxy-config", result.Name)
		}

		if result.Namespace != params.Namespace {
			t.Errorf("ConfigMap namespace = %v, want %v", result.Namespace, params.Namespace)
		}

		// Check that SAR configuration is present
		configData, exists := result.Data["config-file.yaml"]
		if !exists {
			t.Errorf("ConfigMap should contain config-file.yaml data")
		}

		if !strings.Contains(configData, "authorization:") {
			t.Errorf("Config should contain authorization section")
		}

		if !strings.Contains(configData, "resourceAttributes:") {
			t.Errorf("Config should contain resourceAttributes section")
		}
	})

	t.Run("kube-rbac-proxy-role-binding.yaml.tmpl", func(t *testing.T) {
		var result rbac.ClusterRoleBinding
		err := reconciler.Apply(&params, "kube-rbac-proxy-role-binding.yaml.tmpl", &result)
		if err != nil {
			t.Errorf("Apply() error = %v", err)
			return
		}

		if result.Name != "test-registry-auth-delegator" {
			t.Errorf("ClusterRoleBinding name = %v, want test-registry-auth-delegator", result.Name)
		}

		if result.RoleRef.Name != "system:auth-delegator" {
			t.Errorf("RoleRef name = %v, want system:auth-delegator", result.RoleRef.Name)
		}

		if len(result.Subjects) != 1 || result.Subjects[0].Name != "test-registry" {
			t.Errorf("Subject should be test-registry ServiceAccount")
		}
	})
}

func TestKubeRBACProxyDeploymentGeneration(t *testing.T) {
	// parse all templates
	templates, err := config.ParseTemplates()
	if err != nil {
		t.Errorf("ParseTemplates() error = %v", err)
		t.FailNow()
	}
	reconciler := controller.ModelRegistryReconciler{
		Log:         logr.Logger{},
		Template:    templates,
		IsOpenShift: true,
	}

	httpsPort := int32(8443)
	restPort := int32(8080)
	routePort := int32(443)

	params := controller.ModelRegistryParams{
		Name:      "test-registry",
		Namespace: "test-namespace",
		Spec: &v1beta1.ModelRegistrySpec{
			Rest: v1beta1.RestSpec{
				Port:  &restPort,
				Image: "quay.io/opendatahub/model-registry:latest",
			},
			KubeRBACProxy: &v1beta1.KubeRBACProxyConfig{
				Port:      &httpsPort,
				RoutePort: &routePort,
				Image:     "quay.io/openshift/origin-kube-rbac-proxy:latest",
				Domain:    "example.com",
				// RuntimeDefaults sets these, so they must be present for template rendering
				TLSCertificateSecret: &v1beta1.SecretKeyValue{
					Name: "test-registry-kube-rbac-proxy",
					Key:  "tls.crt",
				},
				TLSKeySecret: &v1beta1.SecretKeyValue{
					Name: "test-registry-kube-rbac-proxy",
					Key:  "tls.key",
				},
			},
		},
	}

	t.Run("deployment.yaml.tmpl with kube-rbac-proxy", func(t *testing.T) {
		var result appsv1.Deployment
		err := reconciler.Apply(&params, "deployment.yaml.tmpl", &result)
		if err != nil {
			t.Errorf("Apply() error = %v", err)
			return
		}

		if result.Name != "test-registry" {
			t.Errorf("Deployment name = %v, want test-registry", result.Name)
		}

		if result.Namespace != params.Namespace {
			t.Errorf("Deployment namespace = %v, want %v", result.Namespace, params.Namespace)
		}

		// Check that kube-rbac-proxy container is present
		var kubeRBACProxyContainer *corev1.Container
		for _, container := range result.Spec.Template.Spec.Containers {
			if container.Name == "kube-rbac-proxy" {
				kubeRBACProxyContainer = &container
				break
			}
		}

		if kubeRBACProxyContainer == nil {
			t.Errorf("kube-rbac-proxy container should be present in deployment")
		}

		// Check kube-rbac-proxy specific arguments
		expectedArgs := []string{
			"--secure-listen-address=0.0.0.0:8443",
			"--upstream=http://127.0.0.1:8080/",
			"--config-file=/etc/kube-rbac-proxy/config-file.yaml",
		}

		for _, expectedArg := range expectedArgs {
			found := false
			for _, arg := range kubeRBACProxyContainer.Args {
				if arg == expectedArg {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("Expected argument %s not found in kube-rbac-proxy container args: %v", expectedArg, kubeRBACProxyContainer.Args)
			}
		}

		// Check that oauth-proxy specific args are NOT present
		for _, arg := range kubeRBACProxyContainer.Args {
			if strings.Contains(arg, "--provider=openshift") {
				t.Errorf("Should not contain oauth-proxy specific arg: %s", arg)
			}
			if strings.Contains(arg, "--cookie-secret") {
				t.Errorf("Should not contain oauth-proxy specific arg: %s", arg)
			}
		}

		// Check that OAuth proxy container is NOT present
		for _, container := range result.Spec.Template.Spec.Containers {
			if container.Name == "oauth-proxy" {
				t.Errorf("oauth-proxy container should not be present when using KubeRBACProxy")
			}
		}

		// Check that kube-rbac-proxy config volume is mounted
		foundConfigMount := false
		for _, mount := range kubeRBACProxyContainer.VolumeMounts {
			if mount.MountPath == "/etc/kube-rbac-proxy" {
				foundConfigMount = true
				break
			}
		}
		if !foundConfigMount {
			t.Errorf("kube-rbac-proxy config volume mount not found")
		}
	})
}

func TestDeploymentWithResources(t *testing.T) {
	// parse all templates
	templates, err := config.ParseTemplates()
	if err != nil {
		t.Errorf("ParseTemplates() error = %v", err)
		t.FailNow()
	}
	reconciler := controller.ModelRegistryReconciler{
		Log:         logr.Logger{},
		Template:    templates,
		IsOpenShift: true,
	}

	httpsPort := int32(8443)
	restPort := int32(8080)
	routePort := int32(443)

	params := controller.ModelRegistryParams{
		Name:      "test-registry",
		Namespace: "test-namespace",
		Spec: &v1beta1.ModelRegistrySpec{
			Rest: v1beta1.RestSpec{
				Port:  &restPort,
				Image: "quay.io/opendatahub/model-registry:latest",
				Resources: &corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						"cpu":    config.ModelRegistryRestResourceRequirements.Requests["cpu"],
						"memory": config.ModelRegistryRestResourceRequirements.Requests["memory"],
					},
					Limits: corev1.ResourceList{
						"cpu":    config.ModelRegistryRestResourceRequirements.Limits["cpu"],
						"memory": config.ModelRegistryRestResourceRequirements.Limits["memory"],
					},
				},
			},
			KubeRBACProxy: &v1beta1.KubeRBACProxyConfig{
				Port:      &httpsPort,
				RoutePort: &routePort,
				Image:     "quay.io/openshift/origin-kube-rbac-proxy:latest",
				Domain:    "example.com",
				TLSCertificateSecret: &v1beta1.SecretKeyValue{
					Name: "test-registry-kube-rbac-proxy",
					Key:  "tls.crt",
				},
				TLSKeySecret: &v1beta1.SecretKeyValue{
					Name: "test-registry-kube-rbac-proxy",
					Key:  "tls.key",
				},
			},
		},
	}

	t.Run("deployment.yaml.tmpl with resources", func(t *testing.T) {
		var result appsv1.Deployment
		err := reconciler.Apply(&params, "deployment.yaml.tmpl", &result)
		if err != nil {
			t.Errorf("Apply() error = %v", err)
			return
		}

		// Find the REST container
		var restContainer *corev1.Container
		for i, container := range result.Spec.Template.Spec.Containers {
			if container.Name == "rest-container" {
				restContainer = &result.Spec.Template.Spec.Containers[i]
				break
			}
		}

		if restContainer == nil {
			t.Errorf("rest-container should be present in deployment")
			return
		}

		// Verify resources are set
		if restContainer.Resources.Requests == nil {
			t.Errorf("Resources.Requests should be set")
			return
		}

		if restContainer.Resources.Limits == nil {
			t.Errorf("Resources.Limits should be set")
			return
		}

		// Check CPU and memory requests
		cpuRequest := restContainer.Resources.Requests["cpu"]
		if cpuRequest.IsZero() {
			t.Errorf("CPU request should not be zero")
		}

		memoryRequest := restContainer.Resources.Requests["memory"]
		if memoryRequest.IsZero() {
			t.Errorf("Memory request should not be zero")
		}

		// Check CPU and memory limits
		memoryLimit := restContainer.Resources.Limits["memory"]
		if memoryLimit.IsZero() {
			t.Errorf("Memory limit should not be zero")
		}
	})
}

func TestCatalogDeployment(t *testing.T) {
	// Clear any env vars from previous tests
	os.Unsetenv(config.RestImage)

	// parse all templates
	templates, err := config.ParseTemplates()
	if err != nil {
		t.Errorf("ParseTemplates() error = %v", err)
		t.FailNow()
	}

	config.SetDefaultDomain("example.com", nil, false)

	catalogReconciler := controller.ModelCatalogReconciler{
		Log:         logr.Logger{},
		Template:    templates,
		IsOpenShift: true,
	}

	params := controller.ModelCatalogParams{
		Name:      "model-catalog",
		Namespace: "test-namespace",
		Component: "model-catalog",
	}

	t.Run("catalog-deployment.yaml.tmpl", func(t *testing.T) {
		var result appsv1.Deployment
		err := catalogReconciler.Apply(&params, "catalog-deployment.yaml.tmpl", &result)
		if err != nil {
			t.Errorf("Apply() error = %v", err)
			return
		}

		if result.Name != "model-catalog" {
			t.Errorf("Deployment name = %v, want model-catalog", result.Name)
		}

		if result.Namespace != params.Namespace {
			t.Errorf("Deployment namespace = %v, want %v", result.Namespace, params.Namespace)
		}

		// Find the catalog container
		var catalogContainer *corev1.Container
		for i, container := range result.Spec.Template.Spec.Containers {
			if container.Name == "catalog" {
				catalogContainer = &result.Spec.Template.Spec.Containers[i]
				break
			}
		}

		if catalogContainer == nil {
			t.Errorf("catalog container should be present in deployment")
			return
		}

		// Verify image is set (it should be the default image)
		if catalogContainer.Image == "" {
			t.Errorf("Catalog container image should be set")
		}

		// Check for kube-rbac-proxy container
		var kubeRBACProxyContainer *corev1.Container
		for i, container := range result.Spec.Template.Spec.Containers {
			if container.Name == "kube-rbac-proxy" {
				kubeRBACProxyContainer = &result.Spec.Template.Spec.Containers[i]
				break
			}
		}

		if kubeRBACProxyContainer == nil {
			t.Errorf("kube-rbac-proxy container should be present in catalog deployment")
			return
		}

		// Verify kube-rbac-proxy has correct args
		hasSecureListenAddr := false
		hasUpstream := false
		for _, arg := range kubeRBACProxyContainer.Args {
			if strings.HasPrefix(arg, "--secure-listen-address=") {
				hasSecureListenAddr = true
			}
			if strings.HasPrefix(arg, "--upstream=") {
				hasUpstream = true
			}
		}

		if !hasSecureListenAddr {
			t.Errorf("kube-rbac-proxy should have --secure-listen-address argument")
		}
		if !hasUpstream {
			t.Errorf("kube-rbac-proxy should have --upstream argument")
		}
	})
}

func TestCatalogPostgresSecret(t *testing.T) {
	// Clear any env vars from previous tests
	os.Unsetenv(config.CatalogPostgresUser)
	os.Unsetenv(config.CatalogPostgresPassword)
	os.Unsetenv(config.CatalogPostgresDatabase)

	// parse all templates
	templates, err := config.ParseTemplates()
	if err != nil {
		t.Errorf("ParseTemplates() error = %v", err)
		t.FailNow()
	}

	catalogReconciler := controller.ModelCatalogReconciler{
		Log:         logr.Logger{},
		Template:    templates,
		IsOpenShift: true,
	}

	params := controller.ModelCatalogParams{
		Name:      "model-catalog",
		Namespace: "test-namespace",
		Component: "model-catalog-postgres",
	}

	t.Run("catalog-postgres-secret.yaml.tmpl", func(t *testing.T) {
		var result corev1.Secret
		err := catalogReconciler.Apply(&params, "catalog-postgres-secret.yaml.tmpl", &result)
		if err != nil {
			t.Errorf("Apply() error = %v", err)
			return
		}

		// Verify secret metadata
		if result.Name != "model-catalog-postgres" {
			t.Errorf("Secret name = %v, want model-catalog-postgres", result.Name)
		}

		if result.Namespace != params.Namespace {
			t.Errorf("Secret namespace = %v, want %v", result.Namespace, params.Namespace)
		}

		// Verify labels
		expectedLabels := map[string]string{
			"app":                          "model-catalog-postgres",
			"component":                    "model-catalog-postgres",
			"app.kubernetes.io/name":       "model-catalog-postgres",
			"app.kubernetes.io/instance":   "model-catalog-postgres",
			"app.kubernetes.io/component":  "database",
			"app.kubernetes.io/created-by": "model-registry-operator",
			"app.kubernetes.io/part-of":    "model-catalog",
			"app.kubernetes.io/managed-by": "model-registry-operator",
		}

		for key, expectedValue := range expectedLabels {
			if result.Labels[key] != expectedValue {
				t.Errorf("Label %s = %v, want %v", key, result.Labels[key], expectedValue)
			}
		}

		// Verify annotations
		expectedAnnotations := map[string]string{
			"template.openshift.io/expose-database_name": "{.data['database-name']}",
			"template.openshift.io/expose-password":      "{.data['database-password']}",
			"template.openshift.io/expose-username":      "{.data['database-user']}",
		}

		for key, expectedValue := range expectedAnnotations {
			if result.Annotations[key] != expectedValue {
				t.Errorf("Annotation %s = %v, want %v", key, result.Annotations[key], expectedValue)
			}
		}

		// Verify data fields exist and contain base64 encoded values
		if result.Data == nil {
			t.Errorf("Secret data should not be nil")
			return
		}

		requiredDataKeys := []string{"database-name", "database-password", "database-user"}
		for _, key := range requiredDataKeys {
			if _, exists := result.Data[key]; !exists {
				t.Errorf("Secret should contain data key: %s", key)
			}
		}

		// Verify the base64 encoded values decode to expected defaults
		expectedValues := map[string]string{
			"database-name":     config.DefaultCatalogPostgresDatabase,
			"database-password": config.DefaultCatalogPostgresPassword,
			"database-user":     config.DefaultCatalogPostgresUser,
		}

		for dataKey, expectedValue := range expectedValues {
			if encodedValue, exists := result.Data[dataKey]; exists {
				decodedValue := string(encodedValue)
				if decodedValue != expectedValue {
					t.Errorf("Decoded %s = %v, want %v", dataKey, decodedValue, expectedValue)
				}
			}
		}
	})
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
