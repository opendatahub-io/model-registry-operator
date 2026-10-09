package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	catalogv1alpha1 "github.com/opendatahub-io/model-registry-operator/api/catalog/v1alpha1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/uuid"
)

const (
	conditionCatalogReady         = "Ready"
	conditionImageSelectionReady  = "ImageSelectionReady"
	conditionDataActivationReady  = "DataActivationReady"
	conditionWorkloadAvailable    = "WorkloadAvailable"
	catalogImageAttemptAnnotation = "aihub.opendatahub.io/data-image-attempt"
)

// Persist a new opaque attempt only for changed image intent, immutable images,
// or relevant import outcomes. Ordinary resyncs, timestamps, history entries and
// unrelated Catalog spec changes do not invalidate an activated image pair.
func recordCatalogImageAttempt(catalog *catalogv1alpha1.Catalog, params *CatalogParams, sources [2]catalogv1alpha1.CatalogImageSource) {
	selection := func(value *string) string {
		if value == nil {
			return ""
		}
		return *value
	}
	requested := catalogv1alpha1.CatalogImageSelections{
		Catalog: selection(catalog.Spec.CatalogDataImageStream), Benchmark: selection(catalog.Spec.BenchmarkDataImageStream),
	}
	conditions := [2]*metav1.Condition{
		apimeta.FindStatusCondition(catalog.Status.Conditions, "CatalogDataImageResolved"),
		apimeta.FindStatusCondition(catalog.Status.Conditions, "BenchmarkDataImageResolved"),
	}
	outcome := catalogv1alpha1.CatalogImageOutcome{CatalogUID: catalog.UID, State: "Succeeded", Stage: "Selection", Reason: "ImagesResolved", Message: "Both requested data images are resolved"}
	for _, condition := range conditions {
		if condition.Status == metav1.ConditionFalse && condition.Reason != "NoSuccessfulImport" {
			outcome.State, outcome.Reason, outcome.Message = "Failed", condition.Reason, condition.Message
			break
		}
		if condition.Status != metav1.ConditionTrue {
			outcome.State, outcome.Reason, outcome.Message = "Pending", condition.Reason, condition.Message
		}
	}
	if outcome.Reason == "ImportFailed" || outcome.Reason == "InvalidImportedImage" || outcome.Reason == "NoSuccessfulImport" || outcome.Reason == "AwaitingSuccessfulImport" || outcome.Reason == "ImageStreamUnavailable" {
		outcome.Stage = "Import"
	}
	var images *catalogv1alpha1.CatalogResolvedImages
	if outcome.State == "Succeeded" {
		resolved := [2]catalogv1alpha1.CatalogResolvedImage{}
		for index, item := range []struct{ selection, image string }{
			{requested.Catalog, params.CatalogDataImage}, {requested.Benchmark, params.BenchmarkDataImage},
		} {
			repository, digest, found := strings.Cut(item.image, "@")
			if !found || repository == "" || !catalogDataDigestPattern.MatchString(digest) {
				outcome.State, outcome.Reason, outcome.Message = "Pending", "ImmutableImagesRequired", "Activation requires digest-pinned catalog and benchmark references; configure immutable release images"
				break
			}
			mode := "ReleaseDefault"
			if item.selection == "stable" {
				mode = "Stable"
			} else if item.selection != "" {
				mode = "Pinned"
			}
			resolved[index] = catalogv1alpha1.CatalogResolvedImage{Mode: mode, ImageReference: item.image, Digest: digest, Source: sources[index]}
		}
		if outcome.State == "Succeeded" {
			images = &catalogv1alpha1.CatalogResolvedImages{Catalog: resolved[0], Benchmark: resolved[1]}
			refs := images.References()
			outcome.Images = &refs
		}
	}
	// Fingerprint only meaningful observations, never condition transition times.
	observations := [2]string{}
	for index, condition := range conditions {
		observations[index] = string(condition.Status) + "/" + condition.Reason
		if condition.Status == metav1.ConditionTrue {
			// One healthy image may advance while the other fails. That import
			// still changes current intent, even though no complete pair exists.
			observations[index] += "/" + [2]string{params.CatalogDataImage, params.BenchmarkDataImage}[index]
		}
	}
	evidence, _ := json.Marshal(struct {
		UID          string
		Requested    catalogv1alpha1.CatalogImageSelections
		Images       *catalogv1alpha1.CatalogResolvedImages
		Sources      [2]catalogv1alpha1.CatalogImageSource
		Observations [2]string
		State        string
	}{string(catalog.UID), requested, images, sources, observations, outcome.State})
	hash := sha256.Sum256(evidence)
	observationID := hex.EncodeToString(hash[:])
	if catalog.Status.ImageUpdate == nil {
		catalog.Status.ImageUpdate = &catalogv1alpha1.CatalogImageUpdateStatus{}
	}
	update := catalog.Status.ImageUpdate
	if update.CurrentAttempt.ObservationID != observationID || update.CurrentAttempt.AttemptID == "" {
		update.CurrentAttempt.AttemptID = string(uuid.NewUUID())
	}
	outcome.AttemptID = update.CurrentAttempt.AttemptID
	update.CurrentAttempt = catalogv1alpha1.CatalogImageAttempt{
		CatalogUID: catalog.UID, CatalogGeneration: catalog.Generation,
		AttemptID: outcome.AttemptID, ObservationID: observationID,
		RequestedSelection: requested, ResolvedImages: images, SelectionOutcome: outcome,
	}
	if images != nil {
		params.DataImageAttemptID = outcome.AttemptID
	}
	reduceCatalogImageReadiness(catalog)
}

// One reducer owns aggregate readiness. A resolved image or a healthy Deployment
// cannot stand in for genuine activation of the current attempt and image pair.
// Ready is status evidence for the future runtime gate, not an implemented gate.
func reduceCatalogImageReadiness(catalog *catalogv1alpha1.Catalog) {
	catalog.Status.ObservedGeneration = catalog.Generation
	update := catalog.Status.ImageUpdate
	selectionStatus, selectionReason, selectionMessage := metav1.ConditionUnknown, "SelectionPending", "Waiting for image resolution"
	activationStatus, activationReason, activationMessage := metav1.ConditionUnknown, "ActivationPending", "Waiting for validated activation of both current images"
	var failure *catalogv1alpha1.CatalogImageOutcome
	activationSucceeded := false
	if update != nil {
		if outcome := update.CurrentSelection(catalog); outcome != nil {
			selectionReason, selectionMessage = outcome.Reason, outcome.Message
			switch outcome.State {
			case "Succeeded":
				selectionStatus = metav1.ConditionTrue
			case "Failed":
				selectionStatus, failure = metav1.ConditionFalse, outcome
			}
		}
		if activation := update.CurrentActivation(catalog); activation != nil {
			activationReason, activationMessage = activation.Reason, activation.Message
			switch activation.State {
			case "Failed":
				activationStatus, failure = metav1.ConditionFalse, activation
			case "Succeeded":
				activationStatus, activationSucceeded = metav1.ConditionTrue, true
				if last := update.LastSuccessfulActivation; last == nil || last.AttemptID != activation.AttemptID || last.CatalogUID != catalog.UID || last.Images != *activation.Images {
					update.LastSuccessfulActivation = &catalogv1alpha1.CatalogSuccessfulActivation{
						CatalogUID: catalog.UID, AttemptID: activation.AttemptID, Images: *activation.Images, ActivatedAt: metav1.Now(),
					}
				}
			}
		}
		if failure != nil {
			update.LastFailure = failure.DeepCopy()
		} else if activationSucceeded {
			update.LastFailure = nil
		}
	}
	setCatalogDataImageCondition(catalog, conditionImageSelectionReady, selectionStatus, selectionReason, selectionMessage)
	setCatalogDataImageCondition(catalog, conditionDataActivationReady, activationStatus, activationReason, activationMessage)
	degraded, reason, message := metav1.ConditionFalse, "DataImagesHealthy", "No confirmed data image failure"
	if update != nil && update.LastFailure != nil {
		degraded, reason, message = metav1.ConditionTrue, update.LastFailure.Reason, update.LastFailure.Message
	}
	setCatalogDataImageCondition(catalog, ConditionTypeDegraded, degraded, reason, message)
	ready, readyReason, readyMessage := metav1.ConditionFalse, activationReason, activationMessage
	blocked, blockedReason, blockedMessage := metav1.ConditionTrue, activationReason, activationMessage
	if failure != nil {
		readyReason, readyMessage = "DataImagesUnavailable", fmt.Sprintf("%s: %s; activation of a valid current image pair is required for recovery", failure.Reason, failure.Message)
		blockedReason, blockedMessage = failure.Reason, readyMessage
	} else if selectionStatus != metav1.ConditionTrue {
		readyReason, readyMessage = selectionReason, selectionMessage
		blockedReason, blockedMessage = selectionReason, selectionMessage
	} else if activationSucceeded {
		blocked, blockedReason, blockedMessage = metav1.ConditionFalse, "UpdatesAllowed", "The current image pair has successfully activated"
		workload := apimeta.FindStatusCondition(catalog.Status.Conditions, conditionWorkloadAvailable)
		readyReason, readyMessage = "WorkloadPending", "Waiting for healthy Catalog resources"
		if workload != nil && workload.ObservedGeneration == catalog.Generation {
			readyReason, readyMessage = workload.Reason, workload.Message
			if workload.Status == metav1.ConditionTrue {
				ready = metav1.ConditionTrue
			}
		}
	}
	// Resource failures must also remain visible while activation is pending.
	if workload := apimeta.FindStatusCondition(catalog.Status.Conditions, conditionWorkloadAvailable); failure == nil && workload != nil && workload.ObservedGeneration == catalog.Generation && workload.Reason == ReasonResourcesUnavailable {
		ready, readyReason, readyMessage = metav1.ConditionFalse, workload.Reason, workload.Message
	}
	setCatalogDataImageCondition(catalog, conditionDataImageUpdateBlocked, blocked, blockedReason, blockedMessage)
	setCatalogDataImageCondition(catalog, conditionCatalogReady, ready, readyReason, readyMessage)
	setCatalogDataImageCondition(catalog, ConditionTypeAvailable, ready, readyReason, readyMessage)
}
