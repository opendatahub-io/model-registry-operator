package controller

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	aihubv1alpha1 "github.com/opendatahub-io/model-registry-operator/api/aihub/v1alpha1"
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
		{name: "init pull retry", stage: "Pull", reason: reasonDataImagePullRetrying, init: true, status: corev1.ContainerStatus{Name: "catalog-data-init", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff", Message: "denied"}}}},
		{name: "initial pull retry", stage: "Pull", reason: reasonDataImagePullRetrying, init: true, status: corev1.ContainerStatus{Name: "catalog-data-init", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ErrImagePull"}}}},
		{name: "invalid image name", stage: "Pull", reason: "DataImagePullFailed", init: true, status: corev1.ContainerStatus{Name: "catalog-data-init", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "InvalidImageName"}}}},
		{name: "benchmark copy", stage: "Initialization", reason: "DataImageInitializationFailed", init: true, status: corev1.ContainerStatus{Name: "benchmark-data-init", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1, Message: "missing benchmarks"}}}},
		{name: "init crash loop", stage: "Initialization", reason: "DataImageInitializationFailed", init: true, status: corev1.ContainerStatus{Name: "catalog-data-init", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}, LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1}}}},
		{name: "required benchmark", init: true, stage: "Loading", reason: "BenchmarkContentInvalid", status: corev1.ContainerStatus{Name: "catalog-content-validation", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1, Message: `{"stage":"Loading","reason":"BenchmarkContentInvalid","message":"performance.ndjson line 2: invalid JSON"}`}}}},
		{name: "required yaml", init: true, stage: "Loading", reason: "CatalogContentInvalid", status: corev1.ContainerStatus{Name: "catalog-content-validation", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}, LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1, Message: `{"stage":"Loading","reason":"CatalogContentInvalid","message":"missing catalog"}`}}}},
		{name: "catalog restarting", stage: "Activation", reason: reasonCatalogRestarting, status: corev1.ContainerStatus{Name: "catalog", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1, Reason: "Error", Message: "not JSON"}}}},
		{name: "catalog crash loop", stage: "Activation", reason: reasonCatalogRestarting, status: corev1.ContainerStatus{Name: "catalog", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}, LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 137}}}},
		{name: "creating", init: true, status: corev1.ContainerStatus{Name: "catalog-data-init", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ContainerCreating"}}}},
		{name: "completed init", init: true, status: corev1.ContainerStatus{Name: "catalog-data-init", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}}}},
		{name: "running replacement retains previous crash", status: corev1.ContainerStatus{Name: "catalog", Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}, LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1}}}},
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

func TestCatalogPodFailureDoesNotExposeContainerText(t *testing.T) {
	pod := &corev1.Pod{Status: corev1.PodStatus{InitContainerStatuses: []corev1.ContainerStatus{{
		Name: "catalog-data-init",
		State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{
			Reason: "InvalidImageName", Message: "https://user:secret@example.com/data?token=secret",
		}},
	}}}}
	failure := safeCatalogActivationFailure(catalogPodFailure(pod))
	if failure == nil || failure.Reason != "DataImagePullFailed" || strings.Contains(failure.Message, "secret") || len(failure.Message) > 512 {
		t.Fatalf("unsafe pull failure: %+v", failure)
	}
}

func TestCatalogPullRetryRecoveryAndDeadline(t *testing.T) {
	for _, tc := range []struct {
		name    string
		timeout bool
	}{{name: "recovers"}, {name: "times out", timeout: true}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			r, catalog := readyDataImageCatalog(t)
			attempt := catalog.Status.Activation.AttemptID
			pod := &corev1.Pod{}
			key := client.ObjectKey{Name: "validated-" + attempt, Namespace: catalog.Namespace}
			if err := r.Get(ctx, key, pod); err != nil {
				t.Fatal(err)
			}
			pod.Status.InitContainerStatuses = []corev1.ContainerStatus{{Name: "catalog-data-init", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff"}}}}
			pod.Status.ContainerStatuses = nil
			if err := r.Status().Update(ctx, pod); err != nil {
				t.Fatal(err)
			}
			reconcile := func() {
				t.Helper()
				if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(catalog)}); err != nil {
					t.Fatal(err)
				}
				if err := r.Get(ctx, client.ObjectKeyFromObject(catalog), catalog); err != nil {
					t.Fatal(err)
				}
			}
			reconcile()
			if a := catalog.Status.Activation; a.State != "Pending" || a.Stage != "Pull" || a.Reason != reasonDataImagePullRetrying || a.AttemptID != attempt || time.Since(a.StartedAt.Time) > time.Minute {
				t.Fatalf("pull retry was not kept pending: %+v", a)
			}
			deployment := &appsv1.Deployment{}
			if err := r.Get(ctx, client.ObjectKey{Name: catalogResourceName, Namespace: catalog.Namespace}, deployment); err != nil {
				t.Fatal(err)
			}
			if deployment.Spec.Replicas == nil || *deployment.Spec.Replicas != 1 {
				t.Fatal("transient pull failure scaled down the workload")
			}
			if tc.timeout {
				catalog.Status.Activation.StartedAt = metav1.NewTime(time.Now().Add(-11 * time.Minute))
				if err := r.Status().Update(ctx, catalog); err != nil {
					t.Fatal(err)
				}
				reconcile()
				if a := catalog.Status.Activation; a.State != "Failed" || a.Stage != "Pull" || a.Reason != "DataImagePullFailed" {
					t.Fatalf("persistent pull failure did not fail after the deadline: %+v", a)
				}
				if err := r.Get(ctx, client.ObjectKeyFromObject(deployment), deployment); err != nil {
					t.Fatal(err)
				}
				if deployment.Spec.Replicas == nil || *deployment.Spec.Replicas != 0 {
					t.Fatal("timed-out pull did not scale down the workload")
				}
				return
			}
			pod.Status = validatedPodStatus()
			if err := r.Status().Update(ctx, pod); err != nil {
				t.Fatal(err)
			}
			reconcile()
			if a := catalog.Status.Activation; a.State != "Succeeded" || a.Reason != "WorkloadValidated" || a.AttemptID != attempt {
				t.Fatalf("recovered pull did not restore activation: %+v", a)
			}
		})
	}
}

func TestCatalogRestartCanRecover(t *testing.T) {
	ctx := context.Background()
	r, catalog := readyDataImageCatalog(t)
	attempt := catalog.Status.Activation.AttemptID
	pod := &corev1.Pod{}
	key := client.ObjectKey{Name: "validated-" + attempt, Namespace: catalog.Namespace}
	if err := r.Get(ctx, key, pod); err != nil {
		t.Fatal(err)
	}
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "catalog", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}, LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 137}}}}
	if err := r.Status().Update(ctx, pod); err != nil {
		t.Fatal(err)
	}
	reconcile := func() {
		t.Helper()
		if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(catalog)}); err != nil {
			t.Fatal(err)
		}
		if err := r.Get(ctx, client.ObjectKeyFromObject(catalog), catalog); err != nil {
			t.Fatal(err)
		}
	}
	reconcile()
	if a := catalog.Status.Activation; a.State != "Pending" || a.Reason != reasonCatalogRestarting || a.AttemptID != attempt {
		t.Fatalf("catalog restart was not given time to recover: %+v", a)
	}
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "catalog", Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}, LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 137}}}}
	if err := r.Status().Update(ctx, pod); err != nil {
		t.Fatal(err)
	}
	reconcile()
	if a := catalog.Status.Activation; a.State != "Succeeded" || a.Reason != "WorkloadValidated" || a.AttemptID != attempt {
		t.Fatalf("ready catalog was failed for an earlier termination: %+v", a)
	}
}

func TestCatalogRecoveryWindowAcrossRestarts(t *testing.T) {
	for _, tc := range []struct {
		name       string
		healthyFor time.Duration
		wantFailed bool
	}{
		{name: "brief recovery keeps deadline", healthyFor: time.Minute, wantFailed: true},
		{name: "sustained recovery resets deadline", healthyFor: 11 * time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			r, catalog := readyDataImageCatalog(t)
			catalog.Status.Activation.StartedAt = metav1.NewTime(time.Now().Add(-time.Hour))
			if err := r.Status().Update(ctx, catalog); err != nil {
				t.Fatal(err)
			}
			pod := &corev1.Pod{}
			key := client.ObjectKey{Name: "validated-" + catalog.Status.Activation.AttemptID, Namespace: catalog.Namespace}
			if err := r.Get(ctx, key, pod); err != nil {
				t.Fatal(err)
			}
			reconcile := func() {
				t.Helper()
				if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(catalog)}); err != nil {
					t.Fatal(err)
				}
				if err := r.Get(ctx, client.ObjectKeyFromObject(catalog), catalog); err != nil {
					t.Fatal(err)
				}
			}
			pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "catalog", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}, LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 137}}}}
			if err := r.Status().Update(ctx, pod); err != nil {
				t.Fatal(err)
			}
			reconcile()
			if a := catalog.Status.Activation; a.State != "Pending" || time.Since(a.StartedAt.Time) > time.Minute {
				t.Fatalf("old successful attempt did not get an initial recovery window: %+v", a)
			}
			pod.Status = validatedPodStatus()
			if err := r.Status().Update(ctx, pod); err != nil {
				t.Fatal(err)
			}
			reconcile()
			if a := catalog.Status.Activation; a.State != "Succeeded" || a.RecoveredAt == nil {
				t.Fatalf("recovery was not recorded: %+v", a)
			}
			catalog.Status.Activation.StartedAt = metav1.NewTime(time.Now().Add(-11 * time.Minute))
			recoveredAt := metav1.NewTime(time.Now().Add(-tc.healthyFor))
			catalog.Status.Activation.RecoveredAt = &recoveredAt
			if err := r.Status().Update(ctx, catalog); err != nil {
				t.Fatal(err)
			}
			pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "catalog", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}, LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 137}}}}
			if err := r.Status().Update(ctx, pod); err != nil {
				t.Fatal(err)
			}
			reconcile()
			deployment := &appsv1.Deployment{}
			if err := r.Get(ctx, client.ObjectKey{Name: catalogResourceName, Namespace: catalog.Namespace}, deployment); err != nil {
				t.Fatal(err)
			}
			if tc.wantFailed {
				if catalog.Status.Activation.State != "Failed" || deployment.Spec.Replicas == nil || *deployment.Spec.Replicas != 0 {
					t.Fatalf("repeated restart escaped the recovery deadline: activation=%+v replicas=%v", catalog.Status.Activation, deployment.Spec.Replicas)
				}
			} else if catalog.Status.Activation.State != "Pending" || time.Since(catalog.Status.Activation.StartedAt.Time) > time.Minute || deployment.Spec.Replicas == nil || *deployment.Spec.Replicas != 1 {
				t.Fatalf("restart after sustained health did not get a new window: activation=%+v replicas=%v", catalog.Status.Activation, deployment.Spec.Replicas)
			}
		})
	}
}

func TestCatalogActivationFailureStatusEventsAndPersistence(t *testing.T) {
	ctx := context.Background()
	r, catalog := readyDataImageCatalog(t)
	recorder := events.NewFakeRecorder(10)
	r.Recorder = recorder
	deployment := &appsv1.Deployment{}
	if err := r.Get(ctx, client.ObjectKey{Name: catalogResourceName, Namespace: catalog.Namespace}, deployment); err != nil {
		t.Fatal(err)
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "candidate", Namespace: catalog.Namespace, Labels: deployment.Spec.Template.Labels, Annotations: deployment.Spec.Template.Annotations}, Spec: deployment.Spec.Template.Spec,
		Status: corev1.PodStatus{InitContainerStatuses: []corev1.ContainerStatus{{Name: "catalog-content-validation", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1, Message: `{"stage":"Loading","reason":"BenchmarkContentInvalid","message":"https://user:secret@example.com/data?token=secret"}`}}}}}}
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
	assertDataImageCondition(t, catalog, conditionCatalogReady, metav1.ConditionFalse, "BenchmarkContentInvalid")
	assertDataImageCondition(t, catalog, ConditionTypeDegraded, metav1.ConditionTrue, "BenchmarkContentInvalid")
	assertDataImageCondition(t, catalog, conditionDataImageActivationFailed, metav1.ConditionTrue, "BenchmarkContentInvalid")
	if catalog.Status.Activation == nil || strings.Contains(catalog.Status.Activation.Message, "secret") {
		t.Fatalf("activation status exposed validator input: %+v", catalog.Status.Activation)
	}
	for _, condition := range catalog.Status.Conditions {
		if strings.Contains(condition.Message, "secret") {
			t.Fatalf("Catalog condition %s exposed validator input", condition.Type)
		}
	}
	select {
	case event := <-recorder.Events:
		if !strings.Contains(event, "BenchmarkContentInvalid") || !strings.Contains(event, "Loading") || strings.Contains(event, "secret") {
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
	// Losing the failed pod's current-pair match cannot clear an already
	// recorded failure; a matching successful activation must clear it.
	pod.Spec.InitContainers[1].Image = "other.repository/benchmark@" + dataTestDigest3
	if err := r.Update(ctx, pod); err != nil {
		t.Fatal(err)
	}
	reconcile()
	assertDataImageCondition(t, catalog, conditionDataImageActivationFailed, metav1.ConditionTrue, "BenchmarkContentInvalid")
	assertDataImageCondition(t, catalog, conditionCatalogReady, metav1.ConditionFalse, "BenchmarkContentInvalid")
}

func TestCatalogActivationFailureSurvivesPodReplacement(t *testing.T) {
	for _, transition := range []string{"deleted", "terminating", "creating", "running", "ready", "spec edit"} {
		t.Run(transition, func(t *testing.T) {
			ctx := context.Background()
			r, catalog := readyDataImageCatalog(t)
			deployment := &appsv1.Deployment{}
			if err := r.Get(ctx, client.ObjectKey{Name: catalogResourceName, Namespace: catalog.Namespace}, deployment); err != nil {
				t.Fatal(err)
			}
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "failed-candidate", Namespace: catalog.Namespace, Labels: deployment.Spec.Template.Labels, Annotations: deployment.Spec.Template.Annotations}, Spec: deployment.Spec.Template.Spec,
				Status: corev1.PodStatus{InitContainerStatuses: []corev1.ContainerStatus{{Name: "catalog-content-validation", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1, Message: `{"stage":"Loading","reason":"BenchmarkContentInvalid","message":"bad benchmarks"}`}}}}}}
			if transition == "terminating" {
				pod.Finalizers = []string{"test/retain-pod"}
			}
			if err := r.Create(ctx, pod); err != nil {
				t.Fatal(err)
			}
			key := client.ObjectKeyFromObject(catalog)
			reconcile := func() {
				t.Helper()
				_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: key})
				if err != nil {
					t.Fatal(err)
				}
				if err := r.Get(ctx, key, catalog); err != nil {
					t.Fatal(err)
				}
			}
			reconcile()
			assertDataImageCondition(t, catalog, conditionCatalogReady, metav1.ConditionFalse, "BenchmarkContentInvalid")
			switch transition {
			case "deleted", "terminating":
				if err := r.Delete(ctx, pod); err != nil {
					t.Fatal(err)
				}
			case "creating", "running", "ready":
				status := corev1.ContainerStatus{Name: "catalog"}
				if transition == "creating" {
					status.State.Waiting = &corev1.ContainerStateWaiting{Reason: "ContainerCreating"}
				} else {
					status.State.Running = &corev1.ContainerStateRunning{}
					status.Ready = transition == "ready"
				}
				pod.Status.InitContainerStatuses = nil
				pod.Status.ContainerStatuses = []corev1.ContainerStatus{status}
				if err := r.Status().Update(ctx, pod); err != nil {
					t.Fatal(err)
				}
			case "spec edit":
				catalog.Generation++
				if err := r.Update(ctx, catalog); err != nil {
					t.Fatal(err)
				}
				if err := r.Delete(ctx, pod); err != nil {
					t.Fatal(err)
				}
			}
			// Reconstruct the controller using the persisted API state, as on restart.
			restarted := *r
			r = &restarted
			reconcile()
			assertDataImageCondition(t, catalog, conditionCatalogReady, metav1.ConditionFalse, "BenchmarkContentInvalid")
			assertDataImageCondition(t, catalog, ConditionTypeAvailable, metav1.ConditionFalse, "BenchmarkContentInvalid")
			assertDataImageCondition(t, catalog, ConditionTypeDegraded, metav1.ConditionTrue, "BenchmarkContentInvalid")
			manager := newAIHubConditionManager(&aihubv1alpha1.AIHub{})
			ready, failed := reconcileAIHubCatalogReadiness(catalog, manager)
			if ready || !failed {
				t.Fatalf("activation failure did not propagate to AIHub: ready=%t failed=%t", ready, failed)
			}
		})
	}
}

func TestCatalogDeploymentRequiresContentValidation(t *testing.T) {
	r, catalog := readyDataImageCatalog(t)
	deployment := &appsv1.Deployment{}
	if err := r.Get(context.Background(), client.ObjectKey{Name: catalogResourceName, Namespace: catalog.Namespace}, deployment); err != nil {
		t.Fatal(err)
	}
	for _, container := range deployment.Spec.Template.Spec.InitContainers {
		if container.Name == "benchmark-data-init" && strings.Contains(strings.Join(container.Command, " "), "|| true") {
			t.Fatal("benchmark copy suppresses errors")
		}
		if container.TerminationMessagePolicy != corev1.TerminationMessageReadFile {
			t.Fatalf("init container %s could expose logs in Pod status", container.Name)
		}
	}
	if deployment.Spec.Strategy.Type != appsv1.RecreateDeploymentStrategyType {
		t.Fatal("image updates must drain the old writer")
	}
	found := false
	for _, container := range deployment.Spec.Template.Spec.InitContainers {
		if container.Name == "catalog-content-validation" {
			found = true
			if container.Resources.Requests.Cpu().Sign() <= 0 || container.Resources.Requests.Memory().Sign() <= 0 ||
				container.Resources.Limits.Cpu().Sign() <= 0 || container.Resources.Limits.Memory().Sign() <= 0 {
				t.Fatal("preflight init container needs CPU and memory requests and limits")
			}
		}
	}
	if !found {
		t.Fatal("missing operator preflight init container")
	}
	for _, container := range deployment.Spec.Template.Spec.Containers {
		if container.Name != "catalog" {
			continue
		}
		args := strings.Join(container.Args, " ")
		if strings.Contains(args, "--required-") || strings.Contains(args, "--require-performance") {
			t.Fatal("operator flags leaked into stock runtime")
		}
		if !strings.Contains(args, "--listen=0.0.0.0:8080") {
			t.Fatal("unexpected runtime listener")
		}
		for _, env := range container.Env {
			if strings.HasPrefix(env.Name, "CATALOG_ACTIVATION") || env.Name == "CATALOG_UID" {
				t.Fatal("runtime must be operator agnostic")
			}
		}
	}
}

func TestBenchmarkCopyRequiresDirectoryButAllowsEmptyData(t *testing.T) {
	r, catalog := readyDataImageCatalog(t)
	deployment := &appsv1.Deployment{}
	if err := r.Get(context.Background(), client.ObjectKey{Name: catalogResourceName, Namespace: catalog.Namespace}, deployment); err != nil {
		t.Fatal(err)
	}
	command := ""
	for _, container := range deployment.Spec.Template.Spec.InitContainers {
		if container.Name == "benchmark-data-init" {
			if len(container.Command) != 3 || container.Command[0] != "/bin/sh" || container.Command[1] != "-c" {
				t.Fatalf("unexpected benchmark copy command: %v", container.Command)
			}
			command = container.Command[2]
		}
	}
	if command == "" {
		t.Fatal("missing benchmark copy command")
	}
	for _, tc := range []struct {
		name       string
		prepare    func(t *testing.T, source string)
		wantError  bool
		wantCopied bool
	}{
		{name: "no benchmark directory", wantError: true},
		{name: "empty benchmark directory", prepare: func(t *testing.T, source string) {
			if err := os.Mkdir(source, 0700); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "benchmark content", prepare: func(t *testing.T, source string) {
			if err := os.Mkdir(source, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(source, "metadata.json"), []byte(`{"id":"model"}`), 0600); err != nil {
				t.Fatal(err)
			}
		}, wantCopied: true},
		{name: "invalid benchmark path", prepare: func(t *testing.T, source string) {
			if err := os.WriteFile(source, []byte("not a directory"), 0600); err != nil {
				t.Fatal(err)
			}
		}, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			source := filepath.Join(root, "benchmarks")
			destination := filepath.Join(root, "shared")
			if err := os.Mkdir(destination, 0700); err != nil {
				t.Fatal(err)
			}
			if tc.prepare != nil {
				tc.prepare(t, source)
			}
			script := strings.ReplaceAll(command, "/app/benchmarks", `"${BENCHMARK_SOURCE}"`)
			script = strings.ReplaceAll(script, "/shared-benchmark-data", `"${BENCHMARK_DESTINATION}"`)
			cmd := exec.Command("/bin/sh", "-c", script)
			cmd.Env = append(os.Environ(), "BENCHMARK_SOURCE="+source, "BENCHMARK_DESTINATION="+destination)
			output, err := cmd.CombinedOutput()
			if (err != nil) != tc.wantError {
				t.Fatalf("copy error = %v, output = %q, want error = %t", err, output, tc.wantError)
			}
			_, err = os.Stat(filepath.Join(destination, "metadata.json"))
			if (err == nil) != tc.wantCopied {
				t.Fatalf("metadata copied = %t, want %t", err == nil, tc.wantCopied)
			}
		})
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
