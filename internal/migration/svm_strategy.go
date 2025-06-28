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

package migration

import (
	"context"
	"fmt"
	"time"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

//+kubebuilder:rbac:groups=storagemigration.k8s.io,resources=storageversionmigrations,verbs=get;list;watch;create;update;patch;delete

// SVMStrategy implements migration using Kubernetes StorageVersionMigration API
type SVMStrategy struct {
	client.Client
	SourceVersion string
	TargetVersion string
}

func NewSVMStrategy(client client.Client, sourceVersion, targetVersion string) *SVMStrategy {
	return &SVMStrategy{
		Client:        client,
		SourceVersion: sourceVersion,
		TargetVersion: targetVersion,
	}
}

func (s *SVMStrategy) GetName() string {
	return "StorageVersionMigration"
}

func (s *SVMStrategy) IsSupported(ctx context.Context) bool {
	// Check if the StorageVersionMigration API is available
	gvr := schema.GroupVersionResource{
		Group:    "storagemigration.k8s.io",
		Version:  "v1alpha1",
		Resource: "storageversionmigrations",
	}

	// Try to list StorageVersionMigrations to see if the API exists
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   gvr.Group,
		Version: gvr.Version,
		Kind:    "StorageVersionMigrationList",
	})

	err := s.List(ctx, list)
	if err != nil {
		if errors.IsNotFound(err) || errors.IsMethodNotSupported(err) {
			return false
		}
		// Other errors might indicate the API exists but we don't have permissions
		log.FromContext(ctx).Info("StorageVersionMigration API check returned error, assuming not supported", "error", err)
		return false
	}
	return true
}

func (s *SVMStrategy) PerformMigration(ctx context.Context, crd *apiextensionsv1.CustomResourceDefinition) error {
	log := log.FromContext(ctx).WithName("svm-strategy")

	svmName := fmt.Sprintf("modelregistry-%s-to-%s-migration", s.SourceVersion, s.TargetVersion)

	log.Info("Creating StorageVersionMigration", "name", svmName)

	// Create StorageVersionMigration using unstructured to avoid import issues
	svm := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "storagemigration.k8s.io/v1alpha1",
			"kind":       "StorageVersionMigration",
			"metadata": map[string]interface{}{
				"name": svmName,
				"labels": map[string]interface{}{
					"app.kubernetes.io/name":       "model-registry-operator",
					"app.kubernetes.io/component":  "storage-migration",
					"app.kubernetes.io/managed-by": "model-registry-operator",
				},
			},
			"spec": map[string]interface{}{
				"resource": map[string]interface{}{
					"group":    ModelRegistryGroup,
					"version":  s.TargetVersion,
					"resource": ModelRegistryResource,
				},
			},
		},
	}

	// Check if migration already exists
	existingSVM := &unstructured.Unstructured{}
	existingSVM.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "storagemigration.k8s.io",
		Version: "v1alpha1",
		Kind:    "StorageVersionMigration",
	})

	err := s.Get(ctx, types.NamespacedName{Name: svmName}, existingSVM)
	if err == nil {
		log.Info("StorageVersionMigration already exists, monitoring progress", "name", svmName)
		return s.monitorStorageVersionMigration(ctx, svmName)
	}

	if err := s.Create(ctx, svm); err != nil {
		return fmt.Errorf("failed to create StorageVersionMigration: %w", err)
	}

	log.Info("Successfully created StorageVersionMigration", "name", svmName)

	return s.monitorStorageVersionMigration(ctx, svmName)
}

func (s *SVMStrategy) monitorStorageVersionMigration(ctx context.Context, svmName string) error {
	log := log.FromContext(ctx).WithName("svm-monitor")

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Info("Migration monitor stopping due to context cancellation")
			return nil
		case <-ticker.C:
			svm := &unstructured.Unstructured{}
			svm.SetGroupVersionKind(schema.GroupVersionKind{
				Group:   "storagemigration.k8s.io",
				Version: "v1alpha1",
				Kind:    "StorageVersionMigration",
			})

			err := s.Get(ctx, types.NamespacedName{Name: svmName}, svm)
			if err != nil {
				if errors.IsNotFound(err) {
					log.Info("No StorageVersionMigration found, stopping monitor", "name", svmName)
					return nil
				}
				log.Error(err, "Failed to get StorageVersionMigration", "name", svmName)
				continue
			}

			// Check migration status
			if s.isSVMCompleted(svm) {
				log.Info("Storage version migration completed successfully",
					"migration", svmName,
					"duration", time.Since(svm.GetCreationTimestamp().Time))
				return nil
			}

			if s.isSVMFailed(svm) {
				log.Error(fmt.Errorf("migration failed"),
					"Storage version migration failed",
					"migration", svmName)
				return fmt.Errorf("storage version migration failed")
			}

			// Log progress
			log.Info("Storage version migration in progress",
				"migration", svmName,
				"age", time.Since(svm.GetCreationTimestamp().Time))
		}
	}
}

func (s *SVMStrategy) isSVMCompleted(svm *unstructured.Unstructured) bool {
	conditions := s.extractConditions(svm)
	return meta.IsStatusConditionTrue(conditions, "Succeeded")
}

func (s *SVMStrategy) isSVMFailed(svm *unstructured.Unstructured) bool {
	conditions := s.extractConditions(svm)
	return meta.IsStatusConditionFalse(conditions, "Succeeded")
}

// extractConditions converts unstructured conditions to metav1.Condition slice
func (s *SVMStrategy) extractConditions(svm *unstructured.Unstructured) []metav1.Condition {
	// Extract conditions from the unstructured object
	conditionsRaw, found, err := unstructured.NestedSlice(svm.Object, "status", "conditions")
	if !found || err != nil {
		return nil
	}

	// Convert to metav1.Condition slice
	var conditions []metav1.Condition
	for _, conditionInterface := range conditionsRaw {
		condition, ok := conditionInterface.(map[string]interface{})
		if !ok {
			continue
		}

		// Extract required fields
		condType, typeOk := condition["type"].(string)
		status, statusOk := condition["status"].(string)
		if !typeOk || !statusOk {
			continue
		}

		// Convert to metav1.Condition
		metaCondition := metav1.Condition{
			Type:   condType,
			Status: metav1.ConditionStatus(status),
		}

		// Optional fields
		if reason, ok := condition["reason"].(string); ok {
			metaCondition.Reason = reason
		}
		if message, ok := condition["message"].(string); ok {
			metaCondition.Message = message
		}
		if lastTransitionTime, ok := condition["lastTransitionTime"].(string); ok {
			if parsed, err := time.Parse(time.RFC3339, lastTransitionTime); err == nil {
				metaCondition.LastTransitionTime = metav1.NewTime(parsed)
			}
		}

		conditions = append(conditions, metaCondition)
	}

	return conditions
}
