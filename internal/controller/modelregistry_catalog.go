package controller

import (
	"context"

	"github.com/opendatahub-io/model-registry-operator/api/v1beta1"
	"github.com/opendatahub-io/model-registry-operator/internal/controller/config"
)

func (r *ModelRegistryReconciler) createOrUpdateCatalogConfig(ctx context.Context, params *ModelRegistryParams, registry *v1beta1.ModelRegistry) (OperationResult, error) {
	if !r.EnableModelCatalog {
		// FIXME: Remove resources in this case.
		return ResourceUnchanged, nil
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
	}

	return result, nil
}
