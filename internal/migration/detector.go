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
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/opendatahub-io/model-registry-operator/internal/controller/config"
)

const (
	ModelRegistryCRDName  = "modelregistries.modelregistry.opendatahub.io"
	ModelRegistryGroup    = "modelregistry.opendatahub.io"
	ModelRegistryResource = "modelregistries"
)

//+kubebuilder:rbac:groups=storagemigration.k8s.io,resources=storageversionmigrations,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=apiextensions.k8s.io,resources=customresourcedefinitions,verbs=get;list;watch
//+kubebuilder:rbac:groups=modelregistry.opendatahub.io,resources=modelregistries,verbs=get;list;watch;update;patch

type StorageMigrationManager struct {
	client.Client
	SourceVersion string
	TargetVersion string
	strategies    []MigrationStrategy
}

func NewStorageMigrationManager(client client.Client) *StorageMigrationManager {
	sourceVersion := config.GetStringConfigWithDefault("STORAGE_MIGRATION_SOURCE_VERSION", "")
	targetVersion := config.GetStringConfigWithDefault("STORAGE_MIGRATION_TARGET_VERSION", "")

	return &StorageMigrationManager{
		Client:        client,
		SourceVersion: sourceVersion,
		TargetVersion: targetVersion,
		strategies: []MigrationStrategy{
			NewSVMStrategy(client, sourceVersion, targetVersion),    // Try StorageVersionMigration first
			NewManualStrategy(client, sourceVersion, targetVersion), // Fallback to manual migration
		},
	}
}

func (m *StorageMigrationManager) StartMigrationMonitor(ctx context.Context) {
	log := log.FromContext(ctx).WithName("storage-migration")

	if m.SourceVersion == m.TargetVersion {
		log.Info("Source and target versions are the same, skipping migration",
			"version", m.SourceVersion)
		return
	}

	log.Info("Starting storage migration monitor",
		"sourceVersion", m.SourceVersion,
		"targetVersion", m.TargetVersion)

	go func() {
		// Wait for manager to be ready
		select {
		case <-time.After(30 * time.Second):
		case <-ctx.Done():
			return
		}

		if err := m.checkAndPerformMigration(ctx); err != nil {
			log.Error(err, "Failed to perform migration")
			return
		}
	}()
}

func (m *StorageMigrationManager) checkAndPerformMigration(ctx context.Context) error {
	log := log.FromContext(ctx).WithName("storage-migration")

	// Get the ModelRegistry CRD
	crd := &apiextensionsv1.CustomResourceDefinition{}
	if err := m.Get(ctx, types.NamespacedName{Name: ModelRegistryCRDName}, crd); err != nil {
		return fmt.Errorf("failed to get ModelRegistry CRD: %w", err)
	}

	// Check if source version is still in storage versions
	if !m.needsStorageVersionMigration(crd) {
		log.Info("No storage version migration needed",
			"sourceVersion", m.SourceVersion,
			"storedVersions", crd.Status.StoredVersions)
		return nil
	}

	log.Info("Source version found in storage versions, starting migration",
		"sourceVersion", m.SourceVersion,
		"targetVersion", m.TargetVersion,
		"storedVersions", crd.Status.StoredVersions)

	// Try each strategy in order until one succeeds
	for _, strategy := range m.strategies {
		if strategy.IsSupported(ctx) {
			log.Info("Using migration strategy", "strategy", strategy.GetName())

			if err := strategy.PerformMigration(ctx, crd); err != nil {
				log.Error(err, "Migration strategy failed", "strategy", strategy.GetName())
				continue // Try next strategy
			}

			log.Info("Migration completed successfully", "strategy", strategy.GetName())
			return nil
		} else {
			log.Info("Migration strategy not supported", "strategy", strategy.GetName())
		}
	}

	return fmt.Errorf("no supported migration strategy found")
}

func (m *StorageMigrationManager) needsStorageVersionMigration(crd *apiextensionsv1.CustomResourceDefinition) bool {
	for _, version := range crd.Status.StoredVersions {
		if version == m.SourceVersion {
			return true
		}
	}
	return false
}
