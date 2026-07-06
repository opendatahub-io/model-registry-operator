/*
Copyright 2026.

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
	"errors"
	"fmt"
	"os"
	"text/template"
	"time"

	"github.com/opendatahub-io/model-registry-operator/api/v1beta1"
	"github.com/opendatahub-io/model-registry-operator/internal/controller/config"
	"github.com/opendatahub-io/operator-chaos/pkg/sdk"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbac "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func initChaosReconciler(tmpl *template.Template, cfg *sdk.FaultConfig) *ModelRegistryReconciler {
	chaosClient := sdk.NewChaosClient(k8sClient, cfg)

	return &ModelRegistryReconciler{
		Client:   chaosClient,
		Scheme:   k8sClient.Scheme(),
		Recorder: &events.FakeRecorder{},
		Log:      ctrl.Log.WithName("chaos-test"),
		Template: tmpl,
		Capabilities: ClusterCapabilities{
			IsOpenShift:  false,
			HasUserAPI:   false,
			HasConfigAPI: false,
		},
	}
}

func initCatalogChaosReconciler(tmpl *template.Template, cfg *sdk.FaultConfig, targetNamespace string) *ModelCatalogReconciler {
	chaosClient := sdk.NewChaosClient(k8sClient, cfg)

	return &ModelCatalogReconciler{
		Client:          chaosClient,
		Scheme:          k8sClient.Scheme(),
		Recorder:        &events.FakeRecorder{},
		Log:             ctrl.Log.WithName("chaos-test-catalog"),
		Template:        tmpl,
		TargetNamespace: targetNamespace,
		Enabled:         true,
		Capabilities:    ClusterCapabilities{},
	}
}

var _ = Describe("ModelRegistry chaos resilience", func() {

	ctx := context.Background()

	var chaosTemplate *template.Template

	BeforeEach(func() {
		err := os.Setenv(config.RestImage, config.DefaultRestImage)
		Expect(err).NotTo(HaveOccurred())
		err = os.Setenv(config.PostgresImage, config.DefaultPostgresImage)
		Expect(err).NotTo(HaveOccurred())
		err = os.Setenv(config.KubeRBACProxyImage, config.DefaultKubeRBACProxyImage)
		Expect(err).NotTo(HaveOccurred())

		var parseErr error
		chaosTemplate, parseErr = config.ParseTemplates()
		Expect(parseErr).NotTo(HaveOccurred())
	})

	Describe("reconciler under API faults", func() {

		var (
			namespace         *corev1.Namespace
			typeNamespaceName types.NamespacedName
		)

		createRegistry := func(name string, cfg *sdk.FaultConfig) *ModelRegistryReconciler {
			namespace = &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name,
					Namespace: name,
				},
			}
			typeNamespaceName = types.NamespacedName{Name: name, Namespace: name}

			err := k8sClient.Create(ctx, namespace)
			Expect(err).NotTo(HaveOccurred())

			var restPort int32 = 8080
			var postgresPort int32 = 5432
			mr := &v1beta1.ModelRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name,
					Namespace: name,
				},
				Spec: v1beta1.ModelRegistrySpec{
					Rest: v1beta1.RestSpec{
						Port: &restPort,
					},
					Postgres: &v1beta1.PostgresConfig{
						Host:     "db.example.com",
						Port:     &postgresPort,
						Database: "model_registry",
						Username: "admin",
						PasswordSecret: &v1beta1.SecretKeyValue{
							Name: name + "-db-secret",
							Key:  "password",
						},
					},
				},
			}

			dbSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name + "-db-secret",
					Namespace: name,
				},
				StringData: map[string]string{
					"password": "test-password",
				},
			}
			err = k8sClient.Create(ctx, dbSecret)
			Expect(err).NotTo(HaveOccurred())

			err = k8sClient.Create(ctx, mr)
			Expect(err).NotTo(HaveOccurred())

			return initChaosReconciler(chaosTemplate, cfg)
		}

		AfterEach(func() {
			if namespace != nil {
				found := &v1beta1.ModelRegistry{}
				err := k8sClient.Get(ctx, typeNamespaceName, found)
				if err == nil {
					_ = k8sClient.Delete(ctx, found)
				}
				_ = k8sClient.Delete(ctx, namespace)
			}
			_ = os.Unsetenv(config.RestImage)
			_ = os.Unsetenv(config.PostgresImage)
			_ = os.Unsetenv(config.KubeRBACProxyImage)
		})

		It("should handle Get errors with requeue", func() {
			cfg := sdk.NewFaultConfig(map[sdk.Operation]sdk.FaultSpec{
				sdk.OpGet: {ErrorRate: 1.0, Error: "chaos: connection refused"},
			})

			reconciler := createRegistry("chaos-get-errors", cfg)

			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespaceName,
			})

			Expect(err).To(HaveOccurred(), "expected a chaos error on Get")
			var chaosErr *sdk.ChaosError
			Expect(errors.As(err, &chaosErr)).To(BeTrue(), "error should be a ChaosError")
			Expect(chaosErr.Operation).To(Equal(sdk.OpGet))
		})

		It("should converge after transient Get errors clear", func() {
			cfg := sdk.NewFaultConfig(map[sdk.Operation]sdk.FaultSpec{
				sdk.OpGet: {ErrorRate: 1.0, Error: "chaos: transient connection refused"},
			})

			reconciler := createRegistry("chaos-get-transient", cfg)

			By("Verifying reconciler fails while faults are active")
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespaceName,
			})
			Expect(err).To(HaveOccurred())

			By("Clearing faults and verifying convergence")
			cfg.Deactivate()

			Eventually(func() error {
				_, err := reconciler.Reconcile(ctx, reconcile.Request{
					NamespacedName: typeNamespaceName,
				})
				return err
			}, 30*time.Second, 500*time.Millisecond).Should(Succeed(),
				"reconciler should converge once Get errors stop")
		})

		It("should remain converged when Update faults are present but no drift exists", func() {
			reconciler := createRegistry("chaos-update-no-drift", nil)

			By("Running initial clean reconcile to create resources")
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespaceName,
			})
			Expect(err).NotTo(HaveOccurred())

			By("Injecting Update faults and verifying reconciler stays healthy (no updates needed)")
			cfg := sdk.NewFaultConfig(map[sdk.Operation]sdk.FaultSpec{
				sdk.OpUpdate: {ErrorRate: 1.0, Error: "chaos: the object has been modified"},
			})
			reconciler.Client = sdk.NewChaosClient(k8sClient, cfg)

			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespaceName,
			})
			Expect(err).NotTo(HaveOccurred(),
				"reconciler should succeed when state is converged and no updates are needed")
		})

		It("should handle Create failures and report errors", func() {
			cfg := sdk.NewFaultConfig(map[sdk.Operation]sdk.FaultSpec{
				sdk.OpCreate: {ErrorRate: 1.0, Error: "chaos: quota exceeded"},
			})

			reconciler := createRegistry("chaos-create-fail", cfg)

			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespaceName,
			})

			Expect(err).To(HaveOccurred(), "reconciler should return error when all Creates fail")

			var chaosErr *sdk.ChaosError
			Expect(errors.As(err, &chaosErr)).To(BeTrue(), "error should be a ChaosError")
			Expect(chaosErr.Operation).To(Equal(sdk.OpCreate))
		})

		It("should converge after transient Create failures clear", func() {
			cfg := sdk.NewFaultConfig(map[sdk.Operation]sdk.FaultSpec{
				sdk.OpCreate: {ErrorRate: 1.0, Error: "chaos: quota exceeded"},
			})

			reconciler := createRegistry("chaos-create-transient", cfg)

			By("Verifying reconciler fails while faults are active")
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespaceName,
			})
			Expect(err).To(HaveOccurred())

			By("Clearing faults and verifying convergence")
			cfg.Deactivate()

			Eventually(func() error {
				_, err := reconciler.Reconcile(ctx, reconcile.Request{
					NamespacedName: typeNamespaceName,
				})
				return err
			}, 30*time.Second, 500*time.Millisecond).Should(Succeed(),
				"reconciler should converge once Create failures stop")
		})

		It("should tolerate intermittent API errors and eventually converge", func() {
			cfg := sdk.NewFaultConfig(map[sdk.Operation]sdk.FaultSpec{
				sdk.OpGet:    {ErrorRate: 0.15, Error: "chaos: intermittent timeout"},
				sdk.OpCreate: {ErrorRate: 0.15, Error: "chaos: intermittent quota exceeded"},
			})

			reconciler := createRegistry("chaos-intermittent", cfg)

			Eventually(func() error {
				_, err := reconciler.Reconcile(ctx, reconcile.Request{
					NamespacedName: typeNamespaceName,
				})
				return err
			}, 30*time.Second, 200*time.Millisecond).Should(Succeed(),
				"reconciler should eventually converge despite intermittent errors")
		})
	})
})

var _ = Describe("ModelCatalog chaos resilience", func() {

	ctx := context.Background()

	var chaosTemplate *template.Template

	BeforeEach(func() {
		err := os.Setenv(config.RestImage, config.DefaultRestImage)
		Expect(err).NotTo(HaveOccurred())
		err = os.Setenv(config.PostgresImage, config.DefaultPostgresImage)
		Expect(err).NotTo(HaveOccurred())
		err = os.Setenv(config.CatalogDataImage, config.DefaultCatalogDataImage)
		Expect(err).NotTo(HaveOccurred())
		err = os.Setenv(config.BenchmarkDataImage, config.DefaultBenchmarkDataImage)
		Expect(err).NotTo(HaveOccurred())
		config.SetDefaultDomain("example.com", nil, false)

		var parseErr error
		chaosTemplate, parseErr = config.ParseTemplates()
		Expect(parseErr).NotTo(HaveOccurred())
	})

	Describe("catalog reconciler under API faults", func() {

		var (
			namespace     *corev1.Namespace
			namespaceName string
		)

		createCatalogNamespace := func(suffix string) string {
			namespaceName = fmt.Sprintf("chaos-catalog-%s-%d", suffix, time.Now().UnixNano())
			namespace = &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      namespaceName,
					Namespace: namespaceName,
				},
			}
			err := k8sClient.Create(ctx, namespace)
			Expect(err).NotTo(HaveOccurred())
			return namespaceName
		}

		AfterEach(func() {
			if namespace != nil {
				_ = k8sClient.Delete(ctx, namespace)
			}
			crb := &rbac.ClusterRoleBinding{}
			crbName := modelCatalogName + "-auth-delegator"
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: crbName}, crb); err == nil {
				_ = k8sClient.Delete(ctx, crb)
			}
			_ = os.Unsetenv(config.RestImage)
			_ = os.Unsetenv(config.PostgresImage)
			_ = os.Unsetenv(config.CatalogDataImage)
			_ = os.Unsetenv(config.BenchmarkDataImage)
		})

		It("should handle Get errors with chaos error", func() {
			ns := createCatalogNamespace("get-errors")
			cfg := sdk.NewFaultConfig(map[sdk.Operation]sdk.FaultSpec{
				sdk.OpGet: {ErrorRate: 1.0, Error: "chaos: connection refused"},
			})

			reconciler := initCatalogChaosReconciler(chaosTemplate, cfg, ns)

			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: modelCatalogName, Namespace: ns},
			})

			Expect(err).To(HaveOccurred())
			var chaosErr *sdk.ChaosError
			Expect(errors.As(err, &chaosErr)).To(BeTrue(), "error should be a ChaosError")
		})

		It("should converge after transient Get errors clear", func() {
			ns := createCatalogNamespace("get-transient")
			cfg := sdk.NewFaultConfig(map[sdk.Operation]sdk.FaultSpec{
				sdk.OpGet: {ErrorRate: 1.0, Error: "chaos: transient connection refused"},
			})

			reconciler := initCatalogChaosReconciler(chaosTemplate, cfg, ns)

			By("Verifying reconciler fails while faults are active")
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: modelCatalogName, Namespace: ns},
			})
			Expect(err).To(HaveOccurred())

			By("Clearing faults and verifying convergence")
			cfg.Deactivate()

			Eventually(func() error {
				_, err := reconciler.Reconcile(ctx, reconcile.Request{
					NamespacedName: types.NamespacedName{Name: modelCatalogName, Namespace: ns},
				})
				return err
			}, 30*time.Second, 500*time.Millisecond).Should(Succeed(),
				"catalog reconciler should converge once Get errors stop")
		})

		It("should handle Create failures during resource provisioning", func() {
			ns := createCatalogNamespace("create-fail")
			cfg := sdk.NewFaultConfig(map[sdk.Operation]sdk.FaultSpec{
				sdk.OpCreate: {ErrorRate: 1.0, Error: "chaos: quota exceeded"},
			})

			reconciler := initCatalogChaosReconciler(chaosTemplate, cfg, ns)

			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: modelCatalogName, Namespace: ns},
			})

			Expect(err).To(HaveOccurred(), "catalog reconciler should return error when all Creates fail")

			var chaosErr *sdk.ChaosError
			Expect(errors.As(err, &chaosErr)).To(BeTrue(), "error should be a ChaosError")
			Expect(chaosErr.Operation).To(Equal(sdk.OpCreate))
		})

		It("should converge after transient Create failures clear and verify resources exist", func() {
			ns := createCatalogNamespace("create-transient")
			cfg := sdk.NewFaultConfig(map[sdk.Operation]sdk.FaultSpec{
				sdk.OpCreate: {ErrorRate: 1.0, Error: "chaos: quota exceeded"},
			})

			reconciler := initCatalogChaosReconciler(chaosTemplate, cfg, ns)

			By("Verifying reconciler fails while faults are active")
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: modelCatalogName, Namespace: ns},
			})
			Expect(err).To(HaveOccurred())

			By("Clearing faults and verifying convergence")
			cfg.Deactivate()

			Eventually(func() error {
				_, err := reconciler.Reconcile(ctx, reconcile.Request{
					NamespacedName: types.NamespacedName{Name: modelCatalogName, Namespace: ns},
				})
				return err
			}, 30*time.Second, 500*time.Millisecond).Should(Succeed(),
				"catalog reconciler should converge once Create failures stop")

			By("Verifying catalog deployment was created")
			deploy := &appsv1.Deployment{}
			err = k8sClient.Get(ctx, types.NamespacedName{Name: modelCatalogName, Namespace: ns}, deploy)
			Expect(err).NotTo(HaveOccurred(), "catalog deployment should exist after convergence")

			By("Verifying catalog service account was created")
			sa := &corev1.ServiceAccount{}
			err = k8sClient.Get(ctx, types.NamespacedName{Name: modelCatalogName, Namespace: ns}, sa)
			Expect(err).NotTo(HaveOccurred(), "catalog service account should exist after convergence")
		})
	})
})
