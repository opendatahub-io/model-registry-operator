package controller

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	catalogv1alpha1 "github.com/opendatahub-io/model-registry-operator/api/catalog/v1alpha1"
	"github.com/opendatahub-io/model-registry-operator/internal/controller/config"
	imagev1 "github.com/openshift/api/image/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func assertDataImageCondition(t *testing.T, catalog *catalogv1alpha1.Catalog, conditionType string, status metav1.ConditionStatus, reason string) {
	t.Helper()
	condition := apimeta.FindStatusCondition(catalog.Status.Conditions, conditionType)
	if condition == nil || condition.Status != status || condition.Reason != reason || condition.ObservedGeneration != catalog.Generation {
		t.Fatalf("unexpected %s condition: %+v", conditionType, condition)
	}
}

// Seed a ready Catalog so image import faults can be tested independently of
// workload readiness. The fake API does not run a Deployment or image importer.
func readyDataImageCatalog(t *testing.T) (*CatalogReconciler, *catalogv1alpha1.Catalog) {
	t.Helper()
	t.Setenv(config.CatalogDataImage, dataTestRepository+"@"+dataTestDigest3)
	t.Setenv(config.BenchmarkDataImage, dataTestRepository+"@"+dataTestDigest3)
	ctx := context.Background()
	stable := "stable"
	catalog := &catalogv1alpha1.Catalog{ObjectMeta: metav1.ObjectMeta{Name: "catalog", Namespace: "data-test", UID: types.UID("catalog"), Generation: 1},
		Spec: catalogv1alpha1.CatalogSpec{CatalogDataImageStream: &stable, BenchmarkDataImageStream: &stable}}
	r := newDataImageTestReconciler(t, catalog, true)
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(catalog)}); err != nil {
		t.Fatal(err)
	}
	stream := &imagev1.ImageStream{}
	if err := r.Get(ctx, client.ObjectKey{Name: CatalogDataImageStreamName, Namespace: catalog.Namespace}, stream); err != nil {
		t.Fatal(err)
	}
	stream.Status.Tags = []imagev1.NamedTagEventList{{Tag: "stable", Items: []imagev1.TagEvent{{DockerImageReference: dataTestRepository + "@" + dataTestDigest1}}}}
	if err := r.Status().Update(ctx, stream); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(catalog)}); err != nil {
		t.Fatal(err)
	}
	deployment := &appsv1.Deployment{}
	if err := r.Get(ctx, client.ObjectKey{Name: catalogResourceName, Namespace: catalog.Namespace}, deployment); err != nil {
		t.Fatal(err)
	}
	deployment.Status = completedCatalogDeploymentStatus(deployment)
	if err := r.Status().Update(ctx, deployment); err != nil {
		t.Fatal(err)
	}
	ready := true
	if err := r.Create(ctx, &discoveryv1.EndpointSlice{ObjectMeta: metav1.ObjectMeta{Name: "catalog-ready", Namespace: catalog.Namespace, Labels: map[string]string{discoveryv1.LabelServiceName: catalogResourceName}},
		AddressType: discoveryv1.AddressTypeIPv4, Endpoints: []discoveryv1.Endpoint{{Addresses: []string{"10.0.0.1"}, Conditions: discoveryv1.EndpointConditions{Ready: &ready}}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(catalog)}); err != nil {
		t.Fatal(err)
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(catalog), catalog); err != nil {
		t.Fatal(err)
	}
	assertDataImageCondition(t, catalog, ConditionTypeAvailable, metav1.ConditionTrue, ReasonDeploymentAvailable)
	return r, catalog
}

func TestCatalogDataImageImportHealth(t *testing.T) {
	for _, tc := range []struct {
		name       string
		generation int64
		items      []imagev1.TagEvent
		failure    *imagev1.TagEventCondition
		status     metav1.ConditionStatus
		reason     string
	}{
		{name: "first import pending", status: metav1.ConditionUnknown, reason: "ImportPending"},
		{name: "first import failed", failure: &imagev1.TagEventCondition{Type: imagev1.ImportSuccess, Status: corev1.ConditionFalse, Reason: "Unauthorized", Message: "Registry authentication failed"}, status: metav1.ConditionFalse, reason: "ImportFailed"},
		{name: "successful import", items: []imagev1.TagEvent{{DockerImageReference: dataTestRepository + "@" + dataTestDigest1}}, status: metav1.ConditionTrue, reason: "ImportSucceeded"},
		{name: "failed update with history", items: []imagev1.TagEvent{{DockerImageReference: dataTestRepository + "@" + dataTestDigest1}}, failure: &imagev1.TagEventCondition{Type: imagev1.ImportSuccess, Status: corev1.ConditionFalse}, status: metav1.ConditionFalse, reason: "ImportFailed"},
		{name: "new source pending", generation: 2, items: []imagev1.TagEvent{{Generation: 1, DockerImageReference: dataTestRepository + "@" + dataTestDigest1}}, failure: &imagev1.TagEventCondition{Type: imagev1.ImportSuccess, Status: corev1.ConditionFalse, Generation: 1}, status: metav1.ConditionUnknown, reason: "ImportPending"},
		{name: "new source failed", generation: 2, items: []imagev1.TagEvent{{Generation: 1, DockerImageReference: dataTestRepository + "@" + dataTestDigest1}}, failure: &imagev1.TagEventCondition{Type: imagev1.ImportSuccess, Status: corev1.ConditionFalse, Generation: 2}, status: metav1.ConditionFalse, reason: "ImportFailed"},
		{name: "recovered source ignores old failure", generation: 2, items: []imagev1.TagEvent{{Generation: 2, DockerImageReference: dataTestRepository + "@" + dataTestDigest2}}, failure: &imagev1.TagEventCondition{Type: imagev1.ImportSuccess, Status: corev1.ConditionFalse, Generation: 1}, status: metav1.ConditionTrue, reason: "ImportSucceeded"},
		{name: "malformed imported reference", items: []imagev1.TagEvent{{DockerImageReference: dataTestRepository + ":stable"}}, status: metav1.ConditionFalse, reason: "InvalidImportedImage"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tag := imagev1.NamedTagEventList{Tag: "stable", Items: tc.items}
			if tc.failure != nil {
				tag.Conditions = []imagev1.TagEventCondition{*tc.failure}
			}
			stream := &imagev1.ImageStream{Spec: imagev1.ImageStreamSpec{Tags: []imagev1.TagReference{{Name: "stable", Generation: &tc.generation}}},
				Status: imagev1.ImageStreamStatus{Tags: []imagev1.NamedTagEventList{{Tag: "unrelated"}, tag}}}
			condition := catalogDataImageImportHealth(stream)
			if condition.Status != tc.status || condition.Reason != tc.reason {
				t.Fatalf("unexpected import health: %+v", condition)
			}
			if tc.failure != nil && tc.failure.Message != "" && !strings.Contains(condition.Message, tc.failure.Message) {
				t.Fatal("import failure details must be preserved in Catalog status")
			}
		})
	}
}

func TestCatalogDataImageAPIFailureStatusAndRecovery(t *testing.T) {
	for _, operation := range []string{"get stream", "create stream", "update stream", "read applied deployment", "stop deployment"} {
		t.Run(operation, func(t *testing.T) {
			ctx := context.Background()
			r, catalog := readyDataImageCatalog(t)
			base := r.Client.(client.WithWatch)
			stream := &imagev1.ImageStream{}
			streamKey := client.ObjectKey{Name: CatalogDataImageStreamName, Namespace: catalog.Namespace}
			if err := base.Get(ctx, streamKey, stream); err != nil {
				t.Fatal(err)
			}
			switch operation {
			case "create stream":
				if err := base.Delete(ctx, stream); err != nil {
					t.Fatal(err)
				}
			case "update stream":
				stream.Spec.Tags[0].ImportPolicy.Scheduled = false
				if err := base.Update(ctx, stream); err != nil {
					t.Fatal(err)
				}
			case "read applied deployment":
				stream.Status.Tags = nil
				if err := base.Status().Update(ctx, stream); err != nil {
					t.Fatal(err)
				}
			case "stop deployment":
				stream.Status.Tags[0].Conditions = []imagev1.TagEventCondition{{Type: imagev1.ImportSuccess, Status: corev1.ConditionFalse, Reason: "Unauthorized"}}
				if err := base.Status().Update(ctx, stream); err != nil {
					t.Fatal(err)
				}
			}
			deploymentKey := client.ObjectKey{Name: catalogResourceName, Namespace: catalog.Namespace}
			before := &appsv1.Deployment{}
			if err := base.Get(ctx, deploymentKey, before); err != nil {
				t.Fatal(err)
			}
			fault := errors.New("temporary API failure")
			fail := true
			r.Client = interceptor.NewClient(base, interceptor.Funcs{
				Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					_, isStream := obj.(*imagev1.ImageStream)
					_, isDeployment := obj.(*appsv1.Deployment)
					if fail && ((operation == "get stream" && isStream) || (operation == "read applied deployment" && isDeployment)) {
						return fault
					}
					return c.Get(ctx, key, obj, opts...)
				},
				Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
					if _, isStream := obj.(*imagev1.ImageStream); fail && operation == "create stream" && isStream {
						return fault
					}
					return c.Create(ctx, obj, opts...)
				},
				Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
					if _, isStream := obj.(*imagev1.ImageStream); fail && operation == "update stream" && isStream {
						return fault
					}
					return c.Update(ctx, obj, opts...)
				},
				Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
					if _, isDeployment := obj.(*appsv1.Deployment); fail && operation == "stop deployment" && isDeployment {
						return fault
					}
					return c.Patch(ctx, obj, patch, opts...)
				},
			})
			_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(catalog)})
			if !errors.Is(err, fault) {
				t.Fatalf("reconcile error = %v, want API failure", err)
			}
			if err := base.Get(ctx, client.ObjectKeyFromObject(catalog), catalog); err != nil {
				t.Fatal(err)
			}
			reason := "ImageStreamUnavailable"
			if operation == "read applied deployment" {
				reason = "AppliedImageReadFailed"
			}
			if operation == "stop deployment" {
				reason = "ImportFailed"
			}
			assertDataImageCondition(t, catalog, conditionDataImageResolved, metav1.ConditionFalse, reason)
			assertDataImageCondition(t, catalog, ConditionTypeDegraded, metav1.ConditionTrue, reason)
			expectedAvailability := metav1.ConditionFalse // A containment error never hides a confirmed failure.
			if available := apimeta.FindStatusCondition(catalog.Status.Conditions, ConditionTypeAvailable); available == nil || available.Status != expectedAvailability {
				t.Fatalf("unexpected availability after image fault: %+v", available)
			}
			after := &appsv1.Deployment{}
			if err := base.Get(ctx, deploymentKey, after); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before.Spec.Template, after.Spec.Template) {
				t.Fatal("API failure changed the applied pod template")
			}

			fail = false
			if operation == "read applied deployment" {
				stable := "stable"
				catalog.Spec.BenchmarkDataImageStream = &stable
				catalog.Generation++
				if err := base.Update(ctx, catalog); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(catalog)}); err != nil {
				t.Fatal(err)
			}
			if err := base.Get(ctx, streamKey, stream); err != nil {
				t.Fatal(err)
			}
			stream.Status.Tags = []imagev1.NamedTagEventList{{Tag: "stable", Items: []imagev1.TagEvent{{DockerImageReference: dataTestRepository + "@" + dataTestDigest2}}}}
			if err := base.Status().Update(ctx, stream); err != nil {
				t.Fatal(err)
			}
			if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(catalog)}); err != nil {
				t.Fatal(err)
			}
			if err := base.Get(ctx, client.ObjectKeyFromObject(catalog), catalog); err != nil {
				t.Fatal(err)
			}
			assertDataImageCondition(t, catalog, conditionDataImageImportHealthy, metav1.ConditionTrue, "ImportSucceeded")
			assertDataImageCondition(t, catalog, ConditionTypeDegraded, metav1.ConditionFalse, "DataImagesHealthy")
			if err := base.Get(ctx, deploymentKey, after); err != nil {
				t.Fatal(err)
			}
			for _, container := range after.Spec.Template.Spec.InitContainers {
				if container.Image != dataTestRepository+"@"+dataTestDigest2 {
					t.Fatal("recovery must apply the new successful digest")
				}
			}
		})
	}
}

func TestCatalogDataImageStatusWrites(t *testing.T) {
	for _, failed := range []bool{false, true} {
		name := "healthy observations do not write status"
		if failed {
			name = "repeated import failures do not write status"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			r, catalog := readyDataImageCatalog(t)
			if failed {
				stream := &imagev1.ImageStream{}
				if err := r.Get(ctx, client.ObjectKey{Name: CatalogDataImageStreamName, Namespace: catalog.Namespace}, stream); err != nil {
					t.Fatal(err)
				}
				stream.Status.Tags[0].Conditions = []imagev1.TagEventCondition{{Type: imagev1.ImportSuccess, Status: corev1.ConditionFalse, Reason: "Unauthorized"}}
				if err := r.Status().Update(ctx, stream); err != nil {
					t.Fatal(err)
				}
				if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(catalog)}); err != nil {
					t.Fatal(err)
				}
			}
			writes := 0
			r.Client = interceptor.NewClient(r.Client.(client.WithWatch), interceptor.Funcs{
				SubResourcePatch: func(ctx context.Context, c client.Client, name string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
					if name == "status" {
						writes++
					}
					return c.SubResource(name).Patch(ctx, obj, patch, opts...)
				},
			})
			for range 2 {
				if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(catalog)}); err != nil {
					t.Fatal(err)
				}
			}
			if writes != 0 {
				t.Fatalf("unchanged reconciles wrote status %d times", writes)
			}
		})
	}

	t.Run("retain reconciliation and status errors", func(t *testing.T) {
		ctx := context.Background()
		r, catalog := readyDataImageCatalog(t)
		apiFailure, statusFailure := errors.New("ImageStream unavailable"), errors.New("status write failed")
		r.Client = interceptor.NewClient(r.Client.(client.WithWatch), interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if _, isStream := obj.(*imagev1.ImageStream); isStream {
					return apiFailure
				}
				return c.Get(ctx, key, obj, opts...)
			},
			SubResourcePatch: func(context.Context, client.Client, string, client.Object, client.Patch, ...client.SubResourcePatchOption) error {
				return statusFailure
			},
		})
		_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(catalog)})
		if !errors.Is(err, apiFailure) || !errors.Is(err, statusFailure) {
			t.Fatalf("lost reconcile or status failure: %v", err)
		}
	})
	t.Run("concurrent selection edit rejects stale status", func(t *testing.T) {
		ctx := context.Background()
		r, catalog := readyDataImageCatalog(t)
		base := r.Client.(client.WithWatch)
		apiFailure := errors.New("ImageStream unavailable")
		r.Client = interceptor.NewClient(base, interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if _, isStream := obj.(*imagev1.ImageStream); isStream {
					current := &catalogv1alpha1.Catalog{}
					if err := c.Get(ctx, client.ObjectKeyFromObject(catalog), current); err != nil {
						return err
					}
					pin := dataTestDigest2
					current.Spec.CatalogDataImageStream, current.Spec.BenchmarkDataImageStream = &pin, &pin
					current.Generation++
					if err := c.Update(ctx, current); err != nil {
						return err
					}
					return apiFailure
				}
				return c.Get(ctx, key, obj, opts...)
			},
		})
		_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(catalog)})
		if !errors.Is(err, apiFailure) || !apierrors.IsConflict(err) {
			t.Fatalf("expected API failure and optimistic status conflict, got %v", err)
		}
		if err := base.Get(ctx, client.ObjectKeyFromObject(catalog), catalog); err != nil {
			t.Fatal(err)
		}
		if *catalog.Spec.CatalogDataImageStream != dataTestDigest2 {
			t.Fatal("status update overwrote the concurrent selection")
		}
		if condition := apimeta.FindStatusCondition(catalog.Status.Conditions, ConditionTypeDegraded); condition.Status != metav1.ConditionFalse || condition.ObservedGeneration == catalog.Generation {
			t.Fatal("stale degraded status was published for a newer selection")
		}
	})
}

func TestCatalogManualRecoveryDuringImageStreamOutage(t *testing.T) {
	ctx := context.Background()
	r, catalog := readyDataImageCatalog(t)
	base := r.Client.(client.WithWatch)
	fault := errors.New("ImageStream API unavailable")
	r.Client = interceptor.NewClient(base, interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, isStream := obj.(*imagev1.ImageStream); isStream {
				return fault
			}
			return c.Get(ctx, key, obj, opts...)
		},
	})
	key := client.ObjectKeyFromObject(catalog)
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: key}); !errors.Is(err, fault) {
		t.Fatalf("expected an ImageStream fault: %v", err)
	}
	if err := base.Get(ctx, key, catalog); err != nil {
		t.Fatal(err)
	}
	for _, selection := range []string{dataTestDigest2, ""} {
		catalog.Spec.CatalogDataImageStream, catalog.Spec.BenchmarkDataImageStream = &selection, &selection
		catalog.Generation++
		if err := base.Update(ctx, catalog); err != nil {
			t.Fatal(err)
		}
		if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: key}); err != nil {
			t.Fatalf("manual recovery must not depend on ImageStreams: %v", err)
		}
		deployment := &appsv1.Deployment{}
		if err := base.Get(ctx, client.ObjectKey{Name: catalogResourceName, Namespace: catalog.Namespace}, deployment); err != nil {
			t.Fatal(err)
		}
		if deployment.Spec.Replicas == nil || *deployment.Spec.Replicas != 1 {
			t.Fatal("manual recovery did not restart Catalog serving")
		}
		if err := base.Get(ctx, key, catalog); err != nil {
			t.Fatal(err)
		}
		assertDataImageCondition(t, catalog, conditionDataImageUpdateBlocked, metav1.ConditionFalse, "UpdatesAllowed")
		assertDataImageCondition(t, catalog, ConditionTypeDegraded, metav1.ConditionFalse, "DataImagesHealthy")
	}
}

func TestCatalogPendingRetryDoesNotResumeAfterImportFailure(t *testing.T) {
	ctx := context.Background()
	r, catalog := readyDataImageCatalog(t)
	stream := &imagev1.ImageStream{}
	streamKey := client.ObjectKey{Name: CatalogDataImageStreamName, Namespace: catalog.Namespace}
	if err := r.Get(ctx, streamKey, stream); err != nil {
		t.Fatal(err)
	}
	generation := int64(1)
	stream.Spec.Tags[0].Generation = &generation
	if err := r.Update(ctx, stream); err != nil {
		t.Fatal(err)
	}
	stream.Status.Tags[0].Items[0].Generation = 1
	stream.Status.Tags[0].Conditions = []imagev1.TagEventCondition{{Type: imagev1.ImportSuccess, Status: corev1.ConditionFalse, Generation: 1, Reason: "Unauthorized"}}
	if err := r.Status().Update(ctx, stream); err != nil {
		t.Fatal(err)
	}
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(catalog)}
	checkReplicas := func(want int32) {
		t.Helper()
		if _, err := r.Reconcile(ctx, request); err != nil {
			t.Fatal(err)
		}
		deployment := &appsv1.Deployment{}
		if err := r.Get(ctx, client.ObjectKey{Name: catalogResourceName, Namespace: catalog.Namespace}, deployment); err != nil {
			t.Fatal(err)
		}
		if deployment.Spec.Replicas == nil || *deployment.Spec.Replicas != want {
			t.Fatalf("replicas = %v, want %d", deployment.Spec.Replicas, want)
		}
	}
	checkReplicas(0)
	if err := r.Get(ctx, streamKey, stream); err != nil {
		t.Fatal(err)
	}
	generation = 2
	stream.Spec.Tags[0].Generation = &generation
	if err := r.Update(ctx, stream); err != nil {
		t.Fatal(err)
	}
	// The previous failed condition is stale, but a pending new source must
	// not restore old content before there is a successful import.
	checkReplicas(0)
	if err := r.Get(ctx, request.NamespacedName, catalog); err != nil {
		t.Fatal(err)
	}
	assertDataImageCondition(t, catalog, conditionImageSelectionReady, metav1.ConditionUnknown, "AwaitingSuccessfulImport")
	assertDataImageCondition(t, catalog, ConditionTypeDegraded, metav1.ConditionTrue, "ImportFailed")
	// Persisted conditions preserve this behavior over reconciler restarts.
	r = &CatalogReconciler{Client: r.Client, Scheme: r.Scheme, Template: r.Template, Recorder: r.Recorder, Log: r.Log, Capabilities: r.Capabilities}
	checkReplicas(0)
	if err := r.Get(ctx, streamKey, stream); err != nil {
		t.Fatal(err)
	}
	stream.Status.Tags = []imagev1.NamedTagEventList{{Tag: "stable", Items: []imagev1.TagEvent{{Generation: 2, DockerImageReference: dataTestRepository + "@" + dataTestDigest2}}}}
	if err := r.Status().Update(ctx, stream); err != nil {
		t.Fatal(err)
	}
	checkReplicas(1)
}
