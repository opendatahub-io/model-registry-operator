package controller

import (
	"strings"
	"testing"
	"time"

	"github.com/opendatahub-io/model-registry-operator/internal/controller/config"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestCatalogMirroredActivation(t *testing.T) {
	for _, target := range []string{"none", "catalog", "catalog-data-init", "benchmark-data-init", "different digest", "old attempt"} {
		for _, invalid := range []bool{false, true} {
			rejected := target == "different digest" || target == "old attempt"
			if rejected && invalid {
				continue
			}
			name := target + "/valid"
			if invalid {
				name = target + "/invalid"
			}
			t.Run(name, func(t *testing.T) {
				t.Setenv(config.RestImage, "registry.redhat.io/rhoai/runtime@"+dataTestDigest2)
				ctx := t.Context()
				r, catalog := readyDataImageCatalog(t)
				var pods corev1.PodList
				if err := r.List(ctx, &pods, client.InNamespace(catalog.Namespace)); err != nil {
					t.Fatal(err)
				}
				if len(pods.Items) != 1 {
					t.Fatalf("pods=%d", len(pods.Items))
				}
				pod := &pods.Items[0]
				mirror := func(containers []corev1.Container) {
					for i := range containers {
						if containers[i].Name == target {
							containers[i].Image = strings.Replace(containers[i].Image,
								"registry.redhat.io/rhoai/", "quay.io/rhoai/", 1)
						}
					}
				}
				mirror(pod.Spec.Containers)
				mirror(pod.Spec.InitContainers)
				if target == "different digest" {
					for i := range pod.Spec.Containers {
						if pod.Spec.Containers[i].Name == "catalog" {
							pod.Spec.Containers[i].Image = "quay.io/rhoai/runtime@" + dataTestDigest3
						}
					}
				}
				if target == "old attempt" {
					pod.Annotations[activationAttemptAnnotation] = "older-attempt"
				}
				if err := r.Update(ctx, pod); err != nil {
					t.Fatal(err)
				}
				if invalid {
					pod.Status.InitContainerStatuses = []corev1.ContainerStatus{{
						Name: "catalog-content-validation",
						State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
							ExitCode: 1,
							Message:  `{"stage":"Loading","reason":"CatalogContentInvalid"}`,
						}},
					}}
					pod.Status.ContainerStatuses = nil
					if err := r.Status().Update(ctx, pod); err != nil {
						t.Fatal(err)
					}
				}
				// Model a pending first-start attempt, with consistent readiness conditions.
				catalog.Status.Activation.State = "Pending"
				catalog.Status.Activation.LastSuccessful = nil
				catalog.Status.Activation.StartedAt = metav1.NewTime(time.Now().Add(-9 * time.Minute))
				attempt := catalog.Status.Activation.AttemptID
				setCatalogDataImageCondition(catalog, conditionDataActivationReady,
					metav1.ConditionFalse, "ActivationPending", "Waiting for validation")
				reduceCatalogImageReadiness(catalog)
				if err := r.Status().Update(ctx, catalog); err != nil {
					t.Fatal(err)
				}
				request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(catalog)}
				if _, err := r.Reconcile(ctx, request); err != nil {
					t.Fatal(err)
				}
				if err := r.Get(ctx, request.NamespacedName, catalog); err != nil {
					t.Fatal(err)
				}
				if catalog.Status.Activation.AttemptID != attempt {
					t.Fatal("attempt changed")
				}
				deployment := &appsv1.Deployment{}
				if err := r.Get(ctx, client.ObjectKey{Namespace: catalog.Namespace,
					Name: catalogResourceName}, deployment); err != nil {
					t.Fatal(err)
				}
				if rejected {
					if catalog.Status.Activation.State != "Pending" ||
						catalog.Status.Activation.LastSuccessful != nil || *deployment.Spec.Replicas != 1 {
						t.Fatalf("rejected pod affected activation: %+v", catalog.Status.Activation)
					}
					assertDataImageCondition(t, catalog, conditionCatalogReady,
						metav1.ConditionFalse, "ActivationPending")
				} else if invalid {
					if catalog.Status.Activation.State != "Failed" ||
						catalog.Status.Activation.Reason != "CatalogContentInvalid" ||
						*deployment.Spec.Replicas != 0 {
						t.Fatalf("mirrored failure was not observed: %+v", catalog.Status.Activation)
					}
				} else {
					if catalog.Status.Activation.State != "Succeeded" || *deployment.Spec.Replicas != 1 {
						t.Fatalf("mirrored workload did not activate: %+v", catalog.Status.Activation)
					}
					assertDataImageCondition(t, catalog, conditionCatalogReady,
						metav1.ConditionTrue, ReasonDeploymentAvailable)
				}
			})
		}
	}
}
