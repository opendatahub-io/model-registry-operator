package controller

import (
	"context"
	"encoding/json"
	"fmt"

	catalogv1alpha1 "github.com/opendatahub-io/model-registry-operator/api/catalog/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const conditionDataImageActivationFailed = "DataImageActivationFailed"

// catalogActivationFailure mirrors the runtime's failure-only termination
// report. It is not an activation acknowledgement or serving authorization.
type catalogActivationFailure struct {
	Stage   string `json:"stage"`
	Reason  string `json:"reason"`
	Message string `json:"message"`
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
	for _, pod := range pods.Items {
		if pod.DeletionTimestamp != nil || !matchesCatalogImages(pod.Spec, deployment.Spec.Template.Spec) {
			continue
		}
		if failure := catalogPodFailure(&pod); failure != nil {
			failure.Message = fmt.Sprintf("[%s] pod %s: %s", failure.Stage, pod.Name, failure.Message)
			return failure, nil
		}
	}
	return nil, nil
}

// Compare both data references and the runtime reference so an old rollout
// cannot attribute a different candidate's failure to the current deployment.
// This is failure diagnosis only; attempt-correlated success needs the shared
// 97413 contract and cannot be inferred from these images or a Ready pod.
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
	check := func(status corev1.ContainerStatus, init bool) *catalogActivationFailure {
		if status.State.Waiting != nil {
			switch status.State.Waiting.Reason {
			case "ErrImagePull", "ImagePullBackOff", "InvalidImageName":
				return &catalogActivationFailure{Stage: "Pull", Reason: "DataImagePullFailed", Message: status.Name + ": " + status.State.Waiting.Reason + ": " + status.State.Waiting.Message}
			}
		}
		terminated := status.State.Terminated
		if terminated == nil && status.State.Waiting != nil && status.State.Waiting.Reason == "CrashLoopBackOff" {
			terminated = status.LastTerminationState.Terminated
		}
		if terminated == nil || terminated.ExitCode == 0 {
			return nil
		}
		if init {
			return &catalogActivationFailure{Stage: "Initialization", Reason: "DataImageInitializationFailed", Message: fmt.Sprintf("%s exited with code %d: %s", status.Name, terminated.ExitCode, terminated.Message)}
		}
		var report catalogActivationFailure
		if json.Unmarshal([]byte(terminated.Message), &report) == nil && report.Stage == "Loading" && report.Message != "" &&
			(report.Reason == "CatalogContentInvalid" || report.Reason == "BenchmarkContentInvalid") {
			return &report
		}
		return &catalogActivationFailure{Stage: "Activation", Reason: "CatalogStartupFailed", Message: fmt.Sprintf("%s exited with code %d: %s", status.Name, terminated.ExitCode, terminated.Reason)}
	}
	for _, status := range pod.Status.InitContainerStatuses {
		if status.Name == "catalog-data-init" || status.Name == "benchmark-data-init" {
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
	if pod.GetLabels()["app"] != catalogResourceName || pod.GetLabels()["component"] != "model-catalog" {
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

func activationUnavailableCondition(catalog *catalogv1alpha1.Catalog, failure *catalogActivationFailure) metav1.Condition {
	return metav1.Condition{Type: ConditionTypeAvailable, Status: metav1.ConditionFalse, Reason: failure.Reason,
		Message: failure.Message, ObservedGeneration: catalog.Generation}
}
