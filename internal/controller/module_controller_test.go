/*
Copyright 2024.

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
	"os"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
)

var _ = Describe("ModuleCR Controller", func() {

	ctx := context.Background()

	var reconciler *ModuleCRReconciler

	BeforeEach(func() {
		reconciler = &ModuleCRReconciler{
			Client: k8sClient,
			Log:    ctrl.Log.WithName("test-module-controller"),
		}
	})

	AfterEach(func() {
		moduleCR := &unstructured.Unstructured{}
		moduleCR.SetGroupVersionKind(moduleCRGVK)
		moduleCR.SetName(moduleCRName)
		_ = k8sClient.Delete(ctx, moduleCR)
	})

	Context("when module CR does not exist", func() {
		It("should be a no-op", func() {
			result, err := reconciler.Reconcile(ctx, ctrl.Request{
				NamespacedName: types.NamespacedName{Name: moduleCRName},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(ctrl.Result{}))
		})
	})

	Context("when module CR exists with Managed state", func() {
		BeforeEach(func() {
			moduleCR := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "components.platform.opendatahub.io/v1alpha1",
					"kind":       "ModelRegistry",
					"metadata": map[string]interface{}{
						"name": moduleCRName,
					},
					"spec": map[string]interface{}{
						"managementState":     "Managed",
						"registriesNamespace": "odh-model-registries",
					},
				},
			}
			Expect(k8sClient.Create(ctx, moduleCR)).To(Succeed())
		})

		It("should set Ready=True and phase=Ready with no instances", func() {
			result, err := reconciler.Reconcile(ctx, ctrl.Request{
				NamespacedName: types.NamespacedName{Name: moduleCRName},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(BeNumerically(">", 0))

			updatedCR := &unstructured.Unstructured{}
			updatedCR.SetGroupVersionKind(moduleCRGVK)
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: moduleCRName}, updatedCR)).To(Succeed())

			conditions, found, err := unstructured.NestedSlice(updatedCR.Object, "status", "conditions")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(conditions).To(HaveLen(3))

			readyCond := findCondition(conditions, "Ready")
			Expect(readyCond).NotTo(BeNil())
			Expect(readyCond["status"]).To(Equal("True"))
			Expect(readyCond["reason"]).To(Equal("Ready"))

			provCond := findCondition(conditions, "ProvisioningSucceeded")
			Expect(provCond).NotTo(BeNil())
			Expect(provCond["status"]).To(Equal("True"))

			degradedCond := findCondition(conditions, "Degraded")
			Expect(degradedCond).NotTo(BeNil())
			Expect(degradedCond["status"]).To(Equal("False"))

			phase, found, err := unstructured.NestedString(updatedCR.Object, "status", "phase")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(phase).To(Equal("Ready"))
		})

		It("should set observedGeneration", func() {
			_, err := reconciler.Reconcile(ctx, ctrl.Request{
				NamespacedName: types.NamespacedName{Name: moduleCRName},
			})
			Expect(err).NotTo(HaveOccurred())

			updatedCR := &unstructured.Unstructured{}
			updatedCR.SetGroupVersionKind(moduleCRGVK)
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: moduleCRName}, updatedCR)).To(Succeed())

			observedGen, found, err := unstructured.NestedInt64(updatedCR.Object, "status", "observedGeneration")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(observedGen).To(Equal(updatedCR.GetGeneration()))
		})

		It("should include releases with version", func() {
			_, err := reconciler.Reconcile(ctx, ctrl.Request{
				NamespacedName: types.NamespacedName{Name: moduleCRName},
			})
			Expect(err).NotTo(HaveOccurred())

			updatedCR := &unstructured.Unstructured{}
			updatedCR.SetGroupVersionKind(moduleCRGVK)
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: moduleCRName}, updatedCR)).To(Succeed())

			releases, found, err := unstructured.NestedSlice(updatedCR.Object, "status", "releases")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(releases).To(HaveLen(1))

			release := releases[0].(map[string]interface{})
			Expect(release["name"]).To(Equal("model-registry"))
			Expect(release["version"]).To(Equal("latest"))
		})

		It("should use VERSION env var in releases", func() {
			os.Setenv("VERSION", "v1.2.3")
			defer os.Unsetenv("VERSION")

			_, err := reconciler.Reconcile(ctx, ctrl.Request{
				NamespacedName: types.NamespacedName{Name: moduleCRName},
			})
			Expect(err).NotTo(HaveOccurred())

			updatedCR := &unstructured.Unstructured{}
			updatedCR.SetGroupVersionKind(moduleCRGVK)
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: moduleCRName}, updatedCR)).To(Succeed())

			releases, _, _ := unstructured.NestedSlice(updatedCR.Object, "status", "releases")
			release := releases[0].(map[string]interface{})
			Expect(release["version"]).To(Equal("v1.2.3"))
		})

		It("should preserve lastTransitionTime when status is unchanged", func() {
			_, err := reconciler.Reconcile(ctx, ctrl.Request{
				NamespacedName: types.NamespacedName{Name: moduleCRName},
			})
			Expect(err).NotTo(HaveOccurred())

			updatedCR := &unstructured.Unstructured{}
			updatedCR.SetGroupVersionKind(moduleCRGVK)
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: moduleCRName}, updatedCR)).To(Succeed())

			conditions1, _, _ := unstructured.NestedSlice(updatedCR.Object, "status", "conditions")
			readyCond1 := findCondition(conditions1, "Ready")
			Expect(readyCond1).NotTo(BeNil())
			firstTransitionTime := readyCond1["lastTransitionTime"]

			_, err = reconciler.Reconcile(ctx, ctrl.Request{
				NamespacedName: types.NamespacedName{Name: moduleCRName},
			})
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: moduleCRName}, updatedCR)).To(Succeed())
			conditions2, _, _ := unstructured.NestedSlice(updatedCR.Object, "status", "conditions")
			readyCond2 := findCondition(conditions2, "Ready")
			Expect(readyCond2).NotTo(BeNil())
			Expect(readyCond2["lastTransitionTime"]).To(Equal(firstTransitionTime))
		})
	})

	Context("when module CR has Removed management state", func() {
		BeforeEach(func() {
			moduleCR := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "components.platform.opendatahub.io/v1alpha1",
					"kind":       "ModelRegistry",
					"metadata": map[string]interface{}{
						"name": moduleCRName,
					},
					"spec": map[string]interface{}{
						"managementState":     "Removed",
						"registriesNamespace": "odh-model-registries",
					},
				},
			}
			Expect(k8sClient.Create(ctx, moduleCR)).To(Succeed())
		})

		It("should set Ready=False with reason Removed and phase Not Ready", func() {
			result, err := reconciler.Reconcile(ctx, ctrl.Request{
				NamespacedName: types.NamespacedName{Name: moduleCRName},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(BeNumerically(">", 0))

			updatedCR := &unstructured.Unstructured{}
			updatedCR.SetGroupVersionKind(moduleCRGVK)
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: moduleCRName}, updatedCR)).To(Succeed())

			conditions, _, _ := unstructured.NestedSlice(updatedCR.Object, "status", "conditions")
			readyCond := findCondition(conditions, "Ready")
			Expect(readyCond).NotTo(BeNil())
			Expect(readyCond["status"]).To(Equal("False"))
			Expect(readyCond["reason"]).To(Equal("Removed"))

			phase, _, _ := unstructured.NestedString(updatedCR.Object, "status", "phase")
			Expect(phase).To(Equal("Not Ready"))
		})
	})

	Context("when model registry instances exist", func() {
		BeforeEach(func() {
			moduleCR := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "components.platform.opendatahub.io/v1alpha1",
					"kind":       "ModelRegistry",
					"metadata": map[string]interface{}{
						"name": moduleCRName,
					},
					"spec": map[string]interface{}{
						"managementState":     "Managed",
						"registriesNamespace": "default",
					},
				},
			}
			Expect(k8sClient.Create(ctx, moduleCR)).To(Succeed())
		})

		It("should report Ready=False and phase=Not Ready when an instance is unavailable", func() {
			instance := createTestModelRegistry("test-registry-unavailable", "default")
			Expect(k8sClient.Create(ctx, instance)).To(Succeed())

			setInstanceCondition(ctx, instance, ConditionTypeAvailable, metav1.ConditionFalse, "Unavailable", "Deployment not ready")

			result, err := reconciler.Reconcile(ctx, ctrl.Request{
				NamespacedName: types.NamespacedName{Name: moduleCRName},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(BeNumerically(">", 0))

			updatedCR := &unstructured.Unstructured{}
			updatedCR.SetGroupVersionKind(moduleCRGVK)
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: moduleCRName}, updatedCR)).To(Succeed())

			conditions, _, _ := unstructured.NestedSlice(updatedCR.Object, "status", "conditions")
			readyCond := findCondition(conditions, "Ready")
			Expect(readyCond).NotTo(BeNil())
			Expect(readyCond["status"]).To(Equal("False"))
			Expect(readyCond["reason"]).To(Equal("NotAllRegistriesAvailable"))

			phase, _, _ := unstructured.NestedString(updatedCR.Object, "status", "phase")
			Expect(phase).To(Equal("Not Ready"))

			Expect(k8sClient.Delete(ctx, instance)).To(Succeed())
		})

		It("should report Degraded=True when an instance is degraded", func() {
			instance := createTestModelRegistry("test-registry-degraded", "default")
			Expect(k8sClient.Create(ctx, instance)).To(Succeed())

			setInstanceCondition(ctx, instance, ConditionTypeAvailable, metav1.ConditionTrue, "Available", "Running")
			setInstanceCondition(ctx, instance, ConditionTypeDegraded, metav1.ConditionTrue, "SomeDegradation", "Non-critical issue")

			_, err := reconciler.Reconcile(ctx, ctrl.Request{
				NamespacedName: types.NamespacedName{Name: moduleCRName},
			})
			Expect(err).NotTo(HaveOccurred())

			updatedCR := &unstructured.Unstructured{}
			updatedCR.SetGroupVersionKind(moduleCRGVK)
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: moduleCRName}, updatedCR)).To(Succeed())

			conditions, _, _ := unstructured.NestedSlice(updatedCR.Object, "status", "conditions")
			degradedCond := findCondition(conditions, "Degraded")
			Expect(degradedCond).NotTo(BeNil())
			Expect(degradedCond["status"]).To(Equal("True"))

			Expect(k8sClient.Delete(ctx, instance)).To(Succeed())
		})

		It("should only count instances in the registriesNamespace", func() {
			instanceInScope := createTestModelRegistry("test-registry-in-scope", "default")
			Expect(k8sClient.Create(ctx, instanceInScope)).To(Succeed())
			setInstanceCondition(ctx, instanceInScope, ConditionTypeAvailable, metav1.ConditionTrue, "Available", "Running")

			instanceOutOfScope := createTestModelRegistry("test-registry-out-of-scope", "kube-system")
			Expect(k8sClient.Create(ctx, instanceOutOfScope)).To(Succeed())
			setInstanceCondition(ctx, instanceOutOfScope, ConditionTypeAvailable, metav1.ConditionFalse, "Unavailable", "Not running")

			_, err := reconciler.Reconcile(ctx, ctrl.Request{
				NamespacedName: types.NamespacedName{Name: moduleCRName},
			})
			Expect(err).NotTo(HaveOccurred())

			updatedCR := &unstructured.Unstructured{}
			updatedCR.SetGroupVersionKind(moduleCRGVK)
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: moduleCRName}, updatedCR)).To(Succeed())

			conditions, _, _ := unstructured.NestedSlice(updatedCR.Object, "status", "conditions")
			readyCond := findCondition(conditions, "Ready")
			Expect(readyCond).NotTo(BeNil())
			Expect(readyCond["status"]).To(Equal("True"))

			Expect(k8sClient.Delete(ctx, instanceInScope)).To(Succeed())
			Expect(k8sClient.Delete(ctx, instanceOutOfScope)).To(Succeed())
		})
	})
})

func findCondition(conditions []interface{}, condType string) map[string]interface{} {
	for _, c := range conditions {
		cm, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		if cm["type"] == condType {
			return cm
		}
	}
	return nil
}

func createTestModelRegistry(name, namespace string) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "modelregistry.opendatahub.io/v1beta1",
			"kind":       "ModelRegistry",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": namespace,
			},
			"spec": map[string]interface{}{
				"rest": map[string]interface{}{
					"port": int64(8080),
				},
				"postgres": map[string]interface{}{
					"host":     "localhost",
					"port":     int64(5432),
					"username": "user",
					"database": "testdb",
				},
			},
		},
	}
}

func setInstanceCondition(ctx context.Context, instance *unstructured.Unstructured, condType string, status metav1.ConditionStatus, reason, message string) {
	Expect(k8sClient.Get(ctx, types.NamespacedName{
		Name:      instance.GetName(),
		Namespace: instance.GetNamespace(),
	}, instance)).To(Succeed())

	conditions, _, _ := unstructured.NestedSlice(instance.Object, "status", "conditions")
	newCond := map[string]interface{}{
		"type":               condType,
		"status":             string(status),
		"reason":             reason,
		"message":            message,
		"lastTransitionTime": "2026-01-01T00:00:00Z",
	}

	found := false
	for i, c := range conditions {
		cm, ok := c.(map[string]interface{})
		if ok && cm["type"] == condType {
			conditions[i] = newCond
			found = true
			break
		}
	}
	if !found {
		conditions = append(conditions, newCond)
	}

	Expect(unstructured.SetNestedSlice(instance.Object, conditions, "status", "conditions")).To(Succeed())
	Expect(k8sClient.Status().Update(ctx, instance)).To(Succeed())
}
