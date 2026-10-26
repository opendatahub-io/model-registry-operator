package controller

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	catalogv1alpha1 "github.com/opendatahub-io/model-registry-operator/api/catalog/v1alpha1"
	"github.com/opendatahub-io/model-registry-operator/internal/controller/config"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const activationAttemptAnnotation = "aihub.opendatahub.io/activation-attempt"
const conditionDataActivationReady = "DataActivationReady"

// prepareActivation runs before the workload is applied. A changed selection,
// import outcome, runtime, explicit retry or pair invalidates workload readiness first.
func (r *CatalogReconciler) prepareActivation(ctx context.Context, catalog *catalogv1alpha1.Catalog, params *CatalogParams) error {
	resolved := apimeta.FindStatusCondition(catalog.Status.Conditions, conditionDataImageResolved)
	if resolved == nil {
		return fmt.Errorf("activation requires image resolution observations")
	}
	selection := func(value *string) string {
		if value == nil {
			return ""
		}
		return *value
	}
	request := []string{string(catalog.UID), selection(catalog.Spec.CatalogDataImageStream), selection(catalog.Spec.BenchmarkDataImageStream), params.CatalogDataImage, params.BenchmarkDataImage, config.GetStringConfigWithDefault(config.RestImage, config.DefaultRestImage), config.GetStringConfigWithDefault(config.ModelRegistryOperatorImage, config.DefaultOperatorImage), string(resolved.Status), resolved.Reason, params.ImageImportEvidence, fmt.Sprint(params.AllowLegacyRuntimeFile), catalog.Annotations["aihub.opendatahub.io/retry-activation"]}
	encoded, err := json.Marshal(request)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(encoded)
	fingerprint := hex.EncodeToString(sum[:])
	a := catalog.Status.Activation
	if a == nil || a.RequestHash != fingerprint {
		var previous *catalogv1alpha1.CatalogActivationSuccess
		if a != nil {
			previous = a.LastSuccessful
		}
		var nonce [12]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return err
		}
		images := catalogv1alpha1.CatalogDataImages{Catalog: params.CatalogDataImage, Benchmark: params.BenchmarkDataImage}
		a = &catalogv1alpha1.CatalogActivationStatus{CatalogUID: string(catalog.UID), AttemptID: hex.EncodeToString(nonce[:]), RequestHash: fingerprint, RequestedImages: images, Images: images, State: "Pending", Stage: "Activation", Reason: "ActivationPending", Message: "Waiting for image validation and the selected workload", StartedAt: metav1.Now(), LastSuccessful: previous}
		catalog.Status.Activation = a
	}
	// A timeout or validation failure is terminal for this request. In
	// particular, an unchanged first import must not reset a timed-out
	// attempt to Pending and briefly restart its workload.
	if resolved.Status != metav1.ConditionTrue && a.State != "Failed" {
		a.State, a.Stage, a.Reason, a.Message = "Pending", "Import", resolved.Reason,
			"Selected images are not ready; inspect the Catalog image resolution conditions and correct the selection or import source"
		if resolved.Status == metav1.ConditionFalse && resolved.Reason != "NoSuccessfulImport" {
			a.State = "Failed"
		}
		if resolved.Reason == "InvalidImageSelection" {
			a.Stage = "Selection"
		}

	}
	params.ActivationAttempt = a.AttemptID
	params.ActivationBlocked = a.State == "Failed"
	params.CatalogDataImage, params.BenchmarkDataImage = a.Images.Catalog, a.Images.Benchmark
	if resolved.Status == metav1.ConditionTrue {
		images := a.Images
		catalog.Status.ResolvedImages = &images
	}
	reduceCatalogImageReadiness(catalog)
	return nil
}

// observeActivation consumes Kubernetes evidence from the operator-owned init
// validator and existing workload readiness. The stock catalog process has no activation protocol.
func (r *CatalogReconciler) observeActivation(ctx context.Context, catalog *catalogv1alpha1.Catalog, failure *catalogActivationFailure) error {
	a := catalog.Status.Activation
	if a == nil {
		return nil
	}
	currentReady := false
	if a.State == "Failed" {
		// Normalize persisted failures from earlier operator versions as well.
		if a.Stage == "Import" || a.Stage == "Selection" {
			a.Message = "Selected images could not be resolved; inspect the Catalog image resolution conditions and correct the selection or import source"
		} else {
			safe := safeCatalogActivationFailure(&catalogActivationFailure{Reason: a.Reason})
			a.Stage, a.Reason, a.Message = safe.Stage, safe.Reason, safe.Message
		}
	} else if failure != nil && !retryableCatalogActivation(failure) {
		safe := safeCatalogActivationFailure(failure)
		a.State, a.Stage, a.Reason, a.Message = "Failed", safe.Stage, safe.Reason, safe.Message
	} else {
		ready, err := r.validatedCatalogWorkload(ctx, catalog)
		if err != nil {
			return err
		}
		currentReady = ready
		if ready {
			if a.State == "Pending" && a.LastSuccessful != nil && a.LastSuccessful.AttemptID == a.AttemptID {
				recoveredAt := metav1.Now()
				a.RecoveredAt = &recoveredAt
			}
			a.State, a.Stage, a.Reason, a.Message = "Succeeded", "Activation", "WorkloadValidated", "Selected images passed preflight and the catalog workload is ready"
			if a.LastSuccessful == nil || a.LastSuccessful.AttemptID != a.AttemptID {
				a.LastSuccessful = &catalogv1alpha1.CatalogActivationSuccess{CatalogUID: a.CatalogUID, AttemptID: a.AttemptID, Images: a.Images, ActivatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
			}
		} else if retryableCatalogActivation(failure) {
			if a.State == "Succeeded" {
				// Keep the first recovery deadline through brief ready periods.
				// A sustained healthy period starts a fresh recovery window.
				if a.RecoveredAt == nil || time.Since(a.RecoveredAt.Time) >= 10*time.Minute {
					a.StartedAt = metav1.Now()
				}
				a.RecoveredAt = nil
			}
			a.State, a.Stage, a.Reason = "Pending", failure.Stage, failure.Reason
			switch failure.Reason {
			case reasonDataImagePullRetrying:
				a.Message = "Kubernetes is retrying a selected image pull"
			case reasonCatalogRestarting:
				a.Message = "Catalog process restarted; waiting for a ready workload"
			}
			if time.Since(a.StartedAt.Time) > 10*time.Minute {
				reason := "CatalogStartupFailed"
				if failure.Reason == reasonDataImagePullRetrying {
					reason = "DataImagePullFailed"
				}
				safe := safeCatalogActivationFailure(&catalogActivationFailure{Reason: reason})
				a.State, a.Stage, a.Reason, a.Message = "Failed", safe.Stage, safe.Reason, safe.Message
			}
		} else if time.Since(a.StartedAt.Time) > 10*time.Minute && a.State == "Pending" {
			a.State, a.Stage, a.Reason, a.Message = "Failed", "Activation", "ActivationTimedOut", "Catalog validation/startup did not complete within ten minutes; correct spec.catalogDataImageStream or spec.benchmarkDataImageStream, or change aihub.opendatahub.io/retry-activation"
		}
	}
	ready := metav1.ConditionFalse
	if a.State == "Succeeded" && currentReady {
		ready = metav1.ConditionTrue
	}
	setCatalogDataImageCondition(catalog, conditionDataActivationReady, ready, a.Reason, a.Message)
	failed := metav1.ConditionFalse
	if a.State == "Failed" {
		failed = metav1.ConditionTrue
	}
	if a.State != "Pending" || apimeta.FindStatusCondition(catalog.Status.Conditions, conditionDataImageActivationFailed) == nil {
		setCatalogDataImageCondition(catalog, conditionDataImageActivationFailed, failed, a.Reason, a.Message)
	}
	return nil
}

// Require the current attempt, both image references, successful preflight and
// ready containers. Old endpoints and a Running pod alone are insufficient.
func (r *CatalogReconciler) validatedCatalogWorkload(ctx context.Context, catalog *catalogv1alpha1.Catalog) (bool, error) {
	deployment := &appsv1.Deployment{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: catalog.Namespace, Name: catalogResourceName}, deployment); err != nil {
		return false, client.IgnoreNotFound(err)
	}
	a := catalog.Status.Activation
	if deployment.Spec.Template.Annotations[activationAttemptAnnotation] != a.AttemptID {
		return false, nil
	}
	condition, err := r.checkDeploymentAvailability(ctx, client.ObjectKeyFromObject(deployment), catalogResourceName, catalogResourceName, &a.Images)
	if err != nil || condition.Status != metav1.ConditionTrue {
		return false, err
	}
	var pods corev1.PodList
	if err := r.List(ctx, &pods, client.InNamespace(catalog.Namespace), client.MatchingLabels(deployment.Spec.Selector.MatchLabels)); err != nil {
		return false, err
	}
	count := int32(0)
	for _, pod := range pods.Items {
		if pod.DeletionTimestamp != nil || pod.Annotations[activationAttemptAnnotation] != a.AttemptID || !matchesCatalogImages(pod.Spec, deployment.Spec.Template.Spec) {
			continue
		}
		validated := false
		for _, status := range pod.Status.InitContainerStatuses {
			if status.Name == "catalog-content-validation" && status.State.Terminated != nil && status.State.Terminated.ExitCode == 0 {
				validated = true
			}
		}
		ready := map[string]bool{}
		for _, status := range pod.Status.ContainerStatuses {
			ready[status.Name] = status.Ready
		}
		if validated && ready["catalog"] {
			count++
		}
	}
	desired := int32(1)
	if deployment.Spec.Replicas != nil {
		desired = *deployment.Spec.Replicas
	}
	return desired > 0 && count == desired, nil
}
