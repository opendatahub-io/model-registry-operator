package controller

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"strings"

	catalogv1alpha1 "github.com/opendatahub-io/model-registry-operator/api/catalog/v1alpha1"
	"github.com/opendatahub-io/model-registry-operator/internal/controller/config"
	imagev1 "github.com/openshift/api/image/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// A shared source uses one ImageStream. Separate sources get independent streams.
const CatalogDataImageStreamName = "model-catalog-data"
const BenchmarkDataImageStreamName = "model-catalog-benchmark-data"
const catalogDataImageStreamTag = "stable"
const conditionDataImageResolved = "DataImageResolved"
const conditionDataImageImportHealthy = "DataImageImportHealthy"
const conditionDataImageUpdateBlocked = "DataImageUpdateBlocked"
const managedDataImageSourceAnnotation = "aihub.opendatahub.io/managed-source"

var catalogDataDigestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

func dataImageSelection(selection *string) (string, error) {
	if selection == nil || *selection == "" {
		return "", nil
	}
	if *selection != catalogDataImageStreamTag && !catalogDataDigestPattern.MatchString(*selection) {
		return "", fmt.Errorf("unsupported data image selection %q: use an empty field, stable, or a sha256 digest with 64 lowercase hexadecimal characters", *selection)
	}
	return *selection, nil
}

// The product supplies release-pinned bootstrap images. Import sources default
// to stable in each release repository, and may point to standalone channels.
func catalogDataImageSources() [2]string {
	stable := catalogDataImageStreamTag
	return [2]string{
		config.GetStringConfigWithDefault(config.CatalogDataImageStreamSource, config.ResolveImage(&stable, config.CatalogDataImage, config.DefaultCatalogDataImage)),
		config.GetStringConfigWithDefault(config.BenchmarkDataImageStreamSource, config.ResolveImage(&stable, config.BenchmarkDataImage, config.DefaultBenchmarkDataImage)),
	}
}

func benchmarkDataImageStreamName() string {
	sources := catalogDataImageSources()
	if sources[0] == sources[1] {
		return CatalogDataImageStreamName
	}
	return BenchmarkDataImageStreamName
}

func setCatalogDataImageCondition(catalog *catalogv1alpha1.Catalog, conditionType string, status metav1.ConditionStatus, reason, message string) {
	apimeta.SetStatusCondition(&catalog.Status.Conditions, metav1.Condition{
		Type: conditionType, Status: status, Reason: reason,
		Message: message, ObservedGeneration: catalog.Generation,
	})
}

type catalogDataImagesUnavailable struct {
	cause error
}

func (e *catalogDataImagesUnavailable) Error() string {
	return "Catalog data images are unavailable; deployment scale-down is temporary containment"
}
func (e *catalogDataImagesUnavailable) Unwrap() error { return e.cause }

// Resolve the selections independently. Resolution permits activation to start;
// it never establishes successful activation or restores aggregate readiness.
func (r *CatalogReconciler) resolveCatalogDataImages(ctx context.Context, catalog *catalogv1alpha1.Catalog, params *CatalogParams) error {
	sources := catalogDataImageSources()
	targets := []struct {
		name, streamName, source, sourceEnv, resolvedType, healthType string
		selection                                                     *string
		image                                                         *string
	}{
		{"catalog", CatalogDataImageStreamName, sources[0], config.CatalogDataImageStreamSource, "CatalogDataImageResolved", "CatalogDataImageImportHealthy", catalog.Spec.CatalogDataImageStream, &params.CatalogDataImage},
		{"benchmark", benchmarkDataImageStreamName(), sources[1], config.BenchmarkDataImageStreamSource, "BenchmarkDataImageResolved", "BenchmarkDataImageImportHealthy", catalog.Spec.BenchmarkDataImageStream, &params.BenchmarkDataImage},
	}
	streams := make(map[string]*imagev1.ImageStream)
	var sourceEvidence [2]catalogv1alpha1.CatalogImageSource
	var causes []error
	for index, target := range targets {
		sourceEvidence[index].Reference = *target.image
		previousHealth := apimeta.FindStatusCondition(catalog.Status.Conditions, target.healthType)
		previousResolution := apimeta.FindStatusCondition(catalog.Status.Conditions, target.resolvedType)
		awaitingRecovery := (previousHealth != nil && previousHealth.Status == metav1.ConditionFalse) ||
			(previousResolution != nil && previousResolution.Reason == "AwaitingSuccessfulImport")
		resolved := func(status metav1.ConditionStatus, reason, message string) {
			setCatalogDataImageCondition(catalog, target.resolvedType, status, reason, target.name+": "+message)
		}
		health := func(status metav1.ConditionStatus, reason, message string) {
			setCatalogDataImageCondition(catalog, target.healthType, status, reason, target.name+": "+message)
		}
		selection, err := dataImageSelection(target.selection)
		if selection != catalogDataImageStreamTag || err != nil || !r.Capabilities.IsOpenShift {
			health(metav1.ConditionTrue, "NotTrackingImageStream", "The selection does not follow ImageStream imports")
		}
		if err != nil {
			resolved(metav1.ConditionFalse, "InvalidDataImageSelection", err.Error())
			continue
		}
		if selection == "" {
			resolved(metav1.ConditionTrue, "ReleaseDefault", "Using the current release image: "+*target.image)
			continue
		}
		if selection != catalogDataImageStreamTag {
			// Rollback does not depend on the ImageStream API or import history.
			*target.image = config.ResolveImage(&selection, target.sourceEnv, target.source)
			sourceEvidence[index].Reference = *target.image
			resolved(metav1.ConditionTrue, "DigestPinned", "Using the pinned image: "+*target.image)
			continue
		}
		if !r.Capabilities.IsOpenShift {
			resolved(metav1.ConditionFalse, "UnsupportedDataImageSelection", "The stable selection requires OpenShift ImageStreams")
			continue
		}
		sourceEvidence[index] = catalogv1alpha1.CatalogImageSource{Reference: target.source, ImageStreamName: target.streamName, Tag: catalogDataImageStreamTag}
		stream := streams[target.streamName]
		if stream == nil {
			stream, err = r.ensureCatalogDataImageStreamForSource(ctx, catalog, params, target.streamName, target.source)
			if err != nil {
				resolved(metav1.ConditionFalse, "ImageStreamUnavailable", err.Error())
				health(metav1.ConditionFalse, "ImageStreamUnavailable", err.Error())
				causes = append(causes, err)
				continue
			}
			streams[target.streamName] = stream
		}
		sourceEvidence[index].ImageStreamUID = stream.UID
		for _, tag := range stream.Spec.Tags {
			if tag.Name == catalogDataImageStreamTag {
				if tag.From != nil {
					sourceEvidence[index].Reference = tag.From.Name
				}
				if tag.Generation != nil {
					sourceEvidence[index].TagGeneration = *tag.Generation
				}
			}
		}
		importHealth := catalogDataImageImportHealth(stream)
		health(importHealth.Status, importHealth.Reason, importHealth.Message)
		if importHealth.Status == metav1.ConditionFalse {
			resolved(metav1.ConditionFalse, importHealth.Reason, "The requested import is unusable; Catalog readiness is false and deployment containment is requested. "+importHealth.Message)
			continue
		}
		if importHealth.Status == metav1.ConditionUnknown && awaitingRecovery {
			resolved(metav1.ConditionUnknown, "AwaitingSuccessfulImport", "Waiting for successful import after the previous failure; activation is still required for recovery")
			continue
		}
		if image := lastImportedCatalogDataImage(stream); importHealth.Status == metav1.ConditionTrue && image != "" {
			*target.image = image
			resolved(metav1.ConditionTrue, "ImageStreamResolved", "Using the last successfully imported image: "+image)
			continue
		}
		// Pending is ordinary progress. The bundled image bootstraps a fresh
		// installation, while an existing deployment keeps its current image.
		applied := *params
		if err := r.retainAppliedCatalogDataImages(ctx, catalog, &applied); err != nil {
			resolved(metav1.ConditionFalse, "AppliedImageReadFailed", err.Error())
			causes = append(causes, err)
			continue
		}
		if target.name == "catalog" {
			*target.image = applied.CatalogDataImage
		} else {
			*target.image = applied.BenchmarkDataImage
		}
		resolved(metav1.ConditionFalse, "NoSuccessfulImport", "Waiting for the first successful import; using the current applied or release image: "+*target.image)
	}

	aggregateDataImageConditions(catalog, conditionDataImageResolved, "CatalogDataImageResolved", "BenchmarkDataImageResolved")
	aggregateDataImageConditions(catalog, conditionDataImageImportHealthy, "CatalogDataImageImportHealthy", "BenchmarkDataImageImportHealthy")
	recordCatalogImageAttempt(catalog, params, sourceEvidence)
	if catalog.Status.ImageUpdate.CurrentAttempt.SelectionOutcome.State == "Failed" {
		return &catalogDataImagesUnavailable{cause: errors.Join(causes...)}
	}
	if catalog.Status.ImageUpdate.CurrentAttempt.SelectionOutcome.State == "Pending" && catalog.Status.ImageUpdate.LastFailure != nil {
		return &catalogDataImagesUnavailable{}
	}
	return nil
}

func aggregateDataImageConditions(catalog *catalogv1alpha1.Catalog, aggregateType, catalogType, benchmarkType string) {
	conditions := []*metav1.Condition{apimeta.FindStatusCondition(catalog.Status.Conditions, catalogType), apimeta.FindStatusCondition(catalog.Status.Conditions, benchmarkType)}
	worst := conditions[0]
	priority := func(condition *metav1.Condition) int {
		if condition.Status == metav1.ConditionFalse && condition.Reason != "NoSuccessfulImport" {
			return 0 // Actual faults take precedence over ordinary pending imports.
		}
		if condition.Status != metav1.ConditionTrue {
			return 1
		}
		return 2
	}
	for _, condition := range conditions[1:] {
		if priority(condition) < priority(worst) {
			worst = condition
		}
	}
	reason := worst.Reason
	if worst.Status == metav1.ConditionTrue && conditions[0].Reason != conditions[1].Reason {
		reason = "IndependentSelections"
	}
	setCatalogDataImageCondition(catalog, aggregateType, worst.Status, reason, conditions[0].Message+"; "+conditions[1].Message)
}

// OpenShift keeps failed import conditions and the successful image history
// separately. Ignore failures from an older source generation after a new import
// has been requested, and retain the last successful image while it is pending.
func catalogDataImageImportHealth(stream *imagev1.ImageStream) metav1.Condition {
	condition := metav1.Condition{Status: metav1.ConditionUnknown, Reason: "ImportPending", Message: "Waiting for the standalone data image import"}
	var generation int64
	for _, tag := range stream.Spec.Tags {
		if tag.Name == catalogDataImageStreamTag && tag.Generation != nil {
			generation = *tag.Generation
			break
		}
	}
	for _, tag := range stream.Status.Tags {
		if tag.Tag != catalogDataImageStreamTag {
			continue
		}
		for _, imported := range tag.Conditions {
			if imported.Type == imagev1.ImportSuccess && imported.Status == corev1.ConditionFalse && imported.Generation >= generation {
				condition.Status, condition.Reason = metav1.ConditionFalse, "ImportFailed"
				condition.Message = "The standalone data image import failed; resolve the import or select valid images, then activate the current pair to recover"
				if imported.Reason != "" {
					condition.Message += ": " + imported.Reason
				}
				if imported.Message != "" {
					condition.Message += ": " + imported.Message
				}
				return condition
			}
		}
		if len(tag.Items) == 0 || tag.Items[0].Generation < generation {
			return condition
		}
		if importedCatalogDataImage(tag.Items[0]) == "" {
			condition.Status, condition.Reason = metav1.ConditionFalse, "InvalidImportedImage"
			condition.Message = "The standalone data ImageStream has no usable digest in its latest import; a usable current image pair and validated activation are required for recovery"
			return condition
		}
		condition.Status, condition.Reason = metav1.ConditionTrue, "ImportSucceeded"
		condition.Message = "The standalone data image import succeeded"
		return condition
	}
	return condition
}

func (r *CatalogReconciler) retainAppliedCatalogDataImages(ctx context.Context, catalog *catalogv1alpha1.Catalog, params *CatalogParams) error {
	deployment := &appsv1.Deployment{}
	if err := r.Get(ctx, client.ObjectKey{Name: catalogResourceName, Namespace: catalog.Namespace}, deployment); err != nil {
		if apierrors.IsNotFound(err) {
			return nil // No applied image yet; params already contain the release images.
		}
		return fmt.Errorf("reading applied catalog data images: %w", err)
	}
	for _, container := range deployment.Spec.Template.Spec.InitContainers {
		switch container.Name {
		case "catalog-data-init":
			params.CatalogDataImage = container.Image
		case "benchmark-data-init":
			params.BenchmarkDataImage = container.Image
		}
	}
	return nil
}

func (r *CatalogReconciler) ensureCatalogDataImageStream(ctx context.Context, catalog *catalogv1alpha1.Catalog, params *CatalogParams) (*imagev1.ImageStream, error) {
	return r.ensureCatalogDataImageStreamForSource(ctx, catalog, params, CatalogDataImageStreamName, catalogDataImageSources()[0])
}

func (r *CatalogReconciler) ensureCatalogDataImageStreamForSource(ctx context.Context, catalog *catalogv1alpha1.Catalog, params *CatalogParams, name, source string) (*imagev1.ImageStream, error) {
	renderParams := *params
	renderParams.DataImageStreamName, renderParams.DataImageStreamSource = name, source
	desired := &imagev1.ImageStream{}
	if err := r.Apply(&renderParams, "catalog-data-imagestream.yaml.tmpl", desired); err != nil {
		return nil, fmt.Errorf("rendering catalog data ImageStream: %w", err)
	}
	stream := &imagev1.ImageStream{ObjectMeta: metav1.ObjectMeta{
		Name: desired.Name, Namespace: desired.Namespace,
	}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, stream, func() error {
		if stream.Labels == nil {
			stream.Labels = make(map[string]string)
		}
		maps.Copy(stream.Labels, desired.Labels)
		if err := controllerutil.SetControllerReference(catalog, stream, r.Scheme); err != nil {
			return err
		}
		// Preserve import generations and unrelated tags. OpenShift maintains the
		// generation of this tag and its successful import history in status.
		for _, desiredTag := range desired.Spec.Tags {
			index := -1
			for i := range stream.Spec.Tags {
				if stream.Spec.Tags[i].Name == desiredTag.Name {
					index = i
					break
				}
			}
			if index == -1 {
				stream.Spec.Tags = append(stream.Spec.Tags, *desiredTag.DeepCopy())
				continue
			}
			tag := &stream.Spec.Tags[index]
			// Track changes to the configured source while preserving manual
			// ImageStream overrides. OpenShift assigns the new import generation.
			managedSource := tag.Annotations[managedDataImageSourceAnnotation]
			if tag.From == nil || (tag.From.Kind == "DockerImage" && tag.From.Name == managedSource) {
				if tag.From == nil || tag.From.Name != desiredTag.From.Name || tag.From.Kind != desiredTag.From.Kind {
					tag.From = desiredTag.From.DeepCopy()
					generation := int64(0) // Explicitly request a fresh OpenShift import.
					tag.Generation = &generation
				}
			}
			if tag.From.Kind == desiredTag.From.Kind && tag.From.Name == desiredTag.From.Name {
				if tag.Annotations == nil {
					tag.Annotations = make(map[string]string)
				}
				tag.Annotations[managedDataImageSourceAnnotation] = desiredTag.From.Name
			}
			tag.Reference = desiredTag.Reference
			tag.ImportPolicy = desiredTag.ImportPolicy
			tag.ReferencePolicy = desiredTag.ReferencePolicy
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("reconciling catalog data ImageStream: %w", err)
	}
	return stream, nil
}

// Import failures update conditions without replacing the successful status
// history. Resolve an immutable digest from that history, never spec.tags.from.
func lastImportedCatalogDataImage(stream *imagev1.ImageStream) string {
	for _, tag := range stream.Status.Tags {
		if tag.Tag != catalogDataImageStreamTag {
			continue
		}
		for _, item := range tag.Items {
			if image := importedCatalogDataImage(item); image != "" {
				return image
			}
		}
	}
	return ""
}

func importedCatalogDataImage(item imagev1.TagEvent) string {
	repository, digest, _ := strings.Cut(item.DockerImageReference, "@")
	if item.Image != "" {
		digest = item.Image
	}
	if repository == "" || !catalogDataDigestPattern.MatchString(digest) {
		return ""
	}
	// A TagEvent may report a tagged source alongside its imported digest.
	if colon := strings.LastIndexByte(repository, ':'); colon > strings.LastIndexByte(repository, '/') {
		repository = repository[:colon]
	}
	return repository + "@" + digest
}

func (r *CatalogReconciler) getCatalogsForDataImageStream(ctx context.Context, object client.Object) []reconcile.Request {
	if object.GetName() != CatalogDataImageStreamName && object.GetName() != BenchmarkDataImageStreamName {
		return nil
	}
	var catalogs catalogv1alpha1.CatalogList
	if err := r.List(ctx, &catalogs, client.InNamespace(object.GetNamespace())); err != nil {
		r.Log.Error(err, "failed to list Catalogs for data ImageStream")
		return nil
	}
	var requests []reconcile.Request
	for _, catalog := range catalogs.Items {
		catalogSelection, catalogErr := dataImageSelection(catalog.Spec.CatalogDataImageStream)
		benchmarkSelection, benchmarkErr := dataImageSelection(catalog.Spec.BenchmarkDataImageStream)
		followsCatalog := object.GetName() == CatalogDataImageStreamName && catalogErr == nil && catalogSelection == catalogDataImageStreamTag
		followsBenchmark := object.GetName() == benchmarkDataImageStreamName() && benchmarkErr == nil && benchmarkSelection == catalogDataImageStreamTag
		if followsCatalog || followsBenchmark {
			requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&catalog)})
		}
	}
	return requests
}
