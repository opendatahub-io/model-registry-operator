/*
Copyright 2024.

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
	"time"

	"github.com/go-logr/logr"
	"github.com/opendatahub-io/model-registry-operator/api/v1beta1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	klog "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

var moduleCRGVK = schema.GroupVersionKind{
	Group:   "components.platform.opendatahub.io",
	Version: "v1alpha1",
	Kind:    "ModelRegistry",
}

const (
	moduleCRName          = "default-modelregistry"
	defaultModuleVersion  = "latest"
)

type ModuleCRReconciler struct {
	client.Client
	Log logr.Logger
}

func (r *ModuleCRReconciler) Reconcile(ctx context.Context, _ ctrl.Request) (ctrl.Result, error) {
	log := klog.FromContext(ctx)

	moduleCR := &unstructured.Unstructured{}
	moduleCR.SetGroupVersionKind(moduleCRGVK)

	err := r.Get(ctx, types.NamespacedName{Name: moduleCRName}, moduleCR)
	if err != nil {
		if errors.IsNotFound(err) || meta.IsNoMatchError(err) {
			log.V(1).Info("module CR not found, standalone mode")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	mgmtState, _, _ := unstructured.NestedString(moduleCR.Object, "spec", "managementState")
	registriesNS, _, _ := unstructured.NestedString(moduleCR.Object, "spec", "registriesNamespace")

	var registries v1beta1.ModelRegistryList
	if mgmtState == "Managed" {
		listOpts := []client.ListOption{}
		if registriesNS != "" {
			listOpts = append(listOpts, client.InNamespace(registriesNS))
		}
		if err := r.List(ctx, &registries, listOpts...); err != nil {
			log.Error(err, "failed to list ModelRegistry instances")
			return ctrl.Result{}, err
		}
	}

	if err := r.updateModuleCRStatus(ctx, moduleCR, &registries, mgmtState); err != nil {
		log.Error(err, "failed to update module CR status")
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}

func (r *ModuleCRReconciler) updateModuleCRStatus(ctx context.Context, moduleCR *unstructured.Unstructured, registries *v1beta1.ModelRegistryList, mgmtState string) error {
	var readyStatus, readyReason, readyMessage string
	var provisioningStatus, provisioningReason, provisioningMessage string
	var degradedStatus, degradedReason, degradedMessage string

	if mgmtState != "Managed" {
		readyStatus = "False"
		readyReason = "Removed"
		readyMessage = "Component is not managed"
		provisioningStatus = "False"
		provisioningReason = "Removed"
		provisioningMessage = ""
		degradedStatus = "False"
		degradedReason = "Removed"
		degradedMessage = ""
	} else {
		totalRegistries := len(registries.Items)
		availableCount := 0
		anyDegraded := false

		for i := range registries.Items {
			reg := &registries.Items[i]
			if meta.IsStatusConditionTrue(reg.Status.Conditions, ConditionTypeAvailable) {
				availableCount++
			}
			if meta.IsStatusConditionTrue(reg.Status.Conditions, ConditionTypeDegraded) {
				anyDegraded = true
			}
		}

		readyStatus = "True"
		readyReason = "Ready"
		readyMessage = "All model registry components are running"
		if totalRegistries == 0 {
			readyMessage = "No model registries configured"
		} else if availableCount < totalRegistries {
			readyStatus = "False"
			readyReason = "NotAllRegistriesAvailable"
			readyMessage = fmt.Sprintf("%d/%d model registries available", availableCount, totalRegistries)
		}

		provisioningStatus = "True"
		provisioningReason = "ProvisioningComplete"
		provisioningMessage = "Manifests applied successfully"

		degradedStatus = "False"
		degradedReason = "NoWarnings"
		degradedMessage = ""
		if anyDegraded {
			degradedStatus = "True"
			degradedReason = "InstanceDegraded"
			degradedMessage = "One or more model registry instances are degraded"
		}
	}

	generation := moduleCR.GetGeneration()
	existingConditions, _, _ := unstructured.NestedSlice(moduleCR.Object, "status", "conditions")

	conditions := []interface{}{
		map[string]interface{}{
			"type":               "Ready",
			"status":             readyStatus,
			"reason":             readyReason,
			"message":            readyMessage,
			"lastTransitionTime": conditionTransitionTime(existingConditions, "Ready", readyStatus),
			"observedGeneration": generation,
		},
		map[string]interface{}{
			"type":               "ProvisioningSucceeded",
			"status":             provisioningStatus,
			"reason":             provisioningReason,
			"message":            provisioningMessage,
			"lastTransitionTime": conditionTransitionTime(existingConditions, "ProvisioningSucceeded", provisioningStatus),
			"observedGeneration": generation,
		},
		map[string]interface{}{
			"type":               "Degraded",
			"status":             degradedStatus,
			"reason":             degradedReason,
			"message":            degradedMessage,
			"lastTransitionTime": conditionTransitionTime(existingConditions, "Degraded", degradedStatus),
			"observedGeneration": generation,
		},
	}

	version := os.Getenv("VERSION")
	if version == "" {
		version = defaultModuleVersion
	}

	releases := []interface{}{
		map[string]interface{}{
			"name":    "model-registry",
			"repoUrl": "https://github.com/kubeflow/model-registry",
			"version": version,
		},
	}

	phase := "Not Ready"
	if readyStatus == "True" {
		phase = "Ready"
	}

	status := map[string]interface{}{
		"observedGeneration": generation,
		"phase":              phase,
		"conditions":         conditions,
		"releases":           releases,
	}

	if err := unstructured.SetNestedField(moduleCR.Object, status, "status"); err != nil {
		return fmt.Errorf("failed to set status: %w", err)
	}

	return r.Status().Update(ctx, moduleCR)
}

func findExistingCondition(conditions []interface{}, condType string) map[string]interface{} {
	for _, c := range conditions {
		cm, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		if cm["type"] == condType {
			return cm
		}
	}
	return nil
}

func conditionTransitionTime(existingConditions []interface{}, condType, newStatus string) string {
	now := time.Now().UTC().Format(time.RFC3339)
	existing := findExistingCondition(existingConditions, condType)
	if existing != nil {
		if existingStatus, ok := existing["status"].(string); ok && existingStatus == newStatus {
			if existingTime, ok := existing["lastTransitionTime"].(string); ok && existingTime != "" {
				return existingTime
			}
		}
	}
	return now
}

func (r *ModuleCRReconciler) SetupWithManager(mgr ctrl.Manager) error {
	moduleCRObj := &unstructured.Unstructured{}
	moduleCRObj.SetGroupVersionKind(moduleCRGVK)

	mapToModuleCR := handler.EnqueueRequestsFromMapFunc(
		func(_ context.Context, _ client.Object) []reconcile.Request {
			return []reconcile.Request{{
				NamespacedName: types.NamespacedName{Name: moduleCRName},
			}}
		},
	)

	return ctrl.NewControllerManagedBy(mgr).
		Named("modulecr").
		For(moduleCRObj).
		Watches(&v1beta1.ModelRegistry{}, mapToModuleCR).
		Complete(r)
}
