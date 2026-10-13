package controller

import (
	"context"
	"encoding/json"
	"fmt"

	catalogv1alpha1 "github.com/opendatahub-io/model-registry-operator/api/catalog/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const conditionDataImageActivationFailed = "DataImageActivationFailed"
const reasonDataImagePullRetrying = "DataImagePullRetrying"
const reasonCatalogRestarting = "CatalogRestarting"

// catalogActivationFailure is emitted by operator-owned validation components.
type catalogActivationFailure struct {
	Stage   string `json:"stage"`
	Reason  string `json:"reason"`
	Message string `json:"message"`
}

// Kubernetes status and termination messages may contain arbitrary container
// output. Persist and emit only descriptions derived from known failure codes.
func safeCatalogActivationFailure(failure *catalogActivationFailure) *catalogActivationFailure {
	if failure == nil {
		return nil
	}
	stage, reason, summary := "Activation", failure.Reason, "Catalog activation failed"
	switch reason {
	case "CatalogPodFailed":
		summary = "Catalog pod failed"
	case "CatalogContainerFailed":
		stage, summary = "Initialization", "Catalog container could not start"
	case "DataImagePullFailed":
		stage, summary = "Pull", "Selected image could not be pulled"
	case "DataImageInitializationFailed":
		stage, summary = "Initialization", "Required data image files could not be copied"
	case "CatalogContentInvalid":
		stage, summary = "Loading", "Shipped catalog content failed validation"
	case "BenchmarkContentInvalid":
		stage, summary = "Loading", "Shipped benchmark content failed validation"
	case "ActivationFailed":
		summary = "Catalog validation failed"
	case "ActivationTimedOut":
		summary = "Catalog validation or startup did not complete within ten minutes"
	case "CatalogStartupFailed":
		summary = "Catalog process did not start"
	default:
		reason = "CatalogStartupFailed"
	}
	return &catalogActivationFailure{
		Stage: stage, Reason: reason,
		Message: fmt.Sprintf("[%s] %s: %s. Correct spec.catalogDataImageStream or spec.benchmarkDataImageStream and retry the selected images", stage, reason, summary),
	}
}

func (r *CatalogReconciler) catalogActivationFailure(ctx context.Context, catalog *catalogv1alpha1.Catalog) (*catalogActivationFailure, error) {
	deployment := &appsv1.Deployment{}
	if err := r.Get(ctx, client.ObjectKey{Name: catalogResourceName, Namespace: catalog.Namespace}, deployment); err != nil {
		return nil, err
	}
	var pods corev1.PodList
	if err := r.List(ctx, &pods, client.InNamespace(catalog.Namespace), client.MatchingLabels(deployment.Spec.Selector.MatchLabels)); err != nil {
		return nil, err
	}
	var retrying *catalogActivationFailure
	for _, pod := range pods.Items {
		if pod.DeletionTimestamp != nil || !matchesCatalogImages(pod.Spec, deployment.Spec.Template.Spec) ||
			(catalog.Status.Activation != nil && pod.Annotations[activationAttemptAnnotation] != catalog.Status.Activation.AttemptID) {
			continue
		}
		if failure := catalogPodFailure(&pod); failure != nil {
			if retryableCatalogActivation(failure) {
				retrying = failure
				continue
			}
			return safeCatalogActivationFailure(failure), nil
		}
	}
	return retrying, nil
}

func retryableCatalogActivation(observation *catalogActivationFailure) bool {
	return observation != nil && (observation.Reason == reasonDataImagePullRetrying || observation.Reason == reasonCatalogRestarting)
}

// Compare both data references and the runtime reference so an old rollout
// cannot attribute a different candidate's failure to the current deployment.
// Successful activation additionally requires the validator and current workload.
func matchesCatalogImages(pod, desired corev1.PodSpec) bool {
	image := func(containers []corev1.Container, name string) string {
		for _, container := range containers {
			if container.Name == name {
				return container.Image
			}
		}
		return ""
	}
	for _, name := range []string{"catalog-data-init", "benchmark-data-init"} {
		wanted := image(desired.InitContainers, name)
		if wanted == "" || image(pod.InitContainers, name) != wanted {
			return false
		}
	}
	wanted := image(desired.Containers, "catalog")
	return wanted != "" && image(pod.Containers, "catalog") == wanted
}

func catalogPodFailure(pod *corev1.Pod) *catalogActivationFailure {
	if pod.Status.Phase == corev1.PodFailed {
		return &catalogActivationFailure{Stage: "Activation", Reason: "CatalogPodFailed"}
	}
	check := func(status corev1.ContainerStatus, init bool) *catalogActivationFailure {
		if status.State.Waiting != nil {
			switch status.State.Waiting.Reason {
			case "CreateContainerConfigError", "CreateContainerError", "RunContainerError":
				return &catalogActivationFailure{Stage: "Initialization", Reason: "CatalogContainerFailed"}
			case "ErrImagePull", "ImagePullBackOff":
				return &catalogActivationFailure{Stage: "Pull", Reason: reasonDataImagePullRetrying}
			case "InvalidImageName":
				return &catalogActivationFailure{Stage: "Pull", Reason: "DataImagePullFailed"}
			}
		}
		terminated := status.State.Terminated
		if terminated == nil && status.State.Waiting != nil && status.State.Waiting.Reason == "CrashLoopBackOff" {
			terminated = status.LastTerminationState.Terminated
		}
		if terminated == nil || terminated.ExitCode == 0 {
			return nil
		}
		// The catalog process can restart after a probe failure or OOM. Give a
		// current attempt time to recover rather than latching a prior exit.
		if !init {
			return &catalogActivationFailure{Stage: "Activation", Reason: reasonCatalogRestarting}
		}
		if init && status.Name != "catalog-content-validation" {
			return &catalogActivationFailure{Stage: "Initialization", Reason: "DataImageInitializationFailed"}
		}
		var report catalogActivationFailure
		if status.Name == "catalog-content-validation" && json.Unmarshal([]byte(terminated.Message), &report) == nil && (report.Stage == "Loading" || report.Stage == "Activation") &&
			(report.Reason == "CatalogContentInvalid" || report.Reason == "BenchmarkContentInvalid" || report.Reason == "ActivationFailed") {
			return &catalogActivationFailure{Stage: report.Stage, Reason: report.Reason}
		}
		return &catalogActivationFailure{Stage: "Activation", Reason: "CatalogStartupFailed"}
	}
	for _, status := range pod.Status.InitContainerStatuses {
		if status.Name == "catalog-data-init" || status.Name == "benchmark-data-init" || status.Name == "catalog-content-validation" {
			if failure := check(status, true); failure != nil {
				return failure
			}
		}
	}
	for _, status := range pod.Status.ContainerStatuses {
		if status.Name == "catalog" {
			if failure := check(status, false); failure != nil {
				return failure
			}
		}
	}
	return nil
}

func (r *CatalogReconciler) getCatalogsForPod(ctx context.Context, pod client.Object) []reconcile.Request {
	if !isCatalogPod(pod) {
		return nil
	}
	var catalogs catalogv1alpha1.CatalogList
	if err := r.List(ctx, &catalogs, client.InNamespace(pod.GetNamespace())); err != nil {
		return nil
	}
	requests := make([]reconcile.Request, 0, len(catalogs.Items))
	for _, catalog := range catalogs.Items {
		requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&catalog)})
	}
	return requests
}

func isCatalogPod(pod client.Object) bool {
	return pod.GetLabels()["app"] == catalogResourceName && pod.GetLabels()["component"] == catalogResourceName
}
