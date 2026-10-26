package controller

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	catalogv1alpha1 "github.com/opendatahub-io/model-registry-operator/api/catalog/v1alpha1"
	"github.com/opendatahub-io/model-registry-operator/internal/controller/config"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
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
	copyCommand := deployment.Spec.Template.Spec.InitContainers[0].Command[2]
	if !strings.Contains(copyCommand, "serving_runtimes: []") {
		t.Fatal("release image must tolerate the parent pin without a serving-runtime file")
	}
	data := filepath.Join(t.TempDir(), "data")
	shared := filepath.Join(t.TempDir(), "shared")
	if err := os.Mkdir(data, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(shared, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, "models.yaml"), []byte("models: []\n"), 0600); err != nil {
		t.Fatal(err)
	}
	copyCommand = strings.ReplaceAll(copyCommand, "/app/data/", data+"/")
	copyCommand = strings.ReplaceAll(copyCommand, "/shared-data/", shared+"/")
	if output, err := exec.Command("/bin/sh", "-c", copyCommand).CombinedOutput(); err != nil {
		t.Fatalf("release data copy failed: %v: command=%q output=%s", err, copyCommand, output)
	}
	content, err := os.ReadFile(filepath.Join(shared, "redhat-serving-runtimes-catalog.yaml"))
	if err != nil || string(content) != "serving_runtimes: []\n" {
		t.Fatalf("missing legacy serving-runtime fallback: %q, %v", content, err)
	}
	published := []byte("serving_runtimes: [{name: published}]\n")
	if err := os.WriteFile(filepath.Join(data, "redhat-serving-runtimes-catalog.yaml"), published, 0600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("/bin/sh", "-c", copyCommand).CombinedOutput(); err != nil {
		t.Fatalf("published data copy failed: %v: %s", err, output)
	}
	content, err = os.ReadFile(filepath.Join(shared, "redhat-serving-runtimes-catalog.yaml"))
	if err != nil || string(content) != string(published) {
		t.Fatalf("published serving runtimes were replaced: %q, %v", content, err)
	}
}

func TestTimedOutPendingImportStaysContained(t *testing.T) {
	ctx := context.Background()
	stable := "stable"
	catalog := &catalogv1alpha1.Catalog{ObjectMeta: metav1.ObjectMeta{Name: "catalog", Namespace: "data-test", UID: "catalog", Generation: 1},
		Spec: catalogv1alpha1.CatalogSpec{CatalogDataImageStream: &stable, BenchmarkDataImageStream: &stable}}
	r := newDataImageTestReconciler(t, catalog, true)
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(catalog)}
	if _, err := r.Reconcile(ctx, request); err != nil {
		t.Fatal(err)
	}
	if err := r.Get(ctx, request.NamespacedName, catalog); err != nil {
		t.Fatal(err)
	}
	attempt := catalog.Status.Activation.AttemptID
	catalog.Status.Activation.StartedAt = metav1.NewTime(time.Now().Add(-11 * time.Minute))
	if err := r.Status().Update(ctx, catalog); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, request); err != nil {
		t.Fatal(err)
	}
	if err := r.Get(ctx, request.NamespacedName, catalog); err != nil {
		t.Fatal(err)
	}
	if catalog.Status.Activation.State != "Failed" || catalog.Status.Activation.Reason != "ActivationTimedOut" {
		t.Fatalf("pending import was not timed out: %+v", catalog.Status.Activation)
	}
	resumed := false
	r.Client = interceptor.NewClient(r.Client.(client.WithWatch), interceptor.Funcs{
		Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			if deployment, ok := obj.(*appsv1.Deployment); ok && deployment.Name == catalogResourceName &&
				deployment.Spec.Replicas != nil && *deployment.Spec.Replicas > 0 {
				resumed = true
			}
			return c.Update(ctx, obj, opts...)
		},
	})
	r.resourceManager = nil
	if _, err := r.Reconcile(ctx, request); err != nil {
		t.Fatal(err)
	}
	if err := r.Get(ctx, request.NamespacedName, catalog); err != nil {
		t.Fatal(err)
	}
	deployment := &appsv1.Deployment{}
	if err := r.Get(ctx, client.ObjectKey{Name: catalogResourceName, Namespace: catalog.Namespace}, deployment); err != nil {
		t.Fatal(err)
	}
	if resumed || deployment.Spec.Replicas == nil || *deployment.Spec.Replicas != 0 || catalog.Status.Activation.AttemptID != attempt || catalog.Status.Activation.Reason != "ActivationTimedOut" {
		t.Fatalf("unchanged timed-out attempt resumed: activation=%+v replicas=%v update=%t", catalog.Status.Activation, deployment.Spec.Replicas, resumed)
	}
	if !strings.Contains(deployment.Spec.Template.Spec.InitContainers[0].Command[2], "serving_runtimes: []") {
		t.Fatal("pending import must retain release image compatibility")
	}
}

func TestSupersededFailureDoesNotContainNewAttempt(t *testing.T) {
	ctx := context.Background()
	r, catalog := readyDataImageCatalog(t)
	base := r.Client.(client.WithWatch)
	before := catalog.DeepCopy()
	catalog.Status.Activation.State = "Failed"
	catalog.Status.Activation.Stage = "Loading"
	catalog.Status.Activation.Reason = "BenchmarkContentInvalid"
	installed := false
	r.Client = interceptor.NewClient(base, interceptor.Funcs{
		SubResourcePatch: func(ctx context.Context, c client.Client, name string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
			if name != "status" || installed {
				return c.SubResource(name).Patch(ctx, obj, patch, opts...)
			}
			installed = true
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
			current.Status.Activation = current.Status.Activation.DeepCopy()
			current.Status.Activation.AttemptID = "newer-attempt"
			current.Status.Activation.State = "Pending"
			if err := c.Status().Update(ctx, current); err != nil {
				return err
			}
			deployment := &appsv1.Deployment{}
			if err := c.Get(ctx, client.ObjectKey{Name: catalogResourceName, Namespace: catalog.Namespace}, deployment); err != nil {
				return err
			}
			deployment.Spec.Template.Annotations[activationAttemptAnnotation] = "newer-attempt"
			for i := range deployment.Spec.Template.Spec.InitContainers {
				if deployment.Spec.Template.Spec.InitContainers[i].Name == "catalog-data-init" || deployment.Spec.Template.Spec.InitContainers[i].Name == "benchmark-data-init" {
					deployment.Spec.Template.Spec.InitContainers[i].Image = dataTestRepository + "@" + dataTestDigest2
				}
			}
			if err := c.Update(ctx, deployment); err != nil {
				return err
			}
			return apierrors.NewConflict(catalogv1alpha1.GroupVersion.WithResource("catalogs").GroupResource(), catalog.Name, errors.New("newer attempt"))
		},
	})
	if _, err := r.updateStatus(ctx, catalog, before); !apierrors.IsConflict(err) || !installed {
		t.Fatalf("expected conflicting status patch after newer attempt, got %v", err)
	}
	if err := r.containCatalogImageFailure(ctx, catalog); err != nil {
		t.Fatal(err)
	}
	deployment := &appsv1.Deployment{}
	if err := base.Get(ctx, client.ObjectKey{Name: catalogResourceName, Namespace: catalog.Namespace}, deployment); err != nil {
		t.Fatal(err)
	}
	if deployment.Spec.Replicas == nil || *deployment.Spec.Replicas != 1 || deployment.Spec.Template.Annotations[activationAttemptAnnotation] != "newer-attempt" {
		t.Fatalf("superseded failure contained newer attempt: replicas=%v annotations=%v", deployment.Spec.Replicas, deployment.Spec.Template.Annotations)
	}
}

func TestContainmentReadsCurrentAttemptPastStaleCache(t *testing.T) {
	ctx := context.Background()
	r, catalog := readyDataImageCatalog(t)
	base := r.Client.(client.WithWatch)
	stale := catalog.DeepCopy()
	current := catalog.DeepCopy()
	current.Status.Activation.AttemptID = "newer-attempt"
	current.Status.Activation.State = "Pending"
	if err := base.Status().Update(ctx, current); err != nil {
		t.Fatal(err)
	}
	r.Client = interceptor.NewClient(base, interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if cached, ok := obj.(*catalogv1alpha1.Catalog); ok {
				stale.DeepCopyInto(cached)
				return nil
			}
			return c.Get(ctx, key, obj, opts...)
		},
	})
	if err := r.containCatalogImageFailure(ctx, stale); err != nil {
		t.Fatal(err)
	}
	deployment := &appsv1.Deployment{}
	if err := base.Get(ctx, client.ObjectKey{Name: catalogResourceName, Namespace: catalog.Namespace}, deployment); err != nil {
		t.Fatal(err)
	}
	if deployment.Spec.Replicas == nil || *deployment.Spec.Replicas != 1 {
		t.Fatal("stale cached attempt scaled down the deployment")
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
