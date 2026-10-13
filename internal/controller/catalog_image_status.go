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
// These observations do not prove validated data activation. Activation-dependent
// readiness must land together with the runtime producer and gate in 97414.
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
	setCatalogDataImageCondition(catalog, conditionCatalogReady, ready, reason, message)
	setCatalogDataImageCondition(catalog, ConditionTypeAvailable, ready, reason, message)
}
