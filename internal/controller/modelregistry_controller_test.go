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
	"fmt"
	"github.com/opendatahub-io/model-registry-operator/internal/controller/config"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/client-go/tools/record"
	"os"
	ctrl "sigs.k8s.io/controller-runtime"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/opendatahub-io/model-registry-operator/api/v1alpha1"
)

var _ = Describe("ModelRegistry controller", func() {
	Context("ModelRegistry controller test", func() {

		const ModelRegistryName = "test-modelregistry"

		ctx := context.Background()

		namespace := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name:      ModelRegistryName,
				Namespace: ModelRegistryName,
			},
		}

		typeNamespaceName := types.NamespacedName{Name: ModelRegistryName, Namespace: ModelRegistryName}
		modelRegistry := &v1alpha1.ModelRegistry{}

		// load templates
		template, err := config.ParseTemplates()
		Expect(err).To(Not(HaveOccurred()))

		BeforeEach(func() {
			By("Creating the Namespace to perform the tests")
			err := k8sClient.Create(ctx, namespace)
			Expect(err).To(Not(HaveOccurred()))

			By("Setting the Image ENV VARs which stores the Server images")
			err = os.Setenv("GRPC_IMAGE", "gcr.io/tfx-oss-public/ml_metadata_store_server:1.14.0")
			Expect(err).To(Not(HaveOccurred()))
			err = os.Setenv("REST_IMAGE", "quay.io/opendatahub/model-registry:latest")
			Expect(err).To(Not(HaveOccurred()))

			By("creating the custom resource for the Kind ModelRegistry")
			err = k8sClient.Get(ctx, typeNamespaceName, modelRegistry)
			if err != nil && errors.IsNotFound(err) {
				// Let's mock our custom resource at the same way that we would
				// apply on the cluster the manifest under config/samples
				var gRPCPort int32 = 9090
				var restPort int32 = 8080
				var postgresPort int32 = 5432
				modelRegistry = &v1alpha1.ModelRegistry{
					ObjectMeta: metav1.ObjectMeta{
						Name:      ModelRegistryName,
						Namespace: namespace.Name,
					},
					Spec: v1alpha1.ModelRegistrySpec{
						Grpc: v1alpha1.GrpcSpec{
							Port: &gRPCPort,
						},
						Rest: v1alpha1.RestSpec{
							Port: &restPort,
						},
						Postgres: v1alpha1.PostgresConfig{
							Host:     "model-registry-db",
							Port:     &postgresPort,
							Database: "model-registry",
							Username: "mlmduser",
							PasswordSecret: &v1alpha1.SecretKeyValue{
								Name: "model-registry-db",
								Key:  "database-password",
							},
						},
					},
				}
				err = k8sClient.Create(ctx, modelRegistry)
				Expect(err).To(Not(HaveOccurred()))
			}
		})

		AfterEach(func() {
			By("removing the custom resource for the Kind ModelRegistry")
			found := &v1alpha1.ModelRegistry{}
			err := k8sClient.Get(ctx, typeNamespaceName, found)
			Expect(err).To(Not(HaveOccurred()))

			Eventually(func() error {
				return k8sClient.Delete(context.TODO(), found)
			}, 2*time.Minute, time.Second).Should(Succeed())

			// TODO(user): Attention if you improve this code by adding other context test you MUST
			// be aware of the current delete namespace limitations.
			// More info: https://book.kubebuilder.io/reference/envtest.html#testing-considerations
			By("Deleting the Namespace to perform the tests")
			_ = k8sClient.Delete(ctx, namespace)

			By("Removing the Image ENV VARs which stores the Server images")
			_ = os.Unsetenv("GRPC_IMAGE")
			_ = os.Unsetenv("REST_IMAGE")
		})

		It("should successfully reconcile a custom resource for ModelRegistry", func() {
			By("Checking if the custom resource was successfully created")
			Eventually(func() error {
				found := &v1alpha1.ModelRegistry{}
				return k8sClient.Get(ctx, typeNamespaceName, found)
			}, time.Minute, time.Second).Should(Succeed())

			modelRegistryReconciler := &ModelRegistryReconciler{
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
				Recorder: &record.FakeRecorder{},
				Log:      ctrl.Log.WithName("controller"),
				Template: template,
			}

			By("Reconciling the custom resource created")
			Eventually(func() error {
				result, err := modelRegistryReconciler.Reconcile(ctx, reconcile.Request{
					NamespacedName: typeNamespaceName,
				})
				if err != nil {
					return err
				}
				if !result.IsZero() {
					return fmt.Errorf("non-empty reconcile result")
				}
				// reconcile done!
				return nil
			}, time.Minute, time.Second).Should(Succeed())
			//Expect(err).To(Not(HaveOccurred()))

			By("Checking if Deployment was successfully created in the reconciliation")
			Eventually(func() error {
				found := &appsv1.Deployment{}
				return k8sClient.Get(ctx, typeNamespaceName, found)
			}, time.Minute, time.Second).Should(Succeed())

			By("Checking the latest Status Condition added to the ModelRegistry instance")
			Eventually(func() error {
				err := k8sClient.Get(ctx, typeNamespaceName, modelRegistry)
				Expect(err).To(Not(HaveOccurred()))
				if !meta.IsStatusConditionTrue(modelRegistry.Status.Conditions, ConditionTypeProgressing) {
					return fmt.Errorf("Condition %s is not true", ConditionTypeProgressing)
				}
				//if !meta.IsStatusConditionTrue(modelRegistry.Status.Conditions, ConditionTypeAvailable) {
				//	return fmt.Errorf("Condition %s is not true", ConditionTypeAvailable)
				//}
				return nil
			}, time.Minute, time.Second).Should(Succeed())
		})
	})
})
