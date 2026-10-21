package controller

import (
	"path/filepath"
	"testing"

	catalogv1alpha1 "github.com/opendatahub-io/model-registry-operator/api/catalog/v1alpha1"
	"github.com/opendatahub-io/model-registry-operator/api/v1beta1"
	"github.com/opendatahub-io/model-registry-operator/internal/controller/config"
	"github.com/opendatahub-io/odh-platform-utilities/pkg/render/kustomize"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Exercise the shipping overlay and AIHub image projection, then use each
// rendered manager's environment to produce its actual operand Deployment.
// This checks the development default and preserves product digest overrides
// through both boundaries.
func TestProxyTLSImagesThroughAIHubOverlay(t *testing.T) {
	const upstream = "quay.io/opendatahub/odh-kube-rbac-proxy:odh-stable"
	const product = "quay.io/rhoai/odh-kube-rbac-proxy-rhel9@sha256:708ae79bb67a98d9f5196a1a9de62ba4f60aabdeadd908e25dfd2afc1bf43f9c"
	renderPath := filepath.Join(assembleManifests(t), "modelregistry", "overlays", "odh")
	for _, tc := range []struct{ name, override, want string }{
		{name: "upstream default", want: upstream},
		{name: "product override", override: product, want: product},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resources, err := kustomize.Render(renderPath, nil, kustomize.WithNamespace("proxy-images"))
			if err != nil {
				t.Fatal(err)
			}
			images := ResolveChildImages(fakeGetenv(map[string]string{config.KubeRBACProxyImage: tc.override}))
			found := map[string]bool{}
			for i := range resources {
				u := &resources[i]
				if u.GetKind() != "Deployment" || (u.GetName() != childDeploymentName && u.GetName() != catalogDeploymentName) {
					continue
				}
				if err := stampChildOperatorDeployment(u, images, "proxy-images", "proxy-images", ""); err != nil {
					t.Fatal(err)
				}
				manager := findContainer(t, deploymentFromUnstructured(t, u), "manager")
				proxyImage := ""
				for _, env := range manager.Env {
					if env.Name == config.KubeRBACProxyImage {
						proxyImage = env.Value
					}
				}
				if proxyImage != tc.want {
					t.Fatalf("%s manager proxy image = %q, want %q", u.GetName(), proxyImage, tc.want)
				}
				t.Setenv(config.KubeRBACProxyImage, proxyImage)
				templates, err := config.ParseTemplates()
				if err != nil {
					t.Fatal(err)
				}
				operand := &appsv1.Deployment{}
				if u.GetName() == childDeploymentName {
					restPort, proxyPort, routePort, dbPort := int32(8080), int32(8443), int32(443), int32(5432)
					registry := &v1beta1.ModelRegistry{ObjectMeta: metav1.ObjectMeta{Name: "registry", Namespace: "proxy-images"}, Spec: v1beta1.ModelRegistrySpec{
						Rest:          v1beta1.RestSpec{Port: &restPort},
						Postgres:      &v1beta1.PostgresConfig{Host: "database", Port: &dbPort},
						KubeRBACProxy: &v1beta1.KubeRBACProxyConfig{Port: &proxyPort, RoutePort: &routePort},
					}}
					registry.Default()
					registry.RuntimeDefaults()
					r := &ModelRegistryReconciler{Template: templates}
					err = r.Apply(&ModelRegistryParams{Name: registry.Name, Namespace: registry.Namespace, Spec: &registry.Spec}, "deployment.yaml.tmpl", operand)
				} else {
					r := &CatalogReconciler{Template: templates}
					catalog := &catalogv1alpha1.Catalog{ObjectMeta: metav1.ObjectMeta{Name: "catalog", Namespace: "proxy-images"}}
					err = r.Apply(r.buildCatalogParams(catalog, nil, nil, nil, ""), "catalog-deployment.yaml.tmpl", operand)
				}
				if err != nil {
					t.Fatal(err)
				}
				proxy := findContainer(t, operand, "kube-rbac-proxy")
				if proxy.Image != tc.want {
					t.Fatalf("%s operand proxy image = %q, want %q", u.GetName(), proxy.Image, tc.want)
				}
				found[u.GetName()] = true
			}
			for _, name := range []string{childDeploymentName, catalogDeploymentName} {
				if !found[name] {
					t.Errorf("rendered overlay missing %s", name)
				}
			}
		})
	}
}
