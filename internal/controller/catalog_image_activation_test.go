package controller

import (
	"context"
	"reflect"
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
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// A test-only activation producer. Production must obtain this evidence from
// validated runtime activation; no controller code fabricates these outcomes.
func reportDataImageActivation(t *testing.T, r *CatalogReconciler, catalog *catalogv1alpha1.Catalog, state, stage, reason string) {
	t.Helper()
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(catalog), catalog); err != nil {
		t.Fatal(err)
	}
	before := catalog.DeepCopy()
	attempt := &catalog.Status.ImageUpdate.CurrentAttempt
	if attempt.ResolvedImages == nil {
		t.Fatal("activation requires a resolved immutable image pair")
	}
	refs := attempt.ResolvedImages.References()
	catalog.Status.ImageUpdate.ActivationOutcome = &catalogv1alpha1.CatalogImageOutcome{
		CatalogUID: catalog.UID, AttemptID: attempt.AttemptID, Images: &refs,
		State: state, Stage: stage, Reason: reason, Message: "Runtime reported " + reason,
	}
	if err := r.Status().Patch(context.Background(), catalog, client.MergeFrom(before)); err != nil {
		t.Fatal(err)
	}
}

func TestCatalogActivationRequiredForRecovery(t *testing.T) {
	ctx := context.Background()
	r, catalog := readyDataImageCatalog(t)
	key := client.ObjectKeyFromObject(catalog)
	reconcile := func() {
		t.Helper()
		if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: key}); err != nil {
			t.Fatal(err)
		}
		if err := r.Get(ctx, key, catalog); err != nil {
			t.Fatal(err)
		}
	}
	firstAttempt := catalog.Status.ImageUpdate.CurrentAttempt.AttemptID
	lastSuccess := catalog.Status.ImageUpdate.LastSuccessfulActivation.DeepCopy()
	lateSuccess := catalog.Status.ImageUpdate.ActivationOutcome.DeepCopy()
	stream := &imagev1.ImageStream{}
	streamKey := client.ObjectKey{Name: CatalogDataImageStreamName, Namespace: catalog.Namespace}
	if err := r.Get(ctx, streamKey, stream); err != nil {
		t.Fatal(err)
	}
	stream.Status.Tags[0].Conditions = []imagev1.TagEventCondition{{Type: imagev1.ImportSuccess, Status: corev1.ConditionFalse, Reason: "Unauthorized"}}
	if err := r.Status().Update(ctx, stream); err != nil {
		t.Fatal(err)
	}
	reconcile()
	assertDataImageCondition(t, catalog, conditionCatalogReady, metav1.ConditionFalse, "DataImagesUnavailable")
	if catalog.Status.ImageUpdate.CurrentAttempt.ResolvedImages != nil {
		t.Fatal("failed import produced a candidate from history")
	}
	stream.Status.Tags[0].Conditions = nil
	stream.Status.Tags[0].Items[0].DockerImageReference = dataTestRepository + "@" + dataTestDigest2
	if err := r.Status().Update(ctx, stream); err != nil {
		t.Fatal(err)
	}
	reconcile()
	secondAttempt := catalog.Status.ImageUpdate.CurrentAttempt.AttemptID
	if secondAttempt == firstAttempt {
		t.Fatal("scheduled import did not create a new attempt")
	}
	assertDataImageCondition(t, catalog, conditionCatalogReady, metav1.ConditionFalse, "ActivationPending")
	assertDataImageCondition(t, catalog, ConditionTypeDegraded, metav1.ConditionTrue, "ImportFailed")
	if !reflect.DeepEqual(lastSuccess, catalog.Status.ImageUpdate.LastSuccessfulActivation) {
		t.Fatal("resolution overwrote last successful activation")
	}
	deployment := &appsv1.Deployment{}
	if err := r.Get(ctx, client.ObjectKey{Name: catalogResourceName, Namespace: catalog.Namespace}, deployment); err != nil {
		t.Fatal(err)
	}
	if deployment.Spec.Template.Annotations[catalogImageAttemptAnnotation] != secondAttempt {
		t.Fatal("workload was not correlated with persisted attempt")
	}
	if deployment.Spec.Replicas == nil || *deployment.Spec.Replicas != 1 {
		t.Fatal("pending recovery must permit candidate activation")
	}
	wantEnv := map[string]string{"CATALOG_IMAGE_ATTEMPT_ID": secondAttempt, "CATALOG_CR_UID": string(catalog.UID), "CATALOG_DATA_IMAGE_REFERENCE": dataTestRepository + "@" + dataTestDigest2, "CATALOG_BENCHMARK_IMAGE_REFERENCE": dataTestRepository + "@" + dataTestDigest2}
	for _, env := range deployment.Spec.Template.Spec.Containers[0].Env {
		if expected, exists := wantEnv[env.Name]; exists {
			if env.Value != expected {
				t.Fatalf("unexpected runtime correlation: %s=%s", env.Name, env.Value)
			}
			delete(wantEnv, env.Name)
		}
	}
	if len(wantEnv) != 0 {
		t.Fatalf("missing runtime correlation: %v", wantEnv)
	}
	// A restart and late success for the previous attempt cannot authorize B.
	r = &CatalogReconciler{Client: r.Client, Scheme: r.Scheme, Template: r.Template, Capabilities: r.Capabilities}
	before := catalog.DeepCopy()
	catalog.Status.ImageUpdate.ActivationOutcome = lateSuccess
	if err := r.Status().Patch(ctx, catalog, client.MergeFrom(before)); err != nil {
		t.Fatal(err)
	}
	reconcile()
	assertDataImageCondition(t, catalog, conditionCatalogReady, metav1.ConditionFalse, "ActivationPending")
	if catalog.Status.ImageUpdate.CurrentAttempt.AttemptID != secondAttempt {
		t.Fatal("resync changed attempt identity")
	}
	for _, invalid := range []string{"wrong benchmark", "wrong UID", "premature success", "missing pair"} {
		t.Run(invalid, func(t *testing.T) {
			reportDataImageActivation(t, r, catalog, "Succeeded", "Activation", "Activated")
			before := catalog.DeepCopy()
			outcome := catalog.Status.ImageUpdate.ActivationOutcome
			switch invalid {
			case "wrong benchmark":
				outcome.Images.Benchmark = dataTestRepository + "@" + dataTestDigest1
			case "wrong UID":
				outcome.CatalogUID = "old-catalog"
			case "premature success":
				outcome.Stage = "Loading"
			case "missing pair":
				outcome.Images = nil
			}
			if err := r.Status().Patch(ctx, catalog, client.MergeFrom(before)); err != nil {
				t.Fatal(err)
			}
			reconcile()
			assertDataImageCondition(t, catalog, conditionCatalogReady, metav1.ConditionFalse, "ActivationPending")
			if !reflect.DeepEqual(lastSuccess, catalog.Status.ImageUpdate.LastSuccessfulActivation) {
				t.Fatal("invalid outcome overwrote last success")
			}
		})
	}
	reportDataImageActivation(t, r, catalog, "Failed", "Loading", "BenchmarkContentInvalid")
	reconcile()
	assertDataImageCondition(t, catalog, ConditionTypeDegraded, metav1.ConditionTrue, "BenchmarkContentInvalid")
	assertDataImageCondition(t, catalog, conditionDataActivationReady, metav1.ConditionFalse, "BenchmarkContentInvalid")
	if err := r.Get(ctx, client.ObjectKey{Name: catalogResourceName, Namespace: catalog.Namespace}, deployment); err != nil {
		t.Fatal(err)
	}
	if *deployment.Spec.Replicas != 0 {
		t.Fatal("activation failure did not request containment")
	}
	// Explicit rollback is a new attempt even if the digest was activated before.
	oldDigest := dataTestDigest1
	catalog.Spec.CatalogDataImageStream, catalog.Spec.BenchmarkDataImageStream = &oldDigest, &oldDigest
	catalog.Generation++
	if err := r.Update(ctx, catalog); err != nil {
		t.Fatal(err)
	}
	reconcile()
	rollbackAttempt := catalog.Status.ImageUpdate.CurrentAttempt.AttemptID
	assertDataImageCondition(t, catalog, conditionCatalogReady, metav1.ConditionFalse, "ActivationPending")
	if rollbackAttempt == firstAttempt || rollbackAttempt == secondAttempt {
		t.Fatal("rollback reused a superseded attempt")
	}
	reportDataImageActivation(t, r, catalog, "Succeeded", "Activation", "Activated")
	reconcile()
	assertDataImageCondition(t, catalog, conditionCatalogReady, metav1.ConditionTrue, ReasonDeploymentAvailable)
	assertDataImageCondition(t, catalog, ConditionTypeDegraded, metav1.ConditionFalse, "DataImagesHealthy")
	if catalog.Status.ImageUpdate.LastSuccessfulActivation.AttemptID != rollbackAttempt {
		t.Fatal("successful rollback was not recorded")
	}
	recordedSuccess := catalog.Status.ImageUpdate.LastSuccessfulActivation.DeepCopy()
	reconcile()
	if !reflect.DeepEqual(recordedSuccess, catalog.Status.ImageUpdate.LastSuccessfulActivation) {
		t.Fatal("resync changed activation metadata")
	}
}

func TestCatalogUnchangedImportsPreserveActivation(t *testing.T) {
	ctx := context.Background()
	r, catalog := readyDataImageCatalog(t)
	attemptID := catalog.Status.ImageUpdate.CurrentAttempt.AttemptID
	stream := &imagev1.ImageStream{}
	key := client.ObjectKey{Name: CatalogDataImageStreamName, Namespace: catalog.Namespace}
	if err := r.Get(ctx, key, stream); err != nil {
		t.Fatal(err)
	}
	stream.Labels["unrelated"] = "updated"
	stream.Status.Tags[0].Items[0].Created = metav1.Now()
	stream.Status.Tags = append(stream.Status.Tags, imagev1.NamedTagEventList{Tag: "unrelated"})
	if err := r.Update(ctx, stream); err != nil {
		t.Fatal(err)
	}
	if err := r.Status().Update(ctx, stream); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(catalog)}); err != nil {
		t.Fatal(err)
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(catalog), catalog); err != nil {
		t.Fatal(err)
	}
	if catalog.Status.ImageUpdate.CurrentAttempt.AttemptID != attemptID {
		t.Fatal("unchanged digest/unrelated stream edits invalidated activation")
	}
	assertDataImageCondition(t, catalog, conditionCatalogReady, metav1.ConditionTrue, ReasonDeploymentAvailable)
	// A non-image Catalog edit must not discard activation of this image pair.
	catalog.Generation++
	if err := r.Update(ctx, catalog); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(catalog)}); err != nil {
		t.Fatal(err)
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(catalog), catalog); err != nil {
		t.Fatal(err)
	}
	if catalog.Status.ImageUpdate.CurrentAttempt.AttemptID != attemptID {
		t.Fatal("non-image generation change invalidated activation")
	}
	assertDataImageCondition(t, catalog, conditionCatalogReady, metav1.ConditionTrue, ReasonDeploymentAvailable)
	stream.Status.Tags[0].Conditions = []imagev1.TagEventCondition{{Type: imagev1.ImportSuccess, Status: corev1.ConditionFalse}}
	if err := r.Status().Update(ctx, stream); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(catalog)}); err != nil {
		t.Fatal(err)
	}
	stream.Status.Tags[0].Conditions = nil
	if err := r.Status().Update(ctx, stream); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(catalog)}); err != nil {
		t.Fatal(err)
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(catalog), catalog); err != nil {
		t.Fatal(err)
	}
	if catalog.Status.ImageUpdate.CurrentAttempt.AttemptID == attemptID {
		t.Fatal("failure followed by same-digest success reused old activation")
	}
	assertDataImageCondition(t, catalog, conditionCatalogReady, metav1.ConditionFalse, "ActivationPending")
}

func TestCatalogActivationFailureStages(t *testing.T) {
	for _, stage := range []string{"Pull", "Initialization", "Loading", "Activation"} {
		t.Run(stage, func(t *testing.T) {
			r, catalog := readyDataImageCatalog(t)
			last := catalog.Status.ImageUpdate.LastSuccessfulActivation.DeepCopy()
			reportDataImageActivation(t, r, catalog, "Failed", stage, stage+"Failed")
			for range 2 {
				if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(catalog)}); err != nil {
					t.Fatal(err)
				}
				if err := r.Get(context.Background(), client.ObjectKeyFromObject(catalog), catalog); err != nil {
					t.Fatal(err)
				}
				assertDataImageCondition(t, catalog, conditionCatalogReady, metav1.ConditionFalse, "DataImagesUnavailable")
				assertDataImageCondition(t, catalog, conditionDataActivationReady, metav1.ConditionFalse, stage+"Failed")
				if !reflect.DeepEqual(last, catalog.Status.ImageUpdate.LastSuccessfulActivation) {
					t.Fatal("activation failure overwrote recovery material")
				}
			}
		})
	}
}

func TestCatalogDistinctImagePairActivation(t *testing.T) {
	r, catalog := readyDataImageCatalog(t)
	catalogPin, benchmarkPin := dataTestDigest1, dataTestDigest2
	catalog.Spec.CatalogDataImageStream, catalog.Spec.BenchmarkDataImageStream = &catalogPin, &benchmarkPin
	catalog.Generation++
	ctx := context.Background()
	if err := r.Update(ctx, catalog); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(catalog)}); err != nil {
		t.Fatal(err)
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(catalog), catalog); err != nil {
		t.Fatal(err)
	}
	assertDataImageCondition(t, catalog, conditionCatalogReady, metav1.ConditionFalse, "ActivationPending")
	refs := catalog.Status.ImageUpdate.CurrentAttempt.ResolvedImages.References()
	if refs.Catalog == refs.Benchmark {
		t.Fatal("independent image selections were collapsed")
	}
	reportDataImageActivation(t, r, catalog, "Succeeded", "Activation", "Activated")
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(catalog)}); err != nil {
		t.Fatal(err)
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(catalog), catalog); err != nil {
		t.Fatal(err)
	}
	assertDataImageCondition(t, catalog, conditionCatalogReady, metav1.ConditionTrue, ReasonDeploymentAvailable)
	// Last-success metadata cannot authorize serving when current evidence is gone.
	before := catalog.DeepCopy()
	catalog.Status.ImageUpdate.ActivationOutcome = nil
	if err := r.Status().Patch(ctx, catalog, client.MergeFrom(before)); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(catalog)}); err != nil {
		t.Fatal(err)
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(catalog), catalog); err != nil {
		t.Fatal(err)
	}
	assertDataImageCondition(t, catalog, conditionCatalogReady, metav1.ConditionFalse, "ActivationPending")
	if catalog.Status.ImageUpdate.LastSuccessfulActivation.Images != refs {
		t.Fatal("lost distinct-pair recovery metadata")
	}
}

func TestCatalogCandidatePublicationAndConcurrentActivation(t *testing.T) {
	ctx := context.Background()
	t.Setenv(config.CatalogDataImage, dataTestRepository+"@"+dataTestDigest1)
	t.Setenv(config.BenchmarkDataImage, dataTestRepository+"@"+dataTestDigest2)
	catalog := &catalogv1alpha1.Catalog{ObjectMeta: metav1.ObjectMeta{Name: "catalog", Namespace: "publication-test", UID: "publication", Generation: 1}}
	r := newDataImageTestReconciler(t, catalog, false)
	base := r.Client.(client.WithWatch)
	published := false
	r.Client = interceptor.NewClient(base, interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, options ...client.CreateOption) error {
			if err := c.Create(ctx, obj, options...); err != nil {
				return err
			}
			deployment, ok := obj.(*appsv1.Deployment)
			if !ok || deployment.Name != catalogResourceName || published {
				return nil
			}
			current := &catalogv1alpha1.Catalog{}
			if err := c.Get(ctx, client.ObjectKeyFromObject(catalog), current); err != nil {
				return err
			}
			attempt := &current.Status.ImageUpdate.CurrentAttempt
			if attempt.ResolvedImages == nil || deployment.Spec.Template.Annotations[catalogImageAttemptAnnotation] != attempt.AttemptID {
				t.Fatal("candidate reached workload before matching persisted intent")
			}
			assertDataImageCondition(t, current, conditionCatalogReady, metav1.ConditionFalse, "ActivationPending")
			refs := attempt.ResolvedImages.References()
			for _, container := range deployment.Spec.Template.Spec.InitContainers {
				want := refs.Catalog
				if container.Name == "benchmark-data-init" {
					want = refs.Benchmark
				}
				if container.Image != want {
					t.Fatal("workload image pair differs from persisted candidate")
				}
			}
			before := current.DeepCopy()
			current.Status.ImageUpdate.ActivationOutcome = &catalogv1alpha1.CatalogImageOutcome{CatalogUID: current.UID, AttemptID: attempt.AttemptID, Images: &refs, State: "Pending", Stage: "Pull", Reason: "ImagePullPending", Message: "Runtime is pulling the current pair"}
			published = true
			return c.Status().Patch(ctx, current, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{}))
		},
	})
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(catalog)}); !apierrors.IsConflict(err) {
		t.Fatalf("expected optimistic conflict with concurrent producer, got %v", err)
	}
	if !published {
		t.Fatal("candidate publication was not exercised")
	}
	if err := base.Get(ctx, client.ObjectKeyFromObject(catalog), catalog); err != nil {
		t.Fatal(err)
	}
	producerOutcome := catalog.Status.ImageUpdate.ActivationOutcome.DeepCopy()
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(catalog)}); err != nil {
		t.Fatal(err)
	}
	if err := base.Get(ctx, client.ObjectKeyFromObject(catalog), catalog); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(producerOutcome, catalog.Status.ImageUpdate.ActivationOutcome) {
		t.Fatal("controller overwrote activation-owned status during retry")
	}
	assertDataImageCondition(t, catalog, conditionCatalogReady, metav1.ConditionFalse, "ImagePullPending")
	if catalog.Status.ImageUpdate.LastSuccessfulActivation != nil {
		t.Fatal("pending producer evidence created activation success")
	}
}

func TestCatalogUnknownWorkloadHealthDoesNotEstablishReadiness(t *testing.T) {
	r, catalog := readyDataImageCatalog(t)
	base := r.Client.(client.WithWatch)
	r.Client = interceptor.NewClient(base, interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, options ...client.GetOption) error {
			if _, ok := obj.(*appsv1.Deployment); ok {
				return apierrors.NewServiceUnavailable("workload health cannot be observed")
			}
			return c.Get(ctx, key, obj, options...)
		},
	})
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(catalog)}); !apierrors.IsServiceUnavailable(err) {
		t.Fatalf("expected workload observation failure, got %v", err)
	}
	if err := base.Get(context.Background(), client.ObjectKeyFromObject(catalog), catalog); err != nil {
		t.Fatal(err)
	}
	assertDataImageCondition(t, catalog, conditionCatalogReady, metav1.ConditionFalse, "WorkloadStatusUnknown")
	assertDataImageCondition(t, catalog, ConditionTypeDegraded, metav1.ConditionFalse, "DataImagesHealthy")
	if catalog.Status.ImageUpdate.LastSuccessfulActivation == nil {
		t.Fatal("unknown workload health discarded validated recovery metadata")
	}
}
