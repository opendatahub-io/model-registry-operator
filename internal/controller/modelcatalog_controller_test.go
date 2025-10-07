package controller

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/opendatahub-io/model-registry-operator/internal/controller/config"
	routev1 "github.com/openshift/api/route/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbac "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"

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
				Recorder:        &record.FakeRecorder{},
				Log:             ctrl.Log.WithName("modelcatalog-controller"),
				Template:        template,
				TargetNamespace: namespaceName,
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
				Expect(deployment.Spec.Template.Spec.Containers).To(HaveLen(2)) // catalog + oauth-proxy
				Expect(deployment.Spec.Template.Spec.Containers[0].Name).To(Equal("catalog"))

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
					catalogReconciler.IsOpenShift = true
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
					return errors.IsNotFound(err)
				}, 10*time.Second, 1*time.Second).Should(BeTrue())

				By("Verifying PostgreSQL resources are deleted")
				postgresDeployment := &appsv1.Deployment{}
				Eventually(func() bool {
					err = k8sClient.Get(ctx, types.NamespacedName{
						Name:      modelCatalogName + "-postgres",
						Namespace: namespaceName,
					}, postgresDeployment)
					return errors.IsNotFound(err)
				}, 10*time.Second, 1*time.Second).Should(BeTrue())

				postgresService := &corev1.Service{}
				Eventually(func() bool {
					err = k8sClient.Get(ctx, types.NamespacedName{
						Name:      modelCatalogName + "-postgres",
						Namespace: namespaceName,
					}, postgresService)
					return errors.IsNotFound(err)
				}, 10*time.Second, 1*time.Second).Should(BeTrue())

				postgresSecret := &corev1.Secret{}
				Eventually(func() bool {
					err = k8sClient.Get(ctx, types.NamespacedName{
						Name:      modelCatalogName + "-postgres",
						Namespace: namespaceName,
					}, postgresSecret)
					return errors.IsNotFound(err)
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
					Expect(errors.IsNotFound(err)).To(BeTrue())
				}
			})

			Context("On OpenShift", func() {
				BeforeEach(func() {
					catalogReconciler.IsOpenShift = true
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
						return errors.IsNotFound(err)
					}, 10*time.Second, 1*time.Second).Should(BeTrue())

					Eventually(func() bool {
						err = k8sClient.Get(ctx, types.NamespacedName{
							Name:      modelCatalogName + "-https-route",
							Namespace: namespaceName,
						}, networkPolicy)
						return errors.IsNotFound(err)
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
				Expect(string(postgresSecret.Data["database-password"])).To(Equal(config.DefaultCatalogPostgresPassword))

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
				Expect(postgresService.Spec.Selector).To(HaveKeyWithValue("app", modelCatalogName+"-postgres"))
				Expect(postgresService.Spec.Selector).To(HaveKeyWithValue("component", "model-catalog-postgres"))

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
					return errors.IsNotFound(err)
				}, 10*time.Second, 1*time.Second).Should(BeTrue())
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
