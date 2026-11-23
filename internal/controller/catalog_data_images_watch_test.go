package controller

import (
	"context"
	"fmt"
	"time"

	catalogv1alpha1 "github.com/opendatahub-io/model-registry-operator/api/catalog/v1alpha1"
	"github.com/opendatahub-io/model-registry-operator/internal/controller/config"
	imagev1 "github.com/openshift/api/image/v1"
	routev1 "github.com/openshift/api/route/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/events"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	controllerconfig "sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Catalog digest-only selections", func() {
	It("uses release digests, rejects floating selections, and rolls out explicit digest changes", func() {
		releaseDefault := "registry.redhat.io/rhai/ai-hub-catalog-rhel9@sha256:0e396f316c5da71164d97e36a2d950be1ba15fc3659b177797a7f0b8b74e5b96"
		GinkgoT().Setenv(config.CatalogDataImage, releaseDefault)
		GinkgoT().Setenv(config.BenchmarkDataImage, releaseDefault)
		GinkgoT().Setenv(config.CatalogDataImageStreamSource, releaseDefault)
		GinkgoT().Setenv(config.BenchmarkDataImageStreamSource, releaseDefault)
		// ImageStreams are served by OpenShift's aggregated API in production.
		// This minimal CRD lets envtest exercise their spec/status watch behavior.
		preserveUnknown := true
		_, err := envtest.InstallCRDs(cfg, envtest.CRDInstallOptions{CRDs: []*apiextensionsv1.CustomResourceDefinition{{
			ObjectMeta: metav1.ObjectMeta{Name: "imagestreams.image.openshift.io"},
			Spec: apiextensionsv1.CustomResourceDefinitionSpec{
				Group: "image.openshift.io", Scope: apiextensionsv1.NamespaceScoped,
				Names: apiextensionsv1.CustomResourceDefinitionNames{Plural: "imagestreams", Singular: "imagestream", Kind: "ImageStream", ListKind: "ImageStreamList"},
				Versions: []apiextensionsv1.CustomResourceDefinitionVersion{{Name: "v1", Served: true, Storage: true,
					Schema: &apiextensionsv1.CustomResourceValidation{OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
						Type: "object", Properties: map[string]apiextensionsv1.JSONSchemaProps{
							"spec": {Type: "object", XPreserveUnknownFields: &preserveUnknown}, "status": {Type: "object", XPreserveUnknownFields: &preserveUnknown},
						},
					}}, Subresources: &apiextensionsv1.CustomResourceSubresources{Status: &apiextensionsv1.CustomResourceSubresourceStatus{}},
				}},
			},
		}}})
		Expect(err).NotTo(HaveOccurred())

		scheme := runtime.NewScheme()
		for _, add := range []func(*runtime.Scheme) error{clientgoscheme.AddToScheme, catalogv1alpha1.AddToScheme, imagev1.AddToScheme, routev1.AddToScheme} {
			Expect(add(scheme)).To(Succeed())
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		namespace := fmt.Sprintf("catalog-image-watch-%d", time.Now().UnixNano())
		Expect(k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}})).To(Succeed())
		previousNamespace := config.GetRegistriesNamespace()
		config.SetRegistriesNamespace(namespace)
		defer config.SetRegistriesNamespace(previousNamespace)
		config.SetDefaultDomain("example.com", nil, false)

		mgr, err := ctrl.NewManager(cfg, ctrl.Options{
			Scheme: scheme, Metrics: metricsserver.Options{BindAddress: "0"},
			Cache: cache.Options{DefaultNamespaces: map[string]cache.Config{namespace: {}}},
			// Other isolated managers in this suite also register the Catalog controller.
			Controller: controllerconfig.Controller{SkipNameValidation: new(true)},
		})
		Expect(err).NotTo(HaveOccurred())
		templates, err := config.ParseTemplates()
		Expect(err).NotTo(HaveOccurred())
		r := &CatalogReconciler{
			Client: mgr.GetClient(), Scheme: scheme, Template: templates,
			Recorder: &events.FakeRecorder{}, Log: ctrl.Log.WithName("catalog-image-watch-test"),
			Capabilities: ClusterCapabilities{IsOpenShift: true},
		}
		Expect(r.SetupWithManager(mgr)).To(Succeed())
		done := make(chan error, 1)
		go func() { done <- mgr.Start(ctx) }()
		defer func() {
			cancel()
			Eventually(done, 10*time.Second).Should(Receive(BeNil()))
		}()
		Expect(mgr.GetCache().WaitForCacheSync(ctx)).To(BeTrue())
		direct, err := client.New(cfg, client.Options{Scheme: scheme})
		Expect(err).NotTo(HaveOccurred())
		catalog := &catalogv1alpha1.Catalog{ObjectMeta: metav1.ObjectMeta{Name: "catalog", Namespace: namespace}}
		for _, selection := range []string{"stable", ":stable", "latest"} {
			catalog.Spec.CatalogDataImageStream = &selection
			Expect(apierrors.IsInvalid(direct.Create(ctx, catalog))).To(BeTrue())
		}
		catalog.Spec.CatalogDataImageStream = nil
		Expect(direct.Create(ctx, catalog)).To(Succeed())
		deploymentKey := client.ObjectKey{Name: catalogResourceName, Namespace: namespace}
		deploymentImages := func() []string {
			deployment := &appsv1.Deployment{}
			if err := direct.Get(ctx, deploymentKey, deployment); err != nil {
				return nil
			}
			var images []string
			for _, container := range deployment.Spec.Template.Spec.InitContainers {
				if container.Name == "catalog-data-init" || container.Name == "benchmark-data-init" {
					images = append(images, container.Image)
				}
			}
			return images
		}
		Eventually(deploymentImages, 10*time.Second).Should(Equal([]string{releaseDefault, releaseDefault}))
		for _, selection := range []string{"stable", ":stable", "latest"} {
			err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
				if err := direct.Get(ctx, client.ObjectKeyFromObject(catalog), catalog); err != nil {
					return err
				}
				catalog.Spec.BenchmarkDataImageStream = &selection
				return direct.Update(ctx, catalog)
			})
			Expect(apierrors.IsInvalid(err)).To(BeTrue())
		}
		setBenchmark := func(selection *string) {
			Expect(retry.RetryOnConflict(retry.DefaultRetry, func() error {
				if err := direct.Get(ctx, client.ObjectKeyFromObject(catalog), catalog); err != nil {
					return err
				}
				catalog.Spec.BenchmarkDataImageStream = selection
				return direct.Update(ctx, catalog)
			})).To(Succeed())
		}
		pin := dataTestDigest2
		setBenchmark(&pin)
		Eventually(deploymentImages, 10*time.Second).Should(Equal([]string{releaseDefault, "registry.redhat.io/rhai/ai-hub-catalog-rhel9@" + dataTestDigest2}))
		setBenchmark(nil)
		Eventually(deploymentImages, 10*time.Second).Should(Equal([]string{releaseDefault, releaseDefault}))
		streams := &imagev1.ImageStreamList{}
		Expect(direct.List(ctx, streams, client.InNamespace(namespace))).To(Succeed())
		Expect(streams.Items).To(BeEmpty())
	})
})
