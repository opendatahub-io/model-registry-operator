package controller

import (
	"context"
	"errors"
	"testing"
	"time"

	catalogv1alpha1 "github.com/opendatahub-io/model-registry-operator/api/catalog/v1alpha1"
	"github.com/opendatahub-io/model-registry-operator/internal/controller/config"
	imagev1 "github.com/openshift/api/image/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func markDataImageWorkloadReady(t *testing.T, r *CatalogReconciler, catalog *catalogv1alpha1.Catalog) {
	t.Helper()
	ctx := context.Background()
	deployment := &appsv1.Deployment{}
	if err := r.Get(ctx, client.ObjectKey{Name: catalogResourceName, Namespace: catalog.Namespace}, deployment); err != nil {
		t.Fatal(err)
	}
	deployment.Status = completedCatalogDeploymentStatus(deployment)
	if err := r.Status().Update(ctx, deployment); err != nil {
		t.Fatal(err)
	}
	ready := true
	if err := r.Create(ctx, &discoveryv1.EndpointSlice{
		ObjectMeta:  metav1.ObjectMeta{Name: "catalog-ready", Namespace: catalog.Namespace, Labels: map[string]string{discoveryv1.LabelServiceName: catalogResourceName}},
		AddressType: discoveryv1.AddressTypeIPv4, Endpoints: []discoveryv1.Endpoint{{Addresses: []string{"10.0.0.1"}, Conditions: discoveryv1.EndpointConditions{Ready: &ready}}},
	}); err != nil {
		t.Fatal(err)
	}
}

// envtest and the fake client do not run the Deployment controller.
func completedCatalogDeploymentStatus(deployment *appsv1.Deployment) appsv1.DeploymentStatus {
	replicas := int32(1)
	if deployment.Spec.Replicas != nil {
		replicas = *deployment.Spec.Replicas
	}
	return appsv1.DeploymentStatus{
		ObservedGeneration: deployment.Generation, Replicas: replicas, UpdatedReplicas: replicas, ReadyReplicas: replicas, AvailableReplicas: replicas,
		Conditions: []appsv1.DeploymentCondition{{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionTrue, LastTransitionTime: metav1.NewTime(time.Now().Add(-time.Minute))}},
	}
}

func TestCatalogReleaseDefaultsBecomeReadyWithoutActivationReporter(t *testing.T) {
	for _, defaults := range [][2]string{
		{config.DefaultCatalogDataImage, config.DefaultBenchmarkDataImage},
		{dataTestRepository + "@" + dataTestDigest1, "registry.redhat.io/rhoai/benchmarks@" + dataTestDigest2},
	} {
		t.Run(defaults[0], func(t *testing.T) {
			t.Setenv(config.CatalogDataImage, defaults[0])
			t.Setenv(config.BenchmarkDataImage, defaults[1])
			ctx := context.Background()
			catalog := &catalogv1alpha1.Catalog{ObjectMeta: metav1.ObjectMeta{Name: "catalog", Namespace: "data-test", UID: types.UID("catalog"), Generation: 1}}
			r := newDataImageTestReconciler(t, catalog, true)
			request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(catalog)}
			if _, err := r.Reconcile(ctx, request); err != nil {
				t.Fatal(err)
			}
			markDataImageWorkloadReady(t, r, catalog)
			for range 2 {
				result, err := r.Reconcile(ctx, request)
				if err != nil || result != (ctrl.Result{}) {
					t.Fatalf("healthy release defaults keep reconciling: result=%+v, error=%v", result, err)
				}
			}
			if err := r.Get(ctx, request.NamespacedName, catalog); err != nil {
				t.Fatal(err)
			}
			assertDataImageCondition(t, catalog, conditionCatalogReady, metav1.ConditionTrue, ReasonDeploymentAvailable)
			assertDataImageCondition(t, catalog, ConditionTypeDegraded, metav1.ConditionFalse, "DataImagesHealthy")
			streams := &imagev1.ImageStreamList{}
			if err := r.List(ctx, streams); err != nil || len(streams.Items) != 0 {
				t.Fatalf("empty defaults must not opt into ImageStreams: streams=%+v, error=%v", streams.Items, err)
			}
			deployment := &appsv1.Deployment{}
			if err := r.Get(ctx, client.ObjectKey{Name: catalogResourceName, Namespace: catalog.Namespace}, deployment); err != nil {
				t.Fatal(err)
			}
			if deployment.Spec.Template.Spec.InitContainers[0].Image != defaults[0] || deployment.Spec.Template.Spec.InitContainers[1].Image != defaults[1] {
				t.Fatal("empty selections did not preserve the independent product defaults")
			}
		})
	}
}

func TestCatalogPendingImportUsesWatchAfterWorkloadBecomesReady(t *testing.T) {
	ctx := context.Background()
	stable := "stable"
	catalog := &catalogv1alpha1.Catalog{ObjectMeta: metav1.ObjectMeta{Name: "catalog", Namespace: "data-test", UID: types.UID("catalog"), Generation: 1},
		Spec: catalogv1alpha1.CatalogSpec{CatalogDataImageStream: &stable, BenchmarkDataImageStream: &stable}}
	r := newDataImageTestReconciler(t, catalog, true)
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(catalog)}
	if _, err := r.Reconcile(ctx, request); err != nil {
		t.Fatal(err)
	}
	markDataImageWorkloadReady(t, r, catalog)
	for range 2 {
		result, err := r.Reconcile(ctx, request)
		if err != nil || result != (ctrl.Result{}) {
			t.Fatalf("pending import with healthy workload must wait on the watch: result=%+v, error=%v", result, err)
		}
	}
	if err := r.Get(ctx, request.NamespacedName, catalog); err != nil {
		t.Fatal(err)
	}
	assertDataImageCondition(t, catalog, conditionCatalogReady, metav1.ConditionFalse, "NoSuccessfulImport")
	assertDataImageCondition(t, catalog, conditionImageSelectionReady, metav1.ConditionUnknown, "NoSuccessfulImport")
	assertDataImageCondition(t, catalog, ConditionTypeDegraded, metav1.ConditionFalse, "DataImagesHealthy")
}

func TestCatalogClearingAfterImportFailureRecoversToDevelopmentDefaults(t *testing.T) {
	ctx := context.Background()
	r, catalog := readyDataImageCatalog(t)
	stream := &imagev1.ImageStream{}
	if err := r.Get(ctx, client.ObjectKey{Name: CatalogDataImageStreamName, Namespace: catalog.Namespace}, stream); err != nil {
		t.Fatal(err)
	}
	stream.Status.Tags[0].Conditions = []imagev1.TagEventCondition{{Type: imagev1.ImportSuccess, Status: corev1.ConditionFalse, Reason: "Unauthorized"}}
	if err := r.Status().Update(ctx, stream); err != nil {
		t.Fatal(err)
	}
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(catalog)}
	if _, err := r.Reconcile(ctx, request); err != nil {
		t.Fatal(err)
	}
	if err := r.Get(ctx, request.NamespacedName, catalog); err != nil {
		t.Fatal(err)
	}
	assertDataImageCondition(t, catalog, ConditionTypeDegraded, metav1.ConditionTrue, "ImportFailed")
	t.Setenv(config.CatalogDataImage, config.DefaultCatalogDataImage)
	t.Setenv(config.BenchmarkDataImage, config.DefaultBenchmarkDataImage)
	catalog.Spec.CatalogDataImageStream, catalog.Spec.BenchmarkDataImageStream = nil, nil
	catalog.Generation++
	if err := r.Update(ctx, catalog); err != nil {
		t.Fatal(err)
	}
	// Clearing must recover after a controller restart without an import or
	// an activation writer, even though the bundled references use tags.
	r = &CatalogReconciler{Client: r.Client, Scheme: r.Scheme, Template: r.Template, Capabilities: r.Capabilities}
	if _, err := r.Reconcile(ctx, request); err != nil {
		t.Fatal(err)
	}
	if err := r.Get(ctx, request.NamespacedName, catalog); err != nil {
		t.Fatal(err)
	}
	assertDataImageCondition(t, catalog, conditionDataImageUpdateBlocked, metav1.ConditionFalse, "UpdatesAllowed")
	assertDataImageCondition(t, catalog, ConditionTypeDegraded, metav1.ConditionFalse, "DataImagesHealthy")
	deployment := &appsv1.Deployment{}
	if err := r.Get(ctx, client.ObjectKey{Name: catalogResourceName, Namespace: catalog.Namespace}, deployment); err != nil {
		t.Fatal(err)
	}
	if deployment.Spec.Replicas == nil || *deployment.Spec.Replicas != 1 || deployment.Spec.Template.Spec.InitContainers[0].Image != config.DefaultCatalogDataImage || deployment.Spec.Template.Spec.InitContainers[1].Image != config.DefaultBenchmarkDataImage {
		t.Fatal("clearing did not restore the product default rollout")
	}
}

func TestCatalogWorkloadReadFailureInvalidatesReadyStatus(t *testing.T) {
	ctx := context.Background()
	r, catalog := readyDataImageCatalog(t)
	fault := errors.New("Deployment API unavailable")
	base := r.Client.(client.WithWatch)
	r.Client = interceptor.NewClient(base, interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, deployment := obj.(*appsv1.Deployment); deployment {
				return fault
			}
			return c.Get(ctx, key, obj, opts...)
		},
	})
	if _, err := r.updateStatus(ctx, catalog, catalog.DeepCopy()); !errors.Is(err, fault) {
		t.Fatalf("lost workload observation failure: %v", err)
	}
	if err := base.Get(ctx, client.ObjectKeyFromObject(catalog), catalog); err != nil {
		t.Fatal(err)
	}
	assertDataImageCondition(t, catalog, conditionWorkloadAvailable, metav1.ConditionUnknown, "WorkloadStatusUnknown")
	assertDataImageCondition(t, catalog, conditionCatalogReady, metav1.ConditionFalse, "WorkloadStatusUnknown")
}
