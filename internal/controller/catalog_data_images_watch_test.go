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
	discoveryv1 "k8s.io/api/discovery/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	controllerconfig "sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Catalog data ImageStream watch", func() {
	It("watches shared and separate images, stops on faults, recovers, pins rollback, and restores release defaults", func() {
		releaseDefault := dataTestRepository + "@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		GinkgoT().Setenv(config.CatalogDataImage, releaseDefault)
		GinkgoT().Setenv(config.BenchmarkDataImage, releaseDefault)
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
		stable := "stable"
		catalog := &catalogv1alpha1.Catalog{
			ObjectMeta: metav1.ObjectMeta{Name: "catalog", Namespace: namespace},
			Spec:       catalogv1alpha1.CatalogSpec{CatalogDataImageStream: &stable, BenchmarkDataImageStream: &stable},
		}
		Expect(direct.Create(ctx, catalog)).To(Succeed())
		generation := catalog.Generation
		streamKey := client.ObjectKey{Name: CatalogDataImageStreamName, Namespace: namespace}
		deploymentKey := client.ObjectKey{Name: catalogResourceName, Namespace: namespace}
		stream := &imagev1.ImageStream{}
		Eventually(func() error { return direct.Get(ctx, streamKey, stream) }, 10*time.Second).Should(Succeed())
		Expect(stream.Spec.Tags[0].ImportPolicy.Scheduled).To(BeTrue())
		deploymentImages := func() []string {
			deployment := &appsv1.Deployment{}
			if err := direct.Get(ctx, deploymentKey, deployment); err != nil {
				return nil
			}
			var images []string
			for _, container := range deployment.Spec.Template.Spec.InitContainers {
				images = append(images, container.Image)
			}
			return images
		}
		conditionFor := func(conditionType string) *metav1.Condition {
			observed := &catalogv1alpha1.Catalog{}
			if err := direct.Get(ctx, client.ObjectKeyFromObject(catalog), observed); err != nil {
				return nil
			}
			condition := apimeta.FindStatusCondition(observed.Status.Conditions, conditionType)
			if condition == nil || condition.ObservedGeneration != observed.Generation {
				return nil
			}
			return condition
		}
		conditionReason := func() string {
			if condition := conditionFor(conditionDataImageResolved); condition != nil {
				return condition.Reason
			}
			return ""
		}
		importReason := func() string {
			if condition := conditionFor(conditionDataImageImportHealthy); condition != nil {
				return condition.Reason
			}
			return ""
		}
		degraded := func() metav1.ConditionStatus {
			if condition := conditionFor(ConditionTypeDegraded); condition != nil {
				return condition.Status
			}
			return metav1.ConditionUnknown
		}
		available := func() metav1.ConditionStatus {
			if condition := conditionFor(ConditionTypeAvailable); condition != nil {
				return condition.Status
			}
			return metav1.ConditionUnknown
		}
		// Mark the operand ready so imports must wake the controller through the
		// ImageStream watch, with no rollout polling or status-write loop.
		deployment := &appsv1.Deployment{}
		Eventually(func() error { return direct.Get(ctx, deploymentKey, deployment) }, 10*time.Second).Should(Succeed())
		Eventually(func() error {
			if err := direct.Get(ctx, deploymentKey, deployment); err != nil {
				return err
			}
			deployment.Status.Conditions = []appsv1.DeploymentCondition{{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionTrue, LastTransitionTime: metav1.NewTime(time.Now().Add(-time.Minute))}}
			return direct.Status().Update(ctx, deployment)
		}, 10*time.Second).Should(Succeed())
		ready := true
		Expect(direct.Create(ctx, &discoveryv1.EndpointSlice{
			ObjectMeta:  metav1.ObjectMeta{Name: "catalog-ready", Namespace: namespace, Labels: map[string]string{discoveryv1.LabelServiceName: catalogResourceName}},
			AddressType: discoveryv1.AddressTypeIPv4, Endpoints: []discoveryv1.Endpoint{{Addresses: []string{"10.0.0.1"}, Conditions: discoveryv1.EndpointConditions{Ready: &ready}}},
		})).To(Succeed())
		Eventually(func() bool {
			condition := conditionFor(conditionWorkloadAvailable)
			return condition != nil && condition.Status == metav1.ConditionTrue
		}, 10*time.Second).Should(BeTrue())
		Expect(available()).To(Equal(metav1.ConditionFalse))
		Eventually(importReason, 10*time.Second).Should(Equal("ImportPending"))
		Eventually(degraded, 10*time.Second).Should(Equal(metav1.ConditionFalse))

		updateImport := func(image string, failed bool) {
			Eventually(func() error {
				if err := direct.Get(ctx, streamKey, stream); err != nil {
					return err
				}
				if failed {
					stream.Status.Tags[0].Conditions = []imagev1.TagEventCondition{{Type: imagev1.ImportSuccess, Status: corev1.ConditionFalse, Reason: "Unauthorized", Message: "Registry authentication failed"}}
				} else {
					stream.Status.Tags = []imagev1.NamedTagEventList{{Tag: "stable", Items: []imagev1.TagEvent{{DockerImageReference: image}}}}
				}
				return direct.Status().Update(ctx, stream)
			}, 10*time.Second).Should(Succeed())
		}
		image1 := dataTestRepository + "@" + dataTestDigest1
		image2 := dataTestRepository + "@" + dataTestDigest2
		updateImport(image1, false)
		Eventually(deploymentImages, 10*time.Second).Should(Equal([]string{image1, image1}))
		updateImport(image2, false)
		Eventually(deploymentImages, 10*time.Second).Should(Equal([]string{image2, image2}))
		updateImport("", true)
		Consistently(deploymentImages, time.Second).Should(Equal([]string{image2, image2}))
		Eventually(importReason, 10*time.Second).Should(Equal("ImportFailed"))
		Eventually(degraded, 10*time.Second).Should(Equal(metav1.ConditionTrue))
		Expect(conditionFor(conditionDataImageImportHealthy).Message).To(ContainSubstring("Registry authentication failed"))
		Expect(conditionFor(conditionDataImageResolved).Status).To(Equal(metav1.ConditionFalse))
		Eventually(available, 10*time.Second).Should(Equal(metav1.ConditionFalse))
		updateImport(image2, false)
		Eventually(importReason, 10*time.Second).Should(Equal("ImportSucceeded"))
		Eventually(degraded, 10*time.Second).Should(Equal(metav1.ConditionFalse))
		// Fail again before pinning to prove selection changes clear stale faults.
		updateImport("", true)
		Eventually(degraded, 10*time.Second).Should(Equal(metav1.ConditionTrue))
		Expect(direct.Get(ctx, client.ObjectKeyFromObject(catalog), catalog)).To(Succeed())
		Expect(catalog.Generation).To(Equal(generation))
		Expect(catalog.Spec.CatalogDataImageStream).To(HaveValue(Equal(stable)))
		Expect(catalog.Spec.BenchmarkDataImageStream).To(HaveValue(Equal(stable)))

		setSelections := func(catalogSelection, benchmarkSelection string) {
			Eventually(func() error {
				if err := direct.Get(ctx, client.ObjectKeyFromObject(catalog), catalog); err != nil {
					return err
				}
				catalog.Spec.CatalogDataImageStream, catalog.Spec.BenchmarkDataImageStream = &catalogSelection, &benchmarkSelection
				return direct.Update(ctx, catalog)
			}, 10*time.Second).Should(Succeed())
		}

		// A manual digest absent from stream history still pins the release repository.
		image3 := dataTestRepository + "@" + dataTestDigest3
		setSelections(dataTestDigest3, dataTestDigest3)
		Eventually(deploymentImages, 10*time.Second).Should(Equal([]string{image3, image3}))
		Eventually(conditionReason, 10*time.Second).Should(Equal("DigestPinned"))
		Eventually(importReason, 10*time.Second).Should(Equal("NotTrackingImageStream"))
		Eventually(degraded, 10*time.Second).Should(Equal(metav1.ConditionFalse))
		updateImport(image1, false)
		Consistently(deploymentImages, time.Second).Should(Equal([]string{image3, image3}))
		setSelections(dataTestDigest2, dataTestDigest2)
		Eventually(deploymentImages, 10*time.Second).Should(Equal([]string{image2, image2}))
		for _, pair := range [][2]string{
			{"latest", "latest"}, {"model-catalog-data:stable", "model-catalog-data:stable"},
			{"sha256:invalid", "sha256:invalid"},
		} {
			Expect(direct.Get(ctx, client.ObjectKeyFromObject(catalog), catalog)).To(Succeed())
			beforeGeneration := catalog.Generation
			Eventually(func() error {
				if err := direct.Get(ctx, client.ObjectKeyFromObject(catalog), catalog); err != nil {
					return err
				}
				catalog.Spec.CatalogDataImageStream, catalog.Spec.BenchmarkDataImageStream = &pair[0], &pair[1]
				if err := direct.Update(ctx, catalog); apierrors.IsInvalid(err) {
					return nil
				} else if err != nil {
					return err
				}
				return fmt.Errorf("unsupported selections were accepted: %v", pair)
			}, 10*time.Second).Should(Succeed())
			Expect(direct.Get(ctx, client.ObjectKeyFromObject(catalog), catalog)).To(Succeed())
			Expect(catalog.Generation).To(Equal(beforeGeneration))
			Expect(catalog.Spec.CatalogDataImageStream).To(HaveValue(Equal(dataTestDigest2)))
			Expect(catalog.Spec.BenchmarkDataImageStream).To(HaveValue(Equal(dataTestDigest2)))
			Eventually(conditionReason, 10*time.Second).Should(Equal("DigestPinned"))
			Expect(degraded()).To(Equal(metav1.ConditionFalse))
			Consistently(deploymentImages, 200*time.Millisecond).Should(Equal([]string{image2, image2}))
		}
		setSelections(stable, stable)
		Eventually(deploymentImages, 10*time.Second).Should(Equal([]string{image1, image1}))
		setSelections("", "")
		Eventually(deploymentImages, 10*time.Second).Should(Equal([]string{releaseDefault, releaseDefault}))
		Eventually(conditionReason, 10*time.Second).Should(Equal("ReleaseDefault"))
		Eventually(importReason, 10*time.Second).Should(Equal("NotTrackingImageStream"))
		Eventually(degraded, 10*time.Second).Should(Equal(metav1.ConditionFalse))
		updateImport(image2, false)
		Consistently(deploymentImages, time.Second).Should(Equal([]string{releaseDefault, releaseDefault}))

		// A distinct benchmark source must also wake the controller through an
		// ImageStream status event, without another Catalog edit.
		benchmarkRepository := "registry.example/standalone-benchmarks"
		GinkgoT().Setenv(config.BenchmarkDataImageStreamSource, benchmarkRepository+":stable")
		setSelections(stable, stable)
		benchmarkStream := &imagev1.ImageStream{}
		benchmarkKey := client.ObjectKey{Name: BenchmarkDataImageStreamName, Namespace: namespace}
		Eventually(func() error { return direct.Get(ctx, benchmarkKey, benchmarkStream) }, 10*time.Second).Should(Succeed())
		Expect(benchmarkStream.Spec.Tags[0].From.Name).To(Equal(benchmarkRepository + ":stable"))
		Expect(direct.Get(ctx, client.ObjectKeyFromObject(catalog), catalog)).To(Succeed())
		generation = catalog.Generation
		updateBenchmark := func(digest string, failed bool) {
			Eventually(func() error {
				if err := direct.Get(ctx, benchmarkKey, benchmarkStream); err != nil {
					return err
				}
				benchmarkStream.Status.Tags = []imagev1.NamedTagEventList{{Tag: "stable", Items: []imagev1.TagEvent{{DockerImageReference: benchmarkRepository + "@" + digest}}}}
				if failed {
					benchmarkStream.Status.Tags[0].Conditions = []imagev1.TagEventCondition{{Type: imagev1.ImportSuccess, Status: corev1.ConditionFalse, Reason: "Unauthorized"}}
				}
				return direct.Status().Update(ctx, benchmarkStream)
			}, 10*time.Second).Should(Succeed())
		}
		replicas := func() int32 {
			if err := direct.Get(ctx, deploymentKey, deployment); err != nil || deployment.Spec.Replicas == nil {
				return -1
			}
			return *deployment.Spec.Replicas
		}
		updateBenchmark(dataTestDigest1, false)
		Eventually(deploymentImages, 10*time.Second).Should(Equal([]string{image2, benchmarkRepository + "@" + dataTestDigest1}))
		updateBenchmark(dataTestDigest2, false)
		Eventually(deploymentImages, 10*time.Second).Should(Equal([]string{image2, benchmarkRepository + "@" + dataTestDigest2}))
		updateBenchmark(dataTestDigest2, true)
		Eventually(replicas, 10*time.Second).Should(Equal(int32(0)))
		Eventually(available, 10*time.Second).Should(Equal(metav1.ConditionFalse))
		updateBenchmark(dataTestDigest2, false)
		Eventually(replicas, 10*time.Second).Should(Equal(int32(1)))
		Eventually(degraded, 10*time.Second).Should(Equal(metav1.ConditionFalse))
		Expect(direct.Get(ctx, client.ObjectKeyFromObject(catalog), catalog)).To(Succeed())
		Expect(catalog.Generation).To(Equal(generation))
	})
})
