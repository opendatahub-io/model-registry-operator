/*
Copyright 2023.

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
	"fmt"

	modelregistryv1alpha1 "github.com/opendatahub-io/model-registry-operator/api/v1alpha1"
	"github.com/opendatahub-io/model-registry-operator/internal/controller/config"
	routev1 "github.com/openshift/api/route/v1"
	networking "istio.io/client-go/pkg/apis/networking/v1beta1"
	security "istio.io/client-go/pkg/apis/security/v1beta1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func (r *ModelRegistryReconciler) createOrUpdateIstioConfig(ctx context.Context, params *ModelRegistryParams, registry *modelregistryv1alpha1.ModelRegistry) (OperationResult, error) {
	var result, result2 OperationResult

	var err error
	result, err = r.createOrUpdateVirtualService(ctx, params, registry, "virtual-service.yaml.tmpl")
	if err != nil {
		return result, err
	}

	result2, err = r.createOrUpdateDestinationRule(ctx, params, registry, "destination-rule.yaml.tmpl")
	if err != nil {
		return result2, err
	}
	if result2 != ResourceUnchanged {
		result = result2
	}

	if r.CreateAuthResources {
		result2, err = r.createOrUpdateAuthorizationPolicy(ctx, params, registry, "authorino-authorization-policy.yaml.tmpl")
		if err != nil {
			return result2, err
		}
		if result2 != ResourceUnchanged {
			result = result2
		}

		result2, err = r.createOrUpdateAuthConfig(ctx, params, registry, "authconfig.yaml.tmpl")
		if err != nil {
			return result2, err
		}
		if result2 != ResourceUnchanged {
			result = result2
		}
	}

	if params.Spec.Istio.Gateway != nil {
		result2, err = r.createOrUpdateGateway(ctx, params, registry, "gateway.yaml.tmpl")
		if err != nil {
			return result2, err
		}
		if result2 != ResourceUnchanged {
			result = result2
		}

		if r.IsOpenShift {
			result2, err = r.createOrUpdateGatewayRoutes(ctx, params, registry, "gateway-route.yaml.tmpl")
			if err != nil {
				return result2, err
			}
			if result2 != ResourceUnchanged {
				result = result2
			}
		}
	} else {
		// remove gateway if it exists
		if err = r.deleteGatewayConfig(ctx, params); err != nil {
			return ResourceUpdated, err
		}
	}

	return result, nil
}

func (r *ModelRegistryReconciler) deleteIstioConfig(ctx context.Context, params *ModelRegistryParams) (OperationResult, error) {
	var err error

	objectMeta := metav1.ObjectMeta{Name: params.Name, Namespace: params.Namespace}
	virtualService := networking.VirtualService{ObjectMeta: objectMeta}
	if err = r.Client.Delete(ctx, &virtualService); client.IgnoreNotFound(err) != nil {
		return ResourceUpdated, err
	}

	destinationRule := networking.DestinationRule{ObjectMeta: objectMeta}
	if err = r.Client.Delete(ctx, &destinationRule); client.IgnoreNotFound(err) != nil {
		return ResourceUpdated, err
	}

	if r.CreateAuthResources {
		authorizationPolicy := security.AuthorizationPolicy{ObjectMeta: objectMeta}
		authorizationPolicy.Name = authorizationPolicy.Name + "-authorino"
		if err = r.Client.Delete(ctx, &authorizationPolicy); client.IgnoreNotFound(err) != nil {
			return ResourceUpdated, err
		}

		authConfig := CreateAuthConfig()
		authConfig.SetName(params.Name)
		authConfig.SetNamespace(params.Namespace)
		if err = r.Client.Delete(ctx, authConfig); client.IgnoreNotFound(err) != nil {
			return ResourceUpdated, err
		}
	}

	if err = r.deleteGatewayConfig(ctx, params); err != nil {
		return ResourceUpdated, err
	}

	return ResourceUnchanged, nil
}

func (r *ModelRegistryReconciler) createOrUpdateVirtualService(ctx context.Context, params *ModelRegistryParams,
	registry *modelregistryv1alpha1.ModelRegistry, templateName string) (result OperationResult, err error) {
	result = ResourceUnchanged
	var virtualService networking.VirtualService
	if err = r.Apply(params, templateName, &virtualService); err != nil {
		return result, err
	}
	if err = ctrl.SetControllerReference(registry, &virtualService, r.Scheme); err != nil {
		return result, err
	}

	result, err = r.createOrUpdate(ctx, &networking.VirtualService{}, &virtualService)
	if err != nil {
		return result, err
	}
	return result, nil
}

func (r *ModelRegistryReconciler) createOrUpdateDestinationRule(ctx context.Context, params *ModelRegistryParams,
	registry *modelregistryv1alpha1.ModelRegistry, templateName string) (result OperationResult, err error) {
	result = ResourceUnchanged
	var destinationRule networking.DestinationRule
	if err = r.Apply(params, templateName, &destinationRule); err != nil {
		return result, err
	}
	if err = ctrl.SetControllerReference(registry, &destinationRule, r.Scheme); err != nil {
		return result, err
	}

	result, err = r.createOrUpdate(ctx, &networking.DestinationRule{}, &destinationRule)
	if err != nil {
		return result, err
	}
	return result, nil
}

func (r *ModelRegistryReconciler) createOrUpdateAuthorizationPolicy(ctx context.Context, params *ModelRegistryParams,
	registry *modelregistryv1alpha1.ModelRegistry, templateName string) (result OperationResult, err error) {
	result = ResourceUnchanged
	var authorizationPolicy security.AuthorizationPolicy
	if err = r.Apply(params, templateName, &authorizationPolicy); err != nil {
		return result, err
	}
	if err = ctrl.SetControllerReference(registry, &authorizationPolicy, r.Scheme); err != nil {
		return result, err
	}

	result, err = r.createOrUpdate(ctx, &security.AuthorizationPolicy{}, &authorizationPolicy)
	if err != nil {
		return result, err
	}
	return result, nil
}

func (r *ModelRegistryReconciler) createOrUpdateAuthConfig(ctx context.Context, params *ModelRegistryParams,
	registry *modelregistryv1alpha1.ModelRegistry, templateName string) (result OperationResult, err error) {
	// TODO: Audiences property was not being correctly updated to the default value in previous operator versions
	// Replace this with a conversion webhook in the future
	// Are AuthConfig audiences specified?
	if len(params.Spec.Istio.Audiences) == 0 {
		// use operator serviceaccount audiences by default
		params.Spec.Istio.Audiences = config.GetDefaultAudiences()
	}

	result = ResourceUnchanged
	authConfig := CreateAuthConfig()
	if err = r.Apply(params, templateName, authConfig); err != nil {
		return result, err
	}
	if err = ctrl.SetControllerReference(registry, authConfig, r.Scheme); err != nil {
		return result, err
	}

	// NOTE: AuthConfig CRD uses maps, which is not supported in k8s 3-way merge patch
	// use an Unstructured current object to force it to use a json merge patch instead
	current := CreateAuthConfig()
	result, err = r.createOrUpdate(ctx, current, authConfig)
	if err != nil {
		return result, err
	}
	return result, nil
}

func CreateAuthConfig() *unstructured.Unstructured {
	authConfig := unstructured.Unstructured{}
	authConfig.SetGroupVersionKind(schema.GroupVersionKind{Group: "authorino.kuadrant.io", Version: "v1beta3", Kind: "AuthConfig"})
	return &authConfig
}

func (r *ModelRegistryReconciler) createOrUpdateGateway(ctx context.Context, params *ModelRegistryParams,
	registry *modelregistryv1alpha1.ModelRegistry, templateName string) (result OperationResult, err error) {
	result = ResourceUnchanged
	var gateway networking.Gateway
	if err = r.Apply(params, templateName, &gateway); err != nil {
		return result, err
	}
	if err = ctrl.SetControllerReference(registry, &gateway, r.Scheme); err != nil {
		return result, err
	}

	result, err = r.createOrUpdate(ctx, &networking.Gateway{}, &gateway)
	if err != nil {
		return result, err
	}

	return result, nil
}

func (r *ModelRegistryReconciler) deleteGatewayConfig(ctx context.Context, params *ModelRegistryParams) error {
	gateway := networking.Gateway{ObjectMeta: metav1.ObjectMeta{Name: params.Name, Namespace: params.Namespace}}
	if err := r.Client.Delete(ctx, &gateway); client.IgnoreNotFound(err) != nil {
		return err
	}
	return r.deleteGatewayRoutes(ctx, params.Name)
}

func (r *ModelRegistryReconciler) deleteGatewayRoutes(ctx context.Context, name string) error {
	// delete all gateway routes
	labels := getRouteLabels(name)
	var list routev1.RouteList
	if err := r.Client.List(ctx, &list, labels); err != nil {
		return err
	}
	for _, route := range list.Items {
		if err := r.Client.Delete(ctx, &route); client.IgnoreNotFound(err) != nil {
			return err
		}
	}
	return nil
}

func (r *ModelRegistryReconciler) createOrUpdateGatewayRoutes(ctx context.Context, params *ModelRegistryParams,
	registry *modelregistryv1alpha1.ModelRegistry, templateName string) (result OperationResult, err error) {
	result = ResourceUnchanged

	// get ingress gateway service
	var serviceList corev1.ServiceList
	labels := client.MatchingLabels{"istio": *params.Spec.Istio.Gateway.IstioIngress}
	if params.Spec.Istio.Gateway.ControlPlane != nil {
		labels["istio.io/rev"] = *params.Spec.Istio.Gateway.ControlPlane
	}
	if err = r.Client.List(ctx, &serviceList, labels); err != nil {
		return result, err
	}
	if len(serviceList.Items) != 1 {
		return result, fmt.Errorf("missing unique ingress gateway service with labels %s, found %d services",
			labels, len(serviceList.Items))
	}
	params.IngressService = &serviceList.Items[0]

	// create/update REST route
	result, err = r.handleGatewayRoute(ctx, params, templateName, "-rest",
		params.Spec.Istio.Gateway.Rest.GatewayRoute, params.Spec.Istio.Gateway.Rest.TLS)
	if err != nil {
		return result, err
	}

	// create/update gRPC route
	result2, err := r.handleGatewayRoute(ctx, params, templateName, "-grpc",
		params.Spec.Istio.Gateway.Grpc.GatewayRoute, params.Spec.Istio.Gateway.Grpc.TLS)
	if err != nil {
		return result2, err
	}
	if result2 != ResourceUnchanged {
		result = result2
	}

	return result, nil
}

func (r *ModelRegistryReconciler) handleGatewayRoute(ctx context.Context, params *ModelRegistryParams, templateName string,
	suffix string, gatewayRoute string, tls *modelregistryv1alpha1.TLSServerSettings) (result OperationResult, err error) {

	// route specific params
	params.Host = params.Name + suffix
	params.TLS = tls

	var route routev1.Route
	if err = r.Apply(params, templateName, &route); err != nil {
		return result, err
	}

	if gatewayRoute == config.RouteEnabled {
		result, err = r.createOrUpdate(ctx, &routev1.Route{}, &route)
		if err != nil {
			return result, err
		}
	} else {
		// delete the route if it exists
		if err = r.Client.Delete(ctx, &route); client.IgnoreNotFound(err) != nil {
			result = ResourceUpdated
			return result, err
		}
	}

	return result, nil
}

func getRouteLabels(name string) client.MatchingLabels {
	labels := client.MatchingLabels{
		"app":                     name,
		"component":               "model-registry",
		"maistra.io/gateway-name": name,
	}
	return labels
}
