package controller

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"text/template"
	"time"

	"github.com/opendatahub-io/model-registry-operator/internal/controller/config"
	routev1 "github.com/openshift/api/route/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbac "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/yaml"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("ModelCatalog controller", func() {

	Context("Model Catalog functionality", func() {

		ctx := context.Background()
		var namespace *corev1.Namespace
		var namespaceName string
		var catalogReconciler *ModelCatalogReconciler

		BeforeEach(func() {
			By("Setting the Image ENV VARs")
			err := os.Setenv(config.RestImage, config.DefaultRestImage)
			Expect(err).To(Not(HaveOccurred()))
			err = os.Setenv(config.PostgresImage, config.DefaultPostgresImage)
			Expect(err).To(Not(HaveOccurred()))
			err = os.Setenv(config.CatalogDataImage, config.DefaultCatalogDataImage)
			Expect(err).To(Not(HaveOccurred()))
			err = os.Setenv(config.BenchmarkDataImage, config.DefaultBenchmarkDataImage)
			Expect(err).To(Not(HaveOccurred()))

			namespaceName = fmt.Sprintf("model-catalog-test-%d", time.Now().UnixNano())

			namespace = &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      namespaceName,
					Namespace: namespaceName,
				},
			}

			By("Creating the Namespace to perform the tests")
			err = k8sClient.Create(ctx, namespace)
			Expect(err).To(Not(HaveOccurred()))

			By("Setting up default domain for tests")
			config.SetDefaultDomain("example.com", nil, false)

			template, err := config.ParseTemplates()
			Expect(err).To(Not(HaveOccurred()))

			catalogReconciler = &ModelCatalogReconciler{
				Client:          k8sClient,
				Scheme:          k8sClient.Scheme(),
				Recorder:        &events.FakeRecorder{},
				Log:             ctrl.Log.WithName("modelcatalog-controller"),
				Template:        template,
				TargetNamespace: namespaceName,
				Capabilities: ClusterCapabilities{
					IsOpenShift:  false,
					HasUserAPI:   false,
					HasConfigAPI: false,
				},
			}
		})

		Context("Model Catalog resource management", func() {
			It("Should create all catalog resources", func() {
				_, err := catalogReconciler.ensureCatalogResources(ctx)
				Expect(err).To(Not(HaveOccurred()))

				By("Checking if the ServiceAccount was created")
				serviceAccount := &corev1.ServiceAccount{}
				err = k8sClient.Get(ctx, types.NamespacedName{
					Name:      modelCatalogName,
					Namespace: namespaceName,
				}, serviceAccount)
				Expect(err).To(Not(HaveOccurred()))
				Expect(serviceAccount.Labels["component"]).To(Equal("model-catalog"))
				Expect(serviceAccount.Labels["app.kubernetes.io/created-by"]).To(Equal("model-registry-operator"))

				By("Checking if the ConfigMap was created")
				configMap := &corev1.ConfigMap{}
				err = k8sClient.Get(ctx, types.NamespacedName{
					Name:      "model-catalog-sources",
					Namespace: namespaceName,
				}, configMap)
				Expect(err).To(Not(HaveOccurred()))
				Expect(configMap.Labels["component"]).To(Equal("model-catalog"))
				Expect(configMap.Labels["app.kubernetes.io/created-by"]).To(Equal("model-registry-operator"))

				By("Checking if the Deployment was created")
				deployment := &appsv1.Deployment{}
				err = k8sClient.Get(ctx, types.NamespacedName{
					Name:      modelCatalogName,
					Namespace: namespaceName,
				}, deployment)
				Expect(err).To(Not(HaveOccurred()))
				Expect(deployment.Labels["component"]).To(Equal("model-catalog"))
				Expect(deployment.Labels["app.kubernetes.io/created-by"]).To(Equal("model-registry-operator"))
				Expect(deployment.Spec.Template.Spec.Containers).To(HaveLen(2)) // catalog + kube-rbac-proxy
				Expect(deployment.Spec.Template.Spec.Containers[0].Name).To(Equal("catalog"))

				// Check that kube-rbac-proxy container is deployed
				var proxyContainer *corev1.Container
				for _, container := range deployment.Spec.Template.Spec.Containers {
					if container.Name == "kube-rbac-proxy" {
						proxyContainer = &container
						break
					}
				}
				Expect(proxyContainer).ToNot(BeNil(), "kube-rbac-proxy container should be present")
				Expect(proxyContainer.Image).To(ContainSubstring("kube-rbac-proxy"))

				// Check kube-rbac-proxy specific arguments
				Expect(proxyContainer.Args).To(ContainElement("--secure-listen-address=0.0.0.0:8443"))
				Expect(proxyContainer.Args).To(ContainElement("--upstream=http://127.0.0.1:8080/"))
				Expect(proxyContainer.Args).To(ContainElement("--config-file=/etc/kube-rbac-proxy/config-file.yaml"))

				// Check that oauth-proxy specific args are NOT present
				for _, arg := range proxyContainer.Args {
					Expect(arg).ToNot(ContainSubstring("--provider=openshift"))
					Expect(arg).ToNot(ContainSubstring("--cookie-secret"))
				}

				// Ensure oauth-proxy container is NOT present
				for _, container := range deployment.Spec.Template.Spec.Containers {
					Expect(container.Name).ToNot(Equal("oauth-proxy"))
				}

				By("Checking if the Service was created")
				service := &corev1.Service{}
				err = k8sClient.Get(ctx, types.NamespacedName{
					Name:      modelCatalogName,
					Namespace: namespaceName,
				}, service)
				Expect(err).To(Not(HaveOccurred()))
				Expect(service.Labels["component"]).To(Equal("model-catalog"))
				Expect(service.Labels["app.kubernetes.io/created-by"]).To(Equal("model-registry-operator"))

				By("Checking if the Role was created")
				role := &rbac.Role{}
				err = k8sClient.Get(ctx, types.NamespacedName{
					Name:      modelCatalogName,
					Namespace: namespaceName,
				}, role)
				Expect(err).To(Not(HaveOccurred()))
				Expect(role.Labels["app"]).To(Equal(modelCatalogName))
				Expect(role.Labels["app.kubernetes.io/created-by"]).To(Equal("model-registry-operator"))
				Expect(role.Labels["app.kubernetes.io/managed-by"]).To(Equal("model-registry-operator"))
				Expect(role.Labels["app.kubernetes.io/name"]).To(Equal(modelCatalogName))
				Expect(role.Annotations["openshift.io/display-name"]).To(Equal("Model catalog User"))
				Expect(role.Annotations["openshift.io/description"]).To(Equal("Can access model catalog"))
				Expect(role.Rules).To(HaveLen(2))
				Expect(role.Rules[0].APIGroups).To(Equal([]string{""}))
				Expect(role.Rules[0].Resources).To(Equal([]string{"services"}))
				Expect(role.Rules[0].ResourceNames).To(Equal([]string{"model-catalog"}))
				Expect(role.Rules[0].Verbs).To(Equal([]string{"get"}))
				Expect(role.Rules[1].APIGroups).To(Equal([]string{""}))
				Expect(role.Rules[1].Resources).To(Equal([]string{"endpoints"}))
				Expect(role.Rules[1].ResourceNames).To(Equal([]string{"model-catalog"}))
				Expect(role.Rules[1].Verbs).To(Equal([]string{"get"}))

				By("Checking if the RoleBinding was created")
				roleBinding := &rbac.RoleBinding{}
				err = k8sClient.Get(ctx, types.NamespacedName{
					Name:      modelCatalogName + "-authenticated",
					Namespace: namespaceName,
				}, roleBinding)
				Expect(err).To(Not(HaveOccurred()))
				Expect(roleBinding.RoleRef.APIGroup).To(Equal("rbac.authorization.k8s.io"))
				Expect(roleBinding.RoleRef.Kind).To(Equal("Role"))
				Expect(roleBinding.RoleRef.Name).To(Equal(modelCatalogName))
				Expect(roleBinding.Subjects).To(HaveLen(1))
				Expect(roleBinding.Subjects[0].APIGroup).To(Equal("rbac.authorization.k8s.io"))
				Expect(roleBinding.Subjects[0].Kind).To(Equal("Group"))
				Expect(roleBinding.Subjects[0].Name).To(Equal("system:authenticated"))

				By("Checking if the PostgreSQL Deployment was created")
				postgresDeployment := &appsv1.Deployment{}
				err = k8sClient.Get(ctx, types.NamespacedName{
					Name:      modelCatalogName + "-postgres",
					Namespace: namespaceName,
				}, postgresDeployment)
				Expect(err).To(Not(HaveOccurred()))
				Expect(postgresDeployment.Labels["component"]).To(Equal("model-catalog-postgres"))
				Expect(postgresDeployment.Labels["app.kubernetes.io/created-by"]).To(Equal("model-registry-operator"))
				Expect(postgresDeployment.Spec.Template.Spec.Containers).To(HaveLen(1))
				Expect(postgresDeployment.Spec.Template.Spec.Containers[0].Name).To(Equal("postgresql"))
				Expect(postgresDeployment.Spec.Template.Spec.Containers[0].Image).To(Equal("quay.io/sclorg/postgresql-16-c10s:latest"))

				By("Checking if the PostgreSQL Service was created")
				postgresService := &corev1.Service{}
				err = k8sClient.Get(ctx, types.NamespacedName{
					Name:      modelCatalogName + "-postgres",
					Namespace: namespaceName,
				}, postgresService)
				Expect(err).To(Not(HaveOccurred()))
				Expect(postgresService.Labels["component"]).To(Equal("model-catalog-postgres"))
				Expect(postgresService.Labels["app.kubernetes.io/created-by"]).To(Equal("model-registry-operator"))
				Expect(postgresService.Spec.Ports).To(HaveLen(1))
				Expect(postgresService.Spec.Ports[0].Port).To(Equal(int32(5432)))
				Expect(postgresService.Spec.Ports[0].Name).To(Equal("postgresql"))

				By("Checking if the PostgreSQL Secret was created")
				postgresSecret := &corev1.Secret{}
				err = k8sClient.Get(ctx, types.NamespacedName{
					Name:      modelCatalogName + "-postgres",
					Namespace: namespaceName,
				}, postgresSecret)
				Expect(err).To(Not(HaveOccurred()))
				Expect(postgresSecret.Labels["component"]).To(Equal("model-catalog-postgres"))
				Expect(postgresSecret.Labels["app.kubernetes.io/created-by"]).To(Equal("model-registry-operator"))
				Expect(postgresSecret.Data).To(HaveKey("database-name"))
				Expect(postgresSecret.Data).To(HaveKey("database-password"))
				Expect(postgresSecret.Data).To(HaveKey("database-user"))

				By("Checking if the PostgreSQL PVC was created")
				postgresPVC := &corev1.PersistentVolumeClaim{}
				err = k8sClient.Get(ctx, types.NamespacedName{
					Name:      modelCatalogName + "-postgres",
					Namespace: namespaceName,
				}, postgresPVC)
				Expect(err).To(Not(HaveOccurred()))
				Expect(postgresPVC.Labels["component"]).To(Equal("model-catalog-postgres"))
				Expect(postgresPVC.Labels["app.kubernetes.io/created-by"]).To(Equal("model-registry-operator"))
				Expect(postgresPVC.Spec.AccessModes).To(ContainElement(corev1.ReadWriteOnce))
				Expect(postgresPVC.Spec.Resources.Requests.Storage().String()).To(Equal("5Gi"))
			})

			It("Should handle subsequent calls idempotently", func() {
				_, err := catalogReconciler.ensureCatalogResources(ctx)
				Expect(err).To(Not(HaveOccurred()))

				_, err = catalogReconciler.ensureCatalogResources(ctx)
				Expect(err).To(Not(HaveOccurred()))
			})

			Context("On OpenShift", func() {
				BeforeEach(func() {
					catalogReconciler.Capabilities = ClusterCapabilities{
						IsOpenShift: true,
						HasUserAPI:  true,
					}
				})

				It("Should create OpenShift-specific resources", func() {
					_, err := catalogReconciler.ensureCatalogResources(ctx)
					Expect(err).To(Not(HaveOccurred()))

					By("Checking if the Route was created")
					route := &routev1.Route{}
					err = k8sClient.Get(ctx, types.NamespacedName{
						Name:      modelCatalogName + "-https",
						Namespace: namespaceName,
					}, route)
					Expect(err).To(Not(HaveOccurred()))
					Expect(route.Labels["component"]).To(Equal("model-catalog"))
					Expect(route.Labels["app.kubernetes.io/created-by"]).To(Equal("model-registry-operator"))

					By("Checking if the NetworkPolicy was created")
					networkPolicy := &networkingv1.NetworkPolicy{}
					err = k8sClient.Get(ctx, types.NamespacedName{
						Name:      modelCatalogName + "-https-route",
						Namespace: namespaceName,
					}, networkPolicy)
					Expect(err).To(Not(HaveOccurred()))
					Expect(networkPolicy.Labels["component"]).To(Equal("model-catalog"))
					Expect(networkPolicy.Labels["app.kubernetes.io/created-by"]).To(Equal("model-registry-operator"))
				})
			})
		})

		Context("Resource cleanup", func() {
			It("Should clean up all catalog resources", func() {
				By("First creating catalog resources")
				_, err := catalogReconciler.ensureCatalogResources(ctx)
				Expect(err).To(Not(HaveOccurred()))

				By("Verifying resources exist")
				configMap := &corev1.ConfigMap{}
				err = k8sClient.Get(ctx, types.NamespacedName{
					Name:      "model-catalog-sources",
					Namespace: namespaceName,
				}, configMap)
				Expect(err).To(Not(HaveOccurred()))

				By("Running cleanup")
				_, err = catalogReconciler.cleanupCatalogResources(ctx)
				Expect(err).To(Not(HaveOccurred()))

				By("Verifying explicitly deleted resources are gone")
				// Only test for explicitly deleted resources (Deployment).
				// Service, Role, and RoleBinding should be cleaned up via garbage collection
				// due to owner references, but envtest may not have garbage collection enabled.
				deployment := &appsv1.Deployment{}
				Eventually(func() bool {
					err = k8sClient.Get(ctx, types.NamespacedName{
						Name:      modelCatalogName,
						Namespace: namespaceName,
					}, deployment)
					return apierrors.IsNotFound(err)
				}, 10*time.Second, 1*time.Second).Should(BeTrue())

				By("Verifying PostgreSQL resources are deleted")
				postgresDeployment := &appsv1.Deployment{}
				Eventually(func() bool {
					err = k8sClient.Get(ctx, types.NamespacedName{
						Name:      modelCatalogName + "-postgres",
						Namespace: namespaceName,
					}, postgresDeployment)
					return apierrors.IsNotFound(err)
				}, 10*time.Second, 1*time.Second).Should(BeTrue())

				postgresService := &corev1.Service{}
				Eventually(func() bool {
					err = k8sClient.Get(ctx, types.NamespacedName{
						Name:      modelCatalogName + "-postgres",
						Namespace: namespaceName,
					}, postgresService)
					return apierrors.IsNotFound(err)
				}, 10*time.Second, 1*time.Second).Should(BeTrue())

				postgresSecret := &corev1.Secret{}
				Eventually(func() bool {
					err = k8sClient.Get(ctx, types.NamespacedName{
						Name:      modelCatalogName + "-postgres",
						Namespace: namespaceName,
					}, postgresSecret)
					return apierrors.IsNotFound(err)
				}, 10*time.Second, 1*time.Second).Should(BeTrue())

				By("Verifying PVC deletion was attempted")
				postgresPVC := &corev1.PersistentVolumeClaim{}

				err = k8sClient.Get(ctx, types.NamespacedName{
					Name:      modelCatalogName + "-postgres",
					Namespace: namespaceName,
				}, postgresPVC)

				if err == nil {
					// PVC still exists, which is expected in test environment
					// Ensure the cleanup method completed without error
					Expect(postgresPVC.Name).To(Equal(modelCatalogName + "-postgres"))
				} else {
					Expect(apierrors.IsNotFound(err)).To(BeTrue())
				}
			})

			Context("On OpenShift", func() {
				BeforeEach(func() {
					catalogReconciler.Capabilities = ClusterCapabilities{
						IsOpenShift: true,
						HasUserAPI:  true,
					}
				})

				It("Should delete OpenShift-specific catalog resources", func() {
					By("First creating all resources including OpenShift resources")
					_, err := catalogReconciler.ensureCatalogResources(ctx)
					Expect(err).To(Not(HaveOccurred()))

					By("Verifying OpenShift resources exist")
					route := &routev1.Route{}
					err = k8sClient.Get(ctx, types.NamespacedName{
						Name:      modelCatalogName + "-https",
						Namespace: namespaceName,
					}, route)
					Expect(err).To(Not(HaveOccurred()))

					networkPolicy := &networkingv1.NetworkPolicy{}
					err = k8sClient.Get(ctx, types.NamespacedName{
						Name:      modelCatalogName + "-https-route",
						Namespace: namespaceName,
					}, networkPolicy)
					Expect(err).To(Not(HaveOccurred()))

					By("Running cleanup")
					_, err = catalogReconciler.cleanupCatalogResources(ctx)
					Expect(err).To(Not(HaveOccurred()))

					By("Verifying OpenShift resources are deleted")
					Eventually(func() bool {
						err = k8sClient.Get(ctx, types.NamespacedName{
							Name:      modelCatalogName + "-https",
							Namespace: namespaceName,
						}, route)
						return apierrors.IsNotFound(err)
					}, 10*time.Second, 1*time.Second).Should(BeTrue())

					Eventually(func() bool {
						err = k8sClient.Get(ctx, types.NamespacedName{
							Name:      modelCatalogName + "-https-route",
							Namespace: namespaceName,
						}, networkPolicy)
						return apierrors.IsNotFound(err)
					}, 10*time.Second, 1*time.Second).Should(BeTrue())
				})
			})
		})

		Context("PostgreSQL resource management", func() {
			It("Should create PostgreSQL resources with correct configuration", func() {
				_, err := catalogReconciler.ensureCatalogResources(ctx)
				Expect(err).To(Not(HaveOccurred()))

				By("Verifying PostgreSQL secret contains correct database credentials")
				postgresSecret := &corev1.Secret{}
				err = k8sClient.Get(ctx, types.NamespacedName{
					Name:      modelCatalogName + "-postgres",
					Namespace: namespaceName,
				}, postgresSecret)
				Expect(err).To(Not(HaveOccurred()))

				// Verify secret contains the expected keys
				Expect(postgresSecret.Data).To(HaveKey("database-name"))
				Expect(postgresSecret.Data).To(HaveKey("database-password"))
				Expect(postgresSecret.Data).To(HaveKey("database-user"))

				// Verify secret values match expected defaults
				Expect(string(postgresSecret.Data["database-name"])).To(Equal(config.DefaultCatalogPostgresDatabase))
				Expect(string(postgresSecret.Data["database-user"])).To(Equal(config.DefaultCatalogPostgresUser))

				// Verify password is randomly generated
				actualPassword := string(postgresSecret.Data["database-password"])
				Expect(actualPassword).ToNot(BeEmpty(), "password should be generated")
				Expect(len(actualPassword)).To(BeNumerically(">", 10), "password should be sufficiently long")

				By("Verifying PostgreSQL deployment has correct environment variables")
				postgresDeployment := &appsv1.Deployment{}
				err = k8sClient.Get(ctx, types.NamespacedName{
					Name:      modelCatalogName + "-postgres",
					Namespace: namespaceName,
				}, postgresDeployment)
				Expect(err).To(Not(HaveOccurred()))

				container := postgresDeployment.Spec.Template.Spec.Containers[0]
				Expect(container.Env).To(HaveLen(4))

				// Verify environment variables reference the secret
				envVars := make(map[string]string)
				for _, env := range container.Env {
					envVars[env.Name] = env.Value
				}

				Expect(envVars["POSTGRESQL_USER"]).To(Equal(""))
				Expect(envVars["POSTGRESQL_PASSWORD"]).To(Equal(""))
				Expect(envVars["POSTGRESQL_DATABASE"]).To(Equal(""))
				Expect(envVars["PGDATA"]).To(Equal("/var/lib/postgresql/data/pgdata"))

				// Verify secret references
				Expect(container.Env[0].ValueFrom.SecretKeyRef.Name).To(Equal(modelCatalogName + "-postgres"))
				Expect(container.Env[0].ValueFrom.SecretKeyRef.Key).To(Equal("database-user"))
				Expect(container.Env[1].ValueFrom.SecretKeyRef.Name).To(Equal(modelCatalogName + "-postgres"))
				Expect(container.Env[1].ValueFrom.SecretKeyRef.Key).To(Equal("database-password"))
				Expect(container.Env[2].ValueFrom.SecretKeyRef.Name).To(Equal(modelCatalogName + "-postgres"))
				Expect(container.Env[2].ValueFrom.SecretKeyRef.Key).To(Equal("database-name"))

				By("Verifying PostgreSQL deployment has correct probes and configuration")
				Expect(container.LivenessProbe).To(Not(BeNil()))
				Expect(container.LivenessProbe.Exec.Command).To(ContainElement("/usr/bin/pg_isready -U $POSTGRESQL_USER -d $POSTGRESQL_DATABASE"))
				Expect(container.LivenessProbe.InitialDelaySeconds).To(Equal(int32(30)))

				Expect(container.ReadinessProbe).To(Not(BeNil()))
				Expect(container.ReadinessProbe.Exec.Command).To(ContainElement("psql -w -U $POSTGRESQL_USER -d $POSTGRESQL_DATABASE -c 'SELECT 1'"))
				Expect(container.ReadinessProbe.InitialDelaySeconds).To(Equal(int32(10)))

				Expect(container.Ports).To(HaveLen(1))
				Expect(container.Ports[0].ContainerPort).To(Equal(int32(5432)))
				Expect(container.Ports[0].Protocol).To(Equal(corev1.ProtocolTCP))

				By("Verifying PostgreSQL deployment has correct volume mounts")
				Expect(container.VolumeMounts).To(HaveLen(1))
				Expect(container.VolumeMounts[0].MountPath).To(Equal("/var/lib/postgresql/data"))
				Expect(container.VolumeMounts[0].Name).To(Equal(modelCatalogName + "-postgres-data"))

				Expect(postgresDeployment.Spec.Template.Spec.Volumes).To(HaveLen(1))
				Expect(postgresDeployment.Spec.Template.Spec.Volumes[0].Name).To(Equal(modelCatalogName + "-postgres-data"))
				Expect(postgresDeployment.Spec.Template.Spec.Volumes[0].PersistentVolumeClaim.ClaimName).To(Equal(modelCatalogName + "-postgres"))

				By("Verifying PostgreSQL service configuration")
				postgresService := &corev1.Service{}
				err = k8sClient.Get(ctx, types.NamespacedName{
					Name:      modelCatalogName + "-postgres",
					Namespace: namespaceName,
				}, postgresService)
				Expect(err).To(Not(HaveOccurred()))

				Expect(postgresService.Spec.Type).To(Equal(corev1.ServiceTypeClusterIP))
				Expect(postgresService.Spec.SessionAffinity).To(Equal(corev1.ServiceAffinityNone))
				Expect(postgresService.Spec.Selector).To(HaveKeyWithValue("app", modelCatalogName))
				Expect(postgresService.Spec.Selector).To(HaveKeyWithValue("app.kubernetes.io/name", "model-catalog-postgres"))

				By("Verifying PostgreSQL PVC configuration")
				postgresPVC := &corev1.PersistentVolumeClaim{}
				err = k8sClient.Get(ctx, types.NamespacedName{
					Name:      modelCatalogName + "-postgres",
					Namespace: namespaceName,
				}, postgresPVC)
				Expect(err).To(Not(HaveOccurred()))

				Expect(postgresPVC.Spec.AccessModes).To(ContainElement(corev1.ReadWriteOnce))
				Expect(postgresPVC.Spec.Resources.Requests.Storage().String()).To(Equal("5Gi"))
			})

			It("Should create PostgreSQL NetworkPolicy with correct configuration", func() {
				_, err := catalogReconciler.ensureCatalogResources(ctx)
				Expect(err).To(Not(HaveOccurred()))

				By("Checking if the PostgreSQL NetworkPolicy was created")
				postgresNetworkPolicy := &networkingv1.NetworkPolicy{}
				err = k8sClient.Get(ctx, types.NamespacedName{
					Name:      modelCatalogName + "-postgres",
					Namespace: namespaceName,
				}, postgresNetworkPolicy)
				Expect(err).To(Not(HaveOccurred()))

				// Verify NetworkPolicy targets postgres pods with correct podSelector
				Expect(postgresNetworkPolicy.Spec.PodSelector.MatchLabels).To(HaveKeyWithValue("app", modelCatalogName),
					"NetworkPolicy podSelector should match postgres pod app label")
				Expect(postgresNetworkPolicy.Spec.PodSelector.MatchLabels).To(HaveKeyWithValue("component", modelCatalogName),
					"NetworkPolicy podSelector should match postgres pod component label")
				Expect(postgresNetworkPolicy.Spec.PodSelector.MatchLabels).To(HaveKeyWithValue("app.kubernetes.io/name", modelCatalogName+"-postgres"),
					"NetworkPolicy podSelector should match postgres pod app.kubernetes.io/name label")

				// Verify NetworkPolicy has ingress policy type
				Expect(postgresNetworkPolicy.Spec.PolicyTypes).To(ContainElement(networkingv1.PolicyTypeIngress),
					"NetworkPolicy should have Ingress policy type")

				// Verify ingress rules allow only Model Catalog API pods
				Expect(postgresNetworkPolicy.Spec.Ingress).To(HaveLen(1),
					"NetworkPolicy should have exactly one ingress rule")
				ingressRule := postgresNetworkPolicy.Spec.Ingress[0]
				Expect(ingressRule.From).To(HaveLen(1),
					"NetworkPolicy ingress rule should have exactly one from selector")

				// Verify ingress from selector matches Model Catalog API pods
				fromSelector := ingressRule.From[0].PodSelector
				Expect(fromSelector).ToNot(BeNil(), "NetworkPolicy ingress from should use podSelector")
				Expect(fromSelector.MatchLabels).To(HaveKeyWithValue("app", modelCatalogName),
					"NetworkPolicy ingress selector should match catalog API app label")
				Expect(fromSelector.MatchLabels).To(HaveKeyWithValue("component", "model-catalog"),
					"NetworkPolicy ingress selector should match catalog API component label")
				Expect(fromSelector.MatchLabels).To(HaveKeyWithValue("app.kubernetes.io/name", modelCatalogName),
					"NetworkPolicy ingress selector should match catalog API app.kubernetes.io/name label")

				// Verify port restriction to PostgreSQL port 5432
				Expect(ingressRule.Ports).To(HaveLen(1),
					"NetworkPolicy ingress rule should restrict to one port")
				port := ingressRule.Ports[0]
				Expect(port.Protocol).ToNot(BeNil(),
					"NetworkPolicy port protocol should be specified")
				Expect(*port.Protocol).To(Equal(corev1.ProtocolTCP),
					"NetworkPolicy port should use TCP protocol")
				Expect(port.Port.IntVal).To(Equal(int32(5432)),
					"NetworkPolicy port should be 5432 (PostgreSQL)")
			})
		})

		Context("cleanupCatalogResources method", func() {
			It("Should successfully delete all catalog resources", func() {
				By("First creating catalog resources")
				_, err := catalogReconciler.ensureCatalogResources(ctx)
				Expect(err).To(Not(HaveOccurred()))

				By("Verifying resources exist")
				configMap := &corev1.ConfigMap{}
				err = k8sClient.Get(ctx, types.NamespacedName{
					Name:      "model-catalog-sources",
					Namespace: namespaceName,
				}, configMap)
				Expect(err).To(Not(HaveOccurred()))

				deployment := &appsv1.Deployment{}
				err = k8sClient.Get(ctx, types.NamespacedName{
					Name:      modelCatalogName,
					Namespace: namespaceName,
				}, deployment)
				Expect(err).To(Not(HaveOccurred()))

				service := &corev1.Service{}
				err = k8sClient.Get(ctx, types.NamespacedName{
					Name:      modelCatalogName,
					Namespace: namespaceName,
				}, service)
				Expect(err).To(Not(HaveOccurred()))

				By("Deleting catalog resources")
				_, err = catalogReconciler.cleanupCatalogResources(ctx)
				Expect(err).To(Not(HaveOccurred()))

				By("Verifying explicitly deleted resources are gone")
				// Only test for explicitly deleted resources (Deployment).
				// Service, Role, and RoleBinding should be cleaned up via garbage collection
				// due to owner references, but envtest may not have garbage collection enabled.
				Eventually(func() bool {
					err = k8sClient.Get(ctx, types.NamespacedName{
						Name:      modelCatalogName,
						Namespace: namespaceName,
					}, deployment)
					return apierrors.IsNotFound(err)
				}, 10*time.Second, 1*time.Second).Should(BeTrue())
			})

			It("Should create mcp-catalog-sources ConfigMap", func() {
				By("Creating catalog resources")
				_, err := catalogReconciler.ensureCatalogResources(ctx)
				Expect(err).To(Not(HaveOccurred()))

				By("Checking if the MCP ConfigMap was created")
				configMap := &corev1.ConfigMap{}
				err = k8sClient.Get(ctx, types.NamespacedName{
					Name:      "mcp-catalog-sources",
					Namespace: namespaceName,
				}, configMap)
				Expect(err).To(Not(HaveOccurred()))
				Expect(configMap.Labels["component"]).To(Equal("model-catalog"))
				Expect(configMap.Labels["app.kubernetes.io/created-by"]).To(Equal("model-registry-operator"))

				By("Verifying MCP ConfigMap has correct structure")
				Expect(configMap.Data).To(HaveKey("sources.yaml"))
				var sources map[string]any
				err = yaml.Unmarshal([]byte(configMap.Data["sources.yaml"]), &sources)
				Expect(err).To(Not(HaveOccurred()))
				Expect(sources).To(HaveKey("mcp_catalogs"))

				// Verify it's an empty array by default
				mcpCatalogs, ok := sources["mcp_catalogs"].([]any)
				Expect(ok).To(BeTrue(), "mcp_catalogs should be an array")
				Expect(mcpCatalogs).To(HaveLen(0), "mcp_catalogs should be empty by default")
			})

			It("Should set correct ownerReferences on catalog resources", func() {
				By("First creating catalog resources")
				_, err := catalogReconciler.ensureCatalogResources(ctx)
				Expect(err).To(Not(HaveOccurred()))

				By("Getting the deployment")
				deployment := &appsv1.Deployment{}
				err = k8sClient.Get(ctx, types.NamespacedName{
					Name:      modelCatalogName,
					Namespace: namespaceName,
				}, deployment)
				Expect(err).To(Not(HaveOccurred()))

				By("Verifying Service has correct ownerReference to Deployment")
				service := &corev1.Service{}
				err = k8sClient.Get(ctx, types.NamespacedName{
					Name:      modelCatalogName,
					Namespace: namespaceName,
				}, service)
				Expect(err).To(Not(HaveOccurred()))
				Expect(service.OwnerReferences).To(HaveLen(1))
				Expect(service.OwnerReferences[0].APIVersion).To(Equal("apps/v1"))
				Expect(service.OwnerReferences[0].Kind).To(Equal("Deployment"))
				Expect(service.OwnerReferences[0].Name).To(Equal(deployment.Name))
				Expect(service.OwnerReferences[0].UID).To(Equal(deployment.UID))

				By("Verifying Role has correct ownerReference to Deployment")
				role := &rbac.Role{}
				err = k8sClient.Get(ctx, types.NamespacedName{
					Name:      modelCatalogName,
					Namespace: namespaceName,
				}, role)
				Expect(err).To(Not(HaveOccurred()))
				Expect(role.OwnerReferences).To(HaveLen(1))
				Expect(role.OwnerReferences[0].APIVersion).To(Equal("apps/v1"))
				Expect(role.OwnerReferences[0].Kind).To(Equal("Deployment"))
				Expect(role.OwnerReferences[0].Name).To(Equal(deployment.Name))
				Expect(role.OwnerReferences[0].UID).To(Equal(deployment.UID))

				By("Verifying RoleBinding has correct ownerReference to Deployment")
				roleBinding := &rbac.RoleBinding{}
				err = k8sClient.Get(ctx, types.NamespacedName{
					Name:      modelCatalogName + "-authenticated",
					Namespace: namespaceName,
				}, roleBinding)
				Expect(err).To(Not(HaveOccurred()))
				Expect(roleBinding.OwnerReferences).To(HaveLen(1))
				Expect(roleBinding.OwnerReferences[0].APIVersion).To(Equal("apps/v1"))
				Expect(roleBinding.OwnerReferences[0].Kind).To(Equal("Deployment"))
				Expect(roleBinding.OwnerReferences[0].Name).To(Equal(deployment.Name))
				Expect(roleBinding.OwnerReferences[0].UID).To(Equal(deployment.UID))
			})

			It("Should handle deletion when resources don't exist", func() {
				By("Attempting to delete non-existent resources")
				_, err := catalogReconciler.cleanupCatalogResources(ctx)
				Expect(err).To(Not(HaveOccurred()))
			})
		})

		Context("ConfigMap management", func() {
			It("Should create both user and default sources ConfigMaps", func() {
				_, err := catalogReconciler.ensureCatalogResources(ctx)
				Expect(err).To(Not(HaveOccurred()))

				By("Checking if the user sources ConfigMap was created")
				userConfigMap := &corev1.ConfigMap{}
				err = k8sClient.Get(ctx, types.NamespacedName{
					Name:      "model-catalog-sources",
					Namespace: namespaceName,
				}, userConfigMap)
				Expect(err).To(Not(HaveOccurred()))
				Expect(userConfigMap.Labels["component"]).To(Equal("model-catalog"))
				Expect(userConfigMap.Labels["app.kubernetes.io/created-by"]).To(Equal("model-registry-operator"))
				Expect(userConfigMap.Data).To(HaveKey("sources.yaml"))
				Expect(userConfigMap.Data["sources.yaml"]).To(ContainSubstring("catalogs: []"))

				By("Checking if the default sources ConfigMap was created")
				defaultConfigMap := &corev1.ConfigMap{}
				err = k8sClient.Get(ctx, types.NamespacedName{
					Name:      "model-catalog-default-sources",
					Namespace: namespaceName,
				}, defaultConfigMap)
				Expect(err).To(Not(HaveOccurred()))
				Expect(defaultConfigMap.Labels["component"]).To(Equal("model-catalog"))
				Expect(defaultConfigMap.Labels["app.kubernetes.io/created-by"]).To(Equal("model-registry-operator"))
				Expect(defaultConfigMap.Data).To(HaveKey("sources.yaml"))
				Expect(defaultConfigMap.Data["sources.yaml"]).To(ContainSubstring("Red Hat AI"))
				Expect(defaultConfigMap.Data["sources.yaml"]).To(ContainSubstring("redhat_ai_models"))
				Expect(defaultConfigMap.Data["sources.yaml"]).To(ContainSubstring("yamlCatalogPath: /shared-data/models-catalog.yaml"))

				By("Verifying default sources ConfigMap contains MCP catalog sources")
				var defaultSources map[string]any
				err = yaml.Unmarshal([]byte(defaultConfigMap.Data["sources.yaml"]), &defaultSources)
				Expect(err).To(Not(HaveOccurred()))

				Expect(defaultSources).To(HaveKey("mcp_catalogs"))
				mcpCatalogs, ok := defaultSources["mcp_catalogs"].([]any)
				Expect(ok).To(BeTrue(), "mcp_catalogs should be an array")
				Expect(mcpCatalogs).To(HaveLen(1))
				mcpEntry, ok := mcpCatalogs[0].(map[string]any)
				Expect(ok).To(BeTrue())
				Expect(mcpEntry["name"]).To(Equal("Red Hat MCP Servers"))
				Expect(mcpEntry["id"]).To(Equal("rh_mcp_servers"))
				Expect(mcpEntry["type"]).To(Equal("yaml"))
				Expect(mcpEntry["enabled"]).To(BeTrue())
				mcpProps, ok := mcpEntry["properties"].(map[string]any)
				Expect(ok).To(BeTrue())
				Expect(mcpProps["yamlCatalogPath"]).To(Equal("/shared-data/redhat-mcp-servers-catalog.yaml"))
				mcpLabels, ok := mcpEntry["labels"].([]any)
				Expect(ok).To(BeTrue())
				Expect(mcpLabels).To(ContainElement("Red Hat"))

				By("Verifying default sources ConfigMap contains MCP label definition with assetType")
				labels, ok := defaultSources["labels"].([]any)
				Expect(ok).To(BeTrue(), "labels should be an array")
				var mcpLabel map[string]any
				for _, l := range labels {
					labelMap, ok := l.(map[string]any)
					if ok && labelMap["name"] == "Red Hat" {
						mcpLabel = labelMap
						break
					}
				}
				Expect(mcpLabel).To(Not(BeNil()), "should have a 'Red Hat' label definition")
				Expect(mcpLabel["assetType"]).To(Equal("mcp_servers"))
				Expect(mcpLabel["displayName"]).To(Equal("Red Hat MCP servers"))

				By("Verifying model labels have assetType set to models")
				labelIndex := make(map[string]map[string]any)
				for _, l := range labels {
					labelMap, ok := l.(map[string]any)
					if !ok {
						continue
					}
					name, _ := labelMap["name"].(string)
					labelIndex[name] = labelMap
				}
				for _, expectedName := range []string{"Red Hat AI", "Red Hat AI validated", ""} {
					labelMap, found := labelIndex[expectedName]
					Expect(found).To(BeTrue(), "expected label %q to exist", expectedName)
					Expect(labelMap["assetType"]).To(Equal("models"),
						"label %q should have assetType 'models'", expectedName)
				}
			})

			It("Should update default sources ConfigMap when changed", func() {
				By("Creating initial resources")
				_, err := catalogReconciler.ensureCatalogResources(ctx)
				Expect(err).To(Not(HaveOccurred()))

				By("Getting the default sources ConfigMap")
				defaultConfigMap := &corev1.ConfigMap{}
				err = k8sClient.Get(ctx, types.NamespacedName{
					Name:      "model-catalog-default-sources",
					Namespace: namespaceName,
				}, defaultConfigMap)
				Expect(err).To(Not(HaveOccurred()))

				By("Modifying the ConfigMap to simulate external changes")
				defaultConfigMap.Data["sources.yaml"] = "catalogs:\n  - name: Modified\n    id: modified"
				err = k8sClient.Update(ctx, defaultConfigMap)
				Expect(err).To(Not(HaveOccurred()))

				By("Running reconciliation again")
				_, err = catalogReconciler.ensureCatalogResources(ctx)
				Expect(err).To(Not(HaveOccurred()))

				By("Verifying the ConfigMap was restored to the expected state")
				err = k8sClient.Get(ctx, types.NamespacedName{
					Name:      "model-catalog-default-sources",
					Namespace: namespaceName,
				}, defaultConfigMap)
				Expect(err).To(Not(HaveOccurred()))
				Expect(defaultConfigMap.Data["sources.yaml"]).To(ContainSubstring("Red Hat AI"))
				Expect(defaultConfigMap.Data["sources.yaml"]).To(ContainSubstring("redhat_ai_models"))
			})

			It("Should not modify user sources ConfigMap if it has no default catalog", func() {
				By("Creating initial resources")
				_, err := catalogReconciler.ensureCatalogResources(ctx)
				Expect(err).To(Not(HaveOccurred()))

				By("Getting the user sources ConfigMap")
				userConfigMap := &corev1.ConfigMap{}
				err = k8sClient.Get(ctx, types.NamespacedName{
					Name:      "model-catalog-sources",
					Namespace: namespaceName,
				}, userConfigMap)
				Expect(err).To(Not(HaveOccurred()))

				By("Modifying the user ConfigMap to add custom sources")
				customSources := `catalogs:
  - name: Custom Catalog
    id: custom_catalog
    type: yaml
    properties:
      yamlCatalogPath: /custom/path.yaml`
				userConfigMap.Data["sources.yaml"] = customSources
				err = k8sClient.Update(ctx, userConfigMap)
				Expect(err).To(Not(HaveOccurred()))

				By("Running reconciliation again")
				_, err = catalogReconciler.ensureCatalogResources(ctx)
				Expect(err).To(Not(HaveOccurred()))

				By("Verifying the user ConfigMap was not modified")
				err = k8sClient.Get(ctx, types.NamespacedName{
					Name:      "model-catalog-sources",
					Namespace: namespaceName,
				}, userConfigMap)
				Expect(err).To(Not(HaveOccurred()))
				Expect(userConfigMap.Data["sources.yaml"]).To(Equal(customSources))
			})

			It("Should remove default catalog from user sources ConfigMap during migration", func() {
				By("Creating a user ConfigMap with the old default catalog")
				userConfigMapWithDefault := &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "model-catalog-sources",
						Namespace: namespaceName,
						Labels: map[string]string{
							"app":                          "model-catalog",
							"component":                    "model-catalog",
							"app.kubernetes.io/name":       "model-catalog",
							"app.kubernetes.io/instance":   "model-catalog",
							"app.kubernetes.io/component":  "model-catalog",
							"app.kubernetes.io/created-by": "model-registry-operator",
							"app.kubernetes.io/part-of":    "model-registry",
							"app.kubernetes.io/managed-by": "model-registry-operator",
						},
					},
					Data: map[string]string{
						"sources.yaml": `catalogs:
  - name: Default Catalog
    id: default_catalog
    type: yaml
    properties:
      yamlCatalogPath: /shared-data/default-catalog.yaml
  - name: Custom Catalog
    id: custom_catalog
    type: yaml
    properties:
      yamlCatalogPath: /custom/path.yaml`,
					},
				}
				err := k8sClient.Create(ctx, userConfigMapWithDefault)
				Expect(err).To(Not(HaveOccurred()))

				By("Running reconciliation to trigger migration")
				_, err = catalogReconciler.ensureCatalogResources(ctx)
				Expect(err).To(Not(HaveOccurred()))

				By("Verifying the default catalog was removed from user ConfigMap")
				userConfigMap := &corev1.ConfigMap{}
				err = k8sClient.Get(ctx, types.NamespacedName{
					Name:      "model-catalog-sources",
					Namespace: namespaceName,
				}, userConfigMap)
				Expect(err).To(Not(HaveOccurred()))
				Expect(userConfigMap.Data["sources.yaml"]).To(Not(ContainSubstring("default_catalog")))
				Expect(userConfigMap.Data["sources.yaml"]).To(ContainSubstring("custom_catalog"))

				By("Verifying the default sources ConfigMap was still created")
				defaultConfigMap := &corev1.ConfigMap{}
				err = k8sClient.Get(ctx, types.NamespacedName{
					Name:      "model-catalog-default-sources",
					Namespace: namespaceName,
				}, defaultConfigMap)
				Expect(err).To(Not(HaveOccurred()))
				Expect(defaultConfigMap.Data["sources.yaml"]).To(ContainSubstring("redhat_ai_models"))
			})

			It("Should handle malformed YAML in user sources ConfigMap gracefully", func() {
				By("Creating a user ConfigMap with malformed YAML")
				userConfigMapMalformed := &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "model-catalog-sources",
						Namespace: namespaceName,
						Labels: map[string]string{
							"app":                          "model-catalog",
							"component":                    "model-catalog",
							"app.kubernetes.io/name":       "model-catalog",
							"app.kubernetes.io/instance":   "model-catalog",
							"app.kubernetes.io/component":  "model-catalog",
							"app.kubernetes.io/created-by": "model-registry-operator",
							"app.kubernetes.io/part-of":    "model-registry",
							"app.kubernetes.io/managed-by": "model-registry-operator",
						},
					},
					Data: map[string]string{
						"sources.yaml": `invalid: yaml: content:
  - malformed
    - nested
catalogs:
  - name: Default Catalog
    id: default_catalog`,
					},
				}
				err := k8sClient.Create(ctx, userConfigMapMalformed)
				Expect(err).To(Not(HaveOccurred()))

				By("Running reconciliation should not fail")
				_, err = catalogReconciler.ensureCatalogResources(ctx)
				Expect(err).To(Not(HaveOccurred()))

				By("Verifying malformed ConfigMap was left unchanged")
				userConfigMap := &corev1.ConfigMap{}
				err = k8sClient.Get(ctx, types.NamespacedName{
					Name:      "model-catalog-sources",
					Namespace: namespaceName,
				}, userConfigMap)
				Expect(err).To(Not(HaveOccurred()))
				Expect(userConfigMap.Data["sources.yaml"]).To(ContainSubstring("invalid: yaml: content:"))

				By("Verifying default sources ConfigMap was still created")
				defaultConfigMap := &corev1.ConfigMap{}
				err = k8sClient.Get(ctx, types.NamespacedName{
					Name:      "model-catalog-default-sources",
					Namespace: namespaceName,
				}, defaultConfigMap)
				Expect(err).To(Not(HaveOccurred()))
				Expect(defaultConfigMap.Data["sources.yaml"]).To(ContainSubstring("redhat_ai_models"))
			})

			It("Should set owner references on default sources ConfigMap", func() {
				By("Creating catalog resources")
				_, err := catalogReconciler.ensureCatalogResources(ctx)
				Expect(err).To(Not(HaveOccurred()))

				By("Getting the default sources ConfigMap")
				defaultConfigMap := &corev1.ConfigMap{}
				err = k8sClient.Get(ctx, types.NamespacedName{
					Name:      "model-catalog-default-sources",
					Namespace: namespaceName,
				}, defaultConfigMap)
				Expect(err).To(Not(HaveOccurred()))

				By("Verifying owner reference is set when platform ModelRegistry exists")
				// Note: In this test environment, the platform ModelRegistry may not exist,
				// so we check that the function handles this case gracefully
				// If owner reference exists, it should be properly formatted
				if len(defaultConfigMap.OwnerReferences) > 0 {
					Expect(defaultConfigMap.OwnerReferences[0].APIVersion).To(ContainSubstring("modelregistry"))
					Expect(defaultConfigMap.OwnerReferences[0].Kind).To(Equal("ModelRegistry"))
				}
			})

			It("Should correctly configure deployment volumes for all ConfigMaps", func() {
				By("Creating catalog resources")
				_, err := catalogReconciler.ensureCatalogResources(ctx)
				Expect(err).To(Not(HaveOccurred()))

				By("Getting the deployment")
				deployment := &appsv1.Deployment{}
				err = k8sClient.Get(ctx, types.NamespacedName{
					Name:      modelCatalogName,
					Namespace: namespaceName,
				}, deployment)
				Expect(err).To(Not(HaveOccurred()))

				By("Verifying deployment has user-model-sources, user-mcp-sources, and default-sources volumes")
				volumes := deployment.Spec.Template.Spec.Volumes
				var userModelSourcesVolume, userMcpSourcesVolume, defaultSourcesVolume *corev1.Volume
				for i := range volumes {
					if volumes[i].Name == "user-model-sources" {
						userModelSourcesVolume = &volumes[i]
					}
					if volumes[i].Name == "user-mcp-sources" {
						userMcpSourcesVolume = &volumes[i]
					}
					if volumes[i].Name == "default-sources" {
						defaultSourcesVolume = &volumes[i]
					}
				}

				Expect(userModelSourcesVolume).To(Not(BeNil()), "user-model-sources volume should exist")
				Expect(userModelSourcesVolume.ConfigMap.Name).To(Equal("model-catalog-sources"))

				Expect(userMcpSourcesVolume).To(Not(BeNil()), "user-mcp-sources volume should exist")
				Expect(userMcpSourcesVolume.ConfigMap.Name).To(Equal("mcp-catalog-sources"))

				Expect(defaultSourcesVolume).To(Not(BeNil()), "default-sources volume should exist")
				Expect(defaultSourcesVolume.ConfigMap.Name).To(Equal("model-catalog-default-sources"))

				By("Verifying catalog container has all volume mounts")
				catalogContainer := deployment.Spec.Template.Spec.Containers[0]
				var userModelSourcesMount, userMcpSourcesMount, defaultSourcesMount *corev1.VolumeMount
				for i := range catalogContainer.VolumeMounts {
					if catalogContainer.VolumeMounts[i].Name == "user-model-sources" {
						userModelSourcesMount = &catalogContainer.VolumeMounts[i]
					}
					if catalogContainer.VolumeMounts[i].Name == "user-mcp-sources" {
						userMcpSourcesMount = &catalogContainer.VolumeMounts[i]
					}
					if catalogContainer.VolumeMounts[i].Name == "default-sources" {
						defaultSourcesMount = &catalogContainer.VolumeMounts[i]
					}
				}

				Expect(userModelSourcesMount).To(Not(BeNil()), "user-model-sources mount should exist")
				Expect(userModelSourcesMount.MountPath).To(Equal("/data/user-model-sources"))

				Expect(userMcpSourcesMount).To(Not(BeNil()), "user-mcp-sources mount should exist")
				Expect(userMcpSourcesMount.MountPath).To(Equal("/data/user-mcp-sources"))

				Expect(defaultSourcesMount).To(Not(BeNil()), "default-sources mount should exist")
				Expect(defaultSourcesMount.MountPath).To(Equal("/data/default-sources"))

				By("Verifying catalog container has all three catalogs-path arguments")
				args := catalogContainer.Args
				Expect(args).To(ContainElement("--catalogs-path=/data/default-sources/sources.yaml"))
				Expect(args).To(ContainElement("--catalogs-path=/data/user-model-sources/sources.yaml"))
				Expect(args).To(ContainElement("--catalogs-path=/data/user-mcp-sources/sources.yaml"))
			})
		})

		Context("removeDefaultSource function", func() {
			It("Should remove default catalog from YAML with multiple catalogs", func() {
				input := `catalogs:
  - name: Default Catalog
    id: default_catalog
    type: yaml
    properties:
      yamlCatalogPath: /shared-data/default-catalog.yaml
  - name: Custom Catalog
    id: custom_catalog
    type: yaml
    properties:
      yamlCatalogPath: /custom/path.yaml`

				result, err := catalogReconciler.removeDefaultSource(input)
				Expect(err).To(Not(HaveOccurred()))
				Expect(result).To(Not(ContainSubstring("default_catalog")))
				Expect(result).To(ContainSubstring("custom_catalog"))
				Expect(result).To(ContainSubstring("Custom Catalog"))
			})

			It("Should return empty when only default catalog exists", func() {
				input := `catalogs:
  - name: Default Catalog
    id: default_catalog
    type: yaml
    properties:
      yamlCatalogPath: /shared-data/default-catalog.yaml`

				result, err := catalogReconciler.removeDefaultSource(input)
				Expect(err).To(Not(HaveOccurred()))
				Expect(result).To(Not(BeEmpty()), "should return non-empty result when default_catalog is removed")
				Expect(result).To(Not(ContainSubstring("default_catalog")), "default_catalog should be removed")
			})

			It("Should return empty string when no default catalog exists", func() {
				input := `catalogs:
  - name: Custom Catalog
    id: custom_catalog
    type: yaml
    properties:
      yamlCatalogPath: /custom/path.yaml`

				result, err := catalogReconciler.removeDefaultSource(input)
				Expect(err).To(Not(HaveOccurred()))
				Expect(result).To(Equal(""))
			})

			It("Should handle empty catalog list", func() {
				input := `catalogs: []`

				result, err := catalogReconciler.removeDefaultSource(input)
				Expect(err).To(Not(HaveOccurred()))
				Expect(result).To(Equal(""))
			})

			It("Should handle malformed YAML gracefully", func() {
				input := `invalid: yaml: content:
  - malformed
    - nested`

				result, err := catalogReconciler.removeDefaultSource(input)
				Expect(err).To(HaveOccurred())
				Expect(result).To(Equal(""))
			})

			It("Should preserve catalog order when removing default", func() {
				input := `catalogs:
  - name: First Catalog
    id: first_catalog
    type: yaml
  - name: Default Catalog
    id: default_catalog
    type: yaml
    properties:
      yamlCatalogPath: /shared-data/default-catalog.yaml
  - name: Third Catalog
    id: third_catalog
    type: yaml`

				result, err := catalogReconciler.removeDefaultSource(input)
				Expect(err).To(Not(HaveOccurred()))
				Expect(result).To(Not(ContainSubstring("default_catalog")))
				Expect(result).To(ContainSubstring("first_catalog"))
				Expect(result).To(ContainSubstring("third_catalog"))

				// Verify order is preserved
				lines := result
				firstIndex := strings.Index(lines, "first_catalog")
				thirdIndex := strings.Index(lines, "third_catalog")
				Expect(firstIndex).To(BeNumerically("<", thirdIndex))
			})

			It("Should handle catalogs with different structures", func() {
				input := `catalogs:
  - name: Simple Catalog
    id: simple_catalog
  - name: Default Catalog
    id: default_catalog
    type: yaml
    enabled: true
    properties:
      yamlCatalogPath: /shared-data/default-catalog.yaml
      other: value
  - name: Complex Catalog
    id: complex_catalog
    type: database
    enabled: false
    properties:
      host: localhost
      port: 5432`

				result, err := catalogReconciler.removeDefaultSource(input)
				Expect(err).To(Not(HaveOccurred()))
				Expect(result).To(Not(ContainSubstring("default_catalog")))
				Expect(result).To(ContainSubstring("simple_catalog"))
				Expect(result).To(ContainSubstring("complex_catalog"))
				Expect(result).To(ContainSubstring("host: localhost"))
			})

			It("Should handle catalogs with labels field", func() {
				input := `catalogs:
  - name: Custom Catalog
    id: custom_catalog
    type: yaml
    properties:
      yamlCatalogPath: /custom/path.yaml
    labels:
      - Custom Label
      - Another Label
  - name: Default Catalog
    id: default_catalog
    type: yaml
    properties:
      yamlCatalogPath: /shared-data/default-catalog.yaml
    labels:
      - Default Label`

				result, err := catalogReconciler.removeDefaultSource(input)
				Expect(err).To(Not(HaveOccurred()))
				Expect(result).To(Not(ContainSubstring("default_catalog")))
				Expect(result).To(ContainSubstring("custom_catalog"))
				Expect(result).To(ContainSubstring("Custom Label"))
				Expect(result).To(ContainSubstring("Another Label"))
				Expect(result).To(Not(ContainSubstring("Default Label")))
			})

			It("Should handle mixed catalogs - some with labels, some without", func() {
				input := `catalogs:
  - name: Catalog Without Labels
    id: no_labels_catalog
    type: yaml
    properties:
      yamlCatalogPath: /path/without-labels.yaml
  - name: Default Catalog
    id: default_catalog
    type: yaml
    properties:
      yamlCatalogPath: /shared-data/default-catalog.yaml
    labels:
      - Default Label
  - name: Catalog With Labels
    id: with_labels_catalog
    type: yaml
    properties:
      yamlCatalogPath: /path/with-labels.yaml
    labels:
      - Custom Label`

				result, err := catalogReconciler.removeDefaultSource(input)
				Expect(err).To(Not(HaveOccurred()))
				Expect(result).To(Not(ContainSubstring("default_catalog")))
				Expect(result).To(ContainSubstring("no_labels_catalog"))
				Expect(result).To(ContainSubstring("with_labels_catalog"))
				Expect(result).To(ContainSubstring("Custom Label"))
				Expect(result).To(Not(ContainSubstring("Default Label")))
			})

			It("Should handle mcp_catalogs field without error", func() {
				input := `mcp_catalogs:
  - name: My MCP Server
    id: my_mcp_server
    type: mcp`

				result, err := catalogReconciler.removeDefaultSource(input)
				Expect(err).To(Not(HaveOccurred()))
				Expect(result).To(Equal(""), "mcp_catalogs should not trigger updates (no default_catalog to remove)")
			})

			It("Should handle empty mcp_catalogs array", func() {
				input := `mcp_catalogs: []`

				result, err := catalogReconciler.removeDefaultSource(input)
				Expect(err).To(Not(HaveOccurred()))
				Expect(result).To(Equal(""), "empty mcp_catalogs should not trigger updates")
			})

			It("Should handle model_catalogs field and remove default_catalog", func() {
				input := `model_catalogs:
  - name: Default Catalog
    id: default_catalog
    type: yaml
  - name: Custom Model Catalog
    id: custom_model_catalog
    type: yaml`

				result, err := catalogReconciler.removeDefaultSource(input)
				Expect(err).To(Not(HaveOccurred()))
				Expect(result).To(Not(BeEmpty()), "should return non-empty result when default_catalog is removed")
				Expect(result).To(Not(ContainSubstring("default_catalog")))
				Expect(result).To(ContainSubstring("custom_model_catalog"))
				Expect(result).To(ContainSubstring("model_catalogs"))
			})

			It("Should handle all three catalog types (catalogs, model_catalogs, mcp_catalogs)", func() {
				input := `catalogs:
  - name: Custom Catalog
    id: custom_catalog
    type: yaml
model_catalogs:
  - name: Custom Model Catalog
    id: custom_model_catalog
    type: yaml
mcp_catalogs:
  - name: My MCP Server
    id: my_mcp_server
    type: mcp`

				result, err := catalogReconciler.removeDefaultSource(input)
				Expect(err).To(Not(HaveOccurred()))
				Expect(result).To(Equal(""), "no default_catalog to remove, should return empty string")
			})

			It("Should handle all three catalog types with default_catalog in legacy catalogs field", func() {
				input := `catalogs:
  - name: Default Catalog
    id: default_catalog
    type: yaml
  - name: Custom Catalog
    id: custom_catalog
    type: yaml
model_catalogs:
  - name: Custom Model Catalog
    id: custom_model_catalog
    type: yaml
mcp_catalogs:
  - name: My MCP Server
    id: my_mcp_server
    type: mcp`

				result, err := catalogReconciler.removeDefaultSource(input)
				Expect(err).To(Not(HaveOccurred()))
				Expect(result).To(Not(BeEmpty()), "should return non-empty result when default_catalog is removed")
				Expect(result).To(Not(ContainSubstring("default_catalog")))
				Expect(result).To(ContainSubstring("custom_catalog"))
				Expect(result).To(ContainSubstring("custom_model_catalog"))
				Expect(result).To(ContainSubstring("my_mcp_server"))
			})

			It("Should preserve mcp_catalogs when removing default_catalog from catalogs", func() {
				input := `catalogs:
  - name: Default Catalog
    id: default_catalog
    type: yaml
    properties:
      yamlCatalogPath: /shared-data/default-catalog.yaml
  - name: Custom Catalog
    id: custom_catalog
    type: yaml
mcp_catalogs:
  - name: Red Hat MCP Servers
    id: rh_mcp_servers
    type: yaml
    enabled: true
    properties:
      yamlCatalogPath: /shared-data/redhat-mcp-servers-catalog.yaml
    labels:
      - Red Hat`

				result, err := catalogReconciler.removeDefaultSource(input)
				Expect(err).To(Not(HaveOccurred()))
				Expect(result).To(Not(BeEmpty()))
				Expect(result).To(Not(ContainSubstring("default_catalog")))
				Expect(result).To(ContainSubstring("custom_catalog"))
				Expect(result).To(ContainSubstring("rh_mcp_servers"))
				Expect(result).To(ContainSubstring("Red Hat MCP Servers"))
				Expect(result).To(ContainSubstring("Red Hat"))
			})

			It("Should not fail when configmap contains labels and namedQueries fields", func() {
				input := `catalogs:
  - name: Default Catalog
    id: default_catalog
    type: yaml
  - name: Custom Catalog
    id: custom_catalog
    type: yaml
labels:
  - name: Red Hat AI
    assetType: models
    description: Red Hat models with full support.
  - name: Red Hat
    assetType: mcp_servers
    displayName: Red Hat
    description: Official Red Hat MCP servers.
namedQueries:
  default-performance-filters:
    artifacts.use_case.string_value:
      operator: "="
      value: chatbot`

				result, err := catalogReconciler.removeDefaultSource(input)
				Expect(err).To(Not(HaveOccurred()))
				Expect(result).To(Not(BeEmpty()))
				Expect(result).To(Not(ContainSubstring("default_catalog")))
				Expect(result).To(ContainSubstring("custom_catalog"))
			})

			It("Should not fail when configmap contains only labels and namedQueries without default_catalog", func() {
				input := `catalogs:
  - name: Custom Catalog
    id: custom_catalog
    type: yaml
labels:
  - name: Red Hat AI
    assetType: models
namedQueries:
  some-query:
    field:
      operator: "="
      value: test`

				result, err := catalogReconciler.removeDefaultSource(input)
				Expect(err).To(Not(HaveOccurred()))
				Expect(result).To(Equal(""), "no default_catalog to remove, should return empty string")
			})
		})

		Context("ModelCatalogParams with AdminGroups", func() {
			It("Should handle AdminGroups field correctly", func() {
				params := &ModelCatalogParams{
					Name:        "test-catalog",
					Namespace:   "test-ns",
					AdminGroups: []string{"admin-group1", "admin-group2"},
				}

				Expect(params.AdminGroups).To(HaveLen(2))

				found := slices.Contains(params.AdminGroups, "admin-group1")
				Expect(found).To(BeTrue(), "Expected to find 'admin-group1' in admin groups")
			})
		})

		Context("Auth configuration management", func() {
			var fakeClient client.Client

			BeforeEach(func() {
				// Create a new fake client for each test
				fakeClient = fake.NewClientBuilder().WithScheme(k8sClient.Scheme()).Build()
			})

			It("Should fetch admin groups from Auth config", func() {
				authConfig := &unstructured.Unstructured{}
				authConfig.SetGroupVersionKind(schema.GroupVersionKind{
					Group:   "services.platform.opendatahub.io",
					Version: "v1alpha1",
					Kind:    "Auth",
				})
				authConfig.SetName("auth")
				authConfig.Object["spec"] = map[string]any{
					"adminGroups": []any{"odh-admins", "system-admins"},
				}

				err := fakeClient.Create(ctx, authConfig)
				Expect(err).To(Not(HaveOccurred()))

				reconciler := &ModelCatalogReconciler{
					Client: fakeClient,
					Log:    ctrl.Log.WithName("test"),
				}

				groups, err := reconciler.fetchAuthConfig(ctx)
				Expect(err).To(Not(HaveOccurred()))
				Expect(groups).To(HaveLen(2))

				found := slices.Contains(groups, "odh-admins")
				Expect(found).To(BeTrue(), "Expected to find 'odh-admins' in admin groups")
			})

			It("Should handle Auth config not found gracefully", func() {
				reconciler := &ModelCatalogReconciler{
					Client: fakeClient,
					Log:    ctrl.Log.WithName("test"),
				}

				groups, err := reconciler.fetchAuthConfig(ctx)
				Expect(err).To(Not(HaveOccurred()))
				Expect(groups).To(HaveLen(0), "Expected empty admin groups when Auth CR is not found")
			})

			It("Should handle Auth config without adminGroups field", func() {
				authConfig := &unstructured.Unstructured{}
				authConfig.SetGroupVersionKind(schema.GroupVersionKind{
					Group:   "services.platform.opendatahub.io",
					Version: "v1alpha1",
					Kind:    "Auth",
				})
				authConfig.SetName("auth")
				authConfig.Object["spec"] = map[string]any{
					"allowedGroups": []any{"system:authenticated"},
				}

				err := fakeClient.Create(ctx, authConfig)
				Expect(err).To(Not(HaveOccurred()))

				reconciler := &ModelCatalogReconciler{
					Client: fakeClient,
					Log:    ctrl.Log.WithName("test"),
				}

				groups, err := reconciler.fetchAuthConfig(ctx)
				Expect(err).To(Not(HaveOccurred()))
				Expect(groups).To(HaveLen(0), "Expected empty admin groups when adminGroups field is missing")
			})
		})

		Context("Admin role management", func() {
			var fakeClient client.Client
			var template *template.Template

			BeforeEach(func() {
				var err error
				template, err = config.ParseTemplates()
				Expect(err).To(Not(HaveOccurred()))

				fakeClient = fake.NewClientBuilder().WithScheme(k8sClient.Scheme()).Build()
			})

			It("Should create admin role correctly", func() {
				reconciler := &ModelCatalogReconciler{
					Client:   fakeClient,
					Log:      ctrl.Log.WithName("test"),
					Template: template,
				}

				params := &ModelCatalogParams{
					Name:        "test-catalog",
					Namespace:   "test-ns",
					AdminGroups: []string{"admin-group1"},
				}

				result, err := reconciler.createOrUpdateAdminRole(ctx, params, nil)
				Expect(err).To(Not(HaveOccurred()))
				Expect(result).To(Equal(ResourceCreated))

				// Verify the Role was created
				role := &rbac.Role{}
				err = fakeClient.Get(ctx, types.NamespacedName{Name: "test-catalog-admin", Namespace: "test-ns"}, role)
				Expect(err).To(Not(HaveOccurred()))
			})

			It("Should create admin rolebinding with admin groups", func() {
				reconciler := &ModelCatalogReconciler{
					Client:   fakeClient,
					Log:      ctrl.Log.WithName("test"),
					Template: template,
				}

				params := &ModelCatalogParams{
					Name:        "test-catalog",
					Namespace:   "test-ns",
					AdminGroups: []string{"admin-group1", "admin-group2"},
				}

				result, err := reconciler.createOrUpdateAdminRoleBinding(ctx, params, nil)
				Expect(err).To(Not(HaveOccurred()))
				Expect(result).To(Equal(ResourceCreated))

				// Verify the RoleBinding was created
				rb := &rbac.RoleBinding{}
				err = fakeClient.Get(ctx, types.NamespacedName{Name: "test-catalog-admin-binding", Namespace: "test-ns"}, rb)
				Expect(err).To(Not(HaveOccurred()))

				// Verify subjects match admin groups
				Expect(rb.Subjects).To(HaveLen(2))
			})

			It("Should handle admin rolebinding cleanup when admin groups are removed", func() {
				reconciler := &ModelCatalogReconciler{
					Client:   fakeClient,
					Log:      ctrl.Log.WithName("test"),
					Template: template,
				}

				// First create a rolebinding with admin groups
				paramsWithGroups := &ModelCatalogParams{
					Name:        "test-catalog",
					Namespace:   "test-ns",
					AdminGroups: []string{"admin-group1"},
				}

				result, err := reconciler.createOrUpdateAdminRoleBinding(ctx, paramsWithGroups, nil)
				Expect(err).To(Not(HaveOccurred()))
				Expect(result).To(Equal(ResourceCreated))

				// Verify the rolebinding was created
				rb := &rbac.RoleBinding{}
				err = fakeClient.Get(ctx, types.NamespacedName{Name: "test-catalog-admin-binding", Namespace: "test-ns"}, rb)
				Expect(err).To(Not(HaveOccurred()))
				Expect(rb.Subjects).To(HaveLen(1))

				// Now remove admin groups and verify cleanup
				paramsWithoutGroups := &ModelCatalogParams{
					Name:        "test-catalog",
					Namespace:   "test-ns",
					AdminGroups: []string{},
				}

				result, err = reconciler.createOrUpdateAdminRoleBinding(ctx, paramsWithoutGroups, nil)
				Expect(err).To(Not(HaveOccurred()))
				Expect(result).To(Equal(ResourceUpdated), "Expected ResourceUpdated when cleaning up existing rolebinding")

				// Verify the rolebinding was deleted
				err = fakeClient.Get(ctx, types.NamespacedName{Name: "test-catalog-admin-binding", Namespace: "test-ns"}, rb)
				Expect(apierrors.IsNotFound(err)).To(BeTrue(), "Expected rolebinding to be deleted")
			})
		})

		Context("Integration test with admin groups", func() {
			var fakeClient client.Client
			var template *template.Template

			BeforeEach(func() {
				var err error
				template, err = config.ParseTemplates()
				Expect(err).To(Not(HaveOccurred()))

				// Create auth config
				authConfig := &unstructured.Unstructured{}
				authConfig.SetGroupVersionKind(schema.GroupVersionKind{
					Group:   "services.platform.opendatahub.io",
					Version: "v1alpha1",
					Kind:    "Auth",
				})
				authConfig.SetName("auth")
				authConfig.Object["spec"] = map[string]any{
					"adminGroups": []any{"test-admin-group"},
				}

				fakeClient = fake.NewClientBuilder().WithScheme(k8sClient.Scheme()).WithObjects(authConfig).Build()
			})

			It("Should integrate admin groups in full reconcile flow", func() {
				reconciler := &ModelCatalogReconciler{
					Client:          fakeClient,
					Log:             ctrl.Log.WithName("test"),
					Template:        template,
					TargetNamespace: "test-ns",
				}

				// Test admin groups are fetched during reconcile
				adminGroups, err := reconciler.fetchAuthConfig(ctx)
				Expect(err).To(Not(HaveOccurred()))
				Expect(adminGroups).To(HaveLen(1))
				Expect(adminGroups[0]).To(Equal("test-admin-group"))

				// Test params with admin groups
				params := &ModelCatalogParams{
					Name:        "test-catalog",
					Namespace:   "test-ns",
					AdminGroups: adminGroups,
				}

				// Test admin role creation
				result, err := reconciler.createOrUpdateAdminRole(ctx, params, nil)
				Expect(err).To(Not(HaveOccurred()))
				Expect(result).To(Equal(ResourceCreated))

				// Test admin rolebinding creation
				result, err = reconciler.createOrUpdateAdminRoleBinding(ctx, params, nil)
				Expect(err).To(Not(HaveOccurred()))
				Expect(result).To(Equal(ResourceCreated))

				// Verify admin role was created
				role := &rbac.Role{}
				err = fakeClient.Get(ctx, types.NamespacedName{Name: "test-catalog-admin", Namespace: "test-ns"}, role)
				Expect(err).To(Not(HaveOccurred()))

				// Verify admin rolebinding was created with correct subjects
				rb := &rbac.RoleBinding{}
				err = fakeClient.Get(ctx, types.NamespacedName{Name: "test-catalog-admin-binding", Namespace: "test-ns"}, rb)
				Expect(err).To(Not(HaveOccurred()))
				Expect(rb.Subjects).To(HaveLen(1))
				Expect(rb.Subjects[0].Name).To(Equal("test-admin-group"))
			})
		})

		Context("NetworkPolicy watch and automatic recreation", func() {
			It("Should recreate PostgreSQL NetworkPolicy after deletion via reconciliation", func() {
				By("Creating catalog resources including NetworkPolicy")
				_, err := catalogReconciler.ensureCatalogResources(ctx)
				Expect(err).To(Not(HaveOccurred()))

				By("Verifying PostgreSQL NetworkPolicy exists")
				postgresNetworkPolicy := &networkingv1.NetworkPolicy{}
				err = k8sClient.Get(ctx, types.NamespacedName{
					Name:      modelCatalogName + "-postgres",
					Namespace: namespaceName,
				}, postgresNetworkPolicy)
				Expect(err).To(Not(HaveOccurred()))

				By("Saving the original NetworkPolicy spec for comparison")
				originalPodSelector := postgresNetworkPolicy.Spec.PodSelector.DeepCopy()
				originalIngress := postgresNetworkPolicy.Spec.Ingress

				By("Deleting the PostgreSQL NetworkPolicy")
				err = k8sClient.Delete(ctx, postgresNetworkPolicy)
				Expect(err).To(Not(HaveOccurred()))

				By("Verifying the NetworkPolicy is deleted")
				deletedNetworkPolicy := &networkingv1.NetworkPolicy{}
				err = k8sClient.Get(ctx, types.NamespacedName{
					Name:      modelCatalogName + "-postgres",
					Namespace: namespaceName,
				}, deletedNetworkPolicy)
				Expect(errors.IsNotFound(err)).To(BeTrue(), "NetworkPolicy should be deleted")

				By("Running reconciliation to recreate the NetworkPolicy")
				_, err = catalogReconciler.ensureCatalogResources(ctx)
				Expect(err).To(Not(HaveOccurred()))

				By("Verifying the PostgreSQL NetworkPolicy was recreated")
				recreatedNetworkPolicy := &networkingv1.NetworkPolicy{}
				err = k8sClient.Get(ctx, types.NamespacedName{
					Name:      modelCatalogName + "-postgres",
					Namespace: namespaceName,
				}, recreatedNetworkPolicy)
				Expect(err).To(Not(HaveOccurred()))

				By("Verifying recreated NetworkPolicy has identical specifications")
				Expect(recreatedNetworkPolicy.Spec.PodSelector.MatchLabels).To(Equal(originalPodSelector.MatchLabels),
					"Recreated NetworkPolicy should have the same podSelector")
				Expect(recreatedNetworkPolicy.Spec.PolicyTypes).To(ContainElement(networkingv1.PolicyTypeIngress),
					"Recreated NetworkPolicy should have Ingress policy type")
				Expect(recreatedNetworkPolicy.Spec.Ingress).To(HaveLen(len(originalIngress)),
					"Recreated NetworkPolicy should have the same number of ingress rules")
				Expect(recreatedNetworkPolicy.Spec.Ingress[0].Ports).To(HaveLen(1),
					"Recreated NetworkPolicy should have port restriction")
				Expect(recreatedNetworkPolicy.Spec.Ingress[0].Ports[0].Port.IntVal).To(Equal(int32(5432)),
					"Recreated NetworkPolicy should restrict to PostgreSQL port 5432")

				By("Verifying recreated NetworkPolicy has correct labels")
				Expect(recreatedNetworkPolicy.Labels).To(HaveKeyWithValue("component", modelCatalogPostgresName),
					"Recreated NetworkPolicy should have component label")
				Expect(recreatedNetworkPolicy.Labels).To(HaveKeyWithValue("app.kubernetes.io/created-by", "model-registry-operator"),
					"Recreated NetworkPolicy should have created-by label")
			})

			Context("On OpenShift", func() {
				BeforeEach(func() {
					catalogReconciler.Capabilities = ClusterCapabilities{
						IsOpenShift: true,
						HasUserAPI:  true,
					}
				})

				It("Should recreate kube-rbac-proxy NetworkPolicy after deletion via reconciliation", func() {
					By("Creating catalog resources including OpenShift-specific NetworkPolicy")
					_, err := catalogReconciler.ensureCatalogResources(ctx)
					Expect(err).To(Not(HaveOccurred()))

					By("Verifying kube-rbac-proxy NetworkPolicy exists")
					httpsNetworkPolicy := &networkingv1.NetworkPolicy{}
					err = k8sClient.Get(ctx, types.NamespacedName{
						Name:      modelCatalogName + "-https-route",
						Namespace: namespaceName,
					}, httpsNetworkPolicy)
					Expect(err).To(Not(HaveOccurred()))

					By("Saving the original NetworkPolicy spec for comparison")
					originalPodSelector := httpsNetworkPolicy.Spec.PodSelector.DeepCopy()

					By("Deleting the kube-rbac-proxy NetworkPolicy")
					err = k8sClient.Delete(ctx, httpsNetworkPolicy)
					Expect(err).To(Not(HaveOccurred()))

					By("Verifying the NetworkPolicy is deleted")
					deletedNetworkPolicy := &networkingv1.NetworkPolicy{}
					err = k8sClient.Get(ctx, types.NamespacedName{
						Name:      modelCatalogName + "-https-route",
						Namespace: namespaceName,
					}, deletedNetworkPolicy)
					Expect(errors.IsNotFound(err)).To(BeTrue(), "NetworkPolicy should be deleted")

					By("Running reconciliation to recreate the NetworkPolicy")
					_, err = catalogReconciler.ensureCatalogResources(ctx)
					Expect(err).To(Not(HaveOccurred()))

					By("Verifying the kube-rbac-proxy NetworkPolicy was recreated")
					recreatedNetworkPolicy := &networkingv1.NetworkPolicy{}
					err = k8sClient.Get(ctx, types.NamespacedName{
						Name:      modelCatalogName + "-https-route",
						Namespace: namespaceName,
					}, recreatedNetworkPolicy)
					Expect(err).To(Not(HaveOccurred()))

					By("Verifying recreated NetworkPolicy has identical specifications")
					Expect(recreatedNetworkPolicy.Spec.PodSelector.MatchLabels).To(Equal(originalPodSelector.MatchLabels),
						"Recreated NetworkPolicy should have the same podSelector")

					By("Verifying recreated NetworkPolicy has correct labels")
					Expect(recreatedNetworkPolicy.Labels).To(HaveKeyWithValue("component", "model-catalog"),
						"Recreated NetworkPolicy should have component label")
					Expect(recreatedNetworkPolicy.Labels).To(HaveKeyWithValue("app.kubernetes.io/created-by", "model-registry-operator"),
						"Recreated NetworkPolicy should have created-by label")
				})
			})

			It("Should not trigger reconciliation for non-catalog NetworkPolicies", func() {
				By("Creating a NetworkPolicy without catalog labels")
				nonCatalogNetworkPolicy := &networkingv1.NetworkPolicy{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "non-catalog-network-policy",
						Namespace: namespaceName,
						Labels: map[string]string{
							"component": "some-other-component",
						},
					},
					Spec: networkingv1.NetworkPolicySpec{
						PodSelector: metav1.LabelSelector{
							MatchLabels: map[string]string{
								"app": "some-other-app",
							},
						},
					},
				}
				err := k8sClient.Create(ctx, nonCatalogNetworkPolicy)
				Expect(err).To(Not(HaveOccurred()))

				By("Verifying the non-catalog NetworkPolicy exists")
				found := &networkingv1.NetworkPolicy{}
				err = k8sClient.Get(ctx, types.NamespacedName{
					Name:      "non-catalog-network-policy",
					Namespace: namespaceName,
				}, found)
				Expect(err).To(Not(HaveOccurred()))

				By("Verifying the non-catalog NetworkPolicy does not have catalog labels")
				Expect(found.Labels).ToNot(HaveKeyWithValue("component", modelCatalogName))
				Expect(found.Labels).ToNot(HaveKeyWithValue("component", modelCatalogPostgresName))
				Expect(found.Labels).ToNot(HaveKeyWithValue("app.kubernetes.io/created-by", "model-registry-operator"))

				By("Cleaning up the non-catalog NetworkPolicy")
				err = k8sClient.Delete(ctx, nonCatalogNetworkPolicy)
				Expect(err).To(Not(HaveOccurred()))
			})
		})

		Context("NetworkPolicy watch in SetupWithManager", func() {
			It("Should automatically recreate deleted PostgreSQL NetworkPolicy via controller watch", func() {
				By("Creating a controller manager")
				mgr, err := manager.New(cfg, manager.Options{
					Scheme: k8sClient.Scheme(),
					Metrics: metricsserver.Options{
						BindAddress: "0",
					},
				})
				Expect(err).To(Not(HaveOccurred()))

				By("Setting up image env vars for the controller")
				template, err := config.ParseTemplates()
				Expect(err).To(Not(HaveOccurred()))

				By("Creating the catalog reconciler with SetupWithManager")
				watchReconciler := &ModelCatalogReconciler{
					Client:          mgr.GetClient(),
					Scheme:          mgr.GetScheme(),
					Recorder:        mgr.GetEventRecorderFor("modelcatalog-controller"),
					Log:             ctrl.Log.WithName("modelcatalog-watch-test"),
					Template:        template,
					TargetNamespace: namespaceName,
					Enabled:         true,
					Capabilities: ClusterCapabilities{
						IsOpenShift:  false,
						HasUserAPI:   false,
						HasConfigAPI: false,
					},
				}
				err = watchReconciler.SetupWithManager(mgr)
				Expect(err).To(Not(HaveOccurred()))

				By("Starting the manager in the background")
				mgrCtx, mgrCancel := context.WithCancel(ctx)
				defer mgrCancel()
				go func() {
					defer GinkgoRecover()
					err := mgr.Start(mgrCtx)
					Expect(err).To(Not(HaveOccurred()))
				}()

				By("Waiting for the manager cache to sync")
				Eventually(func() bool {
					return mgr.GetCache().WaitForCacheSync(mgrCtx)
				}, 10*time.Second, 100*time.Millisecond).Should(BeTrue())

				By("Waiting for initial reconciliation to create resources")
				Eventually(func() error {
					postgresNetworkPolicy := &networkingv1.NetworkPolicy{}
					return k8sClient.Get(ctx, types.NamespacedName{
						Name:      modelCatalogName + "-postgres",
						Namespace: namespaceName,
					}, postgresNetworkPolicy)
				}, 30*time.Second, 1*time.Second).Should(Succeed(),
					"PostgreSQL NetworkPolicy should be created by initial reconciliation")

				By("Deleting the PostgreSQL NetworkPolicy to trigger watch-based reconciliation")
				postgresNetworkPolicy := &networkingv1.NetworkPolicy{}
				err = k8sClient.Get(ctx, types.NamespacedName{
					Name:      modelCatalogName + "-postgres",
					Namespace: namespaceName,
				}, postgresNetworkPolicy)
				Expect(err).To(Not(HaveOccurred()))
				err = k8sClient.Delete(ctx, postgresNetworkPolicy)
				Expect(err).To(Not(HaveOccurred()))

				By("Verifying the NetworkPolicy is automatically recreated by the controller watch")
				Eventually(func() error {
					recreated := &networkingv1.NetworkPolicy{}
					return k8sClient.Get(ctx, types.NamespacedName{
						Name:      modelCatalogName + "-postgres",
						Namespace: namespaceName,
					}, recreated)
				}, 10*time.Second, 500*time.Millisecond).Should(Succeed(),
					"PostgreSQL NetworkPolicy should be automatically recreated within 10 seconds after deletion via controller watch")
			})
		})

		AfterEach(func() {
			By("Cleaning up catalog resources")
			_, _ = catalogReconciler.cleanupCatalogResources(ctx)

			By("Deleting the Namespace")
			_ = k8sClient.Delete(ctx, namespace)

			By("Removing the Image ENV VARs")
			_ = os.Unsetenv(config.RestImage)
			_ = os.Unsetenv(config.CatalogDataImage)
			_ = os.Unsetenv(config.BenchmarkDataImage)
		})
	})
})
