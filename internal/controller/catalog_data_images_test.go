package controller

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/go-logr/logr"
	catalogv1alpha1 "github.com/opendatahub-io/model-registry-operator/api/catalog/v1alpha1"
	"github.com/opendatahub-io/model-registry-operator/internal/controller/config"
	imagev1 "github.com/openshift/api/image/v1"
	routev1 "github.com/openshift/api/route/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func newDataImageTestReconciler(t *testing.T, catalog *catalogv1alpha1.Catalog, openShift bool) *CatalogReconciler {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{clientgoscheme.AddToScheme, catalogv1alpha1.AddToScheme, imagev1.AddToScheme, routev1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	templates, err := config.ParseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	base := fake.NewClientBuilder().WithScheme(scheme).WithObjects(catalog).
		WithStatusSubresource(&catalogv1alpha1.Catalog{}, &imagev1.ImageStream{}, &appsv1.Deployment{}).Build()
	// Model the API server's Deployment generation changes so rollout tests
	// cannot accidentally accept status for a previous pod template.
	withGenerations := interceptor.NewClient(base, interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if deployment, ok := obj.(*appsv1.Deployment); ok {
				deployment.Generation = 1
			}
			return c.Create(ctx, obj, opts...)
		},
		Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			if deployment, ok := obj.(*appsv1.Deployment); ok {
				previous := &appsv1.Deployment{}
				if err := c.Get(ctx, client.ObjectKeyFromObject(obj), previous); err != nil {
					return err
				}
				deployment.Generation = previous.Generation
				if !reflect.DeepEqual(previous.Spec, deployment.Spec) {
					deployment.Generation++
				}
			}
			return c.Update(ctx, obj, opts...)
		},
	})
	return &CatalogReconciler{
		Client: withGenerations, APIReader: withGenerations,
		Scheme: scheme, Template: templates, Recorder: &events.FakeRecorder{}, Log: logr.Discard(),
		Capabilities: ClusterCapabilities{IsOpenShift: openShift},
	}
}

const dataTestRepository = "registry.redhat.io/rhoai/catalog-data"
const dataTestDigest1 = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
const dataTestDigest2 = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
const dataTestDigest3 = "sha256:3333333333333333333333333333333333333333333333333333333333333333"

func TestCatalogDataImageSelectionLifecycle(t *testing.T) {
	const releaseDefault = "registry.redhat.io/rhai/ai-hub-catalog-rhel9@sha256:0e396f316c5da71164d97e36a2d950be1ba15fc3659b177797a7f0b8b74e5b96"
	for _, openShift := range []bool{false, true} {
		t.Run(fmt.Sprintf("OpenShift=%t", openShift), func(t *testing.T) {
			t.Setenv(config.CatalogDataImage, releaseDefault)
			t.Setenv(config.BenchmarkDataImage, releaseDefault)
			t.Setenv(config.CatalogDataImageStreamSource, releaseDefault)
			t.Setenv(config.BenchmarkDataImageStreamSource, releaseDefault)
			catalog := &catalogv1alpha1.Catalog{ObjectMeta: metav1.ObjectMeta{Name: "catalog", Namespace: "data-test", UID: "catalog", Generation: 1}}
			r := newDataImageTestReconciler(t, catalog, openShift)
			key := client.ObjectKeyFromObject(catalog)
			reconcile := func() *appsv1.Deployment {
				t.Helper()
				if _, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: key}); err != nil {
					t.Fatal(err)
				}
				if err := r.Get(t.Context(), key, catalog); err != nil {
					t.Fatal(err)
				}
				deployment := &appsv1.Deployment{}
				if err := r.Get(t.Context(), client.ObjectKey{Name: catalogResourceName, Namespace: catalog.Namespace}, deployment); err != nil {
					t.Fatal(err)
				}
				return deployment
			}
			initial := reconcile()
			for _, container := range initial.Spec.Template.Spec.InitContainers {
				if container.Name != "catalog-content-validation" && container.Image != releaseDefault {
					t.Fatalf("unexpected default image: %s", container.Image)
				}
			}
			assertDataImageCondition(t, catalog, conditionDataImageResolved, metav1.ConditionTrue, "ReleaseDefault")
			// Fake-client writes bypass admission, exercising the controller's own guard.
			for _, selection := range []string{"stable", ":stable", "latest", " stable", "sha256:abc"} {
				for _, field := range []string{"catalog", "benchmark"} {
					catalog.Spec.CatalogDataImageStream, catalog.Spec.BenchmarkDataImageStream = nil, nil
					if field == "catalog" {
						catalog.Spec.CatalogDataImageStream = &selection
					} else {
						catalog.Spec.BenchmarkDataImageStream = &selection
					}
					catalog.Generation++
					if err := r.Update(t.Context(), catalog); err != nil {
						t.Fatal(err)
					}
					deployment := reconcile()
					if deployment.Spec.Replicas == nil || *deployment.Spec.Replicas != 0 {
						t.Fatal("invalid selection must contain the workload")
					}
					if !reflect.DeepEqual(initial.Spec.Template, deployment.Spec.Template) {
						t.Fatal("invalid selection replaced the pinned pod template")
					}
					assertDataImageCondition(t, catalog, conditionDataImageResolved, metav1.ConditionFalse, "InvalidDataImageSelection")
				}
			}
			catalog.Spec.CatalogDataImageStream, catalog.Spec.BenchmarkDataImageStream = nil, nil
			catalog.Generation++
			if err := r.Update(t.Context(), catalog); err != nil {
				t.Fatal(err)
			}
			if deployment := reconcile(); deployment.Spec.Replicas == nil || *deployment.Spec.Replicas != 1 {
				t.Fatal("clearing invalid selection must restore release defaults")
			}
			var streams imagev1.ImageStreamList
			if err := r.List(t.Context(), &streams); err != nil || len(streams.Items) != 0 {
				t.Fatalf("must not create ImageStreams: %v, %v", streams.Items, err)
			}
		})
	}
}

func TestCatalogDataImageStreamEventFiltering(t *testing.T) {
	for _, selection := range []string{"", "stable", ":stable", dataTestDigest1} {
		catalog := &catalogv1alpha1.Catalog{ObjectMeta: metav1.ObjectMeta{Name: "catalog", Namespace: "data-test"}, Spec: catalogv1alpha1.CatalogSpec{CatalogDataImageStream: &selection, BenchmarkDataImageStream: &selection}}
		r := newDataImageTestReconciler(t, catalog, true)
		for _, name := range []string{CatalogDataImageStreamName, BenchmarkDataImageStreamName, "unrelated"} {
			stream := &imagev1.ImageStream{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: catalog.Namespace}}
			if requests := r.getCatalogsForDataImageStream(t.Context(), stream); len(requests) != 0 {
				t.Fatalf("selection %q must not follow ImageStream events: %v", selection, requests)
			}
		}
	}
}

func TestCatalogDataImageStreamPreservesImportState(t *testing.T) {
	t.Setenv(config.CatalogDataImage, dataTestRepository+"@"+dataTestDigest1)
	ctx := context.Background()
	catalog := &catalogv1alpha1.Catalog{ObjectMeta: metav1.ObjectMeta{Name: "catalog", Namespace: "data-test", UID: types.UID("catalog")}}
	r := newDataImageTestReconciler(t, catalog, true)
	params := r.buildCatalogParams(catalog, nil, nil, nil, "")
	stream, err := r.ensureCatalogDataImageStream(ctx, catalog, params)
	if err != nil {
		t.Fatal(err)
	}
	generation := int64(7)
	source := dataTestRepository + ":supported-update"
	stream.Spec.Tags[0].From.Name = source
	stream.Spec.Tags[0].Generation = &generation
	stream.Spec.Tags[0].Annotations = map[string]string{"custom": "keep"}
	stream.Spec.Tags[0].ImportPolicy.Scheduled = false
	unrelated := imagev1.TagReference{Name: "custom", From: &corev1.ObjectReference{Kind: "DockerImage", Name: "registry.example/custom:latest"}}
	stream.Spec.Tags = append([]imagev1.TagReference{unrelated}, stream.Spec.Tags...)
	stream.Labels["custom"] = "keep"
	if err := r.Update(ctx, stream); err != nil {
		t.Fatal(err)
	}
	stream.Status.Tags = []imagev1.NamedTagEventList{{Tag: "stable", Items: []imagev1.TagEvent{{DockerImageReference: dataTestRepository + "@" + dataTestDigest2}}}}
	if err := r.Status().Update(ctx, stream); err != nil {
		t.Fatal(err)
	}
	stream, err = r.ensureCatalogDataImageStream(ctx, catalog, params)
	if err != nil {
		t.Fatal(err)
	}
	if len(stream.Spec.Tags) != 2 || !reflect.DeepEqual(stream.Spec.Tags[0], unrelated) {
		t.Fatalf("unrelated tag changed: %+v", stream.Spec.Tags)
	}
	stable := stream.Spec.Tags[1]
	if stable.From.Name != source || stable.Generation == nil || *stable.Generation != generation || stable.Annotations["custom"] != "keep" {
		t.Fatalf("import state changed: %+v", stable)
	}
	if !stable.ImportPolicy.Scheduled || stream.Labels["custom"] != "keep" || lastImportedCatalogDataImage(stream) != dataTestRepository+"@"+dataTestDigest2 {
		t.Fatal("template reconciliation must restore scheduled imports while preserving labels and successful import history")
	}
	version := stream.ResourceVersion
	stream, err = r.ensureCatalogDataImageStream(ctx, catalog, params)
	if err != nil {
		t.Fatal(err)
	}
	if stream.ResourceVersion != version {
		t.Fatal("unchanged template reconciliation must not write the ImageStream")
	}
}

func TestCatalogDataImagesWithoutOpenShift(t *testing.T) {
	t.Setenv(config.CatalogDataImage, "registry.example/catalog:release")
	t.Setenv(config.BenchmarkDataImage, "registry.example/catalog:release")
	ctx := context.Background()
	catalog := &catalogv1alpha1.Catalog{ObjectMeta: metav1.ObjectMeta{Name: "catalog", Namespace: "data-test"}}
	r := newDataImageTestReconciler(t, catalog, false)
	for _, selection := range []string{"", dataTestDigest1, "stable"} {
		catalog.Spec.CatalogDataImageStream, catalog.Spec.BenchmarkDataImageStream = &selection, &selection
		params := r.buildCatalogParams(catalog, nil, nil, nil, "")
		if err := r.resolveCatalogDataImages(ctx, catalog, params); err != nil && selection != "stable" {
			t.Fatal(err)
		}
		want := "registry.example/catalog:release"
		if selection == dataTestDigest1 {
			want = "registry.example/catalog@" + selection
		}
		if params.CatalogDataImage != want || params.BenchmarkDataImage != want {
			t.Fatalf("unexpected resolved images: %q, %q", params.CatalogDataImage, params.BenchmarkDataImage)
		}
		condition := apimeta.FindStatusCondition(catalog.Status.Conditions, conditionDataImageResolved)
		if selection == "stable" && (condition.Status != metav1.ConditionFalse || condition.Reason != "InvalidDataImageSelection") {
			t.Fatalf("stable must be rejected on every platform: %+v", condition)
		}
	}
	var streams imagev1.ImageStreamList
	if err := r.List(ctx, &streams); err != nil || len(streams.Items) != 0 {
		t.Fatalf("ImageStreams should not be provisioned outside OpenShift: %v, %v", streams.Items, err)
	}
}

func TestLastImportedCatalogDataImage(t *testing.T) {
	for _, tc := range []struct {
		name  string
		items []imagev1.TagEvent
		want  string
	}{
		{name: "digest reference", items: []imagev1.TagEvent{{DockerImageReference: dataTestRepository + "@" + dataTestDigest1}}, want: dataTestRepository + "@" + dataTestDigest1},
		{name: "tag source and imported digest", items: []imagev1.TagEvent{{DockerImageReference: "registry.example:5000/data:stable", Image: dataTestDigest2}}, want: "registry.example:5000/data@" + dataTestDigest2},
		{name: "tag alone is not immutable", items: []imagev1.TagEvent{{DockerImageReference: dataTestRepository + ":stable"}}},
		{name: "no repository", items: []imagev1.TagEvent{{Image: dataTestDigest1}}},
		{name: "skip incomplete entry", items: []imagev1.TagEvent{{}, {DockerImageReference: dataTestRepository + "@" + dataTestDigest1}}, want: dataTestRepository + "@" + dataTestDigest1},
		{name: "empty history"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stream := &imagev1.ImageStream{Status: imagev1.ImageStreamStatus{Tags: []imagev1.NamedTagEventList{
				{Tag: "unrelated", Items: []imagev1.TagEvent{{DockerImageReference: dataTestRepository + "@" + dataTestDigest3}}},
				{Tag: "stable", Items: tc.items, Conditions: []imagev1.TagEventCondition{{Type: imagev1.ImportSuccess, Status: corev1.ConditionFalse}}},
			}}}
			if got := lastImportedCatalogDataImage(stream); got != tc.want {
				t.Fatalf("last imported image = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCatalogImageImportEvidenceSeparatesTargets(t *testing.T) {
	evidence := func(generation int64, nextUID string) string {
		first := &imagev1.ImageStream{
			ObjectMeta: metav1.ObjectMeta{UID: types.UID("ab")},
			Spec:       imagev1.ImageStreamSpec{Tags: []imagev1.TagReference{{Name: catalogDataImageStreamTag, Generation: &generation}}},
		}
		second := &imagev1.ImageStream{ObjectMeta: metav1.ObjectMeta{UID: types.UID(nextUID)}}
		return catalogImageImportEvidence(first, "catalog", metav1.ConditionTrue) +
			catalogImageImportEvidence(second, "benchmark", metav1.ConditionTrue)
	}
	if evidence(12, "3") == evidence(1, "23") {
		t.Fatal("different import evidence tuples produced the same fingerprint input")
	}
	stream := &imagev1.ImageStream{ObjectMeta: metav1.ObjectMeta{UID: types.UID("stream")}}
	firstGeneration, nextGeneration := int64(1), int64(2)
	stream.Spec.Tags = []imagev1.TagReference{{Name: catalogDataImageStreamTag, Generation: &firstGeneration}}
	healthy := catalogImageImportEvidence(stream, "catalog", metav1.ConditionTrue)
	pending := catalogImageImportEvidence(stream, "catalog", metav1.ConditionUnknown)
	stream.Spec.Tags[0].Generation = &nextGeneration
	if catalogImageImportEvidence(stream, "catalog", metav1.ConditionTrue) != healthy {
		t.Fatal("a healthy re-import changed activation evidence without changing the image")
	}
	if catalogImageImportEvidence(stream, "catalog", metav1.ConditionUnknown) == pending {
		t.Fatal("a pending import lost its generation evidence")
	}
}
