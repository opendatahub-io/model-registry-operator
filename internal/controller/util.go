// Package controller provides Kubernetes controller implementations for
// ModelRegistry and ModelCatalog resources.
//
// The package includes cluster capability detection to support multiple
// Kubernetes platforms:
//   - Traditional OpenShift (all APIs available)
//   - BYOIDC-enabled OpenShift 4.20+ (route API present, user API absent)
//   - Plain Kubernetes (no OpenShift APIs)
//
// Controllers use runtime capability detection to conditionally create
// platform-specific resources, ensuring graceful operation across all
// supported environments.
package controller

import (
	"context"
	"fmt"
	"strings"
	"text/template"

	"github.com/banzaicloud/k8s-objectmatcher/patch"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/discovery"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	klog "sigs.k8s.io/controller-runtime/pkg/log"
)

// ClusterCapabilities tracks which platform APIs are available in the cluster.
//
// Note: IsOpenShift is determined by the presence of route.openshift.io API, as this
// is the most stable indicator across OpenShift versions (including BYOIDC mode).
// The user.openshift.io API may be absent in BYOIDC clusters while routes remain.
type ClusterCapabilities struct {
	IsOpenShift  bool // true if route.openshift.io API is present
	HasUserAPI   bool // true if user.openshift.io API is present
	HasConfigAPI bool // true if config.openshift.io API is present
}

// IsBYOIDC returns true if the cluster is OpenShift with BYOIDC enabled.
// BYOIDC (Bring Your Own Identity Provider) mode is characterized by:
// - route.openshift.io API is present (IsOpenShift = true)
// - user.openshift.io API is absent (HasUserAPI = false)
//
// In BYOIDC mode, user and group management is delegated to an external
// identity provider (e.g., Keycloak), and the operator must skip creating
// user.openshift.io/v1/Group resources.
func (c *ClusterCapabilities) IsBYOIDC() bool {
	return c.IsOpenShift && !c.HasUserAPI
}

// DetectClusterCapabilities determines which platform-specific APIs are available
// by querying the Kubernetes discovery API. It gracefully handles partial discovery
// failures which occur in BYOIDC-enabled OpenShift clusters where user.openshift.io
// APIs are not available.
func DetectClusterCapabilities(discoveryClient discovery.DiscoveryInterface) (ClusterCapabilities, error) {
	setupLog := ctrl.Log.WithName("setup")
	caps := ClusterCapabilities{}

	groups, err := discoveryClient.ServerGroups()
	if err != nil {
		// Check if this is a partial discovery failure (e.g., BYOIDC mode)
		if discovery.IsGroupDiscoveryFailedError(err) {
			// In partial failure, groups contains the successfully discovered groups
			// The error contains information about which groups failed
			if groups == nil {
				return ClusterCapabilities{}, fmt.Errorf("failed to get API groups with no partial results: %w", err)
			}

			// Examine which specific groups failed and why
			groupErr, ok := err.(*discovery.ErrGroupDiscoveryFailed)
			if !ok {
				return ClusterCapabilities{}, fmt.Errorf("unexpected discovery error type: %w", err)
			}

			// Validate that only user.openshift.io failed (expected in BYOIDC)
			// Any other OpenShift API failures indicate a real problem
			for gv, apiErr := range groupErr.Groups {
				if gv.Group == "user.openshift.io" {
					// Check if this is a "not found" error (expected in BYOIDC)
					// Use apierrors.IsNotFound() as primary check (works with StatusError)
					// Fall back to string matching for generic errors in test scenarios
					if !apierrors.IsNotFound(apiErr) {
						// Also check error message as fallback for non-StatusError types
						errMsg := strings.ToLower(apiErr.Error())
						if !strings.Contains(errMsg, "not found") &&
							!strings.Contains(errMsg, "could not find") &&
							!strings.Contains(errMsg, "does not exist") {
							// user.openshift.io failed for a reason OTHER than missing API
							// This indicates a transient issue (network, RBAC, timeout)
							return ClusterCapabilities{}, fmt.Errorf("user.openshift.io discovery failed (not BYOIDC): %w", apiErr)
						}
					}
					// Valid BYOIDC scenario - user API is intentionally disabled
					setupLog.Info("BYOIDC cluster detected: user.openshift.io API not available",
						"reason", apiErr.Error())
				} else if gv.Group == "route.openshift.io" || gv.Group == "config.openshift.io" {
					// route.openshift.io and config.openshift.io should ALWAYS be present on OpenShift
					// If they failed, this is a real problem, not BYOIDC
					return ClusterCapabilities{}, fmt.Errorf("critical OpenShift API %s failed discovery: %w", gv.Group, apiErr)
				}
				// Other non-OpenShift API failures are logged but don't block
			}
		} else {
			// Complete failure - cannot proceed
			return ClusterCapabilities{}, fmt.Errorf("failed to discover API groups: %w", err)
		}
	}

	// Check for each capability by examining available API groups
	if groups != nil {
		for _, g := range groups.Groups {
			switch g.Name {
			case "route.openshift.io":
				caps.IsOpenShift = true
			case "user.openshift.io":
				caps.HasUserAPI = true
			case "config.openshift.io":
				caps.HasConfigAPI = true
			}
		}
	}

	return caps, nil
}

type TemplateApplier struct {
	Template    *template.Template
	IsOpenShift bool
}

type BaseParams struct {
	Name      string
	Namespace string
}

func (t *TemplateApplier) Apply(params any, templateName string, object any) error {
	builder := strings.Builder{}
	err := t.Template.ExecuteTemplate(&builder, templateName, params)
	if err != nil {
		return fmt.Errorf("error executing template %s: %w", templateName, err)
	}
	err = yaml.UnmarshalStrict([]byte(builder.String()), object)
	if err != nil {
		return fmt.Errorf("error creating %T from template %s: %w", object, templateName, err)
	}
	return nil
}

type ResourceManager struct {
	Client client.Client
}

func (r *ResourceManager) CreateOrUpdate(ctx context.Context, currObj client.Object, newObj client.Object) (OperationResult, error) {
	log := klog.FromContext(ctx)
	result := ResourceUnchanged

	key := client.ObjectKeyFromObject(newObj)
	gvk := newObj.GetObjectKind().GroupVersionKind()
	name := newObj.GetName()

	if err := r.Client.Get(ctx, key, currObj); err != nil {
		if client.IgnoreNotFound(err) == nil {
			result = ResourceCreated
			log.Info("creating", "kind", gvk, "name", name)
			if err := patch.DefaultAnnotator.SetLastAppliedAnnotation(newObj); err != nil {
				return result, err
			}
			return result, r.Client.Create(ctx, newObj)
		}
		return result, err
	}

	// Avoid diff on metadata.resourceVersion alone
	if rv := currObj.GetResourceVersion(); rv != "" {
		newObj.SetResourceVersion(rv)
	}

	patchResult, err := patch.DefaultPatchMaker.Calculate(currObj, newObj, patch.IgnoreStatusFields(),
		patch.IgnoreField("apiVersion"), patch.IgnoreField("kind"))
	if err != nil {
		return result, err
	}
	if !patchResult.IsEmpty() {
		result = ResourceUpdated
		log.Info("updating", "kind", gvk, "name", name)
		if err := patch.DefaultAnnotator.SetLastAppliedAnnotation(newObj); err != nil {
			return result, err
		}
		return result, r.Client.Update(ctx, newObj)
	}

	return result, nil
}

func (r *ResourceManager) CreateIfNotExists(ctx context.Context, currObj client.Object, newObj client.Object) (OperationResult, error) {
	log := klog.FromContext(ctx)
	result := ResourceUnchanged

	key := client.ObjectKeyFromObject(newObj)
	gvk := newObj.GetObjectKind().GroupVersionKind()
	name := newObj.GetName()

	if err := r.Client.Get(ctx, key, currObj); err != nil {
		if client.IgnoreNotFound(err) == nil {
			result = ResourceCreated
			log.Info("creating", "kind", gvk, "name", name)
			if err := patch.DefaultAnnotator.SetLastAppliedAnnotation(newObj); err != nil {
				return result, err
			}
			return result, r.Client.Create(ctx, newObj)
		}
		return result, err
	}
	return result, nil
}
