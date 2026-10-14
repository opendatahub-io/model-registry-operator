package controller

import (
	catalogv1alpha1 "github.com/opendatahub-io/model-registry-operator/api/catalog/v1alpha1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	conditionCatalogReady        = "Ready"
	conditionImageSelectionReady = "ImageSelectionReady"
	conditionWorkloadAvailable   = "WorkloadAvailable"
)

// One reducer owns Catalog readiness for selection/import and workload health.
// Readiness also requires a validated current-attempt workload.
func reduceCatalogImageReadiness(catalog *catalogv1alpha1.Catalog) {
	catalog.Status.ObservedGeneration = catalog.Generation
	selectionStatus, reason, message := metav1.ConditionUnknown, "SelectionPending", "Waiting for current image resolution"
	resolved := apimeta.FindStatusCondition(catalog.Status.Conditions, conditionDataImageResolved)
	if resolved != nil && resolved.ObservedGeneration == catalog.Generation {
		selectionStatus, reason, message = resolved.Status, resolved.Reason, resolved.Message
		if reason == "NoSuccessfulImport" {
			selectionStatus = metav1.ConditionUnknown
		}
	}
	setCatalogDataImageCondition(catalog, conditionImageSelectionReady, selectionStatus, reason, message)

	degradedStatus, degradedReason, degradedMessage := metav1.ConditionFalse, "DataImagesHealthy", "Data image selection and imports have no reported failures"
	if selectionStatus == metav1.ConditionFalse {
		degradedStatus, degradedReason, degradedMessage = metav1.ConditionTrue, reason, message
	} else if blocked := apimeta.FindStatusCondition(catalog.Status.Conditions, conditionDataImageUpdateBlocked); blocked != nil && blocked.ObservedGeneration == catalog.Generation && blocked.Status == metav1.ConditionTrue {
		// Pending retries retain the confirmed failure without inventing a new
		// failed import. Clearing or pinning does not depend on this stream.
		if previous := apimeta.FindStatusCondition(catalog.Status.Conditions, ConditionTypeDegraded); previous != nil && previous.Status == metav1.ConditionTrue {
			degradedStatus, degradedReason, degradedMessage = previous.Status, previous.Reason, previous.Message
		}
	}
	// A persisted activation failure outlives its pod and configuration
	// generation. Workload readiness or a spec edit cannot prove recovery.
	activationFailure := apimeta.FindStatusCondition(catalog.Status.Conditions, conditionDataImageActivationFailed)
	activationFailed := activationFailure != nil && activationFailure.Status == metav1.ConditionTrue
	if activationFailed && degradedStatus == metav1.ConditionFalse {
		degradedStatus, degradedReason, degradedMessage = metav1.ConditionTrue, activationFailure.Reason, activationFailure.Message
	}
	setCatalogDataImageCondition(catalog, ConditionTypeDegraded, degradedStatus, degradedReason, degradedMessage)

	ready := metav1.ConditionFalse
	switch selectionStatus {
	case metav1.ConditionFalse:
		reason = "DataImagesUnavailable"
	case metav1.ConditionTrue:
		reason, message = "WorkloadPending", "Waiting for healthy Catalog resources"
		if workload := apimeta.FindStatusCondition(catalog.Status.Conditions, conditionWorkloadAvailable); workload != nil && workload.ObservedGeneration == catalog.Generation {
			reason, message = workload.Reason, workload.Message
			if workload.Status == metav1.ConditionTrue {
				ready = metav1.ConditionTrue
			}
		}
	}
	activationReady := apimeta.FindStatusCondition(catalog.Status.Conditions, conditionDataActivationReady)
	if catalog.Status.Activation == nil || catalog.Status.Activation.State != "Succeeded" || activationReady == nil || activationReady.Status != metav1.ConditionTrue || activationReady.ObservedGeneration != catalog.Generation {
		if selectionStatus == metav1.ConditionTrue && reason != ReasonResourcesUnavailable {
			ready, reason, message = metav1.ConditionFalse, "ActivationPending", "Waiting for validation and current-attempt workload readiness"
		}
	}
	if activationFailed && selectionStatus != metav1.ConditionFalse {
		ready, reason, message = metav1.ConditionFalse, activationFailure.Reason, activationFailure.Message
	}
	setCatalogDataImageCondition(catalog, conditionCatalogReady, ready, reason, message)
	setCatalogDataImageCondition(catalog, ConditionTypeAvailable, ready, reason, message)
}
