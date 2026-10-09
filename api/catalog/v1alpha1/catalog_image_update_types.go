package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// CatalogImageSelections records the independent administrator selections.
type CatalogImageSelections struct {
	Catalog   string `json:"catalog"`
	Benchmark string `json:"benchmark"`
}

// CatalogImageReferences binds an activation result to both immutable images.
type CatalogImageReferences struct {
	// +kubebuilder:validation:Pattern=`^.+@sha256:[a-f0-9]{64}$`
	Catalog string `json:"catalog"`
	// +kubebuilder:validation:Pattern=`^.+@sha256:[a-f0-9]{64}$`
	Benchmark string `json:"benchmark"`
}

// CatalogImageSource identifies release metadata or relevant ImageStream evidence.
// Import timestamps and resource versions are deliberately not attempt identities.
type CatalogImageSource struct {
	Reference string `json:"reference"`
	// +optional
	ImageStreamName string `json:"imageStreamName,omitempty"`
	// +optional
	ImageStreamUID types.UID `json:"imageStreamUID,omitempty"`
	// +optional
	Tag string `json:"tag,omitempty"`
	// +optional
	TagGeneration int64 `json:"tagGeneration,omitempty"`
}

// CatalogResolvedImage is a candidate for one of the two required datasets.
type CatalogResolvedImage struct {
	// +kubebuilder:validation:Enum=ReleaseDefault;Stable;Pinned
	Mode string `json:"mode"`
	// +kubebuilder:validation:Pattern=`^.+@sha256:[a-f0-9]{64}$`
	ImageReference string `json:"imageReference"`
	// +kubebuilder:validation:Pattern=`^sha256:[a-f0-9]{64}$`
	Digest string             `json:"digest"`
	Source CatalogImageSource `json:"source"`
}

// CatalogResolvedImages must be activated together, even when the images differ.
type CatalogResolvedImages struct {
	Catalog   CatalogResolvedImage `json:"catalog"`
	Benchmark CatalogResolvedImage `json:"benchmark"`
}

// References returns the pair an activation producer must report verbatim.
func (images *CatalogResolvedImages) References() CatalogImageReferences {
	return CatalogImageReferences{Catalog: images.Catalog.ImageReference, Benchmark: images.Benchmark.ImageReference}
}

// CatalogImageOutcome is shared by selection/import and runtime activation.
// The operator owns Selection/Import; the runtime integration owns the other stages.
type CatalogImageOutcome struct {
	CatalogUID types.UID `json:"catalogUID"`
	AttemptID  string    `json:"attemptID"`
	// +kubebuilder:validation:Enum=Pending;Succeeded;Failed
	State string `json:"state"`
	// +kubebuilder:validation:Enum=Selection;Import;Pull;Initialization;Loading;Activation
	Stage   string `json:"stage"`
	Reason  string `json:"reason"`
	Message string `json:"message"`
	// Images is required by the controller for every accepted activation outcome.
	// +optional
	Images *CatalogImageReferences `json:"images,omitempty"`
}

// CatalogImageAttempt persists the identity of current image intent and resolution.
type CatalogImageAttempt struct {
	CatalogUID        types.UID `json:"catalogUID"`
	CatalogGeneration int64     `json:"catalogGeneration"`
	AttemptID         string    `json:"attemptID"`
	// ObservationID fingerprints relevant intent/import evidence, not resourceVersion.
	ObservationID      string                 `json:"observationID"`
	RequestedSelection CatalogImageSelections `json:"requestedSelection"`
	// ResolvedImages exists only when both references are immutable and resolved.
	// +optional
	ResolvedImages   *CatalogResolvedImages `json:"resolvedImages,omitempty"`
	SelectionOutcome CatalogImageOutcome    `json:"selectionOutcome"`
}

// CatalogSuccessfulActivation records recovery material, never serving permission.
type CatalogSuccessfulActivation struct {
	CatalogUID  types.UID              `json:"catalogUID"`
	AttemptID   string                 `json:"attemptID"`
	Images      CatalogImageReferences `json:"images"`
	ActivatedAt metav1.Time            `json:"activatedAt"`
}

// CatalogImageUpdateStatus separates controller and activation-producer ownership.
// ActivationOutcome is written by the future runtime adapter, not inferred from pods.
type CatalogImageUpdateStatus struct {
	CurrentAttempt CatalogImageAttempt `json:"currentAttempt"`
	// +optional
	ActivationOutcome *CatalogImageOutcome `json:"activationOutcome,omitempty"`
	// LastFailure stays visible through pending recovery until validated activation.
	// +optional
	LastFailure *CatalogImageOutcome `json:"lastFailure,omitempty"`
	// +optional
	LastSuccessfulActivation *CatalogSuccessfulActivation `json:"lastSuccessfulActivation,omitempty"`
}

// CurrentSelection rejects observations for a superseded Catalog intent.
func (status *CatalogImageUpdateStatus) CurrentSelection(catalog *Catalog) *CatalogImageOutcome {
	if status == nil || catalog == nil {
		return nil
	}
	attempt := &status.CurrentAttempt
	selection := func(value *string) string {
		if value == nil {
			return ""
		}
		return *value
	}
	if attempt.CatalogUID != catalog.UID || attempt.CatalogGeneration != catalog.Generation || attempt.AttemptID == "" ||
		attempt.RequestedSelection.Catalog != selection(catalog.Spec.CatalogDataImageStream) || attempt.RequestedSelection.Benchmark != selection(catalog.Spec.BenchmarkDataImageStream) ||
		attempt.SelectionOutcome.CatalogUID != catalog.UID || attempt.SelectionOutcome.AttemptID != attempt.AttemptID {
		return nil
	}
	return &attempt.SelectionOutcome
}

// CurrentActivation validates the outcome before it can affect aggregate readiness.
// Success is accepted only at Activation, after both datasets are atomically active.
func (status *CatalogImageUpdateStatus) CurrentActivation(catalog *Catalog) *CatalogImageOutcome {
	selection := status.CurrentSelection(catalog)
	if selection == nil || selection.State != "Succeeded" || status.CurrentAttempt.ResolvedImages == nil {
		return nil
	}
	attempt, outcome := &status.CurrentAttempt, status.ActivationOutcome
	if outcome == nil || outcome.CatalogUID != catalog.UID || outcome.AttemptID != attempt.AttemptID ||
		outcome.Images == nil || *outcome.Images != attempt.ResolvedImages.References() || outcome.Reason == "" || outcome.Message == "" {
		return nil
	}
	switch outcome.Stage {
	case "Pull", "Initialization", "Loading", "Activation":
	default:
		return nil
	}
	switch outcome.State {
	case "Pending", "Failed":
		return outcome
	case "Succeeded":
		if outcome.Stage == "Activation" {
			return outcome
		}
	}
	return nil
}
