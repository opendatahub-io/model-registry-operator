package controller

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	goruntime "runtime"
	"slices"
	"testing"
	"time"

	"github.com/go-logr/logr"
	catalogv1alpha1 "github.com/opendatahub-io/model-registry-operator/api/catalog/v1alpha1"
	"github.com/opendatahub-io/model-registry-operator/api/v1beta1"
	"github.com/opendatahub-io/model-registry-operator/internal/controller/config"
	configv1 "github.com/openshift/api/config/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	controllerconfig "sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/yaml"
)

func TestProxyTLSChangeEnqueuesAllNamespaces(t *testing.T) {
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{v1beta1.AddToScheme, catalogv1alpha1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	var objects []client.Object
	want := map[client.ObjectKey]bool{}
	for _, key := range []client.ObjectKey{{Namespace: "registries-a", Name: "shared-name"}, {Namespace: "registries-b", Name: "shared-name"}, {Namespace: "registries-b", Name: "another"}} {
		objects = append(objects,
			&v1beta1.ModelRegistry{ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace}},
			&catalogv1alpha1.Catalog{ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace}})
		want[key] = true
	}
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
	for _, tc := range []struct {
		name        string
		mapRequests func(context.Context, client.Object) []reconcile.Request
	}{
		{"registries", (&ModelRegistryReconciler{Client: cli}).registriesForTLSChange},
		{"catalogs", (&CatalogReconciler{Client: cli}).catalogsForTLSChange},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := map[client.ObjectKey]bool{}
			requests := tc.mapRequests(t.Context(), &configv1.APIServer{ObjectMeta: metav1.ObjectMeta{Name: "cluster"}})
			for _, req := range requests {
				if got[req.NamespacedName] {
					t.Errorf("duplicate request: %v", req)
				}
				got[req.NamespacedName] = true
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("requests = %v, want all managed CRs %v", got, want)
			}
		})
	}
}

// Use the production SetupWithManager methods and real API-server watch events.
// The APIServer fixture deliberately has no operator labels, as on OpenShift.
func TestProxyTLSAPIServerWatch_Envtest(t *testing.T) {
	assets := os.Getenv("KUBEBUILDER_ASSETS")
	if assets == "" {
		assets = filepath.Join("..", "..", "bin", "k8s", fmt.Sprintf("1.35.0-%s-%s", goruntime.GOOS, goruntime.GOARCH))
	}
	if _, err := os.Stat(filepath.Join(assets, "kube-apiserver")); err != nil {
		t.Skipf("envtest binaries unavailable: %v", err)
	}
	scheme := testScheme(t)
	for _, add := range []func(*runtime.Scheme) error{v1beta1.AddToScheme, configv1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	preserve := true
	apiServerCRD := &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "apiservers.config.openshift.io"},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: "config.openshift.io", Scope: apiextensionsv1.ClusterScoped,
			Names:    apiextensionsv1.CustomResourceDefinitionNames{Plural: "apiservers", Singular: "apiserver", Kind: "APIServer", ListKind: "APIServerList"},
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{{Name: "v1", Served: true, Storage: true, Schema: &apiextensionsv1.CustomResourceValidation{OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{Type: "object", Properties: map[string]apiextensionsv1.JSONSchemaProps{"spec": {Type: "object", XPreserveUnknownFields: &preserve}}}}}},
		},
	}
	useExisting := false
	catalogCRD := &apiextensionsv1.CustomResourceDefinition{}
	catalogManifest, err := os.ReadFile(filepath.Join("..", "..", "config", "overlays", "aihub", "aihub.opendatahub.io_catalogs.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(catalogManifest, catalogCRD); err != nil {
		t.Fatal(err)
	}
	env := &envtest.Environment{Scheme: scheme, BinaryAssetsDirectory: assets, UseExistingCluster: &useExisting, CRDs: []*apiextensionsv1.CustomResourceDefinition{apiServerCRD, catalogCRD}, CRDDirectoryPaths: []string{filepath.Join("..", "..", "config", "crd", "bases")}, ErrorIfCRDPathMissing: true}
	cfg, err := env.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := env.Stop(); err != nil {
			t.Errorf("stop envtest: %v", err)
		}
	}()
	// Separate managers prevent one operand's resource events from waking the other.
	for _, operandKind := range []string{"registry", "catalog"} {
		t.Run(operandKind, func(t *testing.T) {
			mgr, err := ctrl.NewManager(cfg, ctrl.Options{Scheme: scheme, Metrics: metricsserver.Options{BindAddress: "0"}, HealthProbeBindAddress: "0", Controller: controllerconfig.Controller{SkipNameValidation: new(true)}})
			if err != nil {
				t.Fatal(err)
			}
			templates, err := config.ParseTemplates()
			if err != nil {
				t.Fatal(err)
			}
			registry := &ModelRegistryReconciler{Client: mgr.GetClient(), Scheme: scheme, Template: templates, EnableWebhooks: true, Recorder: &events.FakeRecorder{}, Log: logr.Discard(), Capabilities: ClusterCapabilities{HasConfigAPI: true}}
			catalog := &CatalogReconciler{Client: mgr.GetClient(), Scheme: scheme, Template: templates, Recorder: &events.FakeRecorder{}, Log: logr.Discard(), SkipCatalogDBCreation: true, Capabilities: ClusterCapabilities{HasConfigAPI: true}}
			if operandKind == "registry" {
				if err := registry.SetupWithManager(mgr); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := catalog.SetupWithManager(mgr); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan error, 1)
			go func() { done <- mgr.Start(ctx) }()
			defer func() {
				cancel()
				select {
				case err := <-done:
					if err != nil {
						t.Errorf("manager: %v", err)
					}
				case <-time.After(10 * time.Second):
					t.Error("manager shutdown timed out")
				}
			}()
			if !mgr.GetCache().WaitForCacheSync(ctx) {
				t.Fatal("cache did not sync")
			}
			cli, err := client.New(cfg, client.Options{Scheme: scheme})
			if err != nil {
				t.Fatal(err)
			}
			policy := &configv1.APIServer{ObjectMeta: metav1.ObjectMeta{Name: "cluster"}, Spec: configv1.APIServerSpec{TLSAdherence: configv1.TLSAdherencePolicyStrictAllComponents, TLSSecurityProfile: &configv1.TLSSecurityProfile{Type: configv1.TLSProfileModernType}}}
			if err := cli.Create(ctx, policy); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := cli.Delete(context.Background(), policy); err != nil {
					t.Errorf("delete policy: %v", err)
				}
			}()
			var deployments []client.ObjectKey
			// Catalog has a fixed cluster-scoped auth-delegator binding. Keep
			// one instance here; the mapping test covers namespace fan-out.
			namespace := "proxy-watch-" + operandKind
			if err := cli.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}); err != nil {
				t.Fatal(err)
			}
			cr := &v1beta1.ModelRegistry{ObjectMeta: metav1.ObjectMeta{Name: "registry", Namespace: namespace}, Spec: v1beta1.ModelRegistrySpec{Postgres: &v1beta1.PostgresConfig{Host: "database"}, KubeRBACProxy: &v1beta1.KubeRBACProxyConfig{}}}
			if operandKind == "registry" {
				cr.Default()
				if err := cli.Create(ctx, cr); err != nil {
					t.Fatal(err)
				}
				deployments = append(deployments, client.ObjectKeyFromObject(cr))
			} else {
				if err := cli.Create(ctx, &catalogv1alpha1.Catalog{ObjectMeta: metav1.ObjectMeta{Name: "catalog", Namespace: namespace}}); err != nil {
					t.Fatal(err)
				}
				deployments = append(deployments, client.ObjectKey{Name: catalogResourceName, Namespace: namespace})
			}
			// Envtest has no workload or endpoints controllers. Supply their normal
			// readiness evidence so continuous readiness retries cannot mask a
			// missing APIServer watch.
			//nolint:staticcheck // The registry readiness check still reads Endpoints.
			if err := cli.Create(ctx, &corev1.Endpoints{ObjectMeta: metav1.ObjectMeta{Name: cr.Name, Namespace: namespace}, Subsets: []corev1.EndpointSubset{{Addresses: []corev1.EndpointAddress{{IP: "10.0.0.1"}}, Ports: []corev1.EndpointPort{{Port: 8080}}}}}); err != nil {
				t.Fatal(err)
			}
			ready := true
			if err := cli.Create(ctx, &discoveryv1.EndpointSlice{ObjectMeta: metav1.ObjectMeta{Name: "catalog-ready", Namespace: namespace, Labels: map[string]string{discoveryv1.LabelServiceName: catalogResourceName}}, AddressType: discoveryv1.AddressTypeIPv4, Endpoints: []discoveryv1.Endpoint{{Addresses: []string{"10.0.0.2"}, Conditions: discoveryv1.EndpointConditions{Ready: &ready}}}}); err != nil {
				t.Fatal(err)
			}
			waitForTLS := func(version string, timeout time.Duration) map[client.ObjectKey]corev1.PodTemplateSpec {
				t.Helper()
				observed := map[client.ObjectKey]corev1.PodTemplateSpec{}
				deadline := time.Now().Add(timeout)
				var last string
				for time.Now().Before(deadline) {
					for _, key := range deployments {
						d := &appsv1.Deployment{}
						if err := cli.Get(ctx, key, d); err != nil {
							last = err.Error()
							break
						}
						matched := false
						for _, container := range d.Spec.Template.Spec.Containers {
							if container.Name == "kube-rbac-proxy" && slices.Contains(container.Args, "--tls-min-version="+version) {
								matched = true
							}
						}
						if !matched {
							last = fmt.Sprintf("%v: proxy TLS version has not changed to %s", key, version)
							break
						}
						observed[key] = *d.Spec.Template.DeepCopy()
					}
					if len(observed) == len(deployments) {
						return observed
					}
					clear(observed)
					time.Sleep(50 * time.Millisecond)
				}
				t.Fatalf("waiting for APIServer watch to roll workloads: %s", last)
				return nil
			}
			settleWorkloads := func() {
				t.Helper()
				for _, key := range deployments {
					d := &appsv1.Deployment{}
					if err := cli.Get(ctx, key, d); err != nil {
						t.Fatal(err)
					}
					d.Status = completedCatalogDeploymentStatus(d)
					if err := cli.Status().Update(ctx, d); err != nil {
						t.Fatal(err)
					}
					pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: key.Name + "-ready", Namespace: key.Namespace, Labels: d.Spec.Template.Labels, Annotations: d.Spec.Template.Annotations}, Spec: d.Spec.Template.Spec}
					if err := cli.Create(ctx, pod); err != nil && !apierrors.IsAlreadyExists(err) {
						t.Fatal(err)
					}
					if err := cli.Get(ctx, client.ObjectKeyFromObject(pod), pod); err != nil {
						t.Fatal(err)
					}
					pod.Status.Phase = corev1.PodRunning
					pod.Status.ContainerStatuses = nil
					for _, c := range pod.Spec.Containers {
						pod.Status.ContainerStatuses = append(pod.Status.ContainerStatuses, corev1.ContainerStatus{Name: c.Name, Image: c.Image, ImageID: "fixture", Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}})
					}
					pod.Status.InitContainerStatuses = nil
					for _, c := range pod.Spec.InitContainers {
						pod.Status.InitContainerStatuses = append(pod.Status.InitContainerStatuses, corev1.ContainerStatus{Name: c.Name, Image: c.Image, ImageID: "fixture", Ready: true, State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}}})
					}
					if err := cli.Status().Update(ctx, pod); err != nil {
						t.Fatal(err)
					}
				}
				deadline := time.Now().Add(10 * time.Second)
				for time.Now().Before(deadline) {
					allReady := true
					for _, key := range deployments {
						if key.Name == catalogResourceName {
							cr := &catalogv1alpha1.Catalog{}
							if err := cli.Get(ctx, client.ObjectKey{Name: "catalog", Namespace: key.Namespace}, cr); err != nil {
								t.Fatal(err)
							}
							allReady = allReady && apimeta.IsStatusConditionTrue(cr.Status.Conditions, conditionWorkloadAvailable)
						} else {
							cr := &v1beta1.ModelRegistry{}
							if err := cli.Get(ctx, key, cr); err != nil {
								t.Fatal(err)
							}
							allReady = allReady && apimeta.IsStatusConditionTrue(cr.Status.Conditions, ConditionTypeAvailable)
						}
					}
					if allReady {
						time.Sleep(500 * time.Millisecond)
						return
					}
					time.Sleep(50 * time.Millisecond)
				}
				t.Fatal("workloads did not settle before policy edit")
			}
			initial := waitForTLS("VersionTLS13", 30*time.Second)
			settleWorkloads()
			policy.Spec.TLSSecurityProfile = &configv1.TLSSecurityProfile{Type: configv1.TLSProfileIntermediateType}
			if err := cli.Update(ctx, policy); err != nil {
				t.Fatal(err)
			}
			updated := waitForTLS("VersionTLS12", 5*time.Second)
			for key := range initial {
				if reflect.DeepEqual(initial[key], updated[key]) {
					t.Errorf("profile event did not roll %v", key)
				}
			}
			settleWorkloads()
			// Changing only adherence also affects proxy policy. Keep Modern in the
			// object while legacy adherence requires fallback to TLS 1.2.
			policy.Spec.TLSSecurityProfile = &configv1.TLSSecurityProfile{Type: configv1.TLSProfileModernType}
			if err := cli.Update(ctx, policy); err != nil {
				t.Fatal(err)
			}
			waitForTLS("VersionTLS13", 5*time.Second)
			settleWorkloads()
			policy.Spec.TLSAdherence = configv1.TLSAdherencePolicyLegacyAdheringComponentsOnly
			if err := cli.Update(ctx, policy); err != nil {
				t.Fatal(err)
			}
			waitForTLS("VersionTLS12", 5*time.Second)
		})
	}
}
