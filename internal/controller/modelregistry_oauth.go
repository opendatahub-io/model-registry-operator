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
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbac "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func (r *ModelRegistryReconciler) deleteOAuthConfig(ctx context.Context, params *ModelRegistryParams,
	registry *v1beta1.ModelRegistry) (result OperationResult, err error) {

	result = ResourceUnchanged

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

func (r *ModelRegistryReconciler) createOrUpdateKubeRBACProxyConfig(ctx context.Context, params *ModelRegistryParams,
	registry *v1beta1.ModelRegistry) (result OperationResult, err error) {

	result = ResourceUnchanged

	// create kube-rbac-proxy resources
	if registry.Spec.KubeRBACProxy != nil {

		// create kube-rbac-proxy config
		result, err = r.ensureConfigMapExists(ctx, params, registry, "kube-rbac-proxy-config.yaml.tmpl")
		if err != nil {
			return result, err
		}

		// create kube-rbac-proxy rolebinding
		result2, err := r.createOrUpdateClusterRoleBinding(ctx, params, registry, "kube-rbac-proxy-role-binding.yaml.tmpl")
		if err != nil {
			return result2, err
		}
		if result2 != ResourceUnchanged {
			result = result2
		}

		// check if cluster is OpenShift for Route support
		if r.IsOpenShift {
			// create kube-rbac-proxy service route if enabled, delete if disabled
			result2, err := r.createOrUpdateRoute(ctx, params, registry,
				"https-route.yaml.tmpl", registry.Spec.KubeRBACProxy.ServiceRoute)
			if err != nil {
				return result2, err
			}
			if result2 != ResourceUnchanged {
				result = result2
			}

			if registry.Spec.KubeRBACProxy.ServiceRoute == config.RouteEnabled {
				// create kube-rbac-proxy networkpolicy to ensure route is exposed
				result2, err = r.createOrUpdateNetworkPolicy(ctx, params, registry, "proxy-network-policy.yaml.tmpl")
				if err != nil {
					return result2, err
				}
				if result2 != ResourceUnchanged {
					result = result2
				}
			} else {
				// remove kube-rbac-proxy networkpolicy if it exists
				if err = r.deleteKubeRBACProxyNetworkPolicy(ctx, params); err != nil {
					return result, err
				}
			}
		}

	} else {
		// remove kube-rbac-proxy rolebinding if it exists
		if err = r.deleteKubeRBACProxyClusterRoleBinding(ctx, params); err != nil {
			return result, err
		}
		// remove kube-rbac-proxy config if it exists
		if err = r.deleteKubeRBACProxyConfig(ctx, params); err != nil {
			return result, err
		}
		if r.IsOpenShift {
			// remove kube-rbac-proxy route if it exists
			if err = r.deleteKubeRBACProxyRoute(ctx, params); err != nil {
				return result, err
			}
			// remove kube-rbac-proxy networkpolicy if it exists
			if err = r.deleteKubeRBACProxyNetworkPolicy(ctx, params); err != nil {
				return result, err
			}
		}
	}

	return result, nil
}

func (r *ModelRegistryReconciler) deleteKubeRBACProxyClusterRoleBinding(ctx context.Context, params *ModelRegistryParams) error {
	roleBinding := rbac.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: params.Name + "-kube-rbac-proxy"}}
	return client.IgnoreNotFound(r.Client.Delete(ctx, &roleBinding))
}

func (r *ModelRegistryReconciler) deleteKubeRBACProxyConfig(ctx context.Context, params *ModelRegistryParams) error {
	configMap := corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: params.Name + "-kube-rbac-proxy-config", Namespace: params.Namespace}}
	return client.IgnoreNotFound(r.Client.Delete(ctx, &configMap))
}

func (r *ModelRegistryReconciler) deleteKubeRBACProxyRoute(ctx context.Context, params *ModelRegistryParams) error {
	route := routev1.Route{ObjectMeta: metav1.ObjectMeta{Name: params.Name + "-https", Namespace: params.Namespace}}
	return client.IgnoreNotFound(r.Client.Delete(ctx, &route))
}

func (r *ModelRegistryReconciler) deleteKubeRBACProxyNetworkPolicy(ctx context.Context, params *ModelRegistryParams) error {
	networkPolicy := networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: params.Name + "-https-route", Namespace: params.Namespace}}
	return client.IgnoreNotFound(r.Client.Delete(ctx, &networkPolicy))
}
