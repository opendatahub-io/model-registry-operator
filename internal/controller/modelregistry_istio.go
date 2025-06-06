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

	routev1 "github.com/openshift/api/route/v1"
	networking "istio.io/client-go/pkg/apis/networking/v1beta1"
	security "istio.io/client-go/pkg/apis/security/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

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

func CreateAuthConfig() *unstructured.Unstructured {
	authConfig := unstructured.Unstructured{}
	authConfig.SetGroupVersionKind(schema.GroupVersionKind{Group: "authorino.kuadrant.io", Version: "v1beta3", Kind: "AuthConfig"})
	return &authConfig
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

func getRouteLabels(name string) client.MatchingLabels {
	labels := client.MatchingLabels{
		"app":                     name,
		"component":               "model-registry",
		"maistra.io/gateway-name": name,
	}
	return labels
}
