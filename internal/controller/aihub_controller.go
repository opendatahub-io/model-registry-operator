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
	"path/filepath"
	"sort"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	klog "sigs.k8s.io/controller-runtime/pkg/log"

	aihubv1alpha1 "github.com/opendatahub-io/model-registry-operator/api/aihub/v1alpha1"
	catalogv1alpha1 "github.com/opendatahub-io/model-registry-operator/api/catalog/v1alpha1"
	"github.com/opendatahub-io/model-registry-operator/internal/controller/config"
	"github.com/opendatahub-io/odh-platform-utilities/pkg/render/kustomize"
)

const (
	childDeploymentName   = "model-registry-operator-controller-manager"
	childManagerContainer = "manager"
)

// clusterScopedKinds lists the kinds whose metadata.namespace must be cleared
// after kustomize rendering (the namespace transformer may stamp it incorrectly).
var clusterScopedKinds = map[string]bool{
	"CustomResourceDefinition":       true,
	"ClusterRole":                    true,
	"ClusterRoleBinding":             true,
	"ValidatingWebhookConfiguration": true,
	"MutatingWebhookConfiguration":   true,
	"Namespace":                      true,
}

type AIHubReconciler struct {
	client.Client
	Scheme                *runtime.Scheme
	ManifestsTemplatePath string
	Getenv                func(string) string
}

func (r *AIHubReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := klog.FromContext(ctx)

	// 1. Get the AIHub singleton.
	aihub := &aihubv1alpha1.AIHub{}
	if err := r.Get(ctx, req.NamespacedName, aihub); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// 2. Singleton guard.
	if aihub.Name != "default" {
		log.Info("ignoring non-singleton AIHub", "name", aihub.Name)
		return ctrl.Result{}, nil
	}

	spec := aihub.Spec
	log.Info("reconciling AIHub", "applicationNamespace", spec.ApplicationNamespace, "registriesNamespace", spec.RegistriesNamespace)

	// 3. Resolve child images from environment.
	getenv := r.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	images := ResolveChildImages(getenv)

	// 4. Render the model-registry child operator manifests.
	renderPath := filepath.Join(r.ManifestsTemplatePath, "modelregistry", "overlays", "odh")
	resources, err := kustomize.Render(renderPath, nil, kustomize.WithNamespace(spec.ApplicationNamespace))
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("rendering model-registry manifests: %w", err)
	}

	// 5. Stamp the child operator Deployment and strip cluster-scoped namespaces.
	for i := range resources {
		kind := resources[i].GetKind()

		// Clear namespace on cluster-scoped kinds.
		if clusterScopedKinds[kind] {
			resources[i].SetNamespace("")
		}

		// Stamp the child operator Deployment.
		if kind == "Deployment" && resources[i].GetName() == childDeploymentName {
			if err := stampChildOperatorDeployment(&resources[i], images, spec.RegistriesNamespace); err != nil {
				return ctrl.Result{}, fmt.Errorf("stamping child operator deployment: %w", err)
			}
		}
	}

	// 6. Sort: CRDs first to avoid ordering issues.
	sort.SliceStable(resources, func(i, j int) bool {
		iIsCRD := resources[i].GetKind() == "CustomResourceDefinition"
		jIsCRD := resources[j].GetKind() == "CustomResourceDefinition"
		return iIsCRD && !jIsCRD
	})

	// 7. Apply all rendered resources.
	rm := ResourceManager{Client: r.Client}
	for i := range resources {
		newObj := &resources[i]

		currObj := &unstructured.Unstructured{}
		currObj.SetGroupVersionKind(newObj.GroupVersionKind())

		if _, err := rm.CreateOrUpdate(ctx, currObj, newObj); err != nil {
			return ctrl.Result{}, fmt.Errorf("applying %s %s/%s: %w",
				newObj.GetKind(), newObj.GetNamespace(), newObj.GetName(), err)
		}
	}

	// 8. Create the singleton Catalog CR if absent.
	newCatalog := &catalogv1alpha1.Catalog{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "default",
			Namespace: spec.RegistriesNamespace,
		},
	}
	newCatalog.SetGroupVersionKind(catalogv1alpha1.GroupVersion.WithKind("Catalog"))
	currCatalog := &catalogv1alpha1.Catalog{}
	if _, err := rm.CreateIfNotExists(ctx, currCatalog, newCatalog); err != nil {
		return ctrl.Result{}, fmt.Errorf("ensuring Catalog CR: %w", err)
	}

	// 9. Check child Deployment availability.
	childDeploy := &appsv1.Deployment{}
	deployKey := types.NamespacedName{
		Namespace: spec.ApplicationNamespace,
		Name:      childDeploymentName,
	}
	if err := r.Get(ctx, deployKey, childDeploy); err != nil {
		log.Info("child deployment not yet available, requeuing", "error", err)
		return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
	}
	if !isDeploymentAvailable(childDeploy) {
		log.Info("child deployment not yet Available, requeuing")
		return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
	}

	log.Info("AIHub reconciliation complete")
	return ctrl.Result{}, nil
}

func (r *AIHubReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&aihubv1alpha1.AIHub{}).
		Complete(r)
}

// isDeploymentAvailable returns true if the Deployment has condition Available == True.
func isDeploymentAvailable(d *appsv1.Deployment) bool {
	for _, c := range d.Status.Conditions {
		if c.Type == appsv1.DeploymentAvailable {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// stampChildOperatorDeployment mutates the rendered child operator Deployment unstructured
// to set the operator image, upsert operand env vars, and set REGISTRIES_NAMESPACE.
func stampChildOperatorDeployment(u *unstructured.Unstructured, images ChildImages, registriesNs string) error {
	// Convert to typed Deployment.
	deploy := &appsv1.Deployment{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, deploy); err != nil {
		return fmt.Errorf("converting unstructured to Deployment: %w", err)
	}

	// Find the manager container.
	var found bool
	for i := range deploy.Spec.Template.Spec.Containers {
		c := &deploy.Spec.Template.Spec.Containers[i]
		if c.Name != childManagerContainer {
			continue
		}
		found = true

		// Set operator image if provided.
		if images.OperatorImage != "" {
			c.Image = images.OperatorImage
		}

		// Upsert operand env vars.
		for _, env := range images.OperandEnv {
			upsertEnv(c, env)
		}

		// Upsert REGISTRIES_NAMESPACE.
		upsertEnv(c, corev1.EnvVar{Name: config.RegistriesNamespace, Value: registriesNs})

		break
	}
	if !found {
		return fmt.Errorf("container %q not found in Deployment %s", childManagerContainer, deploy.Name)
	}

	// Convert back to unstructured.
	obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(deploy)
	if err != nil {
		return fmt.Errorf("converting Deployment back to unstructured: %w", err)
	}
	u.Object = obj
	return nil
}

// upsertEnv sets an env var on the container, replacing it if it already exists.
func upsertEnv(c *corev1.Container, env corev1.EnvVar) {
	for i := range c.Env {
		if c.Env[i].Name == env.Name {
			c.Env[i] = env
			return
		}
	}
	c.Env = append(c.Env, env)
}
