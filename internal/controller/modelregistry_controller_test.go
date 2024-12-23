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

	"github.com/opendatahub-io/model-registry-operator/internal/controller/config"
	routev1 "github.com/openshift/api/route/v1"
	userv1 "github.com/openshift/api/user/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/opendatahub-io/model-registry-operator/api/v1alpha1"
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
			var modelRegistry *v1alpha1.ModelRegistry
			var registryName string

			BeforeEach(func() {
				By("Setting the Image ENV VARs which stores the Server images")
				err = os.Setenv(config.GrpcImage, config.DefaultGrpcImage)
				Expect(err).To(Not(HaveOccurred()))
				err = os.Setenv(config.RestImage, config.DefaultRestImage)
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
				modelRegistry = &v1alpha1.ModelRegistry{}

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
				modelRegistry = &v1alpha1.ModelRegistry{
					ObjectMeta: metav1.ObjectMeta{
						Name:        registryName,
						Namespace:   namespace.Name,
						Annotations: map[string]string{DisplayNameAnnotation: registryName, DescriptionAnnotation: DescriptionPrefix + registryName},
					},
					Spec: v1alpha1.ModelRegistrySpec{
						Grpc: v1alpha1.GrpcSpec{
							Port: &gRPCPort,
						},
						Rest: v1alpha1.RestSpec{
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
				modelRegistry.Spec.Postgres = &v1alpha1.PostgresConfig{
					Host:     "model-registry-db",
					Port:     &postgresPort,
					Database: "model-registry",
					Username: "mlmduser",
					PasswordSecret: &v1alpha1.SecretKeyValue{
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
				modelRegistry.Spec.MySQL = &v1alpha1.MySQLConfig{
					Host:     "model-registry-db",
					Port:     &mySQLPort,
					Database: "model_registry",
					Username: "mlmduser",
					PasswordSecret: &v1alpha1.SecretKeyValue{
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
				modelRegistry.Spec.MySQL = &v1alpha1.MySQLConfig{
					Host:     "model-registry-db",
					Port:     &mySQLPort,
					Database: "model_registry",
					Username: "mlmduser",
					PasswordSecret: &v1alpha1.SecretKeyValue{
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
				modelRegistry.Spec.MySQL = &v1alpha1.MySQLConfig{
					Host:     "model-registry-db",
					Port:     &mySQLPort,
					Database: "model_registry",
					Username: "mlmduser",
					PasswordSecret: &v1alpha1.SecretKeyValue{
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

			It("When using Istio", func() {
				registryName = "model-registry-istio"
				specInit()

				var mySQLPort int32 = 3306
				modelRegistry.Spec.Postgres = nil
				modelRegistry.Spec.MySQL = &v1alpha1.MySQLConfig{
					Host:     "model-registry-db",
					Port:     &mySQLPort,
					Database: "model_registry",
					Username: "mlmduser",
					PasswordSecret: &v1alpha1.SecretKeyValue{
						Name: "model-registry-db",
						Key:  "database-password",
					},
				}
				modelRegistry.Spec.Istio = &v1alpha1.IstioConfig{
					AuthProvider: "opendatahub-auth-provider",
					AuthConfigLabels: map[string]string{
						"auth": "enabled",
					},
					Gateway: &v1alpha1.GatewayConfig{
						Domain: "example.com",
						Rest: v1alpha1.ServerConfig{
							GatewayRoute: "enabled",
						},
						Grpc: v1alpha1.ServerConfig{
							GatewayRoute: "enabled",
						},
					},
				}

				err = k8sClient.Create(ctx, modelRegistry)
				Expect(err).To(Not(HaveOccurred()))

				modelRegistryReconciler := initModelRegistryReconciler(template)

				Eventually(validateRegistryIstio(ctx, typeNamespaceName, modelRegistry, modelRegistryReconciler),
					time.Minute, time.Second).Should(Succeed())
			})

			It("When using Istio on Openshift", func() {
				registryName = "model-registry-istio-openshift"
				specInit()

				var mySQLPort int32 = 3306
				modelRegistry.Spec.Postgres = nil
				modelRegistry.Spec.MySQL = &v1alpha1.MySQLConfig{
					Host:     "model-registry-db",
					Port:     &mySQLPort,
					Database: "model_registry",
					Username: "mlmduser",
					PasswordSecret: &v1alpha1.SecretKeyValue{
						Name: "model-registry-db",
						Key:  "database-password",
					},
				}
				modelRegistry.Spec.Istio = &v1alpha1.IstioConfig{
					AuthProvider: "opendatahub-auth-provider",
					AuthConfigLabels: map[string]string{
						"auth": "enabled",
					},
					Gateway: &v1alpha1.GatewayConfig{
						Domain: "example.com",
						Rest: v1alpha1.ServerConfig{
							GatewayRoute: "enabled",
						},
						Grpc: v1alpha1.ServerConfig{
							GatewayRoute: "enabled",
						},
					},
				}

				err = k8sClient.Create(ctx, modelRegistry)
				Expect(err).To(Not(HaveOccurred()))

				modelRegistryReconciler := initModelRegistryReconciler(template)

				modelRegistryReconciler.IsOpenShift = true

				Eventually(validateRegistryIstio(ctx, typeNamespaceName, modelRegistry, modelRegistryReconciler),
					time.Minute, time.Second).Should(Succeed())
			})

			It("When using Istio and Authorino", func() {
				registryName = "model-registry-istio-authorino"
				specInit()

				var mySQLPort int32 = 3306
				modelRegistry.Spec.Postgres = nil
				modelRegistry.Spec.MySQL = &v1alpha1.MySQLConfig{
					Host:     "model-registry-db",
					Port:     &mySQLPort,
					Database: "model_registry",
					Username: "mlmduser",
					PasswordSecret: &v1alpha1.SecretKeyValue{
						Name: "model-registry-db",
						Key:  "database-password",
					},
				}
				modelRegistry.Spec.Istio = &v1alpha1.IstioConfig{
					AuthProvider: "opendatahub-auth-provider",
					AuthConfigLabels: map[string]string{
						"auth": "enabled",
					},
					Gateway: &v1alpha1.GatewayConfig{
						Domain: "example.com",
						Rest: v1alpha1.ServerConfig{
							GatewayRoute: "enabled",
						},
						Grpc: v1alpha1.ServerConfig{
							GatewayRoute: "enabled",
						},
					},
				}

				err = k8sClient.Create(ctx, modelRegistry)
				Expect(err).To(Not(HaveOccurred()))

				modelRegistryReconciler := initModelRegistryReconciler(template)

				Eventually(validateRegistryAuth(ctx, typeNamespaceName, modelRegistry, modelRegistryReconciler),
					time.Minute, time.Second).Should(Succeed())
			})

			It("When using Istio and Authorino on openshift", func() {
				registryName = "model-registry-istio-authorino-openshift"
				specInit()

				var mySQLPort int32 = 3306
				modelRegistry.Spec.Postgres = nil
				modelRegistry.Spec.MySQL = &v1alpha1.MySQLConfig{
					Host:     "model-registry-db",
					Port:     &mySQLPort,
					Database: "model_registry",
					Username: "mlmduser",
					PasswordSecret: &v1alpha1.SecretKeyValue{
						Name: "model-registry-db",
						Key:  "database-password",
					},
				}
				modelRegistry.Spec.Istio = &v1alpha1.IstioConfig{
					AuthProvider: "opendatahub-auth-provider",
					AuthConfigLabels: map[string]string{
						"auth": "enabled",
					},
					Gateway: &v1alpha1.GatewayConfig{
						Domain: "example.com",
						Rest: v1alpha1.ServerConfig{
							GatewayRoute: "enabled",
						},
						Grpc: v1alpha1.ServerConfig{
							GatewayRoute: "enabled",
						},
					},
				}

				err = k8sClient.Create(ctx, modelRegistry)
				Expect(err).To(Not(HaveOccurred()))

				modelRegistryReconciler := initModelRegistryReconciler(template)

				modelRegistryReconciler.IsOpenShift = true

				Eventually(validateRegistryAuth(ctx, typeNamespaceName, modelRegistry, modelRegistryReconciler),
					time.Minute, time.Second).Should(Succeed())
			})

			AfterEach(func() {
				By("removing the custom resource for the Kind ModelRegistry")
				found := &v1alpha1.ModelRegistry{}
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
		Client:   k8sClient,
		Scheme:   scheme,
		Recorder: &record.FakeRecorder{},
		Log:      ctrl.Log.WithName("controller"),
		Template: template,
	}

	return modelRegistryReconciler
}

func validateRegistryBase(ctx context.Context, typeNamespaceName types.NamespacedName, modelRegistry *v1alpha1.ModelRegistry, modelRegistryReconciler *ModelRegistryReconciler) func() error {
	return func() error {
		By("Checking if the custom resource was successfully created")
		Eventually(func() error {
			found := &v1alpha1.ModelRegistry{}
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
			{
				Name:  "model-registry-grpc",
				Image: config.DefaultGrpcImage,
			},
		}

		if modelRegistryReconciler.HasIstio {
			mrPod.Spec.Containers = append(mrPod.Spec.Containers, corev1.Container{
				Name:  "istio-proxy",
				Image: "istio-proxy",
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

		if modelRegistry.Spec.Istio != nil && modelRegistry.Spec.Istio.Gateway != nil && modelRegistryReconciler.IsOpenShift {
			By("Checking if the Route was successfully created in the reconciliation")
			routes := &routev1.RouteList{}
			err = k8sClient.List(ctx, routes, client.MatchingLabels{
				"app":                     typeNamespaceName.Name,
				"component":               "model-registry",
				"maistra.io/gateway-name": typeNamespaceName.Name,
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

		By("Checking the latest Status Condition added to the ModelRegistry instance")
		Eventually(func() error {
			err := k8sClient.Get(ctx, typeNamespaceName, modelRegistry)
			Expect(err).To(Not(HaveOccurred()))

			if modelRegistry.Spec.Istio != nil && modelRegistry.Spec.Istio.Gateway != nil {
				hosts := modelRegistry.Status.Hosts
				Expect(len(hosts)).To(Equal(5))
				name := modelRegistry.Name
				namespace := modelRegistry.Namespace
				domain := modelRegistry.Spec.Istio.Gateway.Domain
				Expect(hosts[0]).
					To(Equal(fmt.Sprintf("%s-rest.%s", name, domain)))
				Expect(hosts[1]).
					To(Equal(fmt.Sprintf("%s-grpc.%s", name, domain)))
				Expect(hosts[2]).
					To(Equal(fmt.Sprintf("%s.%s.svc.cluster.local", name, namespace)))
				Expect(hosts[3]).
					To(Equal(fmt.Sprintf("%s.%s", name, namespace)))
				Expect(hosts[4]).
					To(Equal(name))
				Expect(modelRegistry.Status.HostsStr).To(Equal(strings.Join(hosts, ",")))
			} else {
				// also check hosts in status
				hosts := modelRegistry.Status.Hosts
				Expect(len(hosts)).To(Equal(3))
				name := modelRegistry.Name
				namespace := modelRegistry.Namespace
				Expect(hosts[0]).
					To(Equal(fmt.Sprintf("%s.%s.svc.cluster.local", name, namespace)))
				Expect(hosts[1]).
					To(Equal(fmt.Sprintf("%s.%s", name, namespace)))
				Expect(hosts[2]).
					To(Equal(name))
				Expect(modelRegistry.Status.HostsStr).To(Equal(strings.Join(hosts, ",")))
			}

			if !meta.IsStatusConditionTrue(modelRegistry.Status.Conditions, ConditionTypeProgressing) {
				return fmt.Errorf("Condition %s is not true", ConditionTypeProgressing)
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

func validateRegistryOpenshift(ctx context.Context, typeNamespaceName types.NamespacedName, modelRegistry *v1alpha1.ModelRegistry, modelRegistryReconciler *ModelRegistryReconciler) func() error {
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

func validateRegistryIstio(ctx context.Context, typeNamespaceName types.NamespacedName, modelRegistry *v1alpha1.ModelRegistry, modelRegistryReconciler *ModelRegistryReconciler) func() error {
	return func() error {
		modelRegistryReconciler.HasIstio = true

		svc := corev1.Service{}
		svc.Name = "istio"
		svc.Namespace = typeNamespaceName.Namespace
		svc.Labels = map[string]string{"istio": v1alpha1.DefaultIstioGateway}
		svc.Spec.Ports = []corev1.ServicePort{
			{
				Name:       "http2",
				Port:       80,
				TargetPort: intstr.FromInt(80),
			},
			{
				Name:       "https",
				Port:       443,
				TargetPort: intstr.FromInt(443),
			},
		}

		err := k8sClient.Create(ctx, &svc)
		Expect(err).To(Not(HaveOccurred()))

		Eventually(validateRegistryBase(ctx, typeNamespaceName, modelRegistry, modelRegistryReconciler)).Should(Succeed())

		return nil
	}
}

func validateRegistryAuth(ctx context.Context, typeNamespaceName types.NamespacedName, modelRegistry *v1alpha1.ModelRegistry, modelRegistryReconciler *ModelRegistryReconciler) func() error {
	return func() error {
		modelRegistryReconciler.CreateAuthResources = true

		Eventually(validateRegistryIstio(ctx, typeNamespaceName, modelRegistry, modelRegistryReconciler)).Should(Succeed())

		return nil
	}
}
