/*
Copyright 2024.

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

	"github.com/opendatahub-io/model-registry-operator/api/v1beta1"
	"github.com/opendatahub-io/model-registry-operator/internal/controller/config"
	routev1 "github.com/openshift/api/route/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbac "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func (r *ModelRegistryReconciler) createOrUpdateOAuthConfig(ctx context.Context, params *ModelRegistryParams,
	registry *v1beta1.ModelRegistry) (result OperationResult, err error) {

	result = ResourceUnchanged

	// create oauth proxy resources
	if registry.Spec.OAuthProxy != nil {

		// create oauth proxy rolebinding
		result, err = r.createOrUpdateClusterRoleBinding(ctx, params, registry, "proxy-role-binding.yaml.tmpl")
		if err != nil {
			return result, err
		}

		result2, err := r.ensureSecretExists(ctx, params, registry, "proxy-cookie-secret.yaml.tmpl")
		if err != nil {
			return result2, err
		}
		if result2 != ResourceUnchanged {
			result = result2
		}

		// check if cluster is OpenShift for Route support
		if r.IsOpenShift {
			// create oauth proxy service route if enabled, delete if disabled
			result2, err := r.createOrUpdateRoute(ctx, params, registry,
				"https-route.yaml.tmpl", registry.Spec.OAuthProxy.ServiceRoute)
			if err != nil {
				return result2, err
			}
			if result2 != ResourceUnchanged {
				result = result2
			}

			if registry.Spec.OAuthProxy.ServiceRoute == config.RouteEnabled {
				// create oauth proxy networkpolicy to ensure route is exposed
				result2, err = r.createOrUpdateNetworkPolicy(ctx, params, registry, "proxy-network-policy.yaml.tmpl")
				if err != nil {
					return result2, err
				}
				if result2 != ResourceUnchanged {
					result = result2
				}
			} else {
				// remove oauth proxy networkpolicy if it exists
				if err = r.deleteOAuthNetworkPolicy(ctx, params); err != nil {
					return result, err
				}
			}
		}

	} else {
		// remove oauth proxy rolebinding if it exists
		if err = r.deleteOAuthClusterRoleBinding(ctx, params); err != nil {
			return result, err
		}
		if r.IsOpenShift {
			// remove oauth proxy route if it exists
			if err = r.deleteOAuthRoute(ctx, params); err != nil {
				return result, err
			}
			// remove oauth proxy networkpolicy if it exists
			if err = r.deleteOAuthNetworkPolicy(ctx, params); err != nil {
				return result, err
			}
		}
	}

	return result, nil
}

func (r *ModelRegistryReconciler) deleteOAuthClusterRoleBinding(ctx context.Context, params *ModelRegistryParams) error {
	roleBinding := rbac.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: params.Name + "-auth-delegator"}}
	return client.IgnoreNotFound(r.Client.Delete(ctx, &roleBinding))
}

func (r *ModelRegistryReconciler) deleteOAuthRoute(ctx context.Context, params *ModelRegistryParams) error {
	route := routev1.Route{ObjectMeta: metav1.ObjectMeta{Name: params.Name + "-https", Namespace: params.Namespace}}
	return client.IgnoreNotFound(r.Client.Delete(ctx, &route))
}

func (r *ModelRegistryReconciler) deleteOAuthNetworkPolicy(ctx context.Context, params *ModelRegistryParams) error {
	networkPolicy := networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: params.Name + "-https-route", Namespace: params.Namespace}}
	return client.IgnoreNotFound(r.Client.Delete(ctx, &networkPolicy))
}
