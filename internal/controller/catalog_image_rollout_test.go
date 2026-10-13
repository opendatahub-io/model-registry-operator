package controller

import (
	"context"
	"testing"

	catalogv1alpha1 "github.com/opendatahub-io/model-registry-operator/api/catalog/v1alpha1"
	"github.com/opendatahub-io/model-registry-operator/internal/controller/config"
	imagev1 "github.com/openshift/api/image/v1"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestCatalogScheduledImportWaitsForSelectedImagePairRollout(t *testing.T) {
	for _, changed := range []string{"catalog", "benchmark", "both"} {
		t.Run(changed, func(t *testing.T) {
			const benchmarkRepository = "registry.example/benchmarks"
			t.Setenv(config.CatalogDataImageStreamSource, dataTestRepository+":stable")
			t.Setenv(config.BenchmarkDataImageStreamSource, benchmarkRepository+":stable")
			ctx := context.Background()
			stable := "stable"
			catalog := &catalogv1alpha1.Catalog{ObjectMeta: metav1.ObjectMeta{Name: "catalog", Namespace: "data-test", UID: types.UID("catalog"), Generation: 1},
				Spec: catalogv1alpha1.CatalogSpec{CatalogDataImageStream: &stable, BenchmarkDataImageStream: &stable}}
			r := newDataImageTestReconciler(t, catalog, true)
			request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(catalog)}
			reconcile := func() {
				t.Helper()
				if _, err := r.Reconcile(ctx, request); err != nil {
					t.Fatal(err)
				}
				if err := r.Get(ctx, request.NamespacedName, catalog); err != nil {
					t.Fatal(err)
				}
			}
			importImage := func(name, repository, digest string) {
				t.Helper()
				stream := &imagev1.ImageStream{}
				if err := r.Get(ctx, client.ObjectKey{Name: name, Namespace: catalog.Namespace}, stream); err != nil {
					t.Fatal(err)
				}
				stream.Status.Tags = []imagev1.NamedTagEventList{{Tag: "stable", Items: []imagev1.TagEvent{{DockerImageReference: repository + "@" + digest}}}}
				if err := r.Status().Update(ctx, stream); err != nil {
					t.Fatal(err)
				}
			}
			reconcile()
			importImage(CatalogDataImageStreamName, dataTestRepository, dataTestDigest1)
			importImage(BenchmarkDataImageStreamName, benchmarkRepository, dataTestDigest1)
			reconcile()
			markDataImageWorkloadReady(t, r, catalog)
			reconcile()
			assertDataImageCondition(t, catalog, conditionCatalogReady, metav1.ConditionTrue, ReasonDeploymentAvailable)
			generation := catalog.Generation
			want := *catalog.Status.ResolvedImages
			if changed != "benchmark" {
				importImage(CatalogDataImageStreamName, dataTestRepository, dataTestDigest2)
				want.Catalog = dataTestRepository + "@" + dataTestDigest2
			}
			if changed != "catalog" {
				importImage(BenchmarkDataImageStreamName, benchmarkRepository, dataTestDigest2)
				want.Benchmark = benchmarkRepository + "@" + dataTestDigest2
			}
			// Check persisted readiness at the exact point before applying the new
			// template, while the previous deployment and endpoints are healthy.
			checkedBeforeApply := false
			r.Client = interceptor.NewClient(r.Client.(client.WithWatch), interceptor.Funcs{
				Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
					if _, deployment := obj.(*appsv1.Deployment); deployment && obj.GetName() == catalogResourceName {
						current := &catalogv1alpha1.Catalog{}
						if err := c.Get(ctx, request.NamespacedName, current); err != nil {
							return err
						}
						assertDataImageCondition(t, current, conditionCatalogReady, metav1.ConditionFalse, "ImagesUpdating")
						if current.Status.ResolvedImages == nil || *current.Status.ResolvedImages != want {
							t.Fatal("current image pair was not persisted before applying the deployment")
						}
						checkedBeforeApply = true
					}
					return c.Update(ctx, obj, opts...)
				},
			})
			r.resourceManager = nil
			reconcile()
			if !checkedBeforeApply || catalog.Generation != generation {
				t.Fatal("scheduled import did not invalidate readiness independently of the Catalog generation")
			}
			assertDataImageCondition(t, catalog, conditionCatalogReady, metav1.ConditionFalse, ReasonDeploymentUnavailable)
			assertDataImageCondition(t, catalog, ConditionTypeDegraded, metav1.ConditionFalse, "DataImagesHealthy")
			// Persisted intent also protects against reusing old status after restart.
			r = &CatalogReconciler{Client: r.Client, Scheme: r.Scheme, Template: r.Template, Capabilities: r.Capabilities}
			reconcile()
			assertDataImageCondition(t, catalog, conditionCatalogReady, metav1.ConditionFalse, ReasonDeploymentUnavailable)
			deployment := &appsv1.Deployment{}
			deploymentKey := client.ObjectKey{Name: catalogResourceName, Namespace: catalog.Namespace}
			for _, incomplete := range []string{"not updated", "not ready", "not available", "old replicas remain", "complete"} {
				if err := r.Get(ctx, deploymentKey, deployment); err != nil {
					t.Fatal(err)
				}
				deployment.Status = completedCatalogDeploymentStatus(deployment)
				switch incomplete {
				case "not updated":
					deployment.Status.UpdatedReplicas = 0
				case "not ready":
					deployment.Status.ReadyReplicas = 0
				case "not available":
					deployment.Status.AvailableReplicas = 0
				case "old replicas remain":
					deployment.Status.Replicas++
				}
				if err := r.Status().Update(ctx, deployment); err != nil {
					t.Fatal(err)
				}
				reconcile()
				status, reason := metav1.ConditionFalse, ReasonDeploymentUnavailable
				if incomplete == "complete" {
					status, reason = metav1.ConditionTrue, ReasonDeploymentAvailable
				}
				assertDataImageCondition(t, catalog, conditionCatalogReady, status, reason)
			}
		})
	}
}

func TestCatalogUnchangedImportedImagesRetainReadyWorkload(t *testing.T) {
	ctx := context.Background()
	r, catalog := readyDataImageCatalog(t)
	before := catalog.DeepCopy()
	stream := &imagev1.ImageStream{}
	if err := r.Get(ctx, client.ObjectKey{Name: CatalogDataImageStreamName, Namespace: catalog.Namespace}, stream); err != nil {
		t.Fatal(err)
	}
	stream.Status.Tags[0].Items[0].Created = metav1.Now()
	stream.Status.Tags[0].Items = append(stream.Status.Tags[0].Items, imagev1.TagEvent{DockerImageReference: dataTestRepository + "@" + dataTestDigest2})
	if err := r.Status().Update(ctx, stream); err != nil {
		t.Fatal(err)
	}
	writes := 0
	r.Client = interceptor.NewClient(r.Client.(client.WithWatch), interceptor.Funcs{
		SubResourcePatch: func(ctx context.Context, c client.Client, name string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
			if _, catalog := obj.(*catalogv1alpha1.Catalog); catalog && name == "status" {
				writes++
			}
			return c.SubResource(name).Patch(ctx, obj, patch, opts...)
		},
	})
	result, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(catalog)})
	if err != nil || result != (ctrl.Result{}) || writes != 0 {
		t.Fatalf("unchanged images interrupted the healthy workload: result=%+v, error=%v, status writes=%d", result, err, writes)
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(catalog), catalog); err != nil {
		t.Fatal(err)
	}
	if catalog.Generation != before.Generation || *catalog.Status.ResolvedImages != *before.Status.ResolvedImages {
		t.Fatal("unchanged import modified the selected pair")
	}
	assertDataImageCondition(t, catalog, conditionCatalogReady, metav1.ConditionTrue, ReasonDeploymentAvailable)
}

func TestCatalogWorkloadTemplateMustMatchBothResolvedImages(t *testing.T) {
	r, catalog := readyDataImageCatalog(t)
	want := *catalog.Status.ResolvedImages
	for _, changed := range []string{"catalog", "benchmark"} {
		mismatch := want
		if changed == "catalog" {
			mismatch.Catalog = dataTestRepository + "@" + dataTestDigest2
		} else {
			mismatch.Benchmark = dataTestRepository + "@" + dataTestDigest2
		}
		condition, err := r.checkDeploymentAvailability(context.Background(), client.ObjectKey{Name: catalogResourceName, Namespace: catalog.Namespace}, catalogResourceName, catalogResourceName, &mismatch)
		if err != nil || condition.Status != metav1.ConditionFalse || condition.Reason != "ImagesUpdating" {
			t.Fatalf("accepted workload for wrong %s image: condition=%+v, error=%v", changed, condition, err)
		}
	}
}
