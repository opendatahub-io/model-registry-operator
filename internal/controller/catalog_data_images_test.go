package controller

import (
	"context"
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
	return &CatalogReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(catalog).
			WithStatusSubresource(&catalogv1alpha1.Catalog{}, &imagev1.ImageStream{}, &appsv1.Deployment{}).Build(),
		Scheme: scheme, Template: templates, Recorder: &events.FakeRecorder{}, Log: logr.Discard(),
		Capabilities: ClusterCapabilities{IsOpenShift: openShift},
	}
}

const dataTestRepository = "registry.redhat.io/rhoai/catalog-data"
const dataTestDigest1 = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
const dataTestDigest2 = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
const dataTestDigest3 = "sha256:3333333333333333333333333333333333333333333333333333333333333333"

func TestCatalogDataImageSelectionLifecycle(t *testing.T) {
	releaseDefault := dataTestRepository + "@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	t.Setenv(config.CatalogDataImage, releaseDefault)
	// Both repositories can ship the combined image today.
	t.Setenv(config.BenchmarkDataImage, releaseDefault)
	ctx := context.Background()
	catalog := &catalogv1alpha1.Catalog{ObjectMeta: metav1.ObjectMeta{Name: "catalog", Namespace: "data-test", UID: types.UID("catalog"), Generation: 1}}
	r := newDataImageTestReconciler(t, catalog, true)
	key := client.ObjectKeyFromObject(catalog)
	streamKey := client.ObjectKey{Namespace: catalog.Namespace, Name: CatalogDataImageStreamName}
	reconcile := func() *appsv1.Deployment {
		t.Helper()
		if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: key}); err != nil {
			t.Fatal(err)
		}
		deployment := &appsv1.Deployment{}
		if err := r.Get(ctx, client.ObjectKey{Namespace: catalog.Namespace, Name: catalogResourceName}, deployment); err != nil {
			t.Fatal(err)
		}
		return deployment
	}
	assertImages := func(deployment *appsv1.Deployment, want string) {
		t.Helper()
		if len(deployment.Spec.Template.Spec.InitContainers) != 2 {
			t.Fatal("expected both data init containers")
		}
		for _, container := range deployment.Spec.Template.Spec.InitContainers {
			if container.Image != want {
				t.Fatalf("%s image = %q, want %q", container.Name, container.Image, want)
			}
		}
	}
	assertCondition := func(status metav1.ConditionStatus, reason string) {
		t.Helper()
		if err := r.Get(ctx, key, catalog); err != nil {
			t.Fatal(err)
		}
		condition := apimeta.FindStatusCondition(catalog.Status.Conditions, conditionDataImageResolved)
		if condition == nil || condition.Status != status || condition.Reason != reason || condition.Message == "" || condition.ObservedGeneration != catalog.Generation {
			t.Fatalf("unexpected data image condition: %+v", condition)
		}
	}
	setSelections := func(catImage, benchImage *string) {
		t.Helper()
		if err := r.Get(ctx, key, catalog); err != nil {
			t.Fatal(err)
		}
		catalog.Spec.CatalogDataImageStream, catalog.Spec.BenchmarkDataImageStream = catImage, benchImage
		catalog.Generation++
		if err := r.Update(ctx, catalog); err != nil {
			t.Fatal(err)
		}
	}
	getStream := func() *imagev1.ImageStream {
		t.Helper()
		stream := &imagev1.ImageStream{}
		if err := r.Get(ctx, streamKey, stream); err != nil {
			t.Fatal(err)
		}
		return stream
	}
	importImage := func(digest string, failed bool, follows bool) *appsv1.Deployment {
		t.Helper()
		stream := getStream()
		if len(stream.Status.Tags) == 0 {
			stream.Status.Tags = []imagev1.NamedTagEventList{{Tag: catalogDataImageStreamTag}}
		}
		status := &stream.Status.Tags[0]
		if failed {
			status.Conditions = []imagev1.TagEventCondition{{Type: imagev1.ImportSuccess, Status: corev1.ConditionFalse, Reason: "ImportFailed"}}
		} else {
			status.Conditions = nil
			status.Items = append([]imagev1.TagEvent{{Image: digest, DockerImageReference: dataTestRepository + "@" + digest}}, status.Items...)
		}
		if err := r.Status().Update(ctx, stream); err != nil {
			t.Fatal(err)
		}
		requests := r.getCatalogsForDataImageStream(ctx, stream)
		if follows {
			if len(requests) != 1 || requests[0].NamespacedName != key {
				t.Fatalf("ImageStream event requests = %v, want Catalog %v", requests, key)
			}
		} else if len(requests) != 0 {
			t.Fatalf("selection should not follow the stream: %v", requests)
		}
		before, generation := catalog.Spec.DeepCopy(), catalog.Generation
		deployment := reconcile()
		if err := r.Get(ctx, key, catalog); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(before, &catalog.Spec) || generation != catalog.Generation {
			t.Fatal("ImageStream update changed the Catalog spec or generation")
		}
		return deployment
	}

	assertImages(reconcile(), releaseDefault)
	assertCondition(metav1.ConditionTrue, "ReleaseDefault")

	stable := "stable"
	setSelections(&stable, &stable)
	assertImages(reconcile(), releaseDefault)
	stream := getStream()
	if len(stream.Spec.Tags) != 1 || stream.Spec.Tags[0].From.Name != dataTestRepository+":stable" || !stream.Spec.Tags[0].ImportPolicy.Scheduled || stream.Spec.Tags[0].ImportPolicy.ImportMode != imagev1.ImportModePreserveOriginal {
		t.Fatalf("unexpected managed channel: %+v", stream.Spec.Tags)
	}
	if len(stream.OwnerReferences) != 1 || stream.OwnerReferences[0].UID != catalog.UID {
		t.Fatal("ImageStream must be owned by the Catalog")
	}

	assertCondition(metav1.ConditionFalse, "NoSuccessfulImport")
	assertDataImageCondition(t, catalog, conditionDataImageImportHealthy, metav1.ConditionUnknown, "ImportPending")
	assertDataImageCondition(t, catalog, ConditionTypeDegraded, metav1.ConditionFalse, "DataImagesHealthy")
	assertImages(importImage("", true, true), releaseDefault)
	assertDataImageCondition(t, catalog, conditionDataImageImportHealthy, metav1.ConditionFalse, "ImportFailed")
	assertDataImageCondition(t, catalog, ConditionTypeDegraded, metav1.ConditionTrue, "ImportFailed")
	first := importImage(dataTestDigest1, false, true)
	assertImages(first, dataTestRepository+"@"+dataTestDigest1)
	second := importImage(dataTestDigest2, false, true)
	assertImages(second, dataTestRepository+"@"+dataTestDigest2)
	if reflect.DeepEqual(first.Spec.Template, second.Spec.Template) {
		t.Fatal("a successive successful import must change the pod template")
	}
	failed := importImage("", true, true)
	assertImages(failed, dataTestRepository+"@"+dataTestDigest2)
	assertDataImageCondition(t, catalog, conditionDataImageResolved, metav1.ConditionFalse, "ImportFailed")
	assertDataImageCondition(t, catalog, conditionDataImageImportHealthy, metav1.ConditionFalse, "ImportFailed")
	assertDataImageCondition(t, catalog, ConditionTypeDegraded, metav1.ConditionTrue, "ImportFailed")
	if !reflect.DeepEqual(second.Spec.Template, failed.Spec.Template) {
		t.Fatal("a failed import must retain the previous pod template")
	}
	if failed.Spec.Replicas == nil || *failed.Spec.Replicas != 0 {
		t.Fatal("a failed import must stop Catalog serving")
	}
	assertDataImageCondition(t, catalog, ConditionTypeAvailable, metav1.ConditionFalse, "DataImagesUnavailable")
	assertImages(importImage(dataTestDigest3, false, true), dataTestRepository+"@"+dataTestDigest3)
	assertCondition(metav1.ConditionTrue, "ImageStreamResolved")
	assertDataImageCondition(t, catalog, conditionDataImageImportHealthy, metav1.ConditionTrue, "ImportSucceeded")
	assertDataImageCondition(t, catalog, ConditionTypeDegraded, metav1.ConditionFalse, "DataImagesHealthy")

	// Manual selection and rollback pin directly, without changing the import source.
	pin := dataTestDigest2
	setSelections(&pin, &pin)
	assertImages(reconcile(), dataTestRepository+"@"+pin)
	assertCondition(metav1.ConditionTrue, "DigestPinned")
	assertImages(importImage(dataTestDigest3, false, false), dataTestRepository+"@"+pin)
	pin = dataTestDigest1
	setSelections(&pin, &pin)
	assertImages(reconcile(), dataTestRepository+"@"+pin)
	if getStream().Spec.Tags[0].From.Name != dataTestRepository+":stable" {
		t.Fatal("pinning must not change the stable import source")
	}

	// Invalid selections must keep the pinned image, including on an operator restart.
	for _, pair := range [][2]string{
		{"latest", "latest"},
		{"model-catalog-data:stable", "model-catalog-data:stable"},
		{"sha256:bad", "sha256:bad"}, {" stable", " stable"},
		{"registry.example/data@" + dataTestDigest1, "registry.example/data@" + dataTestDigest1},
	} {
		t.Run(pair[0]+"/"+pair[1], func(t *testing.T) {
			setSelections(&pair[0], &pair[1])
			previous := reconcile()
			assertImages(previous, dataTestRepository+"@"+dataTestDigest1)
			if previous.Spec.Replicas == nil || *previous.Spec.Replicas != 0 {
				t.Fatal("an invalid selection must stop Catalog serving")
			}
			assertCondition(metav1.ConditionFalse, "InvalidDataImageSelection")
			assertDataImageCondition(t, catalog, ConditionTypeDegraded, metav1.ConditionTrue, "InvalidDataImageSelection")
			// Use a fresh reconciler with the same persisted resources.
			r = &CatalogReconciler{Client: r.Client, Scheme: r.Scheme, Template: r.Template, Recorder: r.Recorder, Log: r.Log, Capabilities: r.Capabilities}
			next := importImage(dataTestDigest2, false, false)
			assertImages(next, dataTestRepository+"@"+dataTestDigest1)
			if !reflect.DeepEqual(previous.Spec.Template, next.Spec.Template) {
				t.Fatal("invalid selections must retain the applied pod template")
			}
		})
	}

	// Returning to stable resumes at the latest successfully imported digest.
	setSelections(&stable, &stable)
	assertImages(reconcile(), dataTestRepository+"@"+dataTestDigest2)
	// An empty or failed channel after recreation must not undo an applied digest.
	stream = getStream()
	stream.Status.Tags = nil
	if err := r.Status().Update(ctx, stream); err != nil {
		t.Fatal(err)
	}
	assertImages(reconcile(), dataTestRepository+"@"+dataTestDigest2)
	assertCondition(metav1.ConditionFalse, "NoSuccessfulImport")
	assertImages(importImage("", true, true), dataTestRepository+"@"+dataTestDigest2)

	// Clearing both fields returns to the current release, not the initial release.
	releaseDefault = dataTestRepository + "@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	t.Setenv(config.CatalogDataImage, releaseDefault)
	t.Setenv(config.BenchmarkDataImage, releaseDefault)
	empty := ""
	for _, pair := range [][2]*string{{&empty, nil}, {nil, &empty}, {&empty, &empty}, {nil, nil}} {
		setSelections(pair[0], pair[1])
		assertImages(reconcile(), releaseDefault)
		assertCondition(metav1.ConditionTrue, "ReleaseDefault")
		assertDataImageCondition(t, catalog, conditionDataImageImportHealthy, metav1.ConditionTrue, "NotTrackingImageStream")
		assertDataImageCondition(t, catalog, ConditionTypeDegraded, metav1.ConditionFalse, "DataImagesHealthy")
		assertImages(importImage(dataTestDigest3, false, false), releaseDefault)
	}
}

func TestCatalogDataImageStreamEventFiltering(t *testing.T) {
	stable := "stable"
	catalog := &catalogv1alpha1.Catalog{ObjectMeta: metav1.ObjectMeta{Name: "catalog", Namespace: "data-test"}, Spec: catalogv1alpha1.CatalogSpec{CatalogDataImageStream: &stable, BenchmarkDataImageStream: &stable}}
	r := newDataImageTestReconciler(t, catalog, true)
	for _, key := range []client.ObjectKey{{Name: "unrelated", Namespace: catalog.Namespace}, {Name: CatalogDataImageStreamName, Namespace: "another-namespace"}} {
		stream := &imagev1.ImageStream{ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace}}
		if requests := r.getCatalogsForDataImageStream(context.Background(), stream); len(requests) != 0 {
			t.Fatalf("unrelated event should not enqueue the Catalog: %v", requests)
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
		if selection == "stable" && (condition.Status != metav1.ConditionFalse || condition.Reason != "UnsupportedDataImageSelection") {
			t.Fatalf("stable must report missing OpenShift support: %+v", condition)
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
