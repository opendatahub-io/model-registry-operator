package controller

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/opendatahub-io/model-registry-operator/api/v1beta1"
	"github.com/opendatahub-io/model-registry-operator/internal/controller/config"
	routev1 "github.com/openshift/api/route/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("ModelRegistry Catalog controller", func() {

	Context("Model Catalog functionality", func() {

		ctx := context.Background()
		var namespace *corev1.Namespace
		var modelRegistry *v1beta1.ModelRegistry
		var registryName string
		var reconciler *ModelRegistryReconciler
		var params *ModelRegistryParams

		BeforeEach(func() {
			By("Setting the Image ENV VARs")
			err := os.Setenv(config.GrpcImage, config.DefaultGrpcImage)
			Expect(err).To(Not(HaveOccurred()))
			err = os.Setenv(config.RestImage, config.DefaultRestImage)
			Expect(err).To(Not(HaveOccurred()))

			registryName = fmt.Sprintf("model-registry-catalog-test-%d", time.Now().UnixNano())

			namespace = &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      registryName,
					Namespace: registryName,
				},
			}

			By("Creating the Namespace to perform the tests")
			err = k8sClient.Create(ctx, namespace)
			Expect(err).To(Not(HaveOccurred()))

			var mySQLPort int32 = 3306
			var gRPCPort int32 = 9090
			var restPort int32 = 8080
			var oauthPort int32 = 8443
			modelRegistry = &v1beta1.ModelRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name:        registryName,
					Namespace:   namespace.Name,
					Annotations: map[string]string{DisplayNameAnnotation: registryName, DescriptionAnnotation: "Test Catalog Registry"},
				},
				Spec: v1beta1.ModelRegistrySpec{
					Grpc: v1beta1.GrpcSpec{
						Port: &gRPCPort,
					},
					Rest: v1beta1.RestSpec{
						Port:  &restPort,
						Image: "quay.io/opendatahub/model-registry:latest", // Required for catalog container
					},
					MySQL: &v1beta1.MySQLConfig{
						Host:     "model-registry-db",
						Port:     &mySQLPort,
						Database: "model_registry",
						Username: "mlmduser",
						PasswordSecret: &v1beta1.SecretKeyValue{
							Name: "model-registry-db",
							Key:  "database-password",
						},
					},
					OAuthProxy: &v1beta1.OAuthProxyConfig{
						Port:   &oauthPort,                                             // Default OAuth proxy port
						Image:  "registry.redhat.io/openshift4/ose-oauth-proxy:latest", // Default OAuth proxy image
						Domain: "example.com",                                          // Required for valid Route hostnames
					},
				},
			}

			template, err := config.ParseTemplates()
			Expect(err).To(Not(HaveOccurred()))

			reconciler = &ModelRegistryReconciler{
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
				Recorder: &record.FakeRecorder{},
				Log:      ctrl.Log.WithName("controller"),
				Template: template,
			}

			By("Creating the ModelRegistry in Kubernetes to get a UID")
			err = k8sClient.Create(ctx, modelRegistry)
			Expect(err).To(Not(HaveOccurred()))

			params = &ModelRegistryParams{
				Name:      registryName,
				Namespace: registryName,
				Spec:      &modelRegistry.Spec,
			}
		})

		Context("When model catalog is enabled", func() {
			BeforeEach(func() {
				reconciler.EnableModelCatalog = true
			})

			It("Should create all catalog resources", func() {
				result, err := reconciler.createOrUpdateCatalogConfig(ctx, params, modelRegistry)
				Expect(err).To(Not(HaveOccurred()))
				Expect(result).To(Not(Equal(ResourceUnchanged)))

				By("Checking if the ConfigMap was created")
				configMap := &corev1.ConfigMap{}
				err = k8sClient.Get(ctx, types.NamespacedName{
					Name:      "model-catalog-sources",
					Namespace: registryName,
				}, configMap)
				Expect(err).To(Not(HaveOccurred()))
				Expect(configMap.Labels["app"]).To(Equal(registryName))
				Expect(configMap.Labels["component"]).To(Equal("model-registry"))

				By("Checking if the Deployment was created")
				deployment := &appsv1.Deployment{}
				err = k8sClient.Get(ctx, types.NamespacedName{
					Name:      fmt.Sprintf("%s-catalog", registryName),
					Namespace: registryName,
				}, deployment)
				Expect(err).To(Not(HaveOccurred()))
				Expect(deployment.Labels["app"]).To(Equal(registryName))
				Expect(deployment.Labels["component"]).To(Equal("model-catalog"))
				Expect(deployment.Spec.Template.Spec.Containers).To(HaveLen(2)) // catalog + oauth-proxy
				Expect(deployment.Spec.Template.Spec.Containers[0].Name).To(Equal("catalog"))

				By("Checking if the Service was created")
				service := &corev1.Service{}
				err = k8sClient.Get(ctx, types.NamespacedName{
					Name:      fmt.Sprintf("%s-catalog", registryName),
					Namespace: registryName,
				}, service)
				Expect(err).To(Not(HaveOccurred()))
				Expect(service.Labels["app"]).To(Equal(registryName))
				Expect(service.Labels["component"]).To(Equal("model-catalog"))
			})

			It("Should handle subsequent calls idempotently", func() {
				result1, err := reconciler.createOrUpdateCatalogConfig(ctx, params, modelRegistry)
				Expect(err).To(Not(HaveOccurred()))
				Expect(result1).To(Not(Equal(ResourceUnchanged)))

				result2, err := reconciler.createOrUpdateCatalogConfig(ctx, params, modelRegistry)
				Expect(err).To(Not(HaveOccurred()))
				Expect(result2).To(Equal(ResourceUnchanged))
			})

			Context("On OpenShift", func() {
				BeforeEach(func() {
					reconciler.IsOpenShift = true
				})

				It("Should create OpenShift-specific resources", func() {
					result, err := reconciler.createOrUpdateCatalogConfig(ctx, params, modelRegistry)
					Expect(err).To(Not(HaveOccurred()))
					Expect(result).To(Not(Equal(ResourceUnchanged)))

					By("Checking if the Route was created")
					route := &routev1.Route{}
					err = k8sClient.Get(ctx, types.NamespacedName{
						Name:      fmt.Sprintf("%s-catalog-https", registryName),
						Namespace: registryName,
					}, route)
					Expect(err).To(Not(HaveOccurred()))
					Expect(route.Labels["app"]).To(Equal(registryName))
					Expect(route.Labels["component"]).To(Equal("model-catalog"))

					By("Checking if the NetworkPolicy was created")
					networkPolicy := &networkingv1.NetworkPolicy{}
					err = k8sClient.Get(ctx, types.NamespacedName{
						Name:      fmt.Sprintf("%s-catalog-https-route", registryName),
						Namespace: registryName,
					}, networkPolicy)
					Expect(err).To(Not(HaveOccurred()))
					Expect(networkPolicy.Labels["app"]).To(Equal(registryName))
					Expect(networkPolicy.Labels["component"]).To(Equal("model-registry"))
				})
			})
		})

		Context("When model catalog is disabled", func() {
			BeforeEach(func() {
				reconciler.EnableModelCatalog = false
			})

			It("Should not create any catalog resources", func() {
				_, err := reconciler.createOrUpdateCatalogConfig(ctx, params, modelRegistry)
				Expect(err).To(Not(HaveOccurred()))

				By("Checking that no ConfigMap was created")
				configMap := &corev1.ConfigMap{}
				err = k8sClient.Get(ctx, types.NamespacedName{
					Name:      "model-catalog-sources",
					Namespace: registryName,
				}, configMap)
				Expect(errors.IsNotFound(err)).To(BeTrue())

				By("Checking that no Deployment was created")
				deployment := &appsv1.Deployment{}
				err = k8sClient.Get(ctx, types.NamespacedName{
					Name:      fmt.Sprintf("%s-catalog", registryName),
					Namespace: registryName,
				}, deployment)
				Expect(errors.IsNotFound(err)).To(BeTrue())

				By("Checking that no Service was created")
				service := &corev1.Service{}
				err = k8sClient.Get(ctx, types.NamespacedName{
					Name:      fmt.Sprintf("%s-catalog", registryName),
					Namespace: registryName,
				}, service)
				Expect(errors.IsNotFound(err)).To(BeTrue())
			})

			It("Should delete existing catalog resources when disabled", func() {
				By("First enabling catalog to create resources")
				reconciler.EnableModelCatalog = true
				result, err := reconciler.createOrUpdateCatalogConfig(ctx, params, modelRegistry)
				Expect(err).To(Not(HaveOccurred()))
				Expect(result).To(Not(Equal(ResourceUnchanged)))

				By("Verifying resources exist")
				configMap := &corev1.ConfigMap{}
				err = k8sClient.Get(ctx, types.NamespacedName{
					Name:      "model-catalog-sources",
					Namespace: registryName,
				}, configMap)
				Expect(err).To(Not(HaveOccurred()))

				By("Disabling catalog and calling the method again")
				reconciler.EnableModelCatalog = false
				result, err = reconciler.createOrUpdateCatalogConfig(ctx, params, modelRegistry)
				Expect(err).To(Not(HaveOccurred()))

				By("Verifying resources are deleted")
				deployment := &appsv1.Deployment{}
				Eventually(func() bool {
					err = k8sClient.Get(ctx, types.NamespacedName{
						Name:      fmt.Sprintf("%s-catalog", registryName),
						Namespace: registryName,
					}, deployment)
					return errors.IsNotFound(err)
				}, 10*time.Second, 1*time.Second).Should(BeTrue())

				service := &corev1.Service{}
				Eventually(func() bool {
					err = k8sClient.Get(ctx, types.NamespacedName{
						Name:      fmt.Sprintf("%s-catalog", registryName),
						Namespace: registryName,
					}, service)
					return errors.IsNotFound(err)
				}, 10*time.Second, 1*time.Second).Should(BeTrue())
			})

			Context("On OpenShift with existing resources", func() {
				BeforeEach(func() {
					reconciler.IsOpenShift = true
				})

				It("Should delete OpenShift-specific catalog resources when disabled", func() {
					By("First enabling catalog to create resources")
					reconciler.EnableModelCatalog = true
					result, err := reconciler.createOrUpdateCatalogConfig(ctx, params, modelRegistry)
					Expect(err).To(Not(HaveOccurred()))
					Expect(result).To(Not(Equal(ResourceUnchanged)))

					By("Verifying OpenShift resources exist")
					route := &routev1.Route{}
					err = k8sClient.Get(ctx, types.NamespacedName{
						Name:      fmt.Sprintf("%s-catalog-https", registryName),
						Namespace: registryName,
					}, route)
					Expect(err).To(Not(HaveOccurred()))

					networkPolicy := &networkingv1.NetworkPolicy{}
					err = k8sClient.Get(ctx, types.NamespacedName{
						Name:      fmt.Sprintf("%s-catalog-https-route", registryName),
						Namespace: registryName,
					}, networkPolicy)
					Expect(err).To(Not(HaveOccurred()))

					By("Disabling catalog and calling the method again")
					reconciler.EnableModelCatalog = false
					result, err = reconciler.createOrUpdateCatalogConfig(ctx, params, modelRegistry)
					Expect(err).To(Not(HaveOccurred()))

					By("Verifying OpenShift resources are deleted")
					Eventually(func() bool {
						err = k8sClient.Get(ctx, types.NamespacedName{
							Name:      fmt.Sprintf("%s-catalog-https", registryName),
							Namespace: registryName,
						}, route)
						return errors.IsNotFound(err)
					}, 10*time.Second, 1*time.Second).Should(BeTrue())

					Eventually(func() bool {
						err = k8sClient.Get(ctx, types.NamespacedName{
							Name:      fmt.Sprintf("%s-catalog-https-route", registryName),
							Namespace: registryName,
						}, networkPolicy)
						return errors.IsNotFound(err)
					}, 10*time.Second, 1*time.Second).Should(BeTrue())
				})
			})
		})

		Context("deleteCatalogResources method", func() {
			BeforeEach(func() {
				reconciler.EnableModelCatalog = true
			})

			It("Should successfully delete all catalog resources", func() {
				By("First creating catalog resources")
				result, err := reconciler.createOrUpdateCatalogConfig(ctx, params, modelRegistry)
				Expect(err).To(Not(HaveOccurred()))
				Expect(result).To(Not(Equal(ResourceUnchanged)))

				By("Verifying resources exist")
				configMap := &corev1.ConfigMap{}
				err = k8sClient.Get(ctx, types.NamespacedName{
					Name:      "model-catalog-sources",
					Namespace: registryName,
				}, configMap)
				Expect(err).To(Not(HaveOccurred()))

				deployment := &appsv1.Deployment{}
				err = k8sClient.Get(ctx, types.NamespacedName{
					Name:      fmt.Sprintf("%s-catalog", registryName),
					Namespace: registryName,
				}, deployment)
				Expect(err).To(Not(HaveOccurred()))

				service := &corev1.Service{}
				err = k8sClient.Get(ctx, types.NamespacedName{
					Name:      fmt.Sprintf("%s-catalog", registryName),
					Namespace: registryName,
				}, service)
				Expect(err).To(Not(HaveOccurred()))

				By("Deleting catalog resources")
				result, err = reconciler.deleteCatalogResources(ctx, params)
				Expect(err).To(Not(HaveOccurred()))

				By("Verifying resources are deleted")
				Eventually(func() bool {
					err = k8sClient.Get(ctx, types.NamespacedName{
						Name:      fmt.Sprintf("%s-catalog", registryName),
						Namespace: registryName,
					}, deployment)
					return errors.IsNotFound(err)
				}, 10*time.Second, 1*time.Second).Should(BeTrue())

				Eventually(func() bool {
					err = k8sClient.Get(ctx, types.NamespacedName{
						Name:      fmt.Sprintf("%s-catalog", registryName),
						Namespace: registryName,
					}, service)
					return errors.IsNotFound(err)
				}, 10*time.Second, 1*time.Second).Should(BeTrue())
			})

			It("Should handle deletion when resources don't exist", func() {
				By("Attempting to delete non-existent resources")
				result, err := reconciler.deleteCatalogResources(ctx, params)
				Expect(err).To(Not(HaveOccurred()))
				Expect(result).To(Equal(ResourceUnchanged))
			})

			Context("On OpenShift", func() {
				BeforeEach(func() {
					reconciler.IsOpenShift = true
				})

				It("Should delete OpenShift-specific catalog resources", func() {
					By("First creating catalog resources including OpenShift resources")
					result, err := reconciler.createOrUpdateCatalogConfig(ctx, params, modelRegistry)
					Expect(err).To(Not(HaveOccurred()))
					Expect(result).To(Not(Equal(ResourceUnchanged)))

					By("Verifying OpenShift resources exist")
					route := &routev1.Route{}
					err = k8sClient.Get(ctx, types.NamespacedName{
						Name:      fmt.Sprintf("%s-catalog-https", registryName),
						Namespace: registryName,
					}, route)
					Expect(err).To(Not(HaveOccurred()))

					networkPolicy := &networkingv1.NetworkPolicy{}
					err = k8sClient.Get(ctx, types.NamespacedName{
						Name:      fmt.Sprintf("%s-catalog-https-route", registryName),
						Namespace: registryName,
					}, networkPolicy)
					Expect(err).To(Not(HaveOccurred()))

					By("Deleting catalog resources")
					result, err = reconciler.deleteCatalogResources(ctx, params)
					Expect(err).To(Not(HaveOccurred()))

					By("Verifying OpenShift resources are deleted")
					Eventually(func() bool {
						err = k8sClient.Get(ctx, types.NamespacedName{
							Name:      fmt.Sprintf("%s-catalog-https", registryName),
							Namespace: registryName,
						}, route)
						return errors.IsNotFound(err)
					}, 10*time.Second, 1*time.Second).Should(BeTrue())

					Eventually(func() bool {
						err = k8sClient.Get(ctx, types.NamespacedName{
							Name:      fmt.Sprintf("%s-catalog-https-route", registryName),
							Namespace: registryName,
						}, networkPolicy)
						return errors.IsNotFound(err)
					}, 10*time.Second, 1*time.Second).Should(BeTrue())
				})
			})
		})

		AfterEach(func() {
			By("Cleaning up catalog resources")
			if reconciler.EnableModelCatalog {
				reconciler.EnableModelCatalog = false
				_, _ = reconciler.createOrUpdateCatalogConfig(ctx, params, modelRegistry)
			}

			By("Deleting the Namespace")
			_ = k8sClient.Delete(ctx, namespace)

			By("Removing the Image ENV VARs")
			_ = os.Unsetenv(config.GrpcImage)
			_ = os.Unsetenv(config.RestImage)
		})
	})
})
