package controller

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	catalogv1alpha1 "github.com/opendatahub-io/model-registry-operator/api/catalog/v1alpha1"
	"github.com/opendatahub-io/model-registry-operator/api/v1beta1"
	"github.com/opendatahub-io/model-registry-operator/internal/controller/config"
	configv1 "github.com/openshift/api/config/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"
)

// Exercise the real reconciler and templates: a policy change must change the
// operand PodTemplate, while an invalid strict policy must preserve its spec.
func TestProxyTLSReconciliation(t *testing.T) {
	for _, kind := range []string{"registry", "catalog"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			var cli client.Client
			var run func() error
			var key client.ObjectKey
			if kind == "catalog" {
				catalog := &catalogv1alpha1.Catalog{ObjectMeta: metav1.ObjectMeta{Name: "catalog", Namespace: "proxy-tls", UID: "catalog"}}
				r := newDataImageTestReconciler(t, catalog, false)
				if err := configv1.AddToScheme(r.Scheme); err != nil {
					t.Fatal(err)
				}
				r.Capabilities.HasConfigAPI = true
				cli = r.Client
				key = client.ObjectKey{Name: catalogResourceName, Namespace: catalog.Namespace}
				run = func() error {
					_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(catalog)})
					return err
				}
			} else {
				scheme := runtime.NewScheme()
				for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, appsv1.AddToScheme, configv1.AddToScheme, v1beta1.AddToScheme} {
					if err := add(scheme); err != nil {
						t.Fatal(err)
					}
				}
				// Use the standard Kubernetes scheme for every resource managed by the reconciler.
				if err := clientgoscheme.AddToScheme(scheme); err != nil {
					t.Fatal(err)
				}
				registry := &v1beta1.ModelRegistry{ObjectMeta: metav1.ObjectMeta{Name: "registry", Namespace: "proxy-tls", UID: "registry"}, Spec: v1beta1.ModelRegistrySpec{
					Postgres: &v1beta1.PostgresConfig{Host: "database"}, KubeRBACProxy: &v1beta1.KubeRBACProxyConfig{},
				}}
				registry.Default()
				restPort, proxyPort, routePort, dbPort := int32(8080), int32(8443), int32(443), int32(5432)
				registry.Spec.Rest.Port = &restPort
				registry.Spec.KubeRBACProxy.Port = &proxyPort
				registry.Spec.KubeRBACProxy.RoutePort = &routePort
				registry.Spec.Postgres.Port = &dbPort
				cli = fake.NewClientBuilder().WithScheme(scheme).WithObjects(registry).WithStatusSubresource(registry).Build()
				templates, err := config.ParseTemplates()
				if err != nil {
					t.Fatal(err)
				}
				r := &ModelRegistryReconciler{Client: cli, Scheme: scheme, Template: templates, EnableWebhooks: true, Recorder: &events.FakeRecorder{}, Log: logr.Discard(), Capabilities: ClusterCapabilities{HasConfigAPI: true}}
				key = client.ObjectKeyFromObject(registry)
				run = func() error { _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: key}); return err }
			}
			apiServer := &configv1.APIServer{ObjectMeta: metav1.ObjectMeta{Name: "cluster"}, Spec: configv1.APIServerSpec{
				TLSAdherence: configv1.TLSAdherencePolicyStrictAllComponents,
				TLSSecurityProfile: &configv1.TLSSecurityProfile{Type: configv1.TLSProfileCustomType, Custom: &configv1.CustomTLSProfile{TLSProfileSpec: configv1.TLSProfileSpec{
					MinTLSVersion: configv1.VersionTLS12, Ciphers: []string{"ECDHE-RSA-AES128-GCM-SHA256"}, Groups: []configv1.TLSGroup{"secp256r1"},
				}}},
			}}
			if err := cli.Create(ctx, apiServer); err != nil {
				t.Fatal(err)
			}
			getDeployment := func() *appsv1.Deployment {
				t.Helper()
				d := &appsv1.Deployment{}
				if err := cli.Get(ctx, key, d); err != nil {
					t.Fatal(err)
				}
				return d
			}
			assertArg := func(d *appsv1.Deployment, arg string) {
				t.Helper()
				for _, c := range d.Spec.Template.Spec.Containers {
					if c.Name == "kube-rbac-proxy" && slices.Contains(c.Args, arg) {
						return
					}
				}
				t.Fatalf("proxy missing %q: %+v", arg, d.Spec.Template.Spec.Containers)
			}
			if err := run(); err != nil {
				t.Fatal(err)
			}
			initial := getDeployment()
			assertArg(initial, "--tls-min-version=VersionTLS12")
			assertArg(initial, "--tls-cipher-suites=TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256")
			assertArg(initial, "--tls-curve-preferences=23")
			apiServer.Spec.TLSSecurityProfile = &configv1.TLSSecurityProfile{Type: configv1.TLSProfileModernType}
			if err := cli.Update(ctx, apiServer); err != nil {
				t.Fatal(err)
			}
			if err := run(); err != nil {
				t.Fatal(err)
			}
			modern := getDeployment()
			assertArg(modern, "--tls-min-version=VersionTLS13")
			if reflect.DeepEqual(initial.Spec.Template, modern.Spec.Template) {
				t.Fatal("profile change did not change PodTemplate")
			}
			for _, c := range modern.Spec.Template.Spec.Containers {
				if c.Name == "kube-rbac-proxy" {
					for _, arg := range c.Args {
						if strings.HasPrefix(arg, "--tls-cipher-suites=") {
							t.Fatalf("TLS13 received cipher restriction: %s", arg)
						}
					}
				}
			}
			apiServer.Spec.TLSSecurityProfile = &configv1.TLSSecurityProfile{Type: configv1.TLSProfileCustomType}
			if err := cli.Update(ctx, apiServer); err != nil {
				t.Fatal(err)
			}
			if err := run(); err == nil {
				t.Fatal("invalid strict profile was accepted")
			}
			if after := getDeployment(); !reflect.DeepEqual(modern.Spec, after.Spec) {
				t.Fatal("invalid policy changed the workload")
			}
			// The shipped proxy always configures HTTP/2. Its TLS 1.2 server
			// cannot start with an AES-256-only cipher list; resolve this before
			// Kubernetes replaces the previously healthy workload.
			apiServer.Spec.TLSSecurityProfile = &configv1.TLSSecurityProfile{Type: configv1.TLSProfileCustomType, Custom: &configv1.CustomTLSProfile{TLSProfileSpec: configv1.TLSProfileSpec{
				MinTLSVersion: configv1.VersionTLS12, Ciphers: []string{"ECDHE-RSA-AES256-GCM-SHA384"},
			}}}
			if err := cli.Update(ctx, apiServer); err != nil {
				t.Fatal(err)
			}
			if err := run(); err == nil {
				t.Fatal("profile incompatible with proxy HTTP/2 startup was accepted")
			}
			if after := getDeployment(); !reflect.DeepEqual(modern.Spec, after.Spec) {
				t.Fatal("unrepresentable HTTP/2 policy changed the workload")
			}
			apiServer.Spec.TLSAdherence = configv1.TLSAdherencePolicyLegacyAdheringComponentsOnly
			if err := cli.Update(ctx, apiServer); err != nil {
				t.Fatal(err)
			}
			if err := run(); err != nil {
				t.Fatal(err)
			}
			fallback := getDeployment()
			assertArg(fallback, "--tls-min-version=VersionTLS12")
			if err := run(); err != nil {
				t.Fatal(err)
			}
			if after := getDeployment(); !reflect.DeepEqual(fallback.Spec.Template, after.Spec.Template) {
				t.Fatal("unchanged policy changed PodTemplate")
			}
		})
	}
}

func TestProxyTLSWatchPredicate(t *testing.T) {
	p := proxyTLSChangedPredicate()
	a := &configv1.APIServer{ObjectMeta: metav1.ObjectMeta{Name: "cluster"}}
	if !p.Create(event.CreateEvent{Object: a}) || !p.Delete(event.DeleteEvent{Object: a}) {
		t.Fatal("creation/deletion must reconcile fallback changes")
	}
	b := a.DeepCopy()
	b.Labels = map[string]string{"unrelated": "change"}
	if p.Update(event.UpdateEvent{ObjectOld: a, ObjectNew: b}) {
		t.Fatal("unrelated metadata must not roll out proxies")
	}
	b.Spec.TLSAdherence = configv1.TLSAdherencePolicyStrictAllComponents
	if !p.Update(event.UpdateEvent{ObjectOld: a, ObjectNew: b}) {
		t.Fatal("adherence change ignored")
	}
	b = a.DeepCopy()
	b.Spec.TLSSecurityProfile = &configv1.TLSSecurityProfile{Type: configv1.TLSProfileModernType}
	if !p.Update(event.UpdateEvent{ObjectOld: a, ObjectNew: b}) {
		t.Fatal("profile change ignored")
	}
	b.Name = "other"
	if p.Create(event.CreateEvent{Object: b}) {
		t.Fatal("non-cluster APIServer must be ignored")
	}
}
