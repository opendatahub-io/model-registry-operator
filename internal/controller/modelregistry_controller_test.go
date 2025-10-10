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
	"os"
	"strings"
	"text/template"
	"time"

	"github.com/opendatahub-io/model-registry-operator/api/v1beta1"

	networkingv1 "k8s.io/api/networking/v1"

	"github.com/opendatahub-io/model-registry-operator/internal/controller/config"
	routev1 "github.com/openshift/api/route/v1"
	userv1 "github.com/openshift/api/user/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const DescriptionPrefix = "Test Registry "

var _ = Describe("ModelRegistry controller", func() {

	Context("ModelRegistry controller test", func() {

		ctx := context.Background()

		// load templates
		template, err := config.ParseTemplates()
		Expect(err).To(Not(HaveOccurred()))

		Describe("model registries", func() {

			var namespace *corev1.Namespace
			var typeNamespaceName types.NamespacedName
			var modelRegistry *v1beta1.ModelRegistry
			var registryName string

			BeforeEach(func() {
				By("Setting the Image ENV VARs which stores the Server images")
				err = os.Setenv(config.RestImage, config.DefaultRestImage)
				Expect(err).To(Not(HaveOccurred()))
				err = os.Setenv(config.PostgresImage, config.DefaultPostgresImage)
				Expect(err).To(Not(HaveOccurred()))
				err = os.Setenv(config.KubeRBACProxyImage, config.DefaultKubeRBACProxyImage)
				Expect(err).To(Not(HaveOccurred()))
			})

			specInit := func() {
				namespace = &corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name:      registryName,
						Namespace: registryName,
					},
				}
				typeNamespaceName = types.NamespacedName{Name: registryName, Namespace: registryName}
				modelRegistry = &v1beta1.ModelRegistry{}

				By("Creating the Namespace to perform the tests")
				err := k8sClient.Create(ctx, namespace)
				Expect(err).To(Not(HaveOccurred()))

				By("creating the custom resource for the Kind ModelRegistry")
				err = k8sClient.Get(ctx, typeNamespaceName, modelRegistry)
				Expect(err != nil && errors.IsNotFound(err)).To(BeTrue())

				// Let's mock our custom resource in the same way that we would
				// apply on the cluster the manifest under config/samples
				var restPort int32 = 8080
				modelRegistry = &v1beta1.ModelRegistry{
					ObjectMeta: metav1.ObjectMeta{
						Name:        registryName,
						Namespace:   namespace.Name,
						Annotations: map[string]string{DisplayNameAnnotation: registryName, DescriptionAnnotation: DescriptionPrefix + registryName},
					},
					Spec: v1beta1.ModelRegistrySpec{
						Rest: v1beta1.RestSpec{
							Port: &restPort,
						},
					},
				}
			}

			It("When using PostgreSQL database", func() {
				registryName = "model-registry-postgres"
				specInit()

				var postgresPort int32 = 5432
				modelRegistry.Spec.MySQL = nil
				modelRegistry.Spec.Postgres = &v1beta1.PostgresConfig{
					Host:     "model-registry-db",
					Port:     &postgresPort,
					Database: "model-registry",
					Username: "modelregistryuser",
					PasswordSecret: &v1beta1.SecretKeyValue{
						Name: "model-registry-db",
						Key:  "database-password",
					},
				}

				err = k8sClient.Create(ctx, modelRegistry)
				Expect(err).To(Not(HaveOccurred()))

				modelRegistryReconciler := initModelRegistryReconciler(template)

				Eventually(validateRegistryBase(ctx, typeNamespaceName, modelRegistry, modelRegistryReconciler),
					time.Minute, time.Second).Should(Succeed())
			})

			It("When using KubeRBACProxy configuration", func() {
				registryName = "model-registry-kube-rbac-proxy"
				specInit()

				var postgresPort int32 = 5432
				var httpsPort int32 = 8443
				var routePort int32 = 443

				modelRegistry.Spec.MySQL = nil
				modelRegistry.Spec.Postgres = &v1beta1.PostgresConfig{
					Host:     "model-registry-db",
					Port:     &postgresPort,
					Database: "model-registry",
					Username: "modelregistryuser",
					PasswordSecret: &v1beta1.SecretKeyValue{
						Name: "model-registry-db",
						Key:  "database-password",
					},
				}
				modelRegistry.Spec.KubeRBACProxy = &v1beta1.KubeRBACProxyConfig{
					Port:         &httpsPort,
					RoutePort:    &routePort,
					Domain:       "example.com",
					ServiceRoute: config.RouteEnabled,
					Image:        config.DefaultKubeRBACProxyImage,
				}

				err = k8sClient.Create(ctx, modelRegistry)
				Expect(err).To(Not(HaveOccurred()))

				modelRegistryReconciler := initModelRegistryReconciler(template)

				Eventually(func() error {
					_, err := modelRegistryReconciler.Reconcile(ctx, reconcile.Request{
						NamespacedName: typeNamespaceName,
					})
					return err
				}, time.Minute, time.Second).Should(Succeed())

				By("Checking if the Deployment contains kube-rbac-proxy container")
				deployment := &appsv1.Deployment{}
				Eventually(func() error {
					return k8sClient.Get(ctx, typeNamespaceName, deployment)
				}, time.Minute, time.Second).Should(Succeed())

				// Check that kube-rbac-proxy container is present
				var kubeRBACProxyContainer *corev1.Container
				for _, container := range deployment.Spec.Template.Spec.Containers {
					if container.Name == "kube-rbac-proxy" {
						kubeRBACProxyContainer = &container
						break
					}
				}
				Expect(kubeRBACProxyContainer).ToNot(BeNil(), "kube-rbac-proxy container should be present")
				Expect(kubeRBACProxyContainer.Image).To(ContainSubstring("kube-rbac-proxy"))

				// Verify kube-rbac-proxy specific configuration
				Expect(kubeRBACProxyContainer.Args).To(ContainElement("--secure-listen-address=0.0.0.0:8443"))
				Expect(kubeRBACProxyContainer.Args).To(ContainElement("--upstream=http://127.0.0.1:8080/"))
				Expect(kubeRBACProxyContainer.Args).To(ContainElement("--config-file=/etc/kube-rbac-proxy/config-file.yaml"))

				// Check that oauth-proxy container is NOT present
				for _, container := range deployment.Spec.Template.Spec.Containers {
					Expect(container.Name).ToNot(Equal("oauth-proxy"))
				}

				By("Checking if the kube-rbac-proxy ConfigMap was created")
				configMap := &corev1.ConfigMap{}
				Eventually(func() error {
					return k8sClient.Get(ctx, types.NamespacedName{
						Name:      registryName + "-kube-rbac-proxy-config",
						Namespace: registryName,
					}, configMap)
				}, time.Minute, time.Second).Should(Succeed())

				Expect(configMap.Data["config-file.yaml"]).To(ContainSubstring("authorization:"))
				Expect(configMap.Data["config-file.yaml"]).To(ContainSubstring("resourceAttributes:"))

				By("Checking if the kube-rbac-proxy ClusterRoleBinding was created")
				clusterRoleBinding := &rbacv1.ClusterRoleBinding{}
				Eventually(func() error {
					return k8sClient.Get(ctx, types.NamespacedName{
						Name: registryName + "-auth-delegator",
					}, clusterRoleBinding)
				}, time.Minute, time.Second).Should(Succeed())

				Expect(clusterRoleBinding.RoleRef.Name).To(Equal("system:auth-delegator"))
			})

			It("When migrating from OAuth proxy to kube-rbac-proxy", func() {
				registryName = "model-registry-oauth-migration"
				specInit()

				var postgresPort int32 = 5432
				var httpsPort int32 = 8443
				var routePort int32 = 443

				modelRegistry.Spec.MySQL = nil
				modelRegistry.Spec.Postgres = &v1beta1.PostgresConfig{
					Host:     "model-registry-db",
					Port:     &postgresPort,
					Database: "model-registry",
					Username: "modelregistryuser",
					PasswordSecret: &v1beta1.SecretKeyValue{
						Name: "model-registry-db",
						Key:  "database-password",
					},
				}

				// Manually perform the migration (since webhook doesn't run in test environment)
				modelRegistry.Spec.OAuthProxy = &v1beta1.OAuthProxyConfig{
					Port:         &httpsPort,
					RoutePort:    &routePort,
					Domain:       "example.com",
					ServiceRoute: config.RouteEnabled,
					Image:        config.DefaultOAuthProxyImage,
				}

				// Simulate the webhook migration by calling Default() manually
				modelRegistry.Default()

				err = k8sClient.Create(ctx, modelRegistry)
				Expect(err).To(Not(HaveOccurred()))

				// Verify that the migration happened during Default() call
				Expect(modelRegistry.Spec.OAuthProxy).To(BeNil(), "OAuthProxy should be nil after migration")
				Expect(modelRegistry.Spec.KubeRBACProxy).ToNot(BeNil(), "KubeRBACProxy should be set after migration")

				modelRegistryReconciler := initModelRegistryReconciler(template)

				Eventually(func() error {
					_, err := modelRegistryReconciler.Reconcile(ctx, reconcile.Request{
						NamespacedName: typeNamespaceName,
					})
					return err
				}, time.Minute, time.Second).Should(Succeed())

				By("Checking if the Deployment contains kube-rbac-proxy instead of oauth-proxy")
				deployment := &appsv1.Deployment{}
				Eventually(func() error {
					return k8sClient.Get(ctx, typeNamespaceName, deployment)
				}, time.Minute, time.Second).Should(Succeed())

				// Check that kube-rbac-proxy container is present
				var kubeRBACProxyContainer *corev1.Container
				for _, container := range deployment.Spec.Template.Spec.Containers {
					if container.Name == "kube-rbac-proxy" {
						kubeRBACProxyContainer = &container
						break
					}
				}
				Expect(kubeRBACProxyContainer).ToNot(BeNil(), "kube-rbac-proxy container should be present after migration")

				// Check that oauth-proxy container is NOT present
				for _, container := range deployment.Spec.Template.Spec.Containers {
					Expect(container.Name).ToNot(Equal("oauth-proxy"))
				}

				// Verify kube-rbac-proxy specific configuration
				Expect(kubeRBACProxyContainer.Args).To(ContainElement("--secure-listen-address=0.0.0.0:8443"))
				Expect(kubeRBACProxyContainer.Args).To(ContainElement(MatchRegexp(`--upstream=http://127\.0\.0\.1:\d+/`)))
				Expect(kubeRBACProxyContainer.Args).To(ContainElement("--config-file=/etc/kube-rbac-proxy/config-file.yaml"))

				By("Checking if the migrated service uses kube-rbac-proxy port")
				service := &corev1.Service{}
				Eventually(func() error {
					return k8sClient.Get(ctx, typeNamespaceName, service)
				}, time.Minute, time.Second).Should(Succeed())

				Expect(service.Spec.Ports).To(HaveLen(1))
				Expect(service.Spec.Ports[0].Port).To(Equal(httpsPort))
				Expect(service.Spec.Ports[0].Name).To(Equal("https-api"))
			})

			It("When using MySQL database", func() {
				registryName = "model-registry-mysql"
				specInit()

				var mySQLPort int32 = 3306
				modelRegistry.Spec.Postgres = nil
				modelRegistry.Spec.MySQL = &v1beta1.MySQLConfig{
					Host:     "model-registry-db",
					Port:     &mySQLPort,
					Database: "model_registry",
					Username: "modelregistryuser",
					PasswordSecret: &v1beta1.SecretKeyValue{
						Name: "model-registry-db",
						Key:  "database-password",
					},
				}

				err = k8sClient.Create(ctx, modelRegistry)
				Expect(err).To(Not(HaveOccurred()))

				modelRegistryReconciler := initModelRegistryReconciler(template)

				Eventually(validateRegistryBase(ctx, typeNamespaceName, modelRegistry, modelRegistryReconciler),
					time.Minute, time.Second).Should(Succeed())
			})

			It("When using OpenShift - serviceRoute enabled", func() {
				registryName = "model-registry-openshift-with-serviceroute"
				specInit()

				var mySQLPort int32 = 3306
				modelRegistry.Spec.Postgres = nil
				modelRegistry.Spec.MySQL = &v1beta1.MySQLConfig{
					Host:     "model-registry-db",
					Port:     &mySQLPort,
					Database: "model_registry",
					Username: "modelregistryuser",
					PasswordSecret: &v1beta1.SecretKeyValue{
						Name: "model-registry-db",
						Key:  "database-password",
					},
				}
				modelRegistry.Spec.Rest.ServiceRoute = config.RouteEnabled

				err = k8sClient.Create(ctx, modelRegistry)
				Expect(err).To(Not(HaveOccurred()))

				modelRegistryReconciler := initModelRegistryReconciler(template)

				Eventually(validateRegistryOpenshift(ctx, typeNamespaceName, modelRegistry, modelRegistryReconciler),
					time.Minute, time.Second).Should(Succeed())

				By("Checking if the Openshift Route was successfully created in the reconciliation")
				Eventually(func() error {
					found := &routev1.Route{}

					return k8sClient.Get(ctx, types.NamespacedName{Name: fmt.Sprintf("%s-http", modelRegistry.Name), Namespace: modelRegistry.Namespace}, found)
				}, 5*time.Second, time.Second).Should(Succeed())
			})

			It("When using OpenShift - serviceRoute disabled", func() {
				registryName = "model-registry-openshift-without-serviceroute"
				specInit()

				var mySQLPort int32 = 3306
				modelRegistry.Spec.Postgres = nil
				modelRegistry.Spec.MySQL = &v1beta1.MySQLConfig{
					Host:     "model-registry-db",
					Port:     &mySQLPort,
					Database: "model_registry",
					Username: "modelregistryuser",
					PasswordSecret: &v1beta1.SecretKeyValue{
						Name: "model-registry-db",
						Key:  "database-password",
					},
				}

				err = k8sClient.Create(ctx, modelRegistry)
				Expect(err).To(Not(HaveOccurred()))

				modelRegistryReconciler := initModelRegistryReconciler(template)

				Eventually(validateRegistryOpenshift(ctx, typeNamespaceName, modelRegistry, modelRegistryReconciler),
					time.Minute, time.Second).Should(Succeed())

				By("Checking if the Openshift Route was not created in the reconciliation")
				found := &routev1.Route{}

				err := k8sClient.Get(ctx, types.NamespacedName{Name: fmt.Sprintf("%s-http", modelRegistry.Name), Namespace: modelRegistry.Namespace}, found)
				Expect(err).To(HaveOccurred())
			})
			// KubeRBACProxy config tests
			var kubeRBACProxyConfig *v1beta1.KubeRBACProxyConfig
			kubeRBACProxyValidate := func() {
				specInit()

				var mySQLPort int32 = 3306
				modelRegistry.Spec.Postgres = nil
				modelRegistry.Spec.MySQL = &v1beta1.MySQLConfig{
					Host:     "model-registry-db",
					Port:     &mySQLPort,
					Database: "model_registry",
					Username: "modelregistryuser",
					PasswordSecret: &v1beta1.SecretKeyValue{
						Name: "model-registry-db",
						Key:  "database-password",
					},
				}

				// Set the KubeRBACProxy config
				modelRegistry.Spec.KubeRBACProxy = kubeRBACProxyConfig

				err = k8sClient.Create(ctx, modelRegistry)
				Expect(err).To(Not(HaveOccurred()))

				config.SetDefaultDomain("example.com", k8sClient, true)
				modelRegistryReconciler := initModelRegistryReconciler(template)
				modelRegistryReconciler.IsOpenShift = true

				Eventually(validateRegistryKubeRBACProxy(ctx, typeNamespaceName, modelRegistry, modelRegistryReconciler),
					time.Minute, time.Second).Should(Succeed())
			}

			It("When using default KubeRBACProxy config on openshift", func() {
				registryName = "model-registry-kube-rbac-proxy-default"
				kubeRBACProxyConfig = &v1beta1.KubeRBACProxyConfig{}
				kubeRBACProxyValidate()
			})

			It("When using KubeRBACProxy with custom certs on openshift", func() {
				registryName = "model-registry-kube-rbac-proxy-certs"
				kubeRBACProxyConfig = &v1beta1.KubeRBACProxyConfig{
					TLSCertificateSecret: &v1beta1.SecretKeyValue{
						Name: "test-cert-secret",
						Key:  "test-cert-key",
					},
					TLSKeySecret: &v1beta1.SecretKeyValue{
						Name: "test-key-secret",
						Key:  "test-key-key",
					},
				}
				kubeRBACProxyValidate()
			})

			It("When using KubeRBACProxy without route config on openshift", func() {
				registryName = "model-registry-kube-rbac-proxy-noroute"
				kubeRBACProxyConfig = &v1beta1.KubeRBACProxyConfig{
					ServiceRoute: config.RouteDisabled,
				}
				kubeRBACProxyValidate()
			})

			It("When using KubeRBACProxy with custom image on openshift", func() {
				registryName = "model-registry-kube-rbac-proxy-image"
				kubeRBACProxyConfig = &v1beta1.KubeRBACProxyConfig{
					Image: "test-proxy-image",
				}
				kubeRBACProxyValidate()
			})

			It("When using auto-provisioned PostgreSQL database", func() {
				registryName = "model-registry-auto-postgres"
				specInit()

				trueValue := true
				modelRegistry.Spec.Postgres = &v1beta1.PostgresConfig{
					GenerateDeployment: &trueValue,
				}

				err = k8sClient.Create(ctx, modelRegistry)
				Expect(err).To(Not(HaveOccurred()))

				modelRegistryReconciler := initModelRegistryReconciler(template)

				Eventually(validateRegistryBase(ctx, typeNamespaceName, modelRegistry, modelRegistryReconciler),
					time.Minute, time.Second).Should(Succeed())

				By("Checking if the Postgres Secret was successfully created in the reconciliation")
				Eventually(func() error {
					found := &corev1.Secret{}
					return k8sClient.Get(ctx, types.NamespacedName{Name: registryName + "-postgres-credentials", Namespace: namespace.Name}, found)
				}, time.Minute, time.Second).Should(Succeed())

				By("Checking if the Postgres Deployment was successfully created in the reconciliation")
				Eventually(func() error {
					found := &appsv1.Deployment{}
					return k8sClient.Get(ctx, types.NamespacedName{Name: registryName + "-postgres", Namespace: namespace.Name}, found)
				}, time.Minute, time.Second).Should(Succeed())

				By("Checking if the Postgres Service was successfully created in the reconciliation")
				Eventually(func() error {
					found := &corev1.Service{}
					return k8sClient.Get(ctx, types.NamespacedName{Name: registryName + "-postgres", Namespace: namespace.Name}, found)
				}, time.Minute, time.Second).Should(Succeed())

				By("Checking if the Postgres PVC was successfully created in the reconciliation")
				Eventually(func() error {
					found := &corev1.PersistentVolumeClaim{}
					return k8sClient.Get(ctx, types.NamespacedName{Name: registryName + "-postgres-storage", Namespace: namespace.Name}, found)
				}, time.Minute, time.Second).Should(Succeed())
			})

			Context("Legacy catalog resource cleanup", func() {
				It("Should clean up old catalog resources during ModelRegistry reconciliation", func() {
					registryName = "model-registry-migration-test"
					specInit()

					// Create a ModelRegistry first
					modelRegistry.Name = registryName
					modelRegistry.Namespace = registryName
					var mySQLPort int32 = 3306
					modelRegistry.Spec.Postgres = nil
					modelRegistry.Spec.MySQL = &v1beta1.MySQLConfig{
						Host:     "model-registry-db",
						Port:     &mySQLPort,
						Database: "model_registry",
						Username: "modelregistryuser",
						PasswordSecret: &v1beta1.SecretKeyValue{
							Name: "model-registry-db",
							Key:  "database-password",
						},
					}

					err = k8sClient.Create(ctx, modelRegistry)
					Expect(err).To(Not(HaveOccurred()))

					By("Creating legacy catalog resources that would exist from older versions")

					// Create legacy catalog Deployment
					legacyDeployment := &appsv1.Deployment{
						ObjectMeta: metav1.ObjectMeta{
							Name:      fmt.Sprintf("%s-catalog", modelRegistry.Name),
							Namespace: modelRegistry.Namespace,
							Labels: map[string]string{
								"component":                    "model-registry", // old component label
								"app":                          modelRegistry.Name,
								"app.kubernetes.io/created-by": "model-registry-operator",
							},
						},
						Spec: appsv1.DeploymentSpec{
							Selector: &metav1.LabelSelector{
								MatchLabels: map[string]string{"app": modelRegistry.Name + "-catalog"},
							},
							Template: corev1.PodTemplateSpec{
								ObjectMeta: metav1.ObjectMeta{
									Labels: map[string]string{"app": modelRegistry.Name + "-catalog"},
								},
								Spec: corev1.PodSpec{
									Containers: []corev1.Container{{
										Name:  "catalog",
										Image: "registry.redhat.io/ubi8/ubi:latest",
									}},
								},
							},
						},
					}
					err = k8sClient.Create(ctx, legacyDeployment)
					Expect(err).To(Not(HaveOccurred()))

					// Create legacy catalog Service
					legacyService := &corev1.Service{
						ObjectMeta: metav1.ObjectMeta{
							Name:      fmt.Sprintf("%s-catalog", modelRegistry.Name),
							Namespace: modelRegistry.Namespace,
							Labels: map[string]string{
								"component":                    "model-registry", // old component label
								"app":                          modelRegistry.Name,
								"app.kubernetes.io/created-by": "model-registry-operator",
							},
						},
						Spec: corev1.ServiceSpec{
							Selector: map[string]string{"app": modelRegistry.Name + "-catalog"},
							Ports: []corev1.ServicePort{{
								Port: 8080,
								Name: "http",
							}},
						},
					}
					err = k8sClient.Create(ctx, legacyService)
					Expect(err).To(Not(HaveOccurred()))

					// Create legacy OpenShift resources
					legacyRoute := &routev1.Route{
						ObjectMeta: metav1.ObjectMeta{
							Name:      fmt.Sprintf("%s-catalog-https", modelRegistry.Name),
							Namespace: modelRegistry.Namespace,
							Labels: map[string]string{
								"component":                    "model-registry", // old component label
								"app":                          modelRegistry.Name,
								"app.kubernetes.io/created-by": "model-registry-operator",
							},
						},
						Spec: routev1.RouteSpec{
							To: routev1.RouteTargetReference{
								Kind: "Service",
								Name: fmt.Sprintf("%s-catalog", modelRegistry.Name),
							},
						},
					}
					err = k8sClient.Create(ctx, legacyRoute)
					Expect(err).To(Not(HaveOccurred()))

					legacyNetworkPolicy := &networkingv1.NetworkPolicy{
						ObjectMeta: metav1.ObjectMeta{
							Name:      fmt.Sprintf("%s-catalog-https-route", modelRegistry.Name),
							Namespace: modelRegistry.Namespace,
							Labels: map[string]string{
								"component":                    "model-registry", // old component label
								"app":                          modelRegistry.Name,
								"app.kubernetes.io/created-by": "model-registry-operator",
							},
						},
					}
					err = k8sClient.Create(ctx, legacyNetworkPolicy)
					Expect(err).To(Not(HaveOccurred()))

					By("Verifying legacy resources exist before reconciliation")
					Eventually(func() error {
						return k8sClient.Get(ctx, types.NamespacedName{
							Name:      fmt.Sprintf("%s-catalog", modelRegistry.Name),
							Namespace: modelRegistry.Namespace,
						}, &appsv1.Deployment{})
					}, 5*time.Second, time.Second).Should(Succeed())

					By("Performing reconciliation")
					modelRegistryReconciler := initModelRegistryReconciler(template)
					modelRegistryReconciler.IsOpenShift = true

					Eventually(func() error {
						_, err := modelRegistryReconciler.Reconcile(ctx, reconcile.Request{
							NamespacedName: types.NamespacedName{
								Name:      modelRegistry.Name,
								Namespace: modelRegistry.Namespace,
							},
						})
						return err
					}, time.Minute, time.Second).Should(Succeed())

					By("Verifying legacy catalog resources are cleaned up")
					Eventually(func() error {
						return k8sClient.Get(ctx, types.NamespacedName{
							Name:      fmt.Sprintf("%s-catalog", modelRegistry.Name),
							Namespace: modelRegistry.Namespace,
						}, &appsv1.Deployment{})
					}, 10*time.Second, time.Second).ShouldNot(Succeed())

					Eventually(func() error {
						return k8sClient.Get(ctx, types.NamespacedName{
							Name:      fmt.Sprintf("%s-catalog", modelRegistry.Name),
							Namespace: modelRegistry.Namespace,
						}, &corev1.Service{})
					}, 10*time.Second, time.Second).ShouldNot(Succeed())

					Eventually(func() error {
						return k8sClient.Get(ctx, types.NamespacedName{
							Name:      fmt.Sprintf("%s-catalog-https", modelRegistry.Name),
							Namespace: modelRegistry.Namespace,
						}, &routev1.Route{})
					}, 10*time.Second, time.Second).ShouldNot(Succeed())

					Eventually(func() error {
						return k8sClient.Get(ctx, types.NamespacedName{
							Name:      fmt.Sprintf("%s-catalog-https-route", modelRegistry.Name),
							Namespace: modelRegistry.Namespace,
						}, &networkingv1.NetworkPolicy{})
					}, 10*time.Second, time.Second).ShouldNot(Succeed())
				})
			})

			AfterEach(func() {
				By("removing the custom resource for the Kind ModelRegistry")
				found := &v1beta1.ModelRegistry{}
				err := k8sClient.Get(ctx, typeNamespaceName, found)
				Expect(err).To(Not(HaveOccurred()))

				Eventually(func() error {
					return k8sClient.Delete(context.TODO(), found)
				}, 2*time.Minute, time.Second).Should(Succeed())

				By("Cleaning up istio services")
				svc := corev1.Service{}
				svc.Name = "istio"
				svc.Namespace = typeNamespaceName.Namespace

				_ = k8sClient.Delete(ctx, &svc)

				// TODO(user): Attention if you improve this code by adding other context test you MUST
				// be aware of the current delete namespace limitations.
				// More info: https://book.kubebuilder.io/reference/envtest.html#testing-considerations
				By("Deleting the Namespace to perform the tests")
				_ = k8sClient.Delete(ctx, namespace)

				By("Removing the Image ENV VARs which stores the Server images")
				_ = os.Unsetenv(config.RestImage)
				_ = os.Unsetenv(config.OAuthProxyImage)
			})
		})

	})
})

func initModelRegistryReconciler(template *template.Template) *ModelRegistryReconciler {
	scheme := k8sClient.Scheme()

	modelRegistryReconciler := &ModelRegistryReconciler{
		Client:   k8sClient,
		Scheme:   scheme,
		Recorder: &record.FakeRecorder{},
		Log:      ctrl.Log.WithName("controller"),
		Template: template,
	}

	return modelRegistryReconciler
}

func validateRegistryBase(ctx context.Context, typeNamespaceName types.NamespacedName, modelRegistry *v1beta1.ModelRegistry, modelRegistryReconciler *ModelRegistryReconciler) func() error {
	return func() error {
		By("Checking if the custom resource was successfully created")
		Eventually(func() error {
			found := &v1beta1.ModelRegistry{}
			return k8sClient.Get(ctx, typeNamespaceName, found)
		}, time.Minute, time.Second).Should(Succeed())

		By("Mocking the Pod creation to perform the tests")
		mrPod := &corev1.Pod{}
		mrPod.Name = typeNamespaceName.Name
		mrPod.Namespace = typeNamespaceName.Namespace
		mrPod.Labels = map[string]string{
			"app":                    typeNamespaceName.Name,
			"component":              "model-registry",
			"app.kubernetes.io/name": typeNamespaceName.Name,
		}
		mrPod.Spec.Containers = []corev1.Container{
			{
				Name:  "model-registry-rest",
				Image: config.DefaultRestImage,
			},
		}

		// mock oauth proxy container
		if modelRegistry.Spec.KubeRBACProxy != nil {
			image := modelRegistry.Spec.KubeRBACProxy.Image
			if len(image) == 0 {
				image = config.DefaultKubeRBACProxyImage
			}
			mrPod.Spec.Containers = append(mrPod.Spec.Containers, corev1.Container{
				Name:  "kube-rbac-proxy",
				Image: image,
			})
		}

		err := k8sClient.Create(ctx, mrPod)
		Expect(err).To(Not(HaveOccurred()))

		By("Reconciling the custom resource created")
		Eventually(func() error {
			result, err := modelRegistryReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespaceName,
			})

			if err != nil {
				return err
			}
			if !result.IsZero() {
				// set DeploymentAvailable condition in status to True to make reconcile succeed
				deployment := &appsv1.Deployment{}
				derr := k8sClient.Get(ctx, typeNamespaceName, deployment)
				if derr != nil {
					return derr
				}
				conditions := deployment.Status.Conditions
				if len(conditions) == 0 {
					deployment.Status.Conditions = append(conditions, appsv1.DeploymentCondition{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionTrue})
					derr = k8sClient.Status().Update(ctx, deployment)
					if derr != nil {
						return derr
					}
				}

				// Mock route ingress conditions if KubeRBACProxy is configured and routes exist
				if modelRegistry.Spec.KubeRBACProxy != nil && modelRegistry.Spec.KubeRBACProxy.ServiceRoute != config.RouteDisabled && modelRegistryReconciler.IsOpenShift {
					routes := &routev1.RouteList{}
					rerr := k8sClient.List(ctx, routes, client.InNamespace(typeNamespaceName.Namespace), client.MatchingLabels{
						"app":       typeNamespaceName.Name,
						"component": "model-registry",
					})
					if rerr == nil && len(routes.Items) > 0 {
						for i := range routes.Items {
							route := &routes.Items[i]
							// Only update if not already admitted
							if len(route.Status.Ingress) == 0 {
								route.Status.Ingress = []routev1.RouteIngress{
									{
										Conditions: []routev1.RouteIngressCondition{
											{
												Type:   routev1.RouteAdmitted,
												Status: corev1.ConditionTrue,
											},
										},
									},
								}
								_ = k8sClient.Status().Update(ctx, route)
							}
						}
					}
				}

				return fmt.Errorf("non-empty reconcile result")
			}
			// reconcile done!
			return nil
		}, time.Minute, time.Second).Should(Succeed())

		By("Checking if Deployment was successfully created in the reconciliation")
		Eventually(func() error {
			found := &appsv1.Deployment{}
			return k8sClient.Get(ctx, typeNamespaceName, found)
		}, time.Minute, time.Second).Should(Succeed())

		if modelRegistry.Spec.KubeRBACProxy != nil && modelRegistry.Spec.KubeRBACProxy.ServiceRoute != config.RouteDisabled && modelRegistryReconciler.IsOpenShift {
			By("Checking if the Route was successfully created in the reconciliation")
			routes := &routev1.RouteList{}
			err = k8sClient.List(ctx, routes, client.InNamespace(typeNamespaceName.Namespace), client.MatchingLabels{
				"app":       typeNamespaceName.Name,
				"component": "model-registry",
			})
			Expect(err).To(Not(HaveOccurred()))

			By("Mocking the conditions in the Route to perform the tests")
			if len(routes.Items) > 0 {
				for _, route := range routes.Items {
					ingresses := []routev1.RouteIngress{
						{
							Conditions: []routev1.RouteIngressCondition{
								{
									Type:   routev1.RouteAdmitted,
									Status: corev1.ConditionTrue,
								},
							},
						},
					}

					route.Status.Ingress = ingresses

					err = k8sClient.Status().Update(ctx, &route)
					Expect(err).To(Not(HaveOccurred()))
				}

				Eventually(func() error {
					_, err := modelRegistryReconciler.Reconcile(ctx, reconcile.Request{
						NamespacedName: typeNamespaceName,
					})

					return err
				}, time.Minute, time.Second).Should(Succeed())
			}
		}

		By("Checking the latest Status Condition added to the ModelRegistry instance")
		Eventually(func() error {
			err := k8sClient.Get(ctx, typeNamespaceName, modelRegistry)
			Expect(err).To(Not(HaveOccurred()))
			if modelRegistry.Spec.KubeRBACProxy != nil && modelRegistry.Spec.KubeRBACProxy.ServiceRoute != config.RouteDisabled {
				hosts := modelRegistry.Status.Hosts
				Expect(len(hosts)).To(Equal(4))
				name := modelRegistry.Name
				namespace := modelRegistry.Namespace
				domain := modelRegistry.Spec.KubeRBACProxy.Domain
				if domain == "" {
					domain = config.GetDefaultDomain()
				}
				Expect(hosts[0]).
					To(Equal(fmt.Sprintf("%s-rest.%s", name, domain)))
				Expect(hosts[1]).
					To(Equal(fmt.Sprintf("%s.%s.svc.cluster.local", name, namespace)))
				Expect(hosts[2]).
					To(Equal(fmt.Sprintf("%s.%s", name, namespace)))
				Expect(hosts[3]).
					To(Equal(name))
				Expect(modelRegistry.Status.HostsStr).To(Equal(strings.Join(hosts, ",")))
			}
			if meta.IsStatusConditionTrue(modelRegistry.Status.Conditions, ConditionTypeProgressing) {
				return fmt.Errorf("Condition %s is not false", ConditionTypeProgressing)
			}
			if !meta.IsStatusConditionTrue(modelRegistry.Status.Conditions, ConditionTypeAvailable) {
				return fmt.Errorf("Condition %s is not true: %+v", ConditionTypeAvailable, modelRegistry.Status)
			}
			// Check KubeRBACProxy condition if configured
			if modelRegistry.Spec.KubeRBACProxy != nil {
				if !meta.IsStatusConditionTrue(modelRegistry.Status.Conditions, ConditionTypeKubeRBACProxy) {
					return fmt.Errorf("Condition %s is not true: %+v", ConditionTypeKubeRBACProxy, modelRegistry.Status)
				}
			}
			return nil
		}, time.Minute, time.Second).Should(Succeed())

		By("Checking the display name and description were copied to the ModelRegistry service")
		Eventually(func() error {

			service := &corev1.Service{}
			err := k8sClient.Get(ctx, typeNamespaceName, service)
			Expect(err).To(Not(HaveOccurred()))

			name := service.Annotations[DisplayNameAnnotation]
			Expect(name).To(Equal(typeNamespaceName.Name))

			description := service.Annotations[DescriptionAnnotation]
			Expect(description).To(Equal(DescriptionPrefix + typeNamespaceName.Name))

			return nil
		}, time.Minute, time.Second).Should(Succeed())

		return nil
	}
}

func validateRegistryOpenshift(ctx context.Context, typeNamespaceName types.NamespacedName, modelRegistry *v1beta1.ModelRegistry, modelRegistryReconciler *ModelRegistryReconciler) func() error {
	return func() error {
		modelRegistryReconciler.IsOpenShift = true

		Eventually(validateRegistryBase(ctx, typeNamespaceName, modelRegistry, modelRegistryReconciler)).Should(Succeed())

		By("Checking if the Openshift Group was successfully created in the reconciliation")
		Eventually(func() error {
			found := &userv1.Group{}

			return k8sClient.Get(ctx, types.NamespacedName{Name: fmt.Sprintf("%s-users", modelRegistry.Name)}, found)
		}, 5*time.Second, time.Second).Should(Succeed())

		By("Checking if the Openshift RoleBinding was successfully created in the reconciliation")
		Eventually(func() error {
			found := &rbacv1.RoleBinding{}

			return k8sClient.Get(ctx, types.NamespacedName{Name: fmt.Sprintf("%s-users", modelRegistry.Name), Namespace: modelRegistry.Namespace}, found)
		}, 5*time.Second, time.Second).Should(Succeed())

		return nil
	}
}

func validateRegistryKubeRBACProxy(ctx context.Context, typeNamespaceName types.NamespacedName, modelRegistry *v1beta1.ModelRegistry, modelRegistryReconciler *ModelRegistryReconciler) func() error {
	return func() error {
		modelRegistryReconciler.IsOpenShift = true

		Eventually(validateRegistryOpenshift(ctx, typeNamespaceName, modelRegistry, modelRegistryReconciler)).Should(Succeed())

		By("Checking if the kube-rbac-proxy Deployment was configured correctly in the reconciliation (OAuth migrated to kube-rbac-proxy)")
		Eventually(func() error {
			found := &appsv1.Deployment{}

			err := k8sClient.Get(ctx, types.NamespacedName{Name: modelRegistry.Name, Namespace: modelRegistry.Namespace}, found)
			Expect(err).To(Not(HaveOccurred()))

			// Find kube-rbac-proxy container (after migration)
			var proxyContainer *corev1.Container
			for _, container := range found.Spec.Template.Spec.Containers {
				if container.Name == "kube-rbac-proxy" {
					proxyContainer = &container
					break
				}
			}
			Expect(proxyContainer).ToNot(BeNil(), "kube-rbac-proxy container should be present after migration")

			// Check that the migrated registry now has KubeRBACProxy config
			updated := &v1beta1.ModelRegistry{}
			err = k8sClient.Get(ctx, typeNamespaceName, updated)
			Expect(err).To(Not(HaveOccurred()))
			Expect(updated.Spec.KubeRBACProxy).ToNot(BeNil(), "KubeRBACProxy should be set after migration")
			Expect(updated.Spec.OAuthProxy).To(BeNil(), "OAuthProxy should be nil after migration")

			// check image - should match configured or default kube-rbac-proxy image
			if modelRegistry.Spec.KubeRBACProxy.Image != "" {
				// Custom image was configured in test
				Expect(proxyContainer.Image).To(Equal(modelRegistry.Spec.KubeRBACProxy.Image))
			} else {
				// Default image should contain kube-rbac-proxy
				Expect(proxyContainer.Image).To(ContainSubstring("kube-rbac-proxy"))
			}

			// check kube-rbac-proxy specific args (after migration)
			Expect(proxyContainer.Args).Should(ContainElements(
				"--secure-listen-address=0.0.0.0:8443",
				MatchRegexp(`--upstream=http://127\.0\.0\.1:\d+/`),
				"--config-file=/etc/kube-rbac-proxy/config-file.yaml"))

			// check kube-rbac-proxy volumes
			// Deployment always uses separate cert/key volumes (RuntimeDefaults sets TLSCertificateSecret)
			var defaultMode int32 = 0o600
			// Determine expected secret name - use test config if custom certs were set, otherwise default
			expectedCertSecretName := modelRegistry.Name + "-kube-rbac-proxy"
			if modelRegistry.Spec.KubeRBACProxy.TLSCertificateSecret != nil {
				expectedCertSecretName = modelRegistry.Spec.KubeRBACProxy.TLSCertificateSecret.Name
			}
			expectedKeySecretName := modelRegistry.Name + "-kube-rbac-proxy"
			if modelRegistry.Spec.KubeRBACProxy.TLSKeySecret != nil {
				expectedKeySecretName = modelRegistry.Spec.KubeRBACProxy.TLSKeySecret.Name
			}

			Expect(found.Spec.Template.Spec.Volumes).Should(ContainElements(
				corev1.Volume{
					Name: "kube-rbac-proxy-cert",
					VolumeSource: corev1.VolumeSource{
						Secret: &corev1.SecretVolumeSource{
							SecretName:  expectedCertSecretName,
							DefaultMode: &defaultMode,
						},
					}},
				corev1.Volume{
					Name: "kube-rbac-proxy-key",
					VolumeSource: corev1.VolumeSource{
						Secret: &corev1.SecretVolumeSource{
							SecretName:  expectedKeySecretName,
							DefaultMode: &defaultMode,
						},
					}}))
			var configMapDefaultMode int32 = 420
			Expect(found.Spec.Template.Spec.Volumes).Should(ContainElement(
				corev1.Volume{
					Name: "kube-rbac-proxy-config",
					VolumeSource: corev1.VolumeSource{
						ConfigMap: &corev1.ConfigMapVolumeSource{
							LocalObjectReference: corev1.LocalObjectReference{
								Name: modelRegistry.Name + "-kube-rbac-proxy-config",
							},
							DefaultMode: &configMapDefaultMode,
						},
					}}))

			return nil
		}, 5*time.Second, time.Second).Should(Succeed())

		// Mock route ingress conditions similar to validateRegistryBase
		By("Mocking the conditions in kube-rbac-proxy Routes to perform the tests")
		routes := &routev1.RouteList{}
		err := k8sClient.List(ctx, routes, client.InNamespace(typeNamespaceName.Namespace), client.MatchingLabels{
			"app":       typeNamespaceName.Name,
			"component": "model-registry",
		})
		Expect(err).To(Not(HaveOccurred()))

		if len(routes.Items) > 0 {
			for _, route := range routes.Items {
				ingresses := []routev1.RouteIngress{
					{
						Conditions: []routev1.RouteIngressCondition{
							{
								Type:   routev1.RouteAdmitted,
								Status: corev1.ConditionTrue,
							},
						},
					},
				}

				route.Status.Ingress = ingresses

				err = k8sClient.Status().Update(ctx, &route)
				Expect(err).To(Not(HaveOccurred()))
			}

			Eventually(func() error {
				_, err := modelRegistryReconciler.Reconcile(ctx, reconcile.Request{
					NamespacedName: typeNamespaceName,
				})

				return err
			}, time.Minute, time.Second).Should(Succeed())
		}

		// Mock route ingress conditions for KubeRBACProxy routes to prevent reconcile loops
		By("Mocking the conditions in kube-rbac-proxy Routes to prevent reconcile loops")
		httpsRoutes := &routev1.RouteList{}
		err = k8sClient.List(ctx, httpsRoutes, client.InNamespace(typeNamespaceName.Namespace), client.MatchingLabels{
			"app":       typeNamespaceName.Name,
			"component": "model-registry",
		})
		Expect(err).To(Not(HaveOccurred()))

		if len(httpsRoutes.Items) > 0 {
			for _, route := range httpsRoutes.Items {
				// Only update HTTPS routes (KubeRBACProxy routes)
				if strings.Contains(route.Name, "-https") {
					ingresses := []routev1.RouteIngress{
						{
							Conditions: []routev1.RouteIngressCondition{
								{
									Type:   routev1.RouteAdmitted,
									Status: corev1.ConditionTrue,
								},
							},
						},
					}

					route.Status.Ingress = ingresses

					err = k8sClient.Status().Update(ctx, &route)
					Expect(err).To(Not(HaveOccurred()))
				}
			}

			// Perform additional reconcile to process route conditions
			Eventually(func() error {
				_, err := modelRegistryReconciler.Reconcile(ctx, reconcile.Request{
					NamespacedName: typeNamespaceName,
				})

				return err
			}, time.Minute, time.Second).Should(Succeed())
		}

		By("Checking if the kube-rbac-proxy Route was successfully created in the reconciliation")
		matchRoute := Succeed()
		// Check the migrated KubeRBACProxy configuration for route setting
		updated := &v1beta1.ModelRegistry{}
		k8sClient.Get(ctx, typeNamespaceName, updated)
		if updated.Spec.KubeRBACProxy != nil && updated.Spec.KubeRBACProxy.ServiceRoute == config.RouteDisabled {
			matchRoute = Not(Succeed())
		}
		Eventually(func() error {
			found := &routev1.Route{}

			return k8sClient.Get(ctx, types.NamespacedName{
				Name: fmt.Sprintf("%s-https", modelRegistry.Name), Namespace: modelRegistry.Namespace}, found)
		}, 5*time.Second, time.Second).Should(matchRoute)

		By("Checking if the kube-rbac-proxy ClusterRoleBinding was successfully created in the reconciliation")
		Eventually(func() error {
			found := &rbacv1.ClusterRoleBinding{}

			return k8sClient.Get(ctx, types.NamespacedName{Name: fmt.Sprintf("%s-auth-delegator", modelRegistry.Name)}, found)
		}, 5*time.Second, time.Second).Should(Succeed())

		By("Checking if the kube-rbac-proxy NetworkPolicy was successfully created in the reconciliation")
		Eventually(func() error {
			found := &networkingv1.NetworkPolicy{}

			return k8sClient.Get(ctx, types.NamespacedName{Name: fmt.Sprintf("%s-https-route", modelRegistry.Name), Namespace: modelRegistry.Namespace}, found)
		}, 5*time.Second, time.Second).Should(matchRoute)

		return nil
	}
}
