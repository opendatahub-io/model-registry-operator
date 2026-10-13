package controller

import (
	"context"
	"testing"
	"time"

	"github.com/opendatahub-io/model-registry-operator/internal/controller/config"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestActivationFailureRequiresNewValidatedAttempt(t *testing.T) {
	ctx := context.Background()
	r, catalog := readyDataImageCatalog(t)
	old := catalog.Status.Activation.DeepCopy()
	failure := &catalogActivationFailure{Stage: "Loading", Reason: "CatalogContentInvalid", Message: "invalid YAML"}
	if err := r.observeActivation(ctx, catalog, failure); err != nil {
		t.Fatal(err)
	}
	if err := r.observeActivation(ctx, catalog, nil); err != nil {
		t.Fatal(err)
	}
	if catalog.Status.Activation.State != "Failed" {
		t.Fatal("old ready pod cleared failure")
	}
	if *catalog.Status.Activation.LastSuccessful != *old.LastSuccessful {
		t.Fatal("lost recovery metadata")
	}
	if err := r.Status().Update(ctx, catalog); err != nil {
		t.Fatal(err)
	}
	catalog.Annotations = map[string]string{"aihub.opendatahub.io/retry-activation": "retry-1"}
	if err := r.Update(ctx, catalog); err != nil {
		t.Fatal(err)
	}
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
	reconcile()
	if catalog.Status.Activation.AttemptID == old.AttemptID || catalog.Status.Activation.State != "Pending" {
		t.Fatal("retry must create a new pending attempt")
	}
	// Existing successful pods cannot certify the replacement candidate.
	reconcile()
	if catalog.Status.Activation.State != "Pending" {
		t.Fatal("stale pod accepted")
	}
	markDataImageWorkloadReady(t, r, catalog)
	markCatalogValidationReady(t, r, catalog)
	reconcile()
	assertDataImageCondition(t, catalog, conditionDataImageActivationFailed, metav1.ConditionFalse, "WorkloadValidated")
	assertDataImageCondition(t, catalog, conditionCatalogReady, metav1.ConditionTrue, ReasonDeploymentAvailable)

	var maps corev1.ConfigMapList
	if err := r.List(ctx, &maps); err != nil {
		t.Fatal(err)
	}
	for _, cm := range maps.Items {
		if cm.Data["intent.json"] != "" || cm.Data["report.json"] != "" {
			t.Fatal("runtime activation transport remains")
		}
	}
}

func TestActivationValidationDeadline(t *testing.T) {
	ctx := context.Background()
	r, catalog := readyDataImageCatalog(t)
	catalog.Status.Activation.AttemptID = "unvalidated"
	catalog.Status.Activation.State = "Pending"
	catalog.Status.Activation.StartedAt = metav1.NewTime(time.Now().Add(-11 * time.Minute))
	if err := r.observeActivation(ctx, catalog, nil); err != nil {
		t.Fatal(err)
	}
	if catalog.Status.Activation.Reason != "ActivationTimedOut" {
		t.Fatal("missing startup deadline")
	}
}

func TestActivationAcceptsReleaseTagsWithoutRuntimeProtocol(t *testing.T) {
	t.Setenv(config.ModelRegistryOperatorImage, "registry/operator:test")
	ctx := context.Background()
	r, catalog := readyDataImageCatalog(t)
	t.Setenv(config.CatalogDataImage, "registry/catalog:release")
	t.Setenv(config.BenchmarkDataImage, "registry/benchmark:release")
	catalog.Spec.CatalogDataImageStream = nil
	catalog.Spec.BenchmarkDataImageStream = nil
	catalog.Generation++
	if err := r.Update(ctx, catalog); err != nil {
		t.Fatal(err)
	}
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(catalog)}
	if _, err := r.Reconcile(ctx, request); err != nil {
		t.Fatal(err)
	}
	markDataImageWorkloadReady(t, r, catalog)
	markCatalogValidationReady(t, r, catalog)
	if _, err := r.Reconcile(ctx, request); err != nil {
		t.Fatal(err)
	}
	if err := r.Get(ctx, request.NamespacedName, catalog); err != nil {
		t.Fatal(err)
	}
	if catalog.Status.Activation.Images.Catalog != "registry/catalog:release" || catalog.Status.Activation.State != "Succeeded" {
		t.Fatalf("release tags blocked: %+v", catalog.Status.Activation)
	}
	deployment := &appsv1.Deployment{}
	if err := r.Get(ctx, client.ObjectKey{Name: catalogResourceName, Namespace: catalog.Namespace}, deployment); err != nil {
		t.Fatal(err)
	}
	if deployment.Spec.Template.Spec.InitContainers[2].Image != "registry/operator:test" {
		t.Fatal("validator must use released operator image")
	}
}

func TestFailedAttemptRemainsScaledDownUntilSelectionChanges(t *testing.T) {
	ctx := context.Background()
	r, catalog := readyDataImageCatalog(t)
	if err := r.observeActivation(ctx, catalog, &catalogActivationFailure{Stage: "Loading", Reason: "CatalogContentInvalid", Message: "invalid content"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Status().Update(ctx, catalog); err != nil {
		t.Fatal(err)
	}
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(catalog)}
	for i := 0; i < 3; i++ {
		if _, err := r.Reconcile(ctx, request); err != nil {
			t.Fatal(err)
		}
		deployment := &appsv1.Deployment{}
		if err := r.Get(ctx, client.ObjectKey{Namespace: catalog.Namespace, Name: catalogResourceName}, deployment); err != nil {
			t.Fatal(err)
		}
		if deployment.Spec.Replicas == nil || *deployment.Spec.Replicas != 0 {
			t.Fatal("failed attempt resumed old catalog")
		}
	}
	if err := r.Get(ctx, request.NamespacedName, catalog); err != nil {
		t.Fatal(err)
	}
	selection := dataTestDigest2
	catalog.Spec.CatalogDataImageStream = &selection
	catalog.Generation++
	if err := r.Update(ctx, catalog); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, request); err != nil {
		t.Fatal(err)
	}
	deployment := &appsv1.Deployment{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: catalog.Namespace, Name: catalogResourceName}, deployment); err != nil {
		t.Fatal(err)
	}
	if deployment.Spec.Replicas == nil || *deployment.Spec.Replicas != 1 {
		t.Fatal("corrected selection did not permit retry")
	}
}

func TestReadyDeploymentWithoutValidatedPodIsNotActivation(t *testing.T) {
	ctx := context.Background()
	r, catalog := readyDataImageCatalog(t)
	pods := &corev1.PodList{}
	if err := r.List(ctx, pods, client.InNamespace(catalog.Namespace)); err != nil {
		t.Fatal(err)
	}
	for i := range pods.Items {
		if err := r.Delete(ctx, &pods.Items[i]); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(catalog)}); err != nil {
		t.Fatal(err)
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(catalog), catalog); err != nil {
		t.Fatal(err)
	}
	assertDataImageCondition(t, catalog, conditionCatalogReady, metav1.ConditionFalse, "ActivationPending")
}
