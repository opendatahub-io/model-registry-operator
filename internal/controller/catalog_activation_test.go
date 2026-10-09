package controller

import (
	"context"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestCatalogPodFailureStages(t *testing.T) {
	for _, tc := range []struct {
		name, stage, reason string
		init                bool
		status              corev1.ContainerStatus
	}{
		{name: "init pull", stage: "Pull", reason: "DataImagePullFailed", init: true, status: corev1.ContainerStatus{Name: "catalog-data-init", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff", Message: "denied"}}}},
		{name: "benchmark copy", stage: "Initialization", reason: "DataImageInitializationFailed", init: true, status: corev1.ContainerStatus{Name: "benchmark-data-init", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1, Message: "missing benchmarks"}}}},
		{name: "init crash loop", stage: "Initialization", reason: "DataImageInitializationFailed", init: true, status: corev1.ContainerStatus{Name: "catalog-data-init", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}, LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1}}}},
		{name: "required benchmark", stage: "Loading", reason: "BenchmarkContentInvalid", status: corev1.ContainerStatus{Name: "catalog", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1, Message: `{"stage":"Loading","reason":"BenchmarkContentInvalid","message":"performance.ndjson line 2: invalid JSON"}`}}}},
		{name: "required yaml", stage: "Loading", reason: "CatalogContentInvalid", status: corev1.ContainerStatus{Name: "catalog", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}, LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1, Message: `{"stage":"Loading","reason":"CatalogContentInvalid","message":"missing catalog"}`}}}},
		{name: "unstructured startup error", stage: "Activation", reason: "CatalogStartupFailed", status: corev1.ContainerStatus{Name: "catalog", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1, Reason: "Error", Message: "not JSON"}}}},
		{name: "creating", init: true, status: corev1.ContainerStatus{Name: "catalog-data-init", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ContainerCreating"}}}},
		{name: "completed init", init: true, status: corev1.ContainerStatus{Name: "catalog-data-init", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}}}},
		{name: "running recovery ignores previous crash", status: corev1.ContainerStatus{Name: "catalog", Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}, LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pod := &corev1.Pod{}
			if tc.init {
				pod.Status.InitContainerStatuses = []corev1.ContainerStatus{tc.status}
			} else {
				pod.Status.ContainerStatuses = []corev1.ContainerStatus{tc.status}
			}
			failure := catalogPodFailure(pod)
			if tc.stage == "" {
				if failure != nil {
					t.Fatalf("progress/success misclassified as failure: %+v", failure)
				}
				return
			}
			if failure == nil || failure.Stage != tc.stage || failure.Reason != tc.reason {
				t.Fatalf("unexpected failure: %+v", failure)
			}
		})
	}
}

func TestCatalogActivationFailureStatusEventsAndRecovery(t *testing.T) {
	ctx := context.Background()
	r, catalog := readyDataImageCatalog(t)
	recorder := events.NewFakeRecorder(10)
	r.Recorder = recorder
	deployment := &appsv1.Deployment{}
	if err := r.Get(ctx, client.ObjectKey{Name: catalogResourceName, Namespace: catalog.Namespace}, deployment); err != nil {
		t.Fatal(err)
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "candidate", Namespace: catalog.Namespace, Labels: deployment.Spec.Template.Labels}, Spec: deployment.Spec.Template.Spec,
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{Name: "catalog", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1, Message: `{"stage":"Loading","reason":"BenchmarkContentInvalid","message":"broken benchmark content"}`}}}}}}
	if err := r.Create(ctx, pod); err != nil {
		t.Fatal(err)
	}
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
	reconcile()
	assertDataImageCondition(t, catalog, ConditionTypeAvailable, metav1.ConditionFalse, "BenchmarkContentInvalid")
	assertDataImageCondition(t, catalog, ConditionTypeDegraded, metav1.ConditionTrue, "BenchmarkContentInvalid")
	assertDataImageCondition(t, catalog, conditionDataImageActivationFailed, metav1.ConditionTrue, "BenchmarkContentInvalid")
	select {
	case event := <-recorder.Events:
		if !strings.Contains(event, "BenchmarkContentInvalid") || !strings.Contains(event, "Loading") {
			t.Fatalf("unexpected event: %s", event)
		}
	default:
		t.Fatal("missing warning event")
	}
	reconcile()
	select {
	case event := <-recorder.Events:
		t.Fatalf("unchanged failure repeated warning: %s", event)
	default:
	}
	// A stale failed pod from a different image pair must not poison diagnosis.
	pod.Spec.InitContainers[1].Image = "other.repository/benchmark@" + dataTestDigest3
	if err := r.Update(ctx, pod); err != nil {
		t.Fatal(err)
	}
	reconcile()
	assertDataImageCondition(t, catalog, conditionDataImageActivationFailed, metav1.ConditionFalse, "NoReportedActivationFailure")
	// This is absence of reported failure, NOT an activation-success assertion.
}

func TestCatalogDeploymentRequiresContentValidation(t *testing.T) {
	r, catalog := readyDataImageCatalog(t)
	deployment := &appsv1.Deployment{}
	if err := r.Get(context.Background(), client.ObjectKey{Name: catalogResourceName, Namespace: catalog.Namespace}, deployment); err != nil {
		t.Fatal(err)
	}
	for _, container := range deployment.Spec.Template.Spec.InitContainers {
		if container.Name == "benchmark-data-init" && strings.Contains(strings.Join(container.Command, " "), "|| true") {
			t.Fatal("required benchmark copy suppresses errors")
		}
	}
	var found bool
	for _, container := range deployment.Spec.Template.Spec.Containers {
		if container.Name != "catalog" {
			continue
		}
		args := strings.Join(container.Args, " ")
		found = strings.Contains(args, "--required-catalogs-path=/data/default-sources/sources.yaml") && strings.Contains(args, "--require-performance-metrics")
	}
	if !found {
		t.Fatal("runtime required-content validation is not enabled")
	}
}

func TestCatalogPodWatchMapping(t *testing.T) {
	r, catalog := readyDataImageCatalog(t)
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: catalog.Namespace, Labels: map[string]string{"app": catalogResourceName, "component": "model-catalog"}}}
	requests := r.getCatalogsForPod(context.Background(), pod)
	if len(requests) != 1 || requests[0].NamespacedName != client.ObjectKeyFromObject(catalog) {
		t.Fatalf("unexpected requests: %v", requests)
	}
	pod.Labels["component"] = "model-catalog-postgres"
	if got := r.getCatalogsForPod(context.Background(), pod); len(got) != 0 {
		t.Fatalf("unrelated pod triggered Catalog: %v", got)
	}
}
