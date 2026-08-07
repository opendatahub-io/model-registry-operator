/*
Copyright 2023.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/opendatahub-io/model-registry-operator/api/v1beta1"
	"github.com/opendatahub-io/model-registry-operator/internal/controller/config"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

var _ = Describe("CheckDeploymentPods", func() {
	var (
		ctx            context.Context
		namespacedName types.NamespacedName
		proxyType      string
		defaultMsg     string
		defaultReason  string
		defaultStatus  metav1.ConditionStatus
	)

	BeforeEach(func() {
		ctx = context.Background()
		namespacedName = types.NamespacedName{
			Name:      "test-registry",
			Namespace: "test-namespace",
		}
		proxyType = "kube-rbac-proxy"
		defaultMsg = "All deployment pods ready"
		defaultReason = "DeploymentReady"
		defaultStatus = metav1.ConditionTrue
	})

	It("Case A: No pods available", func() {
		fakeClient := fake.NewClientBuilder().WithScheme(k8sClient.Scheme()).Build()
		reconciler := &ModelRegistryReconciler{
			Client: fakeClient,
			Log:    ctrl.Log.WithName("test"),
		}

		msg, reason, status := reconciler.CheckDeploymentPods(ctx, namespacedName, proxyType, ctrl.Log, defaultMsg, defaultReason, defaultStatus)
		Expect(status).To(Equal(metav1.ConditionFalse))
		Expect(reason).To(Equal(ReasonResourcesUnavailable))
		Expect(msg).To(ContainSubstring("No Pods found for Deployment test-registry"))
	})

	It("Case B: 1 container in pod spec", func() {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-pod-1",
				Namespace: namespacedName.Namespace,
				Labels: map[string]string{
					"app":                    namespacedName.Name,
					"component":              "model-registry",
					"app.kubernetes.io/name": namespacedName.Name,
				},
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{Name: "rest-container"},
				},
			},
		}

		fakeClient := fake.NewClientBuilder().WithScheme(k8sClient.Scheme()).WithObjects(pod).Build()
		reconciler := &ModelRegistryReconciler{
			Client: fakeClient,
			Log:    ctrl.Log.WithName("test"),
		}

		msg, reason, status := reconciler.CheckDeploymentPods(ctx, namespacedName, proxyType, ctrl.Log, defaultMsg, defaultReason, defaultStatus)
		Expect(status).To(Equal(metav1.ConditionFalse))
		Expect(reason).To(Equal(ReasonResourcesUnavailable))
		Expect(msg).To(ContainSubstring("proxy unavailable in Pod test-pod-1"))
	})

	It("Case C: 2 containers, both ready", func() {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-pod-1",
				Namespace: namespacedName.Namespace,
				Labels: map[string]string{
					"app":                    namespacedName.Name,
					"component":              "model-registry",
					"app.kubernetes.io/name": namespacedName.Name,
				},
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{Name: "rest-container"},
					{Name: "kube-rbac-proxy"},
				},
			},
			Status: corev1.PodStatus{
				ContainerStatuses: []corev1.ContainerStatus{
					{Name: "rest-container", Ready: true},
					{Name: "kube-rbac-proxy", Ready: true},
				},
			},
		}

		fakeClient := fake.NewClientBuilder().WithScheme(k8sClient.Scheme()).WithObjects(pod).Build()
		reconciler := &ModelRegistryReconciler{
			Client: fakeClient,
			Log:    ctrl.Log.WithName("test"),
		}

		msg, reason, status := reconciler.CheckDeploymentPods(ctx, namespacedName, proxyType, ctrl.Log, defaultMsg, defaultReason, defaultStatus)
		Expect(status).To(Equal(defaultStatus))
		Expect(reason).To(Equal(defaultReason))
		Expect(msg).To(Equal(defaultMsg))
	})

	It("Case D: 2 containers, proxy ContainerCreating", func() {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-pod-1",
				Namespace: namespacedName.Namespace,
				Labels: map[string]string{
					"app":                    namespacedName.Name,
					"component":              "model-registry",
					"app.kubernetes.io/name": namespacedName.Name,
				},
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{Name: "rest-container"},
					{Name: "kube-rbac-proxy"},
				},
			},
			Status: corev1.PodStatus{
				ContainerStatuses: []corev1.ContainerStatus{
					{Name: "rest-container", Ready: true},
					{
						Name:  "kube-rbac-proxy",
						Ready: false,
						State: corev1.ContainerState{
							Waiting: &corev1.ContainerStateWaiting{Reason: "ContainerCreating"},
						},
					},
				},
			},
		}

		fakeClient := fake.NewClientBuilder().WithScheme(k8sClient.Scheme()).WithObjects(pod).Build()
		reconciler := &ModelRegistryReconciler{
			Client: fakeClient,
			Log:    ctrl.Log.WithName("test"),
		}

		msg, reason, status := reconciler.CheckDeploymentPods(ctx, namespacedName, proxyType, ctrl.Log, defaultMsg, defaultReason, defaultStatus)
		Expect(status).To(Equal(metav1.ConditionFalse))
		Expect(reason).To(Equal(ReasonResourcesUnavailable))
		Expect(msg).To(ContainSubstring("container kube-rbac-proxy not ready in Pod test-pod-1"))
	})

	It("Case E: 2 containers, proxy CrashLoopBackOff", func() {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-pod-1",
				Namespace: namespacedName.Namespace,
				Labels: map[string]string{
					"app":                    namespacedName.Name,
					"component":              "model-registry",
					"app.kubernetes.io/name": namespacedName.Name,
				},
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{Name: "rest-container"},
					{Name: "kube-rbac-proxy"},
				},
			},
			Status: corev1.PodStatus{
				ContainerStatuses: []corev1.ContainerStatus{
					{Name: "rest-container", Ready: true},
					{
						Name:  "kube-rbac-proxy",
						Ready: false,
						State: corev1.ContainerState{
							Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"},
						},
					},
				},
			},
		}

		fakeClient := fake.NewClientBuilder().WithScheme(k8sClient.Scheme()).WithObjects(pod).Build()
		reconciler := &ModelRegistryReconciler{
			Client: fakeClient,
			Log:    ctrl.Log.WithName("test"),
		}

		msg, reason, status := reconciler.CheckDeploymentPods(ctx, namespacedName, proxyType, ctrl.Log, defaultMsg, defaultReason, defaultStatus)
		Expect(status).To(Equal(metav1.ConditionFalse))
		Expect(reason).To(Equal(ReasonResourcesUnavailable))
		Expect(msg).To(ContainSubstring("container kube-rbac-proxy not ready in Pod test-pod-1"))
	})

	It("Case F: 2 containers, no ContainerStatuses", func() {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-pod-1",
				Namespace: namespacedName.Namespace,
				Labels: map[string]string{
					"app":                    namespacedName.Name,
					"component":              "model-registry",
					"app.kubernetes.io/name": namespacedName.Name,
				},
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{Name: "rest-container"},
					{Name: "kube-rbac-proxy"},
				},
			},
			Status: corev1.PodStatus{},
		}

		fakeClient := fake.NewClientBuilder().WithScheme(k8sClient.Scheme()).WithObjects(pod).Build()
		reconciler := &ModelRegistryReconciler{
			Client: fakeClient,
			Log:    ctrl.Log.WithName("test"),
		}

		msg, reason, status := reconciler.CheckDeploymentPods(ctx, namespacedName, proxyType, ctrl.Log, defaultMsg, defaultReason, defaultStatus)
		Expect(status).To(Equal(metav1.ConditionFalse))
		Expect(reason).To(Equal(ReasonResourcesUnavailable))
		Expect(msg).To(ContainSubstring("container status not yet available in Pod test-pod-1"))
	})

	It("Case G: Multiple pods, one unready", func() {
		pod1 := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-pod-1",
				Namespace: namespacedName.Namespace,
				Labels: map[string]string{
					"app":                    namespacedName.Name,
					"component":              "model-registry",
					"app.kubernetes.io/name": namespacedName.Name,
				},
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{Name: "rest-container"},
					{Name: "kube-rbac-proxy"},
				},
			},
			Status: corev1.PodStatus{
				ContainerStatuses: []corev1.ContainerStatus{
					{Name: "rest-container", Ready: true},
					{Name: "kube-rbac-proxy", Ready: true},
				},
			},
		}

		pod2 := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-pod-2",
				Namespace: namespacedName.Namespace,
				Labels: map[string]string{
					"app":                    namespacedName.Name,
					"component":              "model-registry",
					"app.kubernetes.io/name": namespacedName.Name,
				},
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{Name: "rest-container"},
					{Name: "kube-rbac-proxy"},
				},
			},
			Status: corev1.PodStatus{
				ContainerStatuses: []corev1.ContainerStatus{
					{Name: "rest-container", Ready: true},
					{Name: "kube-rbac-proxy", Ready: false},
				},
			},
		}

		fakeClient := fake.NewClientBuilder().WithScheme(k8sClient.Scheme()).WithObjects(pod1, pod2).Build()
		reconciler := &ModelRegistryReconciler{
			Client: fakeClient,
			Log:    ctrl.Log.WithName("test"),
		}

		msg, reason, status := reconciler.CheckDeploymentPods(ctx, namespacedName, proxyType, ctrl.Log, defaultMsg, defaultReason, defaultStatus)
		// rolling-update surge: one ready pod is enough
		Expect(status).To(Equal(defaultStatus))
		Expect(reason).To(Equal(defaultReason))
		Expect(msg).To(Equal(defaultMsg))
	})

	It("Case H: Terminating pod skipped during rolling update", func() {
		now := metav1.Now()
		terminatingPod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "test-pod-old",
				Namespace:         namespacedName.Namespace,
				DeletionTimestamp: &now,
				Finalizers:        []string{"test-finalizer"},
				Labels: map[string]string{
					"app":                    namespacedName.Name,
					"component":              "model-registry",
					"app.kubernetes.io/name": namespacedName.Name,
				},
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{Name: "rest-container"},
					{Name: "kube-rbac-proxy"},
				},
			},
			Status: corev1.PodStatus{
				ContainerStatuses: []corev1.ContainerStatus{
					{Name: "rest-container", Ready: false},
					{Name: "kube-rbac-proxy", Ready: false},
				},
			},
		}

		activePod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-pod-new",
				Namespace: namespacedName.Namespace,
				Labels: map[string]string{
					"app":                    namespacedName.Name,
					"component":              "model-registry",
					"app.kubernetes.io/name": namespacedName.Name,
				},
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{Name: "rest-container"},
					{Name: "kube-rbac-proxy"},
				},
			},
			Status: corev1.PodStatus{
				ContainerStatuses: []corev1.ContainerStatus{
					{Name: "rest-container", Ready: true},
					{Name: "kube-rbac-proxy", Ready: true},
				},
			},
		}

		fakeClient := fake.NewClientBuilder().WithScheme(k8sClient.Scheme()).WithObjects(terminatingPod, activePod).Build()
		reconciler := &ModelRegistryReconciler{
			Client: fakeClient,
			Log:    ctrl.Log.WithName("test"),
		}

		msg, reason, status := reconciler.CheckDeploymentPods(ctx, namespacedName, proxyType, ctrl.Log, defaultMsg, defaultReason, defaultStatus)
		Expect(status).To(Equal(defaultStatus))
		Expect(reason).To(Equal(defaultReason))
		Expect(msg).To(Equal(defaultMsg))
	})

	It("Case I: All pods terminating", func() {
		now := metav1.Now()
		terminatingPod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "test-pod-old",
				Namespace:         namespacedName.Namespace,
				DeletionTimestamp: &now,
				Finalizers:        []string{"test-finalizer"},
				Labels: map[string]string{
					"app":                    namespacedName.Name,
					"component":              "model-registry",
					"app.kubernetes.io/name": namespacedName.Name,
				},
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{Name: "rest-container"},
					{Name: "kube-rbac-proxy"},
				},
			},
		}

		fakeClient := fake.NewClientBuilder().WithScheme(k8sClient.Scheme()).WithObjects(terminatingPod).Build()
		reconciler := &ModelRegistryReconciler{
			Client: fakeClient,
			Log:    ctrl.Log.WithName("test"),
		}

		msg, reason, status := reconciler.CheckDeploymentPods(ctx, namespacedName, proxyType, ctrl.Log, defaultMsg, defaultReason, defaultStatus)
		Expect(status).To(Equal(metav1.ConditionFalse))
		Expect(reason).To(Equal(ReasonResourcesUnavailable))
		Expect(msg).To(ContainSubstring("No active Pods found for Deployment test-registry"))
	})

	It("Case J: Multiple pods, all unready", func() {
		pod1 := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-pod-1",
				Namespace: namespacedName.Namespace,
				Labels: map[string]string{
					"app":                    namespacedName.Name,
					"component":              "model-registry",
					"app.kubernetes.io/name": namespacedName.Name,
				},
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{Name: "rest-container"},
					{Name: "kube-rbac-proxy"},
				},
			},
			Status: corev1.PodStatus{
				ContainerStatuses: []corev1.ContainerStatus{
					{Name: "rest-container", Ready: false},
					{Name: "kube-rbac-proxy", Ready: false},
				},
			},
		}

		pod2 := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-pod-2",
				Namespace: namespacedName.Namespace,
				Labels: map[string]string{
					"app":                    namespacedName.Name,
					"component":              "model-registry",
					"app.kubernetes.io/name": namespacedName.Name,
				},
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{Name: "rest-container"},
					{Name: "kube-rbac-proxy"},
				},
			},
			Status: corev1.PodStatus{
				ContainerStatuses: []corev1.ContainerStatus{
					{Name: "rest-container", Ready: true},
					{Name: "kube-rbac-proxy", Ready: false},
				},
			},
		}

		fakeClient := fake.NewClientBuilder().WithScheme(k8sClient.Scheme()).WithObjects(pod1, pod2).Build()
		reconciler := &ModelRegistryReconciler{
			Client: fakeClient,
			Log:    ctrl.Log.WithName("test"),
		}

		msg, reason, status := reconciler.CheckDeploymentPods(ctx, namespacedName, proxyType, ctrl.Log, defaultMsg, defaultReason, defaultStatus)
		Expect(status).To(Equal(metav1.ConditionFalse))
		Expect(reason).To(Equal(ReasonResourcesUnavailable))
		Expect(msg).To(ContainSubstring("container kube-rbac-proxy not ready in Pod test-pod-2"))
	})
})

var _ = Describe("setRegistryStatus KubeRBACProxy condition", func() {
	var (
		ctx            context.Context
		namespacedName types.NamespacedName
		req            ctrl.Request
		spec           *v1beta1.ModelRegistrySpec
	)

	BeforeEach(func() {
		ctx = context.Background()
		namespacedName = types.NamespacedName{
			Name:      "test-registry",
			Namespace: "test-namespace",
		}
		req = ctrl.Request{NamespacedName: namespacedName}
		spec = &v1beta1.ModelRegistrySpec{
			KubeRBACProxy: &v1beta1.KubeRBACProxyConfig{
				ServiceRoute: config.RouteDisabled,
			},
		}
	})

	It("flips a stale True condition to False when the deployment is unavailable", func() {
		modelRegistry := &v1beta1.ModelRegistry{
			ObjectMeta: metav1.ObjectMeta{
				Name:      namespacedName.Name,
				Namespace: namespacedName.Namespace,
			},
			Spec: *spec,
			Status: v1beta1.ModelRegistryStatus{
				Conditions: []metav1.Condition{
					{
						Type:    ConditionTypeKubeRBACProxy,
						Status:  metav1.ConditionTrue,
						Reason:  ReasonResourcesAvailable,
						Message: "kube-rbac-proxy was successfully created",
					},
				},
			},
		}

		// Deployment has no Available condition, so checkDeploymentAvailability treats it as unavailable.
		deployment := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      namespacedName.Name,
				Namespace: namespacedName.Namespace,
			},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(k8sClient.Scheme()).
			WithObjects(modelRegistry, deployment).
			WithStatusSubresource(modelRegistry).
			Build()
		reconciler := &ModelRegistryReconciler{
			Client: fakeClient,
			Log:    ctrl.Log.WithName("test"),
		}

		params := &ModelRegistryParams{
			Name:      namespacedName.Name,
			Namespace: namespacedName.Namespace,
			Spec:      spec,
		}

		_, err := reconciler.setRegistryStatus(ctx, req, params, ResourceUnchanged)
		Expect(err).ToNot(HaveOccurred())

		updated := &v1beta1.ModelRegistry{}
		Expect(fakeClient.Get(ctx, namespacedName, updated)).To(Succeed())

		condition := meta.FindStatusCondition(updated.Status.Conditions, ConditionTypeKubeRBACProxy)
		Expect(condition).ToNot(BeNil())
		Expect(condition.Status).To(Equal(metav1.ConditionFalse))
		Expect(condition.Reason).To(Equal(ReasonResourcesUnavailable))
	})
})
