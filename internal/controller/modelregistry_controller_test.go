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

	v1 "k8s.io/api/networking/v1"

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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
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
				err = os.Setenv(config.GrpcImage, config.DefaultGrpcImage)
				Expect(err).To(Not(HaveOccurred()))
				err = os.Setenv(config.RestImage, config.DefaultRestImage)
				Expect(err).To(Not(HaveOccurred()))
				err = os.Setenv(config.OAuthProxyImage, config.DefaultOAuthProxyImage)
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
				var gRPCPort int32 = 9090
				var restPort int32 = 8080
				modelRegistry = &v1beta1.ModelRegistry{
					ObjectMeta: metav1.ObjectMeta{
						Name:        registryName,
						Namespace:   namespace.Name,
						Annotations: map[string]string{DisplayNameAnnotation: registryName, DescriptionAnnotation: DescriptionPrefix + registryName},
					},
					Spec: v1beta1.ModelRegistrySpec{
						Grpc: v1beta1.GrpcSpec{
							Port: &gRPCPort,
						},
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
					Username: "mlmduser",
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

			It("When using MySQL database", func() {
				registryName = "model-registry-mysql"
				specInit()

				var mySQLPort int32 = 3306
				modelRegistry.Spec.Postgres = nil
				modelRegistry.Spec.MySQL = &v1beta1.MySQLConfig{
					Host:     "model-registry-db",
					Port:     &mySQLPort,
					Database: "model_registry",
					Username: "mlmduser",
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
					Username: "mlmduser",
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
					Username: "mlmduser",
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
			// Oauth Proxy config tests
			var oauthProxyConfig *v1beta1.OAuthProxyConfig
			oauthValidate := func() {
				specInit()

				var mySQLPort int32 = 3306
				modelRegistry.Spec.Postgres = nil
				modelRegistry.Spec.MySQL = &v1beta1.MySQLConfig{
					Host:     "model-registry-db",
					Port:     &mySQLPort,
					Database: "model_registry",
					Username: "mlmduser",
					PasswordSecret: &v1beta1.SecretKeyValue{
						Name: "model-registry-db",
						Key:  "database-password",
					},
				}
				modelRegistry.Spec.OAuthProxy = oauthProxyConfig

				err = k8sClient.Create(ctx, modelRegistry)
				Expect(err).To(Not(HaveOccurred()))

				config.SetDefaultDomain("example.com", k8sClient, true)
				modelRegistryReconciler := initModelRegistryReconciler(template)
				modelRegistryReconciler.IsOpenShift = true

				Eventually(validateRegistryOauthProxy(ctx, typeNamespaceName, modelRegistry, modelRegistryReconciler),
					time.Minute, time.Second).Should(Succeed())
			}

			It("When using default Oauth Proxy config on openshift", func() {
				registryName = "model-registry-oauth-proxy"
				oauthProxyConfig = &v1beta1.OAuthProxyConfig{}
				oauthValidate()
			})

			It("When using Oauth Proxy with custom certs on openshift", func() {
				registryName = "model-registry-oauth-certs"
				oauthProxyConfig = &v1beta1.OAuthProxyConfig{
					TLSCertificateSecret: &v1beta1.SecretKeyValue{
						Name: "test-cert-secret",
						Key:  "test-cert-key",
					},
					TLSKeySecret: &v1beta1.SecretKeyValue{
						Name: "test-key-secret",
						Key:  "test-key-key",
					},
				}
				oauthValidate()
			})

			It("When using Oauth Proxy without route config on openshift", func() {
				registryName = "model-registry-oauth-noroute"
				oauthProxyConfig = &v1beta1.OAuthProxyConfig{
					ServiceRoute: config.RouteDisabled,
				}
				oauthValidate()
			})

			It("When using Oauth Proxy with custom image on openshift", func() {
				registryName = "model-registry-oauth-image"
				oauthProxyConfig = &v1beta1.OAuthProxyConfig{
					Image: "test-proxy-image",
				}
				oauthValidate()
			})

			// Model Catalog integration tests
			var catalogValidate = func(enableCatalog bool) {
				specInit()

				var mySQLPort int32 = 3306
				modelRegistry.Spec.Postgres = nil
				modelRegistry.Spec.MySQL = &v1beta1.MySQLConfig{
					Host:     "model-registry-db",
					Port:     &mySQLPort,
					Database: "model_registry",
					Username: "mlmduser",
					PasswordSecret: &v1beta1.SecretKeyValue{
						Name: "model-registry-db",
						Key:  "database-password",
					},
				}

				err = k8sClient.Create(ctx, modelRegistry)
				Expect(err).To(Not(HaveOccurred()))

				modelRegistryReconciler := initModelRegistryReconciler(template)
				modelRegistryReconciler.EnableModelCatalog = enableCatalog

				Eventually(validateRegistryCatalog(ctx, typeNamespaceName, modelRegistry, modelRegistryReconciler, enableCatalog),
					time.Minute, time.Second).Should(Succeed())
			}

			It("When model catalog is enabled", func() {
				registryName = "model-registry-catalog-enabled"
				catalogValidate(true)
			})

			It("When model catalog is disabled", func() {
				registryName = "model-registry-catalog-disabled"
				catalogValidate(false)
			})

			It("When model catalog is enabled on OpenShift", func() {
				registryName = "model-registry-catalog-openshift"
				specInit()

				var mySQLPort int32 = 3306
				modelRegistry.Spec.Postgres = nil
				modelRegistry.Spec.MySQL = &v1beta1.MySQLConfig{
					Host:     "model-registry-db",
					Port:     &mySQLPort,
					Database: "model_registry",
					Username: "mlmduser",
					PasswordSecret: &v1beta1.SecretKeyValue{
						Name: "model-registry-db",
						Key:  "database-password",
					},
				}

				err = k8sClient.Create(ctx, modelRegistry)
				Expect(err).To(Not(HaveOccurred()))

				modelRegistryReconciler := initModelRegistryReconciler(template)
				modelRegistryReconciler.EnableModelCatalog = true
				modelRegistryReconciler.IsOpenShift = true

				Eventually(validateRegistryCatalogOpenShift(ctx, typeNamespaceName, modelRegistry, modelRegistryReconciler),
					time.Minute, time.Second).Should(Succeed())
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
				_ = os.Unsetenv(config.GrpcImage)
				_ = os.Unsetenv(config.RestImage)
			})
		})

	})
})

func initModelRegistryReconciler(template *template.Template) *ModelRegistryReconciler {
	scheme := k8sClient.Scheme()

	modelRegistryReconciler := &ModelRegistryReconciler{
		Client:             k8sClient,
		Scheme:             scheme,
		Recorder:           &record.FakeRecorder{},
		Log:                ctrl.Log.WithName("controller"),
		Template:           template,
		EnableModelCatalog: false, // Default to false for most tests
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
		mrPod.Labels = map[string]string{"app": typeNamespaceName.Name, "component": "model-registry"}
		mrPod.Spec.Containers = []corev1.Container{
			{
				Name:  "model-registry-rest",
				Image: config.DefaultRestImage,
			},
		}

		// mock istio proxy container
		if modelRegistryReconciler.HasIstio {
			mrPod.Spec.Containers = append(mrPod.Spec.Containers, corev1.Container{
				Name:  "istio-proxy",
				Image: "istio-proxy",
			})
		}

		// mock oauth proxy container
		if modelRegistry.Spec.OAuthProxy != nil {
			image := modelRegistry.Spec.OAuthProxy.Image
			if len(image) == 0 {
				image = config.DefaultOAuthProxyImage
			}
			mrPod.Spec.Containers = append(mrPod.Spec.Containers, corev1.Container{
				Name:  "oauth-proxy",
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
		if modelRegistryReconciler.CreateAuthResources {
			By("Checking if the Auth resources were successfully created in the reconciliation")
			authConfig := CreateAuthConfig()
			Eventually(func() error {
				return k8sClient.Get(ctx, typeNamespaceName, authConfig)
			}, time.Minute, time.Second).Should(Succeed())

			By("Mocking conditions in the AuthConfig to perform the tests")
			err := unstructured.SetNestedMap(authConfig.Object, map[string]interface{}{
				"conditions": []interface{}{
					map[string]interface{}{
						"type":   "Ready",
						"status": "True",
					},
				}}, "status")
			Expect(err).To(Not(HaveOccurred()))

			By("Updating the AuthConfig to set the Ready condition to True")
			err = k8sClient.Status().Update(ctx, authConfig)
			Expect(err).To(Not(HaveOccurred()))

			authConfigTwo := CreateAuthConfig()
			Eventually(func() error {
				return k8sClient.Get(ctx, typeNamespaceName, authConfigTwo)
			}, time.Minute, time.Second).Should(Succeed())

			Eventually(func() error {
				if authConfig.Object["status"] == nil {
					return fmt.Errorf("status not set")
				}

				return nil
			}, time.Minute, time.Second).Should(Succeed())

			Eventually(func() error {
				_, err := modelRegistryReconciler.Reconcile(ctx, reconcile.Request{
					NamespacedName: typeNamespaceName,
				})

				return err
			}, time.Minute, time.Second).Should(Succeed())
		}

		if modelRegistry.Spec.OAuthProxy != nil && modelRegistry.Spec.OAuthProxy.ServiceRoute != config.RouteDisabled && modelRegistryReconciler.IsOpenShift {
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
			if modelRegistry.Spec.OAuthProxy != nil && modelRegistry.Spec.OAuthProxy.ServiceRoute != config.RouteDisabled {
				hosts := modelRegistry.Status.Hosts
				Expect(len(hosts)).To(Equal(4))
				name := modelRegistry.Name
				namespace := modelRegistry.Namespace
				domain := modelRegistry.Spec.OAuthProxy.Domain
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

func validateRegistryOauthProxy(ctx context.Context, typeNamespaceName types.NamespacedName, modelRegistry *v1beta1.ModelRegistry, modelRegistryReconciler *ModelRegistryReconciler) func() error {
	return func() error {
		modelRegistryReconciler.IsOpenShift = true

		Eventually(validateRegistryOpenshift(ctx, typeNamespaceName, modelRegistry, modelRegistryReconciler)).Should(Succeed())

		By("Checking if the OAuth Proxy Deployment was configured correctly in the reconciliation")
		Eventually(func() error {
			found := &appsv1.Deployment{}

			err := k8sClient.Get(ctx, types.NamespacedName{Name: fmt.Sprintf("%s", modelRegistry.Name), Namespace: modelRegistry.Namespace}, found)
			Expect(err).To(Not(HaveOccurred()))

			proxyContainer := found.Spec.Template.Spec.Containers[1]

			// check image
			if len(modelRegistry.Spec.OAuthProxy.Image) != 0 {
				Expect(proxyContainer.Image).Should(Equal(modelRegistry.Spec.OAuthProxy.Image))
			}

			// check cert args
			certKey := "tls.crt"
			keyKey := "tls.key"
			certSecretName := modelRegistry.Name + "-oauth-proxy"
			keySecretName := modelRegistry.Name + "-oauth-proxy"
			if modelRegistry.Spec.OAuthProxy.TLSCertificateSecret != nil {
				certKey = modelRegistry.Spec.OAuthProxy.TLSCertificateSecret.Key
				certSecretName = modelRegistry.Spec.OAuthProxy.TLSCertificateSecret.Name
			}
			if modelRegistry.Spec.OAuthProxy.TLSKeySecret != nil {
				keyKey = modelRegistry.Spec.OAuthProxy.TLSKeySecret.Key
				keySecretName = modelRegistry.Spec.OAuthProxy.TLSKeySecret.Name
			}
			Expect(proxyContainer.Args).Should(ContainElements(
				fmt.Sprintf("--tls-cert=/etc/tls/private-cert/%s", certKey),
				fmt.Sprintf("--tls-key=/etc/tls/private-key/%s", keyKey)))

			// check cert volumes
			var defaultMode int32 = 0o600
			Expect(found.Spec.Template.Spec.Volumes).Should(ContainElements(
				corev1.Volume{
					Name: "oauth-proxy-cert",
					VolumeSource: corev1.VolumeSource{
						Secret: &corev1.SecretVolumeSource{
							SecretName:  certSecretName,
							DefaultMode: &defaultMode,
						},
					}},
				corev1.Volume{
					Name: "oauth-proxy-key",
					VolumeSource: corev1.VolumeSource{
						Secret: &corev1.SecretVolumeSource{
							SecretName:  keySecretName,
							DefaultMode: &defaultMode,
						},
					}}))

			return nil
		}, 5*time.Second, time.Second).Should(Succeed())

		By("Checking if the OAuth Proxy Route was successfully created in the reconciliation")
		matchRoute := Succeed()
		if modelRegistry.Spec.OAuthProxy.ServiceRoute == config.RouteDisabled {
			matchRoute = Not(Succeed())
		}
		Eventually(func() error {
			found := &routev1.Route{}

			return k8sClient.Get(ctx, types.NamespacedName{
				Name: fmt.Sprintf("%s-https", modelRegistry.Name), Namespace: modelRegistry.Namespace}, found)
		}, 5*time.Second, time.Second).Should(matchRoute)

		By("Checking if the OAuth Proxy ClusterRoleBinding was successfully created in the reconciliation")
		Eventually(func() error {
			found := &rbacv1.ClusterRoleBinding{}

			return k8sClient.Get(ctx, types.NamespacedName{Name: fmt.Sprintf("%s-auth-delegator", modelRegistry.Name)}, found)
		}, 5*time.Second, time.Second).Should(Succeed())

		By("Checking if the OAuth Proxy NetworkPolicy was successfully created in the reconciliation")
		Eventually(func() error {
			found := &v1.NetworkPolicy{}

			return k8sClient.Get(ctx, types.NamespacedName{Name: fmt.Sprintf("%s-https-route", modelRegistry.Name), Namespace: modelRegistry.Namespace}, found)
		}, 5*time.Second, time.Second).Should(matchRoute)

		return nil
	}
}

func validateRegistryCatalog(ctx context.Context, typeNamespaceName types.NamespacedName, modelRegistry *v1beta1.ModelRegistry, modelRegistryReconciler *ModelRegistryReconciler, enableCatalog bool) func() error {
	return func() error {
		Eventually(validateRegistryBase(ctx, typeNamespaceName, modelRegistry, modelRegistryReconciler)).Should(Succeed())

		if enableCatalog {
			By("Checking if the catalog ConfigMap was successfully created in the reconciliation")
			Eventually(func() error {
				found := &corev1.ConfigMap{}
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      "model-catalog-sources",
					Namespace: modelRegistry.Namespace,
				}, found)
			}, 10*time.Second, time.Second).Should(Succeed())

			By("Checking if the catalog Deployment was successfully created in the reconciliation")
			Eventually(func() error {
				found := &appsv1.Deployment{}
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      fmt.Sprintf("%s-catalog", modelRegistry.Name),
					Namespace: modelRegistry.Namespace,
				}, found)
			}, 10*time.Second, time.Second).Should(Succeed())

			By("Checking if the catalog Service was successfully created in the reconciliation")
			Eventually(func() error {
				found := &corev1.Service{}
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      fmt.Sprintf("%s-catalog", modelRegistry.Name),
					Namespace: modelRegistry.Namespace,
				}, found)
			}, 10*time.Second, time.Second).Should(Succeed())
		} else {
			By("Checking that no catalog resources were created when disabled")
			configMap := &corev1.ConfigMap{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      "model-catalog-sources",
				Namespace: modelRegistry.Namespace,
			}, configMap)
			Expect(errors.IsNotFound(err)).To(BeTrue())

			deployment := &appsv1.Deployment{}
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      fmt.Sprintf("%s-catalog", modelRegistry.Name),
				Namespace: modelRegistry.Namespace,
			}, deployment)
			Expect(errors.IsNotFound(err)).To(BeTrue())

			service := &corev1.Service{}
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      fmt.Sprintf("%s-catalog", modelRegistry.Name),
				Namespace: modelRegistry.Namespace,
			}, service)
			Expect(errors.IsNotFound(err)).To(BeTrue())
		}

		return nil
	}
}

func validateRegistryCatalogOpenShift(ctx context.Context, typeNamespaceName types.NamespacedName, modelRegistry *v1beta1.ModelRegistry, modelRegistryReconciler *ModelRegistryReconciler) func() error {
	return func() error {
		Eventually(validateRegistryCatalog(ctx, typeNamespaceName, modelRegistry, modelRegistryReconciler, true)).Should(Succeed())

		By("Checking if the catalog Route was successfully created in the reconciliation")
		Eventually(func() error {
			found := &routev1.Route{}
			return k8sClient.Get(ctx, types.NamespacedName{
				Name:      fmt.Sprintf("%s-catalog-https", modelRegistry.Name),
				Namespace: modelRegistry.Namespace,
			}, found)
		}, 10*time.Second, time.Second).Should(Succeed())

		By("Checking if the catalog NetworkPolicy was successfully created in the reconciliation")
		Eventually(func() error {
			found := &v1.NetworkPolicy{}
			return k8sClient.Get(ctx, types.NamespacedName{
				Name:      fmt.Sprintf("%s-catalog-https-route", modelRegistry.Name),
				Namespace: modelRegistry.Namespace,
			}, found)
		}, 10*time.Second, time.Second).Should(Succeed())

		return nil
	}
}
