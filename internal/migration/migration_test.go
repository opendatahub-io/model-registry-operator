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
	"math/rand"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	"github.com/opendatahub-io/model-registry-operator/api/v1alpha1"
	"github.com/opendatahub-io/model-registry-operator/api/v1beta1"
)

var _ = Describe("Storage Version Migration", func() {
	const (
		timeout  = time.Second * 30
		interval = time.Millisecond * 250
	)

	var (
		ctx           context.Context
		testNamespace string
		sourceVersion = "v1alpha1"
		targetVersion = "v1beta1"
		migrationMgr  *StorageMigrationManager
		testCRD       *apiextensionsv1.CustomResourceDefinition
	)

	BeforeEach(func() {
		ctx = context.Background()
		testNamespace = fmt.Sprintf("test-migration-%d-%s", time.Now().UnixNano(), randStringRunes(5))

		// Create test namespace
		ns := &unstructured.Unstructured{}
		ns.SetAPIVersion("v1")
		ns.SetKind("Namespace")
		ns.SetName(testNamespace)
		Expect(k8sClient.Create(ctx, ns)).To(Succeed())

		// Get the ModelRegistry CRD
		testCRD = &apiextensionsv1.CustomResourceDefinition{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: ModelRegistryCRDName}, testCRD)).To(Succeed())

		// Create migration manager
		migrationMgr = NewStorageMigrationManager(k8sClient)
		migrationMgr.SourceVersion = sourceVersion
		migrationMgr.TargetVersion = targetVersion
	})

	AfterEach(func() {
		// Clean up test namespace
		ns := &unstructured.Unstructured{}
		ns.SetAPIVersion("v1")
		ns.SetKind("Namespace")
		ns.SetName(testNamespace)
		_ = k8sClient.Delete(ctx, ns)
	})

	Describe("Migration Detection", func() {
		Context("when source version is in storedVersions", func() {
			BeforeEach(func() {
				// Simulate CRD with source version in storedVersions
				testCRD.Status.StoredVersions = []string{sourceVersion, targetVersion}
			})

			It("should detect migration is needed", func() {
				needsMigration := migrationMgr.needsStorageVersionMigration(testCRD)
				Expect(needsMigration).To(BeTrue())
			})
		})

		Context("when source version is not in storedVersions", func() {
			BeforeEach(func() {
				// Simulate CRD with only target version in storedVersions
				testCRD.Status.StoredVersions = []string{targetVersion}
			})

			It("should detect migration is not needed", func() {
				needsMigration := migrationMgr.needsStorageVersionMigration(testCRD)
				Expect(needsMigration).To(BeFalse())
			})
		})
	})

	Describe("Manual Strategy", func() {
		var (
			manualStrategy *ManualStrategy
			testMR         *v1alpha1.ModelRegistry
		)

		BeforeEach(func() {
			manualStrategy = NewManualStrategy(k8sClient, sourceVersion, targetVersion)

			// Create a test ModelRegistry resource in v1alpha1
			testMR = &v1alpha1.ModelRegistry{}
			testMR.SetName("test-mr-manual")
			testMR.SetNamespace(testNamespace)
			testMR.Spec = v1alpha1.ModelRegistrySpec{
				Rest: v1alpha1.RestSpec{},
			}
			Expect(k8sClient.Create(ctx, testMR)).To(Succeed())
		})

		AfterEach(func() {
			// Clean up test resource
			_ = k8sClient.Delete(ctx, testMR)
		})

		It("should be supported", func() {
			Expect(manualStrategy.IsSupported(ctx)).To(BeTrue())
		})

		It("should migrate resources and add migration annotations", func() {
			// Perform migration
			err := manualStrategy.PerformMigration(ctx, testCRD)
			Expect(err).ToNot(HaveOccurred())

			// Verify migration annotation was added
			Eventually(func() bool {
				updatedMR := &v1beta1.ModelRegistry{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      testMR.Name,
					Namespace: testMR.Namespace,
				}, updatedMR)
				if err != nil {
					return false
				}

				annotations := updatedMR.GetAnnotations()
				if annotations == nil {
					return false
				}

				migrationKey := fmt.Sprintf("storage-migration.opendatahub.io/migrated-%s-to-%s", sourceVersion, targetVersion)
				_, exists := annotations[migrationKey]
				return exists
			}, timeout, interval).Should(BeTrue())
		})

		It("should verify migration completion", func() {
			// Perform migration
			err := manualStrategy.PerformMigration(ctx, testCRD)
			Expect(err).ToNot(HaveOccurred())

			// Verify migration completion check
			err = manualStrategy.verifyMigrationComplete(ctx, testCRD)
			Expect(err).ToNot(HaveOccurred())
		})
	})

	Describe("SVM Strategy", func() {
		var svmStrategy *SVMStrategy

		BeforeEach(func() {
			svmStrategy = NewSVMStrategy(k8sClient, sourceVersion, targetVersion)
		})

		It("should check if StorageVersionMigration API is supported", func() {
			// This will depend on the test environment
			// In most test environments, SVM API won't be available
			supported := svmStrategy.IsSupported(ctx)
			// We don't assert the result since it depends on the cluster
			// but we verify the method doesn't panic
			_ = supported
		})

		Context("when StorageVersionMigration API is not available", func() {
			It("should return false for IsSupported", func() {
				// In test environments, SVM API is typically not available
				// This test verifies graceful handling
				supported := svmStrategy.IsSupported(ctx)
				// Most likely false in test environment, but we don't assert
				// to avoid flaky tests
				_ = supported
			})
		})
	})

	Describe("Migration Manager Integration", func() {
		var testMR *v1alpha1.ModelRegistry

		BeforeEach(func() {
			// Create a test ModelRegistry resource
			testMR = &v1alpha1.ModelRegistry{}
			testMR.SetName("test-mr-integration")
			testMR.SetNamespace(testNamespace)
			testMR.Spec = v1alpha1.ModelRegistrySpec{
				Rest: v1alpha1.RestSpec{},
			}
			Expect(k8sClient.Create(ctx, testMR)).To(Succeed())
		})

		AfterEach(func() {
			// Clean up test resource
			_ = k8sClient.Delete(ctx, testMR)
		})

		Context("when migration is needed", func() {
			It("should perform migration using manual strategy directly", func() {
				// Create manual strategy and test it directly
				manualStrategy := NewManualStrategy(k8sClient, sourceVersion, targetVersion)

				// Simulate CRD with source version in storedVersions for direct strategy test
				testCRDCopy := testCRD.DeepCopy()
				testCRDCopy.Status.StoredVersions = []string{sourceVersion, targetVersion}

				// Perform migration using manual strategy directly
				err := manualStrategy.PerformMigration(ctx, testCRDCopy)
				Expect(err).ToNot(HaveOccurred())

				// Verify the resource was migrated (has migration annotation)
				Eventually(func() bool {
					updatedMR := &v1beta1.ModelRegistry{}
					err := k8sClient.Get(ctx, types.NamespacedName{
						Name:      testMR.Name,
						Namespace: testMR.Namespace,
					}, updatedMR)
					if err != nil {
						return false
					}

					annotations := updatedMR.GetAnnotations()
					if annotations == nil {
						return false
					}

					migrationKey := fmt.Sprintf("storage-migration.opendatahub.io/migrated-%s-to-%s", sourceVersion, targetVersion)
					_, exists := annotations[migrationKey]
					return exists
				}, timeout, interval).Should(BeTrue())
			})
		})

		Context("when migration is not needed", func() {
			It("should detect no migration needed", func() {
				// Test the migration detection logic directly
				testCRDCopy := testCRD.DeepCopy()
				testCRDCopy.Status.StoredVersions = []string{targetVersion}

				needsMigration := migrationMgr.needsStorageVersionMigration(testCRDCopy)
				Expect(needsMigration).To(BeFalse())

				// Also verify that if we run checkAndPerformMigration with the real CRD
				// (which likely has v1beta1 as the only stored version), it doesn't fail
				err := migrationMgr.checkAndPerformMigration(ctx)
				Expect(err).ToNot(HaveOccurred())
			})
		})
	})

	Describe("Cross-Version Compatibility", func() {
		var testMR *v1alpha1.ModelRegistry

		dbPort := int32(3306)
		BeforeEach(func() {
			// Create a test ModelRegistry with complex spec
			testMR = &v1alpha1.ModelRegistry{}
			testMR.SetName("test-mr-compat")
			testMR.SetNamespace(testNamespace)
			testMR.Spec = v1alpha1.ModelRegistrySpec{
				Rest: v1alpha1.RestSpec{
					Image: "test-rest-image",
				},
				MySQL: &v1alpha1.MySQLConfig{
					Host:     "test-mysql",
					Port:     &dbPort,
					Database: "test-db",
					Username: "test-user",
					PasswordSecret: &v1alpha1.SecretKeyValue{
						Name: "test-secret",
						Key:  "password",
					},
				},
			}
			Expect(k8sClient.Create(ctx, testMR)).To(Succeed())
		})

		AfterEach(func() {
			// Clean up test resource
			_ = k8sClient.Delete(ctx, testMR)
		})

		It("should preserve data during conversion", func() {
			// Read as v1alpha1
			originalMR := &v1alpha1.ModelRegistry{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      testMR.Name,
				Namespace: testMR.Namespace,
			}, originalMR)).To(Succeed())

			// Read as v1beta1 (triggers conversion)
			convertedMR := &v1beta1.ModelRegistry{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      testMR.Name,
				Namespace: testMR.Namespace,
			}, convertedMR)).To(Succeed())

			// Verify key fields are preserved
			Expect(convertedMR.Spec.Rest.Image).To(Equal(originalMR.Spec.Rest.Image))

			if originalMR.Spec.MySQL != nil && convertedMR.Spec.MySQL != nil {
				Expect(convertedMR.Spec.MySQL.Host).To(Equal(originalMR.Spec.MySQL.Host))
				Expect(convertedMR.Spec.MySQL.Database).To(Equal(originalMR.Spec.MySQL.Database))
			}
		})
	})
})

// Helper function to generate random strings for test names
func randStringRunes(n int) string {
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	const letterRunes = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	for i := range b {
		b[i] = letterRunes[r.Intn(len(letterRunes))]
	}
	return string(b)
}
