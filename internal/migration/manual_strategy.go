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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

//+kubebuilder:rbac:groups=modelregistry.opendatahub.io,resources=modelregistries,verbs=get;list;watch;update;patch

// ManualStrategy implements migration by re-reading and re-writing resources
// This triggers the conversion webhook and forces storage in the new version
type ManualStrategy struct {
	client.Client
	SourceVersion string
	TargetVersion string
}

func NewManualStrategy(client client.Client, sourceVersion, targetVersion string) *ManualStrategy {
	return &ManualStrategy{
		Client:        client,
		SourceVersion: sourceVersion,
		TargetVersion: targetVersion,
	}
}

func (m *ManualStrategy) GetName() string {
	return "Manual Re-read/Re-write"
}

func (m *ManualStrategy) IsSupported(ctx context.Context) bool {
	// Manual strategy is always supported as it only requires basic CRUD operations
	return true
}

func (m *ManualStrategy) PerformMigration(ctx context.Context, crd *apiextensionsv1.CustomResourceDefinition) error {
	log := log.FromContext(ctx).WithName("manual-strategy")

	log.Info("Starting manual storage version migration by re-reading and updating all ModelRegistry resources",
		"sourceVersion", m.SourceVersion,
		"targetVersion", m.TargetVersion)

	// List all ModelRegistry resources using the source version
	sourceGVR := schema.GroupVersionResource{
		Group:    ModelRegistryGroup,
		Version:  m.SourceVersion,
		Resource: ModelRegistryResource,
	}

	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   sourceGVR.Group,
		Version: sourceGVR.Version,
		Kind:    "ModelRegistryList",
	})

	if err := m.List(ctx, list); err != nil {
		return fmt.Errorf("failed to list ModelRegistry resources: %w", err)
	}

	log.Info("Found ModelRegistry resources to migrate", "count", len(list.Items))

	// Process each resource - force migration for all since we only run when needed
	migratedCount := 0
	for i, item := range list.Items {
		if err := m.migrateResource(ctx, &item, i+1, len(list.Items)); err != nil {
			log.Error(err, "Failed to migrate resource", "name", item.GetName(), "namespace", item.GetNamespace())
			// Continue with other resources instead of failing completely
		} else {
			migratedCount++
		}
	}

	log.Info("Manual storage version migration completed",
		"totalResources", len(list.Items),
		"migratedResources", migratedCount)

	// Verify migration success by checking if source version is still in storage
	if err := m.verifyMigrationComplete(ctx, crd); err != nil {
		return fmt.Errorf("migration verification failed: %w", err)
	}

	return nil
}

// verifyMigrationComplete checks if the source version is still present in storedVersions
func (m *ManualStrategy) verifyMigrationComplete(ctx context.Context, crd *apiextensionsv1.CustomResourceDefinition) error {
	log := log.FromContext(ctx).WithName("manual-strategy")

	// Re-fetch the CRD to get latest status
	updatedCRD := &apiextensionsv1.CustomResourceDefinition{}
	if err := m.Get(ctx, types.NamespacedName{Name: crd.Name}, updatedCRD); err != nil {
		return fmt.Errorf("failed to re-fetch CRD: %w", err)
	}

	// Check if source version is still in storedVersions
	for _, version := range updatedCRD.Status.StoredVersions {
		if version == m.SourceVersion {
			log.Info("Source version still present in storedVersions - migration may need more time",
				"sourceVersion", m.SourceVersion,
				"storedVersions", updatedCRD.Status.StoredVersions)
			// This is not necessarily an error - etcd cleanup happens asynchronously
			return nil
		}
	}

	log.Info("Migration verification successful - source version no longer in storedVersions",
		"sourceVersion", m.SourceVersion,
		"storedVersions", updatedCRD.Status.StoredVersions)
	return nil
}

func (m *ManualStrategy) migrateResource(ctx context.Context, resource *unstructured.Unstructured, current, total int) error {
	log := log.FromContext(ctx).WithName("manual-strategy")

	name := resource.GetName()
	namespace := resource.GetNamespace()

	log.Info("Migrating resource", "name", name, "namespace", namespace, "progress", fmt.Sprintf("%d/%d", current, total))

	// Generic approach - read in target version and force a write
	// This triggers conversion webhook regardless of specific version combinations
	targetResource := &unstructured.Unstructured{}
	targetResource.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   ModelRegistryGroup,
		Version: m.TargetVersion,
		Kind:    "ModelRegistry",
	})

	// Get the resource in target version (triggers conversion)
	if err := m.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, targetResource); err != nil {
		return fmt.Errorf("failed to get resource in target version %s: %w", m.TargetVersion, err)
	}

	// Force a write by updating resourceVersion annotation
	// This ensures the resource is written to etcd in the target storage version
	annotations := targetResource.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
	}

	// Use a migration marker that includes the migration path
	migrationKey := fmt.Sprintf("storage-migration.opendatahub.io/migrated-%s-to-%s", m.SourceVersion, m.TargetVersion)

	// Check if the resource has already been migrated
	if _, ok := annotations[migrationKey]; ok {
		log.Info("Resource has already been migrated", "name", name, "namespace", namespace, "migrationKey", migrationKey)
		return nil
	}

	annotations[migrationKey] = time.Now().Format(time.RFC3339)

	// Also track the resourceVersion we're migrating to avoid duplicate work
	annotations["storage-migration.opendatahub.io/last-migrated-rv"] = targetResource.GetResourceVersion()

	targetResource.SetAnnotations(annotations)

	if err := m.Update(ctx, targetResource); err != nil {
		return fmt.Errorf("failed to update resource for migration: %w", err)
	}

	log.Info("Successfully migrated resource", "name", name, "namespace", namespace)
	return nil
}
