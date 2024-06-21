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
	"github.com/go-logr/logr"
	authorino "github.com/kuadrant/authorino/api/v1beta2"
	modelregistryv1alpha1 "github.com/opendatahub-io/model-registry-operator/api/v1alpha1"
	routev1 "github.com/openshift/api/route/v1"
	"istio.io/client-go/pkg/apis/networking/v1beta1"
	v1beta12 "istio.io/client-go/pkg/apis/security/v1beta1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	klog "sigs.k8s.io/controller-runtime/pkg/log"
	"strings"
)

// Definitions to manage status conditions
const (
	// ConditionTypeAvailable represents the status of the Deployment reconciliation
	ConditionTypeAvailable = "Available"
	// ConditionTypeProgressing represents the status used when the model registry is being deployed.
	ConditionTypeProgressing = "Progressing"
	// ConditionTypeDegraded represents the status used when the model registry is deleted and the finalizer operations must occur.
	ConditionTypeDegraded = "Degraded"

	// ConditionTypeIstio represents the status of base Istio resources configuration.
	ConditionTypeIstio = "IstioAvailable"
	// ConditionTypeGateway represents the status of Istio Gateway configuration.
	ConditionTypeGateway = "GatewayAvailable"

	ReasonDeploymentCreated     = "CreatedDeployment"
	ReasonDeploymentCreating    = "CreatingDeployment"
	ReasonDeploymentUpdating    = "UpdatingDeployment"
	ReasonDeploymentAvailable   = "DeploymentAvailable"
	ReasonDeploymentUnavailable = "DeploymentUnavailable"

	ReasonResourcesCreated     = "CreatedResources"
	ReasonResourcesAvailable   = "ResourcesAvailable"
	ReasonResourcesUnavailable = "ResourcesUnavailable"
)

func (r *ModelRegistryReconciler) setRegistryStatus(ctx context.Context, req ctrl.Request, operationResult OperationResult) (bool, error) {
	log := klog.FromContext(ctx)

	modelRegistry := &modelregistryv1alpha1.ModelRegistry{}
	if err := r.Get(ctx, req.NamespacedName, modelRegistry); err != nil {
		log.Error(err, "Failed to re-fetch modelRegistry")
		return false, err
	}

	status := metav1.ConditionTrue
	reason := ReasonDeploymentCreated
	message := "Deployment for model registry %s was successfully created"
	switch operationResult {
	case ResourceCreated:
		status = metav1.ConditionFalse
		reason = ReasonDeploymentCreating
		message = "Creating deployment for model registry %s"
	case ResourceUpdated:
		status = metav1.ConditionFalse
		reason = ReasonDeploymentUpdating
		message = "Updating deployment for model registry %s"
	case ResourceUnchanged:
		// ignore
	}

	meta.SetStatusCondition(&modelRegistry.Status.Conditions, metav1.Condition{Type: ConditionTypeProgressing,
		Status: status, Reason: reason,
		Message: fmt.Sprintf(message, modelRegistry.Name)})

	// determine registry available condition
	deployment := &appsv1.Deployment{}
	if err := r.Get(ctx, req.NamespacedName, deployment); err != nil {
		log.Error(err, "Failed to get modelRegistry deployment", "name", req.NamespacedName)
		return false, err
	}
	log.V(10).Info("Found service deployment", "name", len(deployment.Name))

	// check deployment conditions errors
	available := true
	failed := false
	progressing := true
	for _, c := range deployment.Status.Conditions {
		switch c.Type {
		case appsv1.DeploymentAvailable:
			if !failed && progressing {
				available = c.Status == corev1.ConditionTrue
				if !available {
					message = c.Message
				}
			}
		case appsv1.DeploymentProgressing:
			if c.Status == corev1.ConditionFalse && !failed {
				available = false
				progressing = false
				message = c.Message
			}
		case appsv1.DeploymentReplicaFailure:
			if c.Status == corev1.ConditionTrue {
				available = false
				failed = true
				message = c.Message
			}
		}
	}
	if !available {
		reason = ReasonDeploymentUnavailable
		message = fmt.Sprintf("Deployment for model registry %%s is unavailable: %s", message)
	}

	// look for pod level detailed errors, if present
	unavailableReplicas := deployment.Status.UnavailableReplicas
	if unavailableReplicas != 0 {
		available, reason, message = r.CheckPodStatus(ctx, req, available, reason, message, unavailableReplicas)
	}

	if available {
		status = metav1.ConditionTrue
		reason = ReasonDeploymentAvailable
		message = "Deployment for model registry %s is available"
	} else {
		status = metav1.ConditionFalse
	}

	if available && r.HasIstio {
		status, reason, message = r.SetIstioAndGatewayConditions(ctx, req, modelRegistry, status, reason, message)
	}

	meta.SetStatusCondition(&modelRegistry.Status.Conditions, metav1.Condition{Type: ConditionTypeAvailable,
		Status: status, Reason: reason,
		Message: fmt.Sprintf(message, modelRegistry.Name)})
	if err := r.Status().Update(ctx, modelRegistry); err != nil {
		log.Error(err, "Failed to update modelRegistry status")
		return false, err
	}

	return available, nil
}

func (r *ModelRegistryReconciler) CheckPodStatus(ctx context.Context, req ctrl.Request,
	available bool, reason string, message string, unavailableReplicas int32) (bool, string, string) {

	available = false
	reason = ReasonDeploymentUnavailable
	foundError := false

	// find the not ready pod and get message
	var pods corev1.PodList
	r.Client.List(ctx, &pods, client.MatchingLabels{"app": req.Name, "component": "model-registry"}, client.InNamespace(req.Namespace))
	for _, p := range pods.Items {
		// look for not ready container status first
		failedContainers := make(map[string]string)
		for _, s := range p.Status.ContainerStatuses {
			if !s.Ready {
				if s.State.Waiting != nil {
					failedContainers[s.Name] = fmt.Sprintf("{waiting: {reason: %s, message: %s}}", s.State.Waiting.Reason, s.State.Waiting.Message)
				} else if s.State.Terminated != nil {
					failedContainers[s.Name] = fmt.Sprintf("{terminated: {reason: %s, message: %s}}", s.State.Terminated.Reason, s.State.Terminated.Message)
				}
			}
		}
		if len(failedContainers) > 0 {
			foundError = true
			var containerErrors strings.Builder
			first := ""
			containerErrors.WriteString("[")
			for c, e := range failedContainers {
				containerErrors.WriteString(fmt.Sprintf("%s%s: %s", first, c, e))
				if first == "" {
					first = ", "
				}
			}
			containerErrors.WriteString("]")
			message = fmt.Sprintf("Deployment for model registry %%s is unavailable: pod %s has unready containers %v", p.Name, containerErrors.String())
		} else {

			// else use not ready pod status
			for _, c := range p.Status.Conditions {
				if c.Type == corev1.PodReady && c.Status == corev1.ConditionFalse {
					foundError = true
					message = fmt.Sprintf("Deployment for model registry %%s is unavailable: pod %s containers not ready: %s", p.Name, c.Message)
					break
				}
			}
		}
		// report first pod error
		if foundError {
			break
		}
	}

	// generic error if a specific one was not found
	if !foundError {
		message = fmt.Sprintf("Deployment for model registry %%s is unavailable: %d containers not available", unavailableReplicas)
	}

	return available, reason, message
}

func (r *ModelRegistryReconciler) SetIstioAndGatewayConditions(ctx context.Context, req ctrl.Request,
	modelRegistry *modelregistryv1alpha1.ModelRegistry,
	status metav1.ConditionStatus, reason string, message string) (metav1.ConditionStatus, string, string) {
	if modelRegistry.Spec.Istio != nil {
		// set Istio available condition
		if !r.SetIstioCondition(ctx, req, modelRegistry) {
			status = metav1.ConditionFalse
			reason = ReasonResourcesUnavailable
			message = "Istio resources for model registry %s are not available"
		}

		// set Gateway available condition
		if modelRegistry.Spec.Istio.Gateway != nil {
			if !r.SetGatewayCondition(ctx, req, modelRegistry) {
				status = metav1.ConditionFalse
				reason = ReasonResourcesUnavailable
				message = "Istio Gateway resources for model registry %s are not available"
			}
		} else {
			meta.RemoveStatusCondition(&modelRegistry.Status.Conditions, ConditionTypeGateway)
		}
	} else {
		meta.RemoveStatusCondition(&modelRegistry.Status.Conditions, ConditionTypeIstio)
		meta.RemoveStatusCondition(&modelRegistry.Status.Conditions, ConditionTypeGateway)
	}

	return status, reason, message
}

func (r *ModelRegistryReconciler) SetIstioCondition(ctx context.Context, req ctrl.Request,
	modelRegistry *modelregistryv1alpha1.ModelRegistry) bool {

	log := klog.FromContext(ctx)

	reason := ReasonResourcesCreated
	message := "Istio resources for model registry %s were successfully created"

	available := true
	// verify that virtualservice, destinationrule, authorizationpolicy are available
	name := req.NamespacedName
	message, available = r.CheckIstioResourcesAvailable(ctx, name, log, message, available)

	message, available, reason = r.CheckAuthConfigCondition(ctx, name, log, message, available, reason)

	status := metav1.ConditionFalse
	if available {
		if reason == ReasonResourcesAvailable {
			status = metav1.ConditionTrue
		}
		// additionally verify that Deployment pod has 3 containers including the istio-envoy proxy
		message, reason, status = r.CheckDeploymentPods(ctx, name, log, message, reason, status)
	} else {
		status = metav1.ConditionFalse
		reason = ReasonResourcesUnavailable
	}
	meta.SetStatusCondition(&modelRegistry.Status.Conditions, metav1.Condition{Type: ConditionTypeIstio,
		Status: status, Reason: reason,
		Message: fmt.Sprintf(message, modelRegistry.Name)})

	return status == metav1.ConditionTrue
}

func (r *ModelRegistryReconciler) CheckDeploymentPods(ctx context.Context, name types.NamespacedName,
	log logr.Logger, message string, reason string, status metav1.ConditionStatus) (string, string, metav1.ConditionStatus) {

	pods := corev1.PodList{}
	if err := r.Client.List(ctx, &pods,
		client.MatchingLabels{"app": name.Name, "component": "model-registry"},
		client.InNamespace(name.Namespace)); err != nil {

		log.Error(err, "Failed to get model registry pods", "name", name)
		message = fmt.Sprintf("Failed to find Pods for model registry %%s: %s", err.Error())
		reason = ReasonResourcesUnavailable
		status = metav1.ConditionFalse

	} else {
		// check that pods have 3 containers
		for _, pod := range pods.Items {
			if len(pod.Spec.Containers) != 3 {
				message = fmt.Sprintf("Istio proxy unavailable in Pod %s for model registry %%s", pod.Name)
				reason = ReasonResourcesUnavailable
				status = metav1.ConditionFalse
				break
			}
		}
	}

	return message, reason, status
}

func (r *ModelRegistryReconciler) CheckAuthConfigCondition(ctx context.Context, name types.NamespacedName, log logr.Logger, message string, available bool, reason string) (string, bool, string) {
	authConfig := &authorino.AuthConfig{}
	if err := r.Get(ctx, name, authConfig); err != nil {
		log.Error(err, "Failed to get model registry Istio Authorino AuthConfig", "name", name)
		message = fmt.Sprintf("Failed to find AuthConfig for model registry %%s: %s", err.Error())
		available = false
	}

	// check authconfig Ready condition
	if available {
		for _, c := range authConfig.Status.Conditions {
			if c.Type == authorino.StatusConditionReady {
				available = c.Status == corev1.ConditionTrue
				if available {
					reason = ReasonResourcesAvailable
					message = "Istio resources for model registry %s are available"
				} else {
					reason = ReasonResourcesUnavailable
					message = fmt.Sprintf("Istio AuthConfig for model registry %%s is not ready: {reason: %s, message: %s}", c.Reason, c.Message)
				}
				break
			}
		}
	}
	return message, available, reason
}

func (r *ModelRegistryReconciler) CheckIstioResourcesAvailable(ctx context.Context, name types.NamespacedName,
	log logr.Logger, message string, available bool) (string, bool) {

	var resource client.Object
	resource = &v1beta1.VirtualService{}
	if err := r.Get(ctx, name, resource); err != nil {
		log.Error(err, "Failed to get model registry Istio VirtualService", "name", name)
		message = fmt.Sprintf("Failed to find VirtualService for model registry %%s: %s", err.Error())
		available = false
	}
	resource = &v1beta1.DestinationRule{}
	if err := r.Get(ctx, name, resource); err != nil {
		log.Error(err, "Failed to get model registry Istio DestinationRule", "name", name)
		message = fmt.Sprintf("Failed to find DestinationRule for model registry %%s: %s", err.Error())
		available = false
	}
	resource = &v1beta12.AuthorizationPolicy{}
	policyName := name
	policyName.Name = policyName.Name + "-authorino"
	if err := r.Get(ctx, policyName, resource); err != nil {
		log.Error(err, "Failed to get model registry Istio AuthorizationPolicy", "name", policyName)
		message = fmt.Sprintf("Failed to find AuthorizationPolicy %s for model registry %%s: %s", policyName, err.Error())
		available = false
	}

	return message, available
}

func (r *ModelRegistryReconciler) SetGatewayCondition(ctx context.Context, req ctrl.Request,
	modelRegistry *modelregistryv1alpha1.ModelRegistry) bool {

	log := klog.FromContext(ctx)

	reason := ReasonResourcesCreated
	message := "Istio Gateway resources for model registry %s were successfully created"

	available := true
	// verify that gateway is available
	name := req.NamespacedName
	resource := &v1beta1.Gateway{}
	if err := r.Get(ctx, name, resource); err != nil {
		log.Error(err, "Failed to get model registry Istio Gateway", "name", name)
		message = fmt.Sprintf("Failed to find Gateway for model registry %%s: %s", err.Error())
		available = false
	}

	// check routes Ingress Admitted condition
	if available {
		message, available = r.CheckGatewayRoutes(ctx, modelRegistry, name, log, message, available)

		// set Gateway condition true if routes are available
		if available {
			reason = ReasonResourcesAvailable
			message = "Istio Gateway resources for model registry %s are available"
		}
	}

	status := metav1.ConditionFalse
	if available {
		if reason == ReasonResourcesAvailable {
			status = metav1.ConditionTrue
		}
	} else {
		status = metav1.ConditionFalse
		reason = ReasonResourcesUnavailable
	}
	meta.SetStatusCondition(&modelRegistry.Status.Conditions, metav1.Condition{Type: ConditionTypeGateway,
		Status: status, Reason: reason,
		Message: fmt.Sprintf(message, modelRegistry.Name)})

	return status == metav1.ConditionTrue
}

func (r *ModelRegistryReconciler) CheckGatewayRoutes(ctx context.Context, modelRegistry *modelregistryv1alpha1.ModelRegistry, name types.NamespacedName, log logr.Logger, message string, available bool) (string, bool) {
	restRouteEnabled := modelRegistry.Spec.Istio.Gateway.Rest.GatewayRoute == "enabled"
	grpcRouteEnabled := modelRegistry.Spec.Istio.Gateway.Grpc.GatewayRoute == "enabled"

	routeAvailable := map[string]bool{"rest": false, "grpc": false}
	routeMessage := map[string]string{}

	if restRouteEnabled || grpcRouteEnabled {

		routes := &routev1.RouteList{}
		labels := getRouteLabels(name.Name)
		if err := r.Client.List(ctx, routes, labels); err != nil {
			log.Error(err, "Failed to get model registry Routes", "name", name)
			message = fmt.Sprintf("Failed to find Routes for model registry %%s: %s", err.Error())
			available = false
		}

		// check Ingress Admitted condition
		if available {

			// look for conditions in all ingresses in all routes
			available = r.CheckRouteIngressConditions(routes, available, routeAvailable, routeMessage)

			// check that expected routes are available
			if restRouteEnabled && !routeAvailable["rest"] {
				message = routeMessage["rest"]
				if len(message) == 0 {
					available = false
					message = "Istio Gateway REST Route missing for model registry %s"
				}
			}
			if grpcRouteEnabled && !routeAvailable["grpc"] {
				message = routeMessage["grpc"]
				if len(message) == 0 {
					available = false
					message = "Istio Gateway GRPC Route missing for model registry %s"
				}
			}
		}
	}

	return message, available
}

func (r *ModelRegistryReconciler) CheckRouteIngressConditions(routes *routev1.RouteList, available bool,
	routeAvailable map[string]bool, routeMessage map[string]string) bool {

	for _, route := range routes.Items {
		for _, ingress := range route.Status.Ingress {
			for _, c := range ingress.Conditions {

				if c.Type == routev1.RouteAdmitted {
					available = c.Status == corev1.ConditionTrue

					routeName := route.Name
					routeType := routeName[strings.LastIndex(routeName, "-")+1:]
					routeAvailable[routeType] = available

					if !available {
						routeMessage[routeType] = fmt.Sprintf("Istio Gateway Host %s in Route %s for model registry %%s is not available: {reason: %s, message: %s}", ingress.Host, routeName, c.Reason, c.Message)
					}
					break
				}
			}
		}
	}

	return available
}
