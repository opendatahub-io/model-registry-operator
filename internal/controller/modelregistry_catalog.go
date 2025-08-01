package controller

import (
	"context"

	"github.com/opendatahub-io/model-registry-operator/api/v1beta1"
	"github.com/opendatahub-io/model-registry-operator/internal/controller/config"
	routev1 "github.com/openshift/api/route/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
)

func (r *ModelRegistryReconciler) createOrUpdateCatalogConfig(ctx context.Context, params *ModelRegistryParams, registry *v1beta1.ModelRegistry) (OperationResult, error) {
	if !r.EnableModelCatalog {
		// Remove catalog resources when disabled
		return r.deleteCatalogResources(ctx, params)
	}

	result, err := r.ensureConfigMapExists(ctx, params, registry, "catalog-configmap.yaml.tmpl")
	if err != nil {
		return result, err
	}

	result2, err := r.createOrUpdateDeployment(ctx, params, registry, "catalog-deployment.yaml.tmpl")
	if err != nil {
		return result2, err
	}
	if result2 != ResourceUnchanged {
		result = result2
	}

	result2, err = r.createOrUpdateService(ctx, params, registry, "catalog-service.yaml.tmpl")
	if err != nil {
		return result2, err
	}
	if result2 != ResourceUnchanged {
		result = result2
	}

	if r.IsOpenShift {
		result2, err = r.createOrUpdateRoute(ctx, params, registry, "catalog-route.yaml.tmpl", config.RouteEnabled)
		if err != nil {
			return result2, err
		}
		if result2 != ResourceUnchanged {
			result = result2
		}

		result2, err = r.createOrUpdateNetworkPolicy(ctx, params, registry, "catalog-network-policy.yaml.tmpl")
		if err != nil {
			return result2, err
		}
		if result2 != ResourceUnchanged {
			result = result2
		}
	}

	return result, nil
}

// deleteCatalogResources removes all catalog-related resources
func (r *ModelRegistryReconciler) deleteCatalogResources(ctx context.Context, params *ModelRegistryParams) (OperationResult, error) {
	result, err := r.deleteFromTemplate(ctx, params, "catalog-deployment.yaml.tmpl", &appsv1.Deployment{})
	if err != nil {
		return result, err
	}

	result2, err := r.deleteFromTemplate(ctx, params, "catalog-service.yaml.tmpl", &corev1.Service{})
	if err != nil {
		return result2, err
	}
	if result2 != ResourceUnchanged {
		result = result2
	}

	if r.IsOpenShift {
		result2, err = r.deleteFromTemplate(ctx, params, "catalog-route.yaml.tmpl", &routev1.Route{})
		if err != nil {
			return result2, err
		}
		if result2 != ResourceUnchanged {
			result = result2
		}

		result2, err = r.deleteFromTemplate(ctx, params, "catalog-network-policy.yaml.tmpl", &networkingv1.NetworkPolicy{})
		if err != nil {
			return result2, err
		}
		if result2 != ResourceUnchanged {
			result = result2
		}
	}

	return result, nil
}
