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

package v1alpha1_test

import (
	"context"
	"strings"

	catalogv1alpha1 "github.com/opendatahub-io/model-registry-operator/api/catalog/v1alpha1"
	"github.com/opendatahub-io/model-registry-operator/internal/controller/config"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Catalog validating webhook", func() {
	const namespaceName = "webhook-catalog-test"

	BeforeEach(func(ctx context.Context) {
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: namespaceName},
		}
		_ = k8sClient.Create(ctx, ns)
	})

	It("Should reject creation of Catalog CR when name is not catalog", func(ctx context.Context) {
		cat := &catalogv1alpha1.Catalog{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "my-catalog",
				Namespace: namespaceName,
			},
		}
		err := k8sClient.Create(ctx, cat)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("catalog resource name must be 'catalog'"))
	})

	It("Should accept creation of Catalog CR when name is catalog", func(ctx context.Context) {
		cat := &catalogv1alpha1.Catalog{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "catalog",
				Namespace: namespaceName,
			},
		}
		err := k8sClient.Create(ctx, cat)
		Expect(err).NotTo(HaveOccurred())

		By("Allowing updates to the catalog CR")
		cat.Annotations = map[string]string{"updated": "true"}
		err = k8sClient.Update(ctx, cat)
		Expect(err).NotTo(HaveOccurred())

		By("Cleaning up catalog CR")
		err = k8sClient.Delete(ctx, cat)
		Expect(err).NotTo(HaveOccurred())
	})

	It("Should reject creation of Catalog CR outside the configured registries namespace", func(ctx context.Context) {
		Expect(config.SetRegistriesNamespace(namespaceName)).To(Succeed())
		defer func() {
			Expect(config.SetRegistriesNamespace("")).To(Succeed())
		}()

		otherNamespaceName := namespaceName + "-other"
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: otherNamespaceName},
		}
		Expect(k8sClient.Create(ctx, ns)).To(Succeed())

		cat := &catalogv1alpha1.Catalog{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "catalog",
				Namespace: otherNamespaceName,
			},
		}
		err := k8sClient.Create(ctx, cat)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("namespace must be " + namespaceName))
	})

	It("Should preserve deprecated data image fields with strict validation on create and update", func(ctx context.Context) {
		for _, images := range [][2]string{
			{"", ""},
			{"v2.19", "latest"},
			{"sha256:" + strings.Repeat("a", 64), "sha256:" + strings.Repeat("b", 64)},
			{strings.Repeat("a", 128), strings.Repeat("b", 128)},
		} {
			// Send an unstructured manifest so strict validation checks the CRD schema.
			cat := &unstructured.Unstructured{Object: map[string]interface{}{
				"apiVersion": catalogv1alpha1.GroupVersion.String(),
				"kind":       "Catalog",
				"metadata":   map[string]interface{}{"name": "catalog", "namespace": namespaceName},
				"spec": map[string]interface{}{
					"catalogDataImage":   images[0],
					"benchmarkDataImage": images[1],
				},
			}}
			Expect(k8sClient.Create(ctx, cat, client.FieldValidation(metav1.FieldValidationStrict))).To(Succeed())
			assertSavedImages := func(catalogImage, benchmarkImage string) {
				saved := &catalogv1alpha1.Catalog{}
				Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cat), saved)).To(Succeed())
				Expect(saved.Spec.CatalogDataImage).To(Equal(&catalogImage))
				Expect(saved.Spec.BenchmarkDataImage).To(Equal(&benchmarkImage))
			}
			assertSavedImages(images[0], images[1])

			// Legacy fields also coexist with the new selections and remain editable.
			Expect(unstructured.SetNestedField(cat.Object, images[1], "spec", "catalogDataImage")).To(Succeed())
			Expect(unstructured.SetNestedField(cat.Object, images[0], "spec", "benchmarkDataImage")).To(Succeed())
			Expect(unstructured.SetNestedField(cat.Object, "stable", "spec", "catalogDataImageStream")).To(Succeed())
			Expect(unstructured.SetNestedField(cat.Object, "sha256:"+strings.Repeat("c", 64), "spec", "benchmarkDataImageStream")).To(Succeed())
			Expect(k8sClient.Update(ctx, cat, client.FieldValidation(metav1.FieldValidationStrict))).To(Succeed())
			assertSavedImages(images[1], images[0])
			Expect(k8sClient.Delete(ctx, cat)).To(Succeed())
		}
	})

	It("Should reject unsupported image selections on create and update without changing the saved selection", func(ctx context.Context) {
		for _, fieldName := range []string{"catalogDataImageStream", "benchmarkDataImageStream"} {
			for _, invalid := range []string{"latest", " stable", "stable ", "sha256:abc", "sha256:" + strings.Repeat("A", 64), "repository@sha256:" + strings.Repeat("a", 64)} {
				cat := &catalogv1alpha1.Catalog{ObjectMeta: metav1.ObjectMeta{Name: "catalog", Namespace: namespaceName}}
				setSelection := func(value string) {
					if fieldName == "catalogDataImageStream" {
						cat.Spec.CatalogDataImageStream = &value
					} else {
						cat.Spec.BenchmarkDataImageStream = &value
					}
				}
				setSelection(invalid)
				err := k8sClient.Create(ctx, cat)
				Expect(apierrors.IsInvalid(err)).To(BeTrue(), "create with %s=%q: %v", fieldName, invalid, err)
				Expect(err.Error()).To(ContainSubstring("spec." + fieldName))

				setSelection("stable")
				Expect(k8sClient.Create(ctx, cat)).To(Succeed())
				setSelection(invalid)
				err = k8sClient.Update(ctx, cat)
				Expect(apierrors.IsInvalid(err)).To(BeTrue(), "update with %s=%q: %v", fieldName, invalid, err)
				Expect(err.Error()).To(ContainSubstring("spec." + fieldName))
				Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cat), cat)).To(Succeed())
				if fieldName == "catalogDataImageStream" {
					Expect(*cat.Spec.CatalogDataImageStream).To(Equal("stable"))
				} else {
					Expect(*cat.Spec.BenchmarkDataImageStream).To(Equal("stable"))
				}
				Expect(k8sClient.Delete(ctx, cat)).To(Succeed())
			}
		}
	})

	It("Should accept empty and independent image selections", func(ctx context.Context) {
		cat := &catalogv1alpha1.Catalog{ObjectMeta: metav1.ObjectMeta{Name: "catalog", Namespace: namespaceName}}
		Expect(k8sClient.Create(ctx, cat)).To(Succeed())
		DeferCleanup(func(ctx context.Context) { Expect(k8sClient.Delete(ctx, cat)).To(Succeed()) })
		for _, selections := range [][2]string{{"", ""}, {"stable", ""}, {"", "stable"}, {"stable", "sha256:" + strings.Repeat("a", 64)}, {"sha256:" + strings.Repeat("b", 64), "sha256:" + strings.Repeat("c", 64)}} {
			cat.Spec.CatalogDataImageStream, cat.Spec.BenchmarkDataImageStream = &selections[0], &selections[1]
			Expect(k8sClient.Update(ctx, cat)).To(Succeed())
		}
	})
})
