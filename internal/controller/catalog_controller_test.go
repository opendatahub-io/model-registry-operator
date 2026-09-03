package controller

import (
	"context"
	"fmt"
	"os"
	"time"

	catalogv1alpha1 "github.com/opendatahub-io/model-registry-operator/api/catalog/v1alpha1"
	"github.com/opendatahub-io/model-registry-operator/internal/controller/config"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbac "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Catalog controller", func() {

	Context("Catalog resource management and reconciliation", func() {

		ctx := context.Background()
		var namespace *corev1.Namespace
		var namespaceName string
		var catalogReconciler *CatalogReconciler

		BeforeEach(func() {
			By("Setting Image ENV VARs")
			err := os.Setenv(config.RestImage, config.DefaultRestImage)
			Expect(err).To(Not(HaveOccurred()))
			err = os.Setenv(config.PostgresImage, config.DefaultPostgresImage)
			Expect(err).To(Not(HaveOccurred()))
			err = os.Setenv(config.CatalogDataImage, config.DefaultCatalogDataImage)
			Expect(err).To(Not(HaveOccurred()))
			err = os.Setenv(config.BenchmarkDataImage, config.DefaultBenchmarkDataImage)
			Expect(err).To(Not(HaveOccurred()))

			namespaceName = fmt.Sprintf("catalog-test-%d", time.Now().UnixNano())

			namespace = &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      namespaceName,
					Namespace: namespaceName,
				},
			}

			By("Creating the Namespace")
			err = k8sClient.Create(ctx, namespace)
			Expect(err).To(Not(HaveOccurred()))

			By("Setting up default domain")
			config.SetDefaultDomain("example.com", nil, false)

			template, err := config.ParseTemplates()
			Expect(err).To(Not(HaveOccurred()))

			catalogReconciler = &CatalogReconciler{
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
				Recorder: &events.FakeRecorder{},
				Log:      ctrl.Log.WithName("catalog-controller"),
				Template: template,
				Capabilities: ClusterCapabilities{
					IsOpenShift:  false,
					HasUserAPI:   false,
					HasConfigAPI: false,
				},
			}
		})

		It("Should create and manage all resources for a Catalog CR", func() {
			catalog := &catalogv1alpha1.Catalog{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "catalog",
					Namespace: namespaceName,
				},
			}

			By("Creating Catalog CR")
			err := k8sClient.Create(ctx, catalog)
			Expect(err).To(Not(HaveOccurred()))

			By("Reconciling Catalog CR")
			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      "catalog",
					Namespace: namespaceName,
				},
			}
			_, err = catalogReconciler.Reconcile(ctx, req)
			Expect(err).To(Not(HaveOccurred()))

			By("Checking created ServiceAccount and its owner reference")
			sa := &corev1.ServiceAccount{}
			err = k8sClient.Get(ctx, types.NamespacedName{Name: "model-catalog", Namespace: namespaceName}, sa)
			Expect(err).To(Not(HaveOccurred()))
			Expect(sa.OwnerReferences).To(HaveLen(1))
			Expect(sa.OwnerReferences[0].Kind).To(Equal("Catalog"))
			Expect(sa.OwnerReferences[0].Name).To(Equal("catalog"))

			By("Checking created catalog Deployment and owner reference")
			dep := &appsv1.Deployment{}
			err = k8sClient.Get(ctx, types.NamespacedName{Name: "model-catalog", Namespace: namespaceName}, dep)
			Expect(err).To(Not(HaveOccurred()))
			Expect(dep.OwnerReferences).To(HaveLen(1))
			Expect(dep.OwnerReferences[0].Kind).To(Equal("Catalog"))
			Expect(dep.OwnerReferences[0].Name).To(Equal("catalog"))
			catHash := dep.Spec.Template.Annotations["modelregistry.opendatahub.io/postgres-secret-hash"]
			Expect(catHash).To(Not(BeEmpty()))

			By("Checking created catalog Service")
			svc := &corev1.Service{}
			err = k8sClient.Get(ctx, types.NamespacedName{Name: "model-catalog", Namespace: namespaceName}, svc)
			Expect(err).To(Not(HaveOccurred()))
			Expect(svc.OwnerReferences).To(HaveLen(1))
			Expect(svc.OwnerReferences[0].Kind).To(Equal("Catalog"))

			By("Checking created postgres Secret")
			secret := &corev1.Secret{}
			err = k8sClient.Get(ctx, types.NamespacedName{Name: "model-catalog-postgres", Namespace: namespaceName}, secret)
			Expect(err).To(Not(HaveOccurred()))
			Expect(secret.Data).To(HaveKey("database-password"))
			Expect(secret.Data).To(HaveKey("database-salt"))
			Expect(secret.OwnerReferences).To(HaveLen(1))
			Expect(secret.OwnerReferences[0].Kind).To(Equal("Catalog"))

			By("Checking created postgres Deployment")
			pgDep := &appsv1.Deployment{}
			err = k8sClient.Get(ctx, types.NamespacedName{Name: "model-catalog-postgres", Namespace: namespaceName}, pgDep)
			Expect(err).To(Not(HaveOccurred()))
			Expect(pgDep.OwnerReferences).To(HaveLen(1))
			Expect(pgDep.OwnerReferences[0].Kind).To(Equal("Catalog"))
			pgHash := pgDep.Spec.Template.Annotations["modelregistry.opendatahub.io/postgres-secret-hash"]
			Expect(pgHash).To(Equal(catHash))

			By("Checking created postgres Service")
			pgSvc := &corev1.Service{}
			err = k8sClient.Get(ctx, types.NamespacedName{Name: "model-catalog-postgres", Namespace: namespaceName}, pgSvc)
			Expect(err).To(Not(HaveOccurred()))
			Expect(pgSvc.OwnerReferences).To(HaveLen(1))
			Expect(pgSvc.OwnerReferences[0].Kind).To(Equal("Catalog"))

			By("Checking created Role and RoleBinding")
			role := &rbac.Role{}
			err = k8sClient.Get(ctx, types.NamespacedName{Name: "model-catalog", Namespace: namespaceName}, role)
			Expect(err).To(Not(HaveOccurred()))

			rb := &rbac.RoleBinding{}
			err = k8sClient.Get(ctx, types.NamespacedName{Name: "model-catalog-authenticated", Namespace: namespaceName}, rb)
			Expect(err).To(Not(HaveOccurred()))

			By("Checking created kube-rbac-proxy ConfigMap and ClusterRoleBinding")
			cm := &corev1.ConfigMap{}
			err = k8sClient.Get(ctx, types.NamespacedName{Name: "model-catalog-kube-rbac-proxy-config", Namespace: namespaceName}, cm)
			Expect(err).To(Not(HaveOccurred()))

			By("Checking created user-sources ConfigMaps and verifying no owner references")
			for _, cmName := range []string{"model-catalog-sources", "mcp-catalog-sources", "agent-catalog-sources"} {
				userCM := &corev1.ConfigMap{}
				err = k8sClient.Get(ctx, types.NamespacedName{Name: cmName, Namespace: namespaceName}, userCM)
				Expect(err).To(Not(HaveOccurred()))
				Expect(userCM.OwnerReferences).To(BeEmpty())
			}

			crb := &rbac.ClusterRoleBinding{}
			err = k8sClient.Get(ctx, types.NamespacedName{Name: "model-catalog-auth-delegator"}, crb)
			Expect(err).To(Not(HaveOccurred()))

			By("Verifying status is set")
			updatedCatalog := &catalogv1alpha1.Catalog{}
			err = k8sClient.Get(ctx, req.NamespacedName, updatedCatalog)
			Expect(err).To(Not(HaveOccurred()))
			Expect(updatedCatalog.Status.ObservedGeneration).To(Equal(updatedCatalog.Generation))
		})

		It("Should propagate spec.resources and spec.database.volume.sizeLimit to deployments", func() {
			catalog := &catalogv1alpha1.Catalog{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "catalog",
					Namespace: namespaceName,
				},
				Spec: catalogv1alpha1.CatalogSpec{
					Resources: catalogv1alpha1.CatalogResources{
						Catalog: &corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("500m"),
								corev1.ResourceMemory: resource.MustParse("1Gi"),
							},
							Limits: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("1000m"),
								corev1.ResourceMemory: resource.MustParse("2Gi"),
							},
						},
						Postgres: &corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("300m"),
								corev1.ResourceMemory: resource.MustParse("512Mi"),
							},
							Limits: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("600m"),
								corev1.ResourceMemory: resource.MustParse("1Gi"),
							},
						},
					},
					Database: catalogv1alpha1.CatalogDatabase{
						Volume: catalogv1alpha1.CatalogDatabaseVolume{
							SizeLimit: resource.NewQuantity(10*1024*1024*1024, resource.BinarySI), // 10Gi
						},
					},
				},
			}

			err := k8sClient.Create(ctx, catalog)
			Expect(err).To(Not(HaveOccurred()))

			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      "catalog",
					Namespace: namespaceName,
				},
			}
			_, err = catalogReconciler.Reconcile(ctx, req)
			Expect(err).To(Not(HaveOccurred()))

			By("Verifying catalog Deployment resources")
			dep := &appsv1.Deployment{}
			err = k8sClient.Get(ctx, types.NamespacedName{Name: "model-catalog", Namespace: namespaceName}, dep)
			Expect(err).To(Not(HaveOccurred()))
			catalogContainer := dep.Spec.Template.Spec.Containers[0]
			Expect(catalogContainer.Resources.Requests.Cpu().String()).To(Equal("500m"))
			Expect(catalogContainer.Resources.Requests.Memory().String()).To(Equal("1Gi"))

			By("Verifying postgres Deployment resources and emptyDir sizeLimit")
			pgDep := &appsv1.Deployment{}
			err = k8sClient.Get(ctx, types.NamespacedName{Name: "model-catalog-postgres", Namespace: namespaceName}, pgDep)
			Expect(err).To(Not(HaveOccurred()))
			pgContainer := pgDep.Spec.Template.Spec.Containers[0]
			Expect(pgContainer.Resources.Requests.Cpu().String()).To(Equal("300m"))
			Expect(pgContainer.Resources.Requests.Memory().String()).To(Equal("512Mi"))

			emptyDirVol := pgDep.Spec.Template.Spec.Volumes[0]
			Expect(emptyDirVol.EmptyDir).To(Not(BeNil()))
			Expect(emptyDirVol.EmptyDir.SizeLimit.String()).To(Equal("10Gi"))
		})

		It("Should propagate spec.proxy to the catalog deployment and update it on change", func() {
			catalog := &catalogv1alpha1.Catalog{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "catalog",
					Namespace: namespaceName,
				},
			}

			By("Creating Catalog CR without proxy settings")
			err := k8sClient.Create(ctx, catalog)
			Expect(err).To(Not(HaveOccurred()))

			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      "catalog",
					Namespace: namespaceName,
				},
			}
			_, err = catalogReconciler.Reconcile(ctx, req)
			Expect(err).To(Not(HaveOccurred()))

			envNames := func(dep *appsv1.Deployment) []string {
				var names []string
				for _, e := range dep.Spec.Template.Spec.Containers[0].Env {
					names = append(names, e.Name)
				}
				return names
			}

			By("Verifying no proxy env vars are set when spec.proxy is unset")
			dep := &appsv1.Deployment{}
			err = k8sClient.Get(ctx, types.NamespacedName{Name: "model-catalog", Namespace: namespaceName}, dep)
			Expect(err).To(Not(HaveOccurred()))
			Expect(envNames(dep)).ToNot(ContainElements("HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY"))

			By("Setting spec.proxy on the Catalog CR")
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "catalog", Namespace: namespaceName}, catalog)).To(Succeed())
			catalog.Spec.Proxy = &catalogv1alpha1.ProxyConfig{
				HTTPProxy:  "http://proxy.example.com:3128",
				HTTPSProxy: "https://proxy.example.com:3128",
				NoProxy:    ".svc,.cluster.local",
			}
			Expect(k8sClient.Update(ctx, catalog)).To(Succeed())

			_, err = catalogReconciler.Reconcile(ctx, req)
			Expect(err).To(Not(HaveOccurred()))

			By("Verifying proxy env vars are set on the catalog container")
			err = k8sClient.Get(ctx, types.NamespacedName{Name: "model-catalog", Namespace: namespaceName}, dep)
			Expect(err).To(Not(HaveOccurred()))
			catalogContainer := dep.Spec.Template.Spec.Containers[0]
			Expect(catalogContainer.Env).To(ContainElements(
				corev1.EnvVar{Name: "HTTP_PROXY", Value: "http://proxy.example.com:3128"},
				corev1.EnvVar{Name: "HTTPS_PROXY", Value: "https://proxy.example.com:3128"},
				corev1.EnvVar{Name: "NO_PROXY", Value: ".svc,.cluster.local"},
			))

			By("Updating spec.proxy and verifying the deployment is updated on the next reconcile")
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "catalog", Namespace: namespaceName}, catalog)).To(Succeed())
			catalog.Spec.Proxy.HTTPProxy = "http://new-proxy.example.com:3128"
			Expect(k8sClient.Update(ctx, catalog)).To(Succeed())

			_, err = catalogReconciler.Reconcile(ctx, req)
			Expect(err).To(Not(HaveOccurred()))

			err = k8sClient.Get(ctx, types.NamespacedName{Name: "model-catalog", Namespace: namespaceName}, dep)
			Expect(err).To(Not(HaveOccurred()))
			Expect(dep.Spec.Template.Spec.Containers[0].Env).To(ContainElement(
				corev1.EnvVar{Name: "HTTP_PROXY", Value: "http://new-proxy.example.com:3128"},
			))
		})

		It("Should adopt pre-existing legacy-named resources by re-parenting owner references", func() {
			// Resources created by the old ModelCatalogReconciler are named
			// "model-catalog"/"model-catalog-postgres", not after the Catalog CR
			// (which is always named "catalog"). The adopt-or-create migration must
			// key off those legacy names to find and re-parent them; otherwise it
			// creates a duplicate set and orphans the originals.
			legacyOwnerRefs := []metav1.OwnerReference{
				{
					APIVersion: "components.platform.opendatahub.io/v1alpha1",
					Kind:       "ModelRegistry",
					Name:       "default-modelregistry",
					UID:        "12345678-1234-1234-1234-1234567890ab",
				},
			}

			By("Pre-creating a legacy-named ServiceAccount owned by old default-modelregistry")
			oldSA := &corev1.ServiceAccount{
				ObjectMeta: metav1.ObjectMeta{
					Name:            "model-catalog",
					Namespace:       namespaceName,
					OwnerReferences: legacyOwnerRefs,
				},
			}
			err := k8sClient.Create(ctx, oldSA)
			Expect(err).To(Not(HaveOccurred()))

			By("Pre-creating a legacy-named Deployment owned by old default-modelregistry")
			oldDep := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:            "model-catalog",
					Namespace:       namespaceName,
					OwnerReferences: legacyOwnerRefs,
				},
				Spec: appsv1.DeploymentSpec{
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"app": "model-catalog"},
					},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{"app": "model-catalog"},
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{Name: "catalog", Image: "placeholder:latest"},
							},
						},
					},
				},
			}
			err = k8sClient.Create(ctx, oldDep)
			Expect(err).To(Not(HaveOccurred()))

			By("Creating Catalog CR and reconciling")
			catalog := &catalogv1alpha1.Catalog{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "catalog",
					Namespace: namespaceName,
				},
			}
			err = k8sClient.Create(ctx, catalog)
			Expect(err).To(Not(HaveOccurred()))

			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      "catalog",
					Namespace: namespaceName,
				},
			}
			_, err = catalogReconciler.Reconcile(ctx, req)
			Expect(err).To(Not(HaveOccurred()))

			By("Verifying the legacy ServiceAccount's owner reference was replaced with the Catalog CR")
			reconciledSA := &corev1.ServiceAccount{}
			err = k8sClient.Get(ctx, types.NamespacedName{Name: "model-catalog", Namespace: namespaceName}, reconciledSA)
			Expect(err).To(Not(HaveOccurred()))
			Expect(reconciledSA.OwnerReferences).To(HaveLen(1))
			Expect(reconciledSA.OwnerReferences[0].Kind).To(Equal("Catalog"))
			Expect(reconciledSA.OwnerReferences[0].Name).To(Equal("catalog"))
			Expect(reconciledSA.OwnerReferences[0].UID).To(Equal(catalog.UID))

			// The legacy Deployment's selector ("app" only) doesn't match the current
			// template's selector (adds "component" and "app.kubernetes.io/name"),
			// so adopting it hits the immutable-field-conflict path: the reconciler
			// deletes the old Deployment and recreates it on the *next* reconcile
			// (recreating in the same pass would race the informer cache, see
			// createOrUpdateDeployment). Reconcile again to let the recreate land.
			_, err = catalogReconciler.Reconcile(ctx, req)
			Expect(err).To(Not(HaveOccurred()))

			By("Verifying the legacy Deployment's owner reference was replaced with the Catalog CR")
			reconciledDep := &appsv1.Deployment{}
			err = k8sClient.Get(ctx, types.NamespacedName{Name: "model-catalog", Namespace: namespaceName}, reconciledDep)
			Expect(err).To(Not(HaveOccurred()))
			Expect(reconciledDep.OwnerReferences).To(HaveLen(1))
			Expect(reconciledDep.OwnerReferences[0].Kind).To(Equal("Catalog"))
			Expect(reconciledDep.OwnerReferences[0].Name).To(Equal("catalog"))
			Expect(reconciledDep.OwnerReferences[0].UID).To(Equal(catalog.UID))

			By("Verifying no duplicate 'catalog'-named resources were created alongside the adopted ones")
			var dupSA corev1.ServiceAccount
			err = k8sClient.Get(ctx, types.NamespacedName{Name: "catalog", Namespace: namespaceName}, &dupSA)
			Expect(apierrors.IsNotFound(err)).To(BeTrue())
			var dupDep appsv1.Deployment
			err = k8sClient.Get(ctx, types.NamespacedName{Name: "catalog", Namespace: namespaceName}, &dupDep)
			Expect(apierrors.IsNotFound(err)).To(BeTrue())
		})

		It("Should clean up cluster-scoped resources on Catalog deletion", func() {
			catalog := &catalogv1alpha1.Catalog{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "catalog",
					Namespace: namespaceName,
				},
			}
			err := k8sClient.Create(ctx, catalog)
			Expect(err).To(Not(HaveOccurred()))

			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      "catalog",
					Namespace: namespaceName,
				},
			}
			_, err = catalogReconciler.Reconcile(ctx, req)
			Expect(err).To(Not(HaveOccurred()))

			By("Checking cluster-scoped ClusterRoleBinding exists")
			crb := &rbac.ClusterRoleBinding{}
			err = k8sClient.Get(ctx, types.NamespacedName{Name: "model-catalog-auth-delegator"}, crb)
			Expect(err).To(Not(HaveOccurred()))

			By("Deleting Catalog CR")
			err = k8sClient.Get(ctx, req.NamespacedName, catalog)
			Expect(err).To(Not(HaveOccurred()))
			err = k8sClient.Delete(ctx, catalog)
			Expect(err).To(Not(HaveOccurred()))

			By("Reconciling deletion with finalizer")
			_, err = catalogReconciler.Reconcile(ctx, req)
			Expect(err).To(Not(HaveOccurred()))

			By("Verifying ClusterRoleBinding was deleted")
			err = k8sClient.Get(ctx, types.NamespacedName{Name: "model-catalog-auth-delegator"}, crb)
			Expect(apierrors.IsNotFound(err)).To(BeTrue())
		})

		It("Should delete the admin RoleBinding by its rendered name when admin groups are removed", func() {
			catalog := &catalogv1alpha1.Catalog{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "catalog",
					Namespace: namespaceName,
				},
			}
			err := k8sClient.Create(ctx, catalog)
			Expect(err).To(Not(HaveOccurred()))

			owner := &metav1.OwnerReference{
				APIVersion: catalogv1alpha1.GroupVersion.String(),
				Kind:       "Catalog",
				Name:       catalog.Name,
				UID:        catalog.UID,
			}

			By("Creating the admin Role and RoleBinding as if an admin group were configured")
			paramsWithGroups := catalogReconciler.buildCatalogParams(catalog, []string{"admin-group"}, nil)
			_, err = catalogReconciler.createOrUpdateAdminRole(ctx, paramsWithGroups, owner)
			Expect(err).To(Not(HaveOccurred()))
			_, err = catalogReconciler.createOrUpdateAdminRoleBinding(ctx, paramsWithGroups, owner)
			Expect(err).To(Not(HaveOccurred()))

			rbName := types.NamespacedName{Name: fmt.Sprintf("%s-admin-binding", catalogResourceName), Namespace: namespaceName}
			var rb rbac.RoleBinding
			Expect(k8sClient.Get(ctx, rbName, &rb)).To(Succeed())

			By("Removing the admin group and reconciling the admin RoleBinding")
			paramsWithoutGroups := catalogReconciler.buildCatalogParams(catalog, nil, nil)
			_, err = catalogReconciler.createOrUpdateAdminRoleBinding(ctx, paramsWithoutGroups, owner)
			Expect(err).To(Not(HaveOccurred()))

			By("Verifying the admin RoleBinding was deleted")
			err = k8sClient.Get(ctx, rbName, &rb)
			Expect(apierrors.IsNotFound(err)).To(BeTrue())
		})

		It("Should update secret hash annotation on deployments when secret is updated", func() {
			By("Creating a labeled source ConfigMap")
			labeledCM := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "custom-source",
					Namespace: namespaceName,
					Labels: map[string]string{
						catalogSourceLabel: "true",
					},
				},
				Data: map[string]string{
					sourcesFileName: "catalogs: []",
				},
			}
			Expect(k8sClient.Create(ctx, labeledCM)).To(Succeed())

			catalog := &catalogv1alpha1.Catalog{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "catalog",
					Namespace: namespaceName,
				},
			}

			By("Creating Catalog CR")
			Expect(k8sClient.Create(ctx, catalog)).To(Succeed())

			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      "catalog",
					Namespace: namespaceName,
				},
			}
			_, err := catalogReconciler.Reconcile(ctx, req)
			Expect(err).To(Not(HaveOccurred()))

			By("Getting initial deployments and checking secret hash annotation and labeled source volume")
			dep := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "model-catalog", Namespace: namespaceName}, dep)).To(Succeed())
			initialHash := dep.Spec.Template.Annotations["modelregistry.opendatahub.io/postgres-secret-hash"]
			Expect(initialHash).To(Not(BeEmpty()))

			var labeledVol *corev1.Volume
			for i, v := range dep.Spec.Template.Spec.Volumes {
				if v.Name == "labeled-custom-source" {
					labeledVol = &dep.Spec.Template.Spec.Volumes[i]
					break
				}
			}
			Expect(labeledVol).To(Not(BeNil()))
			Expect(labeledVol.ConfigMap).To(Not(BeNil()))

			pgDep := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "model-catalog-postgres", Namespace: namespaceName}, pgDep)).To(Succeed())
			Expect(pgDep.Spec.Template.Annotations["modelregistry.opendatahub.io/postgres-secret-hash"]).To(Equal(initialHash))

			By("Updating postgres Secret password")
			secret := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "model-catalog-postgres", Namespace: namespaceName}, secret)).To(Succeed())
			secret.Data["database-password"] = []byte("updated-secret-password-12345")
			Expect(k8sClient.Update(ctx, secret)).To(Succeed())

			By("Reconciling after secret update")
			_, err = catalogReconciler.Reconcile(ctx, req)
			Expect(err).To(Not(HaveOccurred()))

			By("Checking updated deployments receive new secret hash annotation")
			updatedDep := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "model-catalog", Namespace: namespaceName}, updatedDep)).To(Succeed())
			newHash := updatedDep.Spec.Template.Annotations["modelregistry.opendatahub.io/postgres-secret-hash"]
			Expect(newHash).To(Not(BeEmpty()))
			Expect(newHash).To(Not(Equal(initialHash)))

			updatedPgDep := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "model-catalog-postgres", Namespace: namespaceName}, updatedPgDep)).To(Succeed())
			Expect(updatedPgDep.Spec.Template.Annotations["modelregistry.opendatahub.io/postgres-secret-hash"]).To(Equal(newHash))
		})

		It("Should add missing database-salt to existing secret during reconciliation", func() {
			By("Pre-creating a postgres secret missing database-salt")
			existingSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "model-catalog-postgres",
					Namespace: namespaceName,
				},
				Data: map[string][]byte{
					"database-name":     []byte("catalog"),
					"database-user":     []byte("catalog"),
					"database-password": []byte("existingpassword"),
				},
			}
			Expect(k8sClient.Create(ctx, existingSecret)).To(Succeed())

			catalog := &catalogv1alpha1.Catalog{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "catalog",
					Namespace: namespaceName,
				},
			}
			Expect(k8sClient.Create(ctx, catalog)).To(Succeed())

			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      "catalog",
					Namespace: namespaceName,
				},
			}
			_, err := catalogReconciler.Reconcile(ctx, req)
			Expect(err).To(Not(HaveOccurred()))

			By("Verifying secret now contains database-salt")
			secret := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "model-catalog-postgres", Namespace: namespaceName}, secret)).To(Succeed())
			Expect(secret.Data).To(HaveKey("database-salt"))
			Expect(secret.Data["database-salt"]).To(Not(BeEmpty()))
			Expect(string(secret.Data["database-password"])).To(Equal("existingpassword"))
		})

		It("Should populate database-salt when existing secret has empty database-salt during reconciliation", func() {
			By("Pre-creating a postgres secret with empty database-salt")
			existingSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "model-catalog-postgres",
					Namespace: namespaceName,
				},
				Data: map[string][]byte{
					"database-name":     []byte("catalog"),
					"database-user":     []byte("catalog"),
					"database-password": []byte("existingpassword"),
					"database-salt":     []byte(""),
				},
			}
			Expect(k8sClient.Create(ctx, existingSecret)).To(Succeed())

			catalog := &catalogv1alpha1.Catalog{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "catalog",
					Namespace: namespaceName,
				},
			}
			Expect(k8sClient.Create(ctx, catalog)).To(Succeed())

			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      "catalog",
					Namespace: namespaceName,
				},
			}
			_, err := catalogReconciler.Reconcile(ctx, req)
			Expect(err).To(Not(HaveOccurred()))

			By("Verifying secret now contains populated database-salt")
			secret := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "model-catalog-postgres", Namespace: namespaceName}, secret)).To(Succeed())
			Expect(secret.Data).To(HaveKey("database-salt"))
			Expect(secret.Data["database-salt"]).To(Not(BeEmpty()))
			Expect(string(secret.Data["database-password"])).To(Equal("existingpassword"))
		})

		It("Should recreate postgres secret and refresh deployment hashes when secret is deleted", func() {
			catalog := &catalogv1alpha1.Catalog{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "catalog",
					Namespace: namespaceName,
				},
			}

			By("Creating Catalog CR")
			Expect(k8sClient.Create(ctx, catalog)).To(Succeed())

			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      "catalog",
					Namespace: namespaceName,
				},
			}

			By("Reconciling Catalog CR")
			_, err := catalogReconciler.Reconcile(ctx, req)
			Expect(err).To(Not(HaveOccurred()))

			By("Getting initial postgres Secret and verifying data and ownerReferences")
			initialSecret := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "model-catalog-postgres", Namespace: namespaceName}, initialSecret)).To(Succeed())
			Expect(initialSecret.Data).To(HaveKey("database-password"))
			Expect(initialSecret.Data).To(HaveKey("database-salt"))
			Expect(initialSecret.OwnerReferences).To(HaveLen(1))
			Expect(initialSecret.OwnerReferences[0].Kind).To(Equal("Catalog"))
			Expect(initialSecret.OwnerReferences[0].Name).To(Equal("catalog"))

			By("Noting initial deployment secret hash annotations")
			dep := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "model-catalog", Namespace: namespaceName}, dep)).To(Succeed())
			initialCatHash := dep.Spec.Template.Annotations["modelregistry.opendatahub.io/postgres-secret-hash"]
			Expect(initialCatHash).To(Not(BeEmpty()))

			pgDep := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "model-catalog-postgres", Namespace: namespaceName}, pgDep)).To(Succeed())
			initialPgHash := pgDep.Spec.Template.Annotations["modelregistry.opendatahub.io/postgres-secret-hash"]
			Expect(initialPgHash).To(Equal(initialCatHash))

			By("Deleting the model-catalog-postgres Secret")
			Expect(k8sClient.Delete(ctx, initialSecret)).To(Succeed())

			By("Reconciling the Catalog CR after Secret deletion")
			_, err = catalogReconciler.Reconcile(ctx, req)
			Expect(err).To(Not(HaveOccurred()))

			By("Verifying the model-catalog-postgres Secret is recreated, has valid data (password, salt), and has owner reference to the Catalog CR")
			recreatedSecret := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "model-catalog-postgres", Namespace: namespaceName}, recreatedSecret)).To(Succeed())
			Expect(recreatedSecret.Data).To(HaveKey("database-password"))
			Expect(recreatedSecret.Data["database-password"]).To(Not(BeEmpty()))
			Expect(recreatedSecret.Data).To(HaveKey("database-salt"))
			Expect(recreatedSecret.Data["database-salt"]).To(Not(BeEmpty()))
			Expect(recreatedSecret.OwnerReferences).To(HaveLen(1))
			Expect(recreatedSecret.OwnerReferences[0].Kind).To(Equal("Catalog"))
			Expect(recreatedSecret.OwnerReferences[0].Name).To(Equal("catalog"))

			By("Verifying deployments are updated with new hash annotation reflecting new secret data")
			updatedDep := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "model-catalog", Namespace: namespaceName}, updatedDep)).To(Succeed())
			newCatHash := updatedDep.Spec.Template.Annotations["modelregistry.opendatahub.io/postgres-secret-hash"]
			Expect(newCatHash).To(Not(BeEmpty()))
			Expect(newCatHash).To(Not(Equal(initialCatHash)))

			updatedPgDep := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "model-catalog-postgres", Namespace: namespaceName}, updatedPgDep)).To(Succeed())
			newPgHash := updatedPgDep.Spec.Template.Annotations["modelregistry.opendatahub.io/postgres-secret-hash"]
			Expect(newPgHash).To(Equal(newCatHash))
		})

		It("Should recreate user-sources ConfigMaps with empty owner references if deleted", func() {
			catalog := &catalogv1alpha1.Catalog{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "catalog",
					Namespace: namespaceName,
				},
			}

			By("Creating Catalog CR")
			err := k8sClient.Create(ctx, catalog)
			Expect(err).To(Not(HaveOccurred()))

			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      "catalog",
					Namespace: namespaceName,
				},
			}
			_, err = catalogReconciler.Reconcile(ctx, req)
			Expect(err).To(Not(HaveOccurred()))

			By("Deleting one of the user-sources ConfigMaps")
			cmToDelete := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "model-catalog-sources",
					Namespace: namespaceName,
				},
			}
			err = k8sClient.Delete(ctx, cmToDelete)
			Expect(err).To(Not(HaveOccurred()))

			By("Reconciling again")
			_, err = catalogReconciler.Reconcile(ctx, req)
			Expect(err).To(Not(HaveOccurred()))

			By("Verifying the ConfigMap was recreated with empty OwnerReferences")
			recreatedCM := &corev1.ConfigMap{}
			err = k8sClient.Get(ctx, types.NamespacedName{Name: "model-catalog-sources", Namespace: namespaceName}, recreatedCM)
			Expect(err).To(Not(HaveOccurred()))
			Expect(recreatedCM.OwnerReferences).To(BeEmpty())
		})

		It("Should retain pre-existing user-sources ConfigMaps data without adding owner references", func() {
			By("Pre-creating user-sources ConfigMaps without owner references and with custom data")
			customData := map[string]string{
				"sources.yaml": "catalogs:\n  - name: custom-source\n    type: yaml\n",
			}

			for _, cmName := range []string{"model-catalog-sources", "mcp-catalog-sources", "agent-catalog-sources"} {
				preExistingCM := &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      cmName,
						Namespace: namespaceName,
					},
					Data: customData,
				}
				err := k8sClient.Create(ctx, preExistingCM)
				Expect(err).To(Not(HaveOccurred()))
			}

			By("Creating Catalog CR and reconciling")
			catalog := &catalogv1alpha1.Catalog{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "catalog",
					Namespace: namespaceName,
				},
			}
			err := k8sClient.Create(ctx, catalog)
			Expect(err).To(Not(HaveOccurred()))

			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      "catalog",
					Namespace: namespaceName,
				},
			}
			_, err = catalogReconciler.Reconcile(ctx, req)
			Expect(err).To(Not(HaveOccurred()))

			By("Verifying that pre-existing user-sources ConfigMaps retain custom data and do not get owner references added")
			for _, cmName := range []string{"model-catalog-sources", "mcp-catalog-sources", "agent-catalog-sources"} {
				adoptedCM := &corev1.ConfigMap{}
				err = k8sClient.Get(ctx, types.NamespacedName{Name: cmName, Namespace: namespaceName}, adoptedCM)
				Expect(err).To(Not(HaveOccurred()))
				Expect(adoptedCM.OwnerReferences).To(BeEmpty())
				Expect(adoptedCM.Data).To(Equal(customData))
			}
		})
	})
})
