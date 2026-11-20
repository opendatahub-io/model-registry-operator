package controller

import (
	"context"
	"testing"

	catalogv1alpha1 "github.com/opendatahub-io/model-registry-operator/api/catalog/v1alpha1"
	"github.com/opendatahub-io/model-registry-operator/internal/controller/config"
	imagev1 "github.com/openshift/api/image/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestCatalogIndependentDataImages(t *testing.T) {
	ctx := context.Background()
	catalogRepo, benchmarkRepo := "registry.example/catalog", "registry.example/benchmarks"
	catalogDefault, benchmarkDefault := catalogRepo+"@"+dataTestDigest1, benchmarkRepo+"@"+dataTestDigest2
	t.Setenv(config.CatalogDataImage, catalogDefault)
	t.Setenv(config.BenchmarkDataImage, benchmarkDefault)
	t.Setenv(config.CatalogDataImageStreamSource, catalogRepo+":stable")
	t.Setenv(config.BenchmarkDataImageStreamSource, benchmarkRepo+":stable")
	catalog := &catalogv1alpha1.Catalog{ObjectMeta: metav1.ObjectMeta{Name: "catalog", Namespace: "independent-test", Generation: 1, UID: "independent"}}
	r := newDataImageTestReconciler(t, catalog, true)
	key := client.ObjectKeyFromObject(catalog)
	reconcile := func(catImage, benchImage string, replicas int32) {
		t.Helper()
		if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: key}); err != nil {
			t.Fatal(err)
		}
		deployment := &appsv1.Deployment{}
		if err := r.Get(ctx, client.ObjectKey{Name: catalogResourceName, Namespace: catalog.Namespace}, deployment); err != nil {
			t.Fatal(err)
		}
		if deployment.Spec.Replicas == nil || *deployment.Spec.Replicas != replicas {
			t.Fatalf("Catalog replicas = %v, want %d", deployment.Spec.Replicas, replicas)
		}
		want := map[string]string{"catalog-data-init": catImage, "benchmark-data-init": benchImage}
		for _, container := range deployment.Spec.Template.Spec.InitContainers {
			if wanted, data := want[container.Name]; data && container.Image != wanted {
				t.Fatalf("%s image = %q, want %q", container.Name, container.Image, want[container.Name])
			}
		}
		if err := r.Get(ctx, key, catalog); err != nil {
			t.Fatal(err)
		}
	}
	setSelections := func(catSelection, benchSelection string) {
		t.Helper()
		catalog.Spec.CatalogDataImageStream, catalog.Spec.BenchmarkDataImageStream = &catSelection, &benchSelection
		catalog.Generation++
		if err := r.Update(ctx, catalog); err != nil {
			t.Fatal(err)
		}
	}

	// Unset and explicitly empty selections use the exact release digests,
	// including on OpenShift, without creating an ImageStream.
	for _, empty := range []bool{false, true} {
		if empty {
			setSelections("", "")
		}
		reconcile(catalogDefault, benchmarkDefault, 1)
		assertDataImageCondition(t, catalog, "CatalogDataImageResolved", metav1.ConditionTrue, "ReleaseDefault")
		assertDataImageCondition(t, catalog, "BenchmarkDataImageResolved", metav1.ConditionTrue, "ReleaseDefault")
		streams := &imagev1.ImageStreamList{}
		if err := r.List(ctx, streams, client.InNamespace(catalog.Namespace)); err != nil {
			t.Fatal(err)
		}
		if len(streams.Items) != 0 {
			t.Fatal("release defaults must not create ImageStreams")
		}
	}

	setSelections(dataTestDigest1, dataTestDigest3)
	reconcile(catalogRepo+"@"+dataTestDigest1, benchmarkRepo+"@"+dataTestDigest3, 1)
	setSelections("", dataTestDigest3)
	reconcile(catalogDefault, benchmarkRepo+"@"+dataTestDigest3, 1)
	setSelections(dataTestDigest2, "")
	reconcile(catalogRepo+"@"+dataTestDigest2, benchmarkDefault, 1)
	setSelections("", "")
	reconcile(catalogDefault, benchmarkDefault, 1)
}

func TestCatalogDeprecatedDataImagesAreIgnored(t *testing.T) {
	catalogDefault := "registry.example/catalog@" + dataTestDigest1
	benchmarkDefault := "registry.example/benchmarks@" + dataTestDigest2
	t.Setenv(config.CatalogDataImage, catalogDefault)
	t.Setenv(config.BenchmarkDataImage, benchmarkDefault)
	t.Setenv(config.CatalogDataImageStreamSource, "")
	t.Setenv(config.BenchmarkDataImageStreamSource, "")
	legacyCatalog, legacyBenchmark := "v2.19", dataTestDigest3
	empty, pin := "", dataTestDigest3
	for _, tc := range []struct {
		name      string
		selection *string
		catalog   string
		benchmark string
	}{
		{"unset", nil, catalogDefault, benchmarkDefault},
		{"empty", &empty, catalogDefault, benchmarkDefault},
		{"pinned", &pin, "registry.example/catalog@" + pin, "registry.example/benchmarks@" + pin},
	} {
		t.Run(tc.name, func(t *testing.T) {
			catalog := &catalogv1alpha1.Catalog{
				ObjectMeta: metav1.ObjectMeta{Name: "catalog", Namespace: "deprecated-test"},
				Spec: catalogv1alpha1.CatalogSpec{
					CatalogDataImage: &legacyCatalog, BenchmarkDataImage: &legacyBenchmark, //nolint:staticcheck // Verify that deprecated image fields are ignored.
					CatalogDataImageStream: tc.selection, BenchmarkDataImageStream: tc.selection,
				},
			}
			r := newDataImageTestReconciler(t, catalog, false)
			params := r.buildCatalogParams(catalog, nil, nil, nil, "")
			if err := r.resolveCatalogDataImages(context.Background(), catalog, params); err != nil {
				t.Fatal(err)
			}
			if params.CatalogDataImage != tc.catalog || params.BenchmarkDataImage != tc.benchmark {
				t.Fatalf("deprecated fields affected image resolution: got %q, %q; want %q, %q", params.CatalogDataImage, params.BenchmarkDataImage, tc.catalog, tc.benchmark)
			}
		})
	}
}

func TestCatalogReleaseImageSources(t *testing.T) {
	catalogRelease := "registry.example/catalog@" + dataTestDigest1
	benchmarkRelease := "registry.example/benchmarks@" + dataTestDigest2
	t.Setenv(config.CatalogDataImage, catalogRelease)
	t.Setenv(config.BenchmarkDataImage, benchmarkRelease)
	t.Setenv(config.CatalogDataImageStreamSource, "")
	t.Setenv(config.BenchmarkDataImageStreamSource, "")
	stable := "stable"
	catalog := &catalogv1alpha1.Catalog{
		ObjectMeta: metav1.ObjectMeta{Name: "catalog", Namespace: "release-source-test", UID: "release-source"},
		Spec: catalogv1alpha1.CatalogSpec{
			CatalogDataImageStream: &stable, BenchmarkDataImageStream: &stable,
		},
	}
	r := newDataImageTestReconciler(t, catalog, true)
	resolve := func() {
		t.Helper()
		for index, source := range catalogDataImageSources() {
			name := CatalogDataImageStreamName
			if index == 1 {
				name = benchmarkDataImageStreamName()
			}
			if _, err := r.ensureCatalogDataImageStreamForSource(t.Context(), catalog, r.buildCatalogParams(catalog, nil, nil, nil, ""), name, source); err != nil {
				t.Fatal(err)
			}
		}
	}
	assertSource := func(name, source string) *imagev1.ImageStream {
		t.Helper()
		stream := &imagev1.ImageStream{}
		if err := r.Get(t.Context(), client.ObjectKey{Name: name, Namespace: catalog.Namespace}, stream); err != nil {
			t.Fatal(err)
		}
		if len(stream.Spec.Tags) != 1 || stream.Spec.Tags[0].Name != stable || stream.Spec.Tags[0].From.Name != source {
			t.Fatalf("release image reference was rewritten: %+v", stream.Spec.Tags)
		}
		return stream
	}
	resolve()
	stream := assertSource(CatalogDataImageStreamName, catalogRelease)
	assertSource(BenchmarkDataImageStreamName, benchmarkRelease)
	generation := int64(7)
	stream.Spec.Tags[0].Generation = &generation
	if err := r.Update(t.Context(), stream); err != nil {
		t.Fatal(err)
	}

	// A later operator release changes its supplied digest. The managed stream
	// must request a fresh import without relying on an external stable tag.
	catalogRelease = "registry.example/catalog@" + dataTestDigest3
	t.Setenv(config.CatalogDataImage, catalogRelease)
	resolve()
	stream = assertSource(CatalogDataImageStreamName, catalogRelease)
	if stream.Spec.Tags[0].Generation == nil || *stream.Spec.Tags[0].Generation != 0 {
		t.Fatal("changed release digest must request a new ImageStream import")
	}
	assertSource(BenchmarkDataImageStreamName, benchmarkRelease)
}

func TestCatalogStandaloneImportSources(t *testing.T) {
	t.Setenv(config.CatalogDataImage, "registry.example/bundled-catalog@"+dataTestDigest1)
	t.Setenv(config.BenchmarkDataImage, "registry.example/bundled-benchmarks@"+dataTestDigest2)
	t.Setenv(config.CatalogDataImageStreamSource, "registry.example/standalone-catalog:latest")
	t.Setenv(config.BenchmarkDataImageStreamSource, "registry.example/standalone-benchmarks:tested")
	stable := "stable"
	catalog := &catalogv1alpha1.Catalog{ObjectMeta: metav1.ObjectMeta{Name: "catalog", Namespace: "source-test", UID: "source"},
		Spec: catalogv1alpha1.CatalogSpec{CatalogDataImageStream: &stable, BenchmarkDataImageStream: &stable}}
	r := newDataImageTestReconciler(t, catalog, true)
	params := r.buildCatalogParams(catalog, nil, nil, nil, "")
	for index, source := range catalogDataImageSources() {
		name := CatalogDataImageStreamName
		if index == 1 {
			name = benchmarkDataImageStreamName()
		}
		if _, err := r.ensureCatalogDataImageStreamForSource(t.Context(), catalog, params, name, source); err != nil {
			t.Fatal(err)
		}
	}
	for name, source := range map[string]string{
		CatalogDataImageStreamName:   "registry.example/standalone-catalog:latest",
		BenchmarkDataImageStreamName: "registry.example/standalone-benchmarks:tested",
	} {
		stream := &imagev1.ImageStream{}
		if err := r.Get(context.Background(), client.ObjectKey{Name: name, Namespace: catalog.Namespace}, stream); err != nil {
			t.Fatal(err)
		}
		if stream.Spec.Tags[0].Name != "stable" || stream.Spec.Tags[0].From.Name != source {
			t.Fatalf("source tag was rewritten: %+v", stream.Spec.Tags[0])
		}
	}
	pin1, pin2 := dataTestDigest1, dataTestDigest2
	catalog.Spec.CatalogDataImageStream, catalog.Spec.BenchmarkDataImageStream = &pin1, &pin2
	if err := r.resolveCatalogDataImages(context.Background(), catalog, params); err != nil {
		t.Fatal(err)
	}
	if params.CatalogDataImage != "registry.example/standalone-catalog@"+dataTestDigest1 || params.BenchmarkDataImage != "registry.example/standalone-benchmarks@"+dataTestDigest2 {
		t.Fatalf("pins must use their configured standalone repositories: %q, %q", params.CatalogDataImage, params.BenchmarkDataImage)
	}
}

func TestCatalogRejectsStableBeforeFirstDeployment(t *testing.T) {
	ctx := context.Background()
	t.Setenv(config.CatalogDataImage, dataTestRepository+"@"+dataTestDigest1)
	t.Setenv(config.BenchmarkDataImage, "registry.example/benchmarks@"+dataTestDigest1)
	stable := "stable"
	catalog := &catalogv1alpha1.Catalog{ObjectMeta: metav1.ObjectMeta{Name: "catalog", Namespace: "first-failure-test", Generation: 1, UID: "first-failure"},
		Spec: catalogv1alpha1.CatalogSpec{CatalogDataImageStream: &stable, BenchmarkDataImageStream: &stable}}
	r := newDataImageTestReconciler(t, catalog, true)
	params := r.buildCatalogParams(catalog, nil, nil, nil, "")
	stream, err := r.ensureCatalogDataImageStream(ctx, catalog, params)
	if err != nil {
		t.Fatal(err)
	}
	stream.Status.Tags = []imagev1.NamedTagEventList{{Tag: "stable", Conditions: []imagev1.TagEventCondition{{Type: imagev1.ImportSuccess, Status: corev1.ConditionFalse, Reason: "NotFound"}}}}
	if err := r.Status().Update(ctx, stream); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(catalog)}); err != nil {
		t.Fatal(err)
	}
	deployment := &appsv1.Deployment{}
	if err := r.Get(ctx, client.ObjectKey{Name: catalogResourceName, Namespace: catalog.Namespace}, deployment); !apierrors.IsNotFound(err) {
		t.Fatalf("an invalid selection must not deploy a fallback image: %v", err)
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(catalog), catalog); err != nil {
		t.Fatal(err)
	}
	assertDataImageCondition(t, catalog, conditionDataImageResolved, metav1.ConditionFalse, "InvalidDataImageSelection")
	assertDataImageCondition(t, catalog, ConditionTypeAvailable, metav1.ConditionFalse, "DataImagesUnavailable")
}

func TestCatalogConfiguredSourceChangesPreserveManualOverrides(t *testing.T) {
	ctx := context.Background()
	t.Setenv(config.CatalogDataImageStreamSource, "registry.example/data:latest")
	catalog := &catalogv1alpha1.Catalog{ObjectMeta: metav1.ObjectMeta{Name: "catalog", Namespace: "source-change-test", UID: "source-change"}}
	r := newDataImageTestReconciler(t, catalog, true)
	params := r.buildCatalogParams(catalog, nil, nil, nil, "")
	stream, err := r.ensureCatalogDataImageStream(ctx, catalog, params)
	if err != nil {
		t.Fatal(err)
	}
	generation := int64(7)
	stream.Spec.Tags[0].Generation = &generation
	if err := r.Update(ctx, stream); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.CatalogDataImageStreamSource, "registry.example/data:tested")
	stream, err = r.ensureCatalogDataImageStream(ctx, catalog, params)
	if err != nil {
		t.Fatal(err)
	}
	if stream.Spec.Tags[0].From.Name != "registry.example/data:tested" || stream.Spec.Tags[0].Generation == nil || *stream.Spec.Tags[0].Generation != 0 {
		t.Fatal("a configured source change must request a new import generation")
	}
	stream.Spec.Tags[0].From.Name = "registry.example/data:manual"
	stream.Spec.Tags[0].Generation = &generation
	if err := r.Update(ctx, stream); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.CatalogDataImageStreamSource, "registry.example/data:new-default")
	stream, err = r.ensureCatalogDataImageStream(ctx, catalog, params)
	if err != nil {
		t.Fatal(err)
	}
	if stream.Spec.Tags[0].From.Name != "registry.example/data:manual" || *stream.Spec.Tags[0].Generation != generation {
		t.Fatal("a configured source change must preserve manual import overrides")
	}
}
