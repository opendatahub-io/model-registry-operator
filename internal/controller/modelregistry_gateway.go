/*
Copyright 2025.

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

package controller

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayapiv1beta1 "sigs.k8s.io/gateway-api/apis/v1beta1"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

func (r *ModelRegistryReconciler) createOrUpdateGatewayResources(ctx context.Context, params *ModelRegistryParams) (OperationResult, error) {
	result := ResourceUnchanged

	// ReferenceGrant is namespace-scoped (one per registries namespace), not per-registry.
	// Use createIfNotExists so subsequent registry reconciles don't trigger unnecessary updates.
	result2, err := r.ensureReferenceGrantExists(ctx, params)
	if err != nil {
		return result2, err
	}
	if result2 != ResourceUnchanged {
		result = result2
	}

	result2, err = r.createOrUpdateHTTPRoute(ctx, params)
	if err != nil {
		return result2, err
	}
	if result2 != ResourceUnchanged {
		result = result2
	}

	return result, nil
}

func (r *ModelRegistryReconciler) createOrUpdateHTTPRoute(ctx context.Context, params *ModelRegistryParams) (OperationResult, error) {
	var httpRoute gatewayapiv1.HTTPRoute
	if err := r.Apply(params, "gateway-httproute.yaml.tmpl", &httpRoute); err != nil {
		return ResourceUnchanged, err
	}
	return r.createOrUpdate(ctx, &gatewayapiv1.HTTPRoute{}, &httpRoute)
}

// ensureReferenceGrantExists creates the namespace-scoped ReferenceGrant if it does not already
// exist. It is intentionally not updated on subsequent reconciles — the grant covers all
// registries in the namespace and must not be removed when a single registry is deleted.
func (r *ModelRegistryReconciler) ensureReferenceGrantExists(ctx context.Context, params *ModelRegistryParams) (OperationResult, error) {
	var refGrant gatewayapiv1beta1.ReferenceGrant
	if err := r.Apply(params, "gateway-reference-grant.yaml.tmpl", &refGrant); err != nil {
		return ResourceUnchanged, err
	}
	return r.createIfNotExists(ctx, &gatewayapiv1beta1.ReferenceGrant{}, &refGrant)
}

func (r *ModelRegistryReconciler) deleteGatewayResources(ctx context.Context, params *ModelRegistryParams) error {
	if params.HTTPRouteNamespace == "" {
		return nil
	}
	// Only delete the per-registry HTTPRoute. The ReferenceGrant is shared across all
	// registries in the namespace and must not be removed when a single registry is deleted.
	httpRoute := gatewayapiv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "model-registry-" + params.Name,
			Namespace: params.HTTPRouteNamespace,
		},
	}
	return client.IgnoreNotFound(r.Delete(ctx, &httpRoute))
}
