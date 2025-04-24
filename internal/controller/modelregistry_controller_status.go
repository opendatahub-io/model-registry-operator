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
	"bufio"
	"context"
	"fmt"
	"regexp"
	"strings"

	jsonpatch "github.com/evanphx/json-patch/v5"
	"github.com/go-logr/logr"
	modelregistryv1alpha1 "github.com/opendatahub-io/model-registry-operator/api/v1alpha1"
	routev1 "github.com/openshift/api/route/v1"
	"istio.io/client-go/pkg/apis/networking/v1beta1"
	v1beta12 "istio.io/client-go/pkg/apis/security/v1beta1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/json"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	klog "sigs.k8s.io/controller-runtime/pkg/log"
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
	// ConditionTypeOauthProxy represents the status of OAuth Proxy configuration.
	ConditionTypeOAuthProxy = "OAuthProxyAvailable"

	ReasonDeploymentCreated     = "CreatedDeployment"
	ReasonDeploymentCreating    = "CreatingDeployment"
	ReasonDeploymentUpdating    = "UpdatingDeployment"
	ReasonDeploymentAvailable   = "DeploymentAvailable"
	ReasonDeploymentUnavailable = "DeploymentUnavailable"
	ReasonConfigurationError    = "ConfigurationError"

	ReasonResourcesCreated     = "CreatedResources"
	ReasonResourcesAvailable   = "ResourcesAvailable"
	ReasonResourcesUnavailable = "ResourcesUnavailable"

	grpcContainerName       = "grpc-container"
	containerCreatingReason = "ContainerCreating"
)

// errRegexp is based on the CHECK_EQ macro output used by mlmd container.
// For more details on Abseil logging and CHECK_EQ macro see [Abseil documentation].
//
// [Abseil documentation]: https://abseil.io/docs/cpp/guides/logging#CHECK
var errRegexp = regexp.MustCompile("Check failed: absl::OkStatus\\(\\) == status \\(OK vs. ([^)]+)\\) (.*)")

func (r *ModelRegistryReconciler) setRegistryStatus(ctx context.Context, req ctrl.Request, params *ModelRegistryParams, operationResult OperationResult) (bool, error) {
	log := klog.FromContext(ctx)

	modelRegistry := &modelregistryv1alpha1.ModelRegistry{}
	if err := r.Get(ctx, req.NamespacedName, modelRegistry); err != nil {
		log.Error(err, "Failed to re-fetch modelRegistry")
		return false, err
	}

	r.setRegistryStatusHosts(req, modelRegistry)
	if err := r.setRegistryStatusSpecDefaults(modelRegistry, params.Spec); err != nil {
		// log error but continue updating rest of the status since it's not a blocker
		log.Error(err, "Failed to set registry status defaults")
	}
	// if specDefaults is {}, cleanup runtime properties
	if modelRegistry.Status.SpecDefaults == "{}" {
		// this is an exception to the rule to not modify a resource in reconcile,
		// because mutatingwebhook is not triggered on status update since it's a subresource
		modelRegistry.CleanupRuntimeDefaults()
		if err := r.Client.Update(ctx, modelRegistry); err != nil {
			log.Error(err, "Failed to update modelRegistry runtime defaults")
			return false, err
		}
	}

	status := metav1.ConditionTrue
	reason := ReasonDeploymentCreated
	message := "Deployment was successfully created"
	switch operationResult {
	case ResourceCreated:
		status = metav1.ConditionFalse
		reason = ReasonDeploymentCreating
		message = "Creating deployment"
	case ResourceUpdated:
		status = metav1.ConditionFalse
		reason = ReasonDeploymentUpdating
		message = "Updating deployment"
	case ResourceUnchanged:
		// ignore
	}

	meta.SetStatusCondition(&modelRegistry.Status.Conditions, metav1.Condition{Type: ConditionTypeProgressing,
		Status: status, Reason: reason,
		Message: message})

	// determine registry available condition
	deployment := &appsv1.Deployment{}
	if err := r.Get(ctx, req.NamespacedName, deployment); err != nil {
		log.Error(err, "Failed to get modelRegistry deployment", "name", req.NamespacedName)
		return false, err
	}
	log.V(10).Info("Found service deployment", "name", len(deployment.Name))

	// check deployment conditions errors
	// start with available=false to force DeploymentAvailable condition to set it to true later
	available := false
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
		message = fmt.Sprintf("Deployment is unavailable: %s", message)
	}

	// look for pod level detailed errors, if present
	unavailableReplicas := deployment.Status.UnavailableReplicas
	if unavailableReplicas != 0 {
		available, reason, message = r.CheckPodStatus(ctx, req, available, reason, message, unavailableReplicas)
	}

	if available {
		status = metav1.ConditionTrue
		reason = ReasonDeploymentAvailable
		message = "Deployment is available"
	} else {
		status = metav1.ConditionFalse
	}

	if available {
		if r.HasIstio {
			status, reason, message = r.SetIstioAndGatewayConditions(ctx, req, modelRegistry, status, reason, message)
		}
		if modelRegistry.Spec.OAuthProxy != nil {
			status, reason, message = r.SetOauthProxyCondition(ctx, req, modelRegistry, status, reason, message)
		}
	}

	meta.SetStatusCondition(&modelRegistry.Status.Conditions, metav1.Condition{Type: ConditionTypeAvailable,
		Status: status, Reason: reason,
		Message: message})
	if err := r.Status().Update(ctx, modelRegistry); err != nil {
		log.Error(err, "Failed to update modelRegistry status")
		return false, err
	}

	return available, nil
}

func (r *ModelRegistryReconciler) setRegistryStatusHosts(req ctrl.Request, registry *modelregistryv1alpha1.ModelRegistry) {

	var hosts []string

	istio := registry.Spec.Istio
	name := req.Name
	if istio != nil && istio.Gateway != nil && len(istio.Gateway.Domain) != 0 {
		domain := istio.Gateway.Domain
		hosts = append(hosts, fmt.Sprintf("%s-rest.%s", name, domain))
		hosts = append(hosts, fmt.Sprintf("%s-grpc.%s", name, domain))
	}
	namespace := req.Namespace
	hosts = append(hosts, fmt.Sprintf("%s.%s.svc.cluster.local", name, namespace))
	hosts = append(hosts, fmt.Sprintf("%s.%s", name, namespace))
	hosts = append(hosts, name)

	registry.Status.Hosts = hosts
	registry.Status.HostsStr = strings.Join(hosts, ",")
}

func (r *ModelRegistryReconciler) setRegistryStatusSpecDefaults(registry *modelregistryv1alpha1.ModelRegistry, spec *modelregistryv1alpha1.ModelRegistrySpec) error {
	originalJson, err := json.Marshal(registry.Spec)
	if err != nil {
		return fmt.Errorf("error marshalling spec for model registry %s: %w", registry.Name, err)
	}
	newJson, err := json.Marshal(spec)
	if err != nil {
		return fmt.Errorf("error marshalling spec with defaults for model registry %s: %w", registry.Name, err)
	}
	mergePatch, err := jsonpatch.CreateMergePatch(originalJson, newJson)
	if err != nil {
		return fmt.Errorf("error creating spec defaults: %w", err)
	}
	registry.Status.SpecDefaults = string(mergePatch)
	return nil
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
				// look for MLMD container errors, make sure it has also been created
				if s.Name == grpcContainerName && s.State.Waiting != nil && s.State.Waiting.Reason != containerCreatingReason {
					// check container log for MLMD errors
					dbError, err := r.getGrpcContainerDBerror(ctx, p)
					if err != nil {
						// log K8s error
						r.Log.Error(err, "failed to get grpc container error")
					}
					if dbError != nil {
						// MLMD errors take priority
						reason = ReasonConfigurationError
						message = fmt.Sprintf("Metadata database configuration error: %s", dbError)
						return available, reason, message
					}
				}
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
			message = fmt.Sprintf("Deployment is unavailable: pod %s has unready containers %s", p.Name, containerErrors.String())
		} else {

			// else use not ready pod status
			for _, c := range p.Status.Conditions {
				if c.Type == corev1.PodReady && c.Status == corev1.ConditionFalse {
					foundError = true
					message = fmt.Sprintf("Deployment is unavailable: pod %s containers not ready: {reason: %s, message: %s}", p.Name, c.Reason, c.Message)
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
		message = fmt.Sprintf("Deployment is unavailable: %d containers unavailable", unavailableReplicas)
	}

	return available, reason, message
}

// getGrpcContainerDBerror scrapes container log and returns a database connection error if it exists in the logs
// it also returns a k8s API error if it cannot read the container log
func (r *ModelRegistryReconciler) getGrpcContainerDBerror(ctx context.Context, pod corev1.Pod) (error, error) {
	request := r.ClientSet.CoreV1().Pods(pod.Namespace).GetLogs(pod.Name, &corev1.PodLogOptions{Container: grpcContainerName})
	podLogs, err := request.Stream(ctx)
	if err != nil {
		return nil, err
	}
	defer podLogs.Close()

	scanner := bufio.NewScanner(podLogs)
	for scanner.Scan() {
		line := scanner.Text()
		submatch := errRegexp.FindStringSubmatch(line)
		if len(submatch) > 0 {
			return fmt.Errorf("%s: %s", submatch[2], submatch[1]), nil
		}
	}
	if err = scanner.Err(); err != nil {
		return nil, err
	}
	return nil, nil
}

func (r *ModelRegistryReconciler) SetIstioAndGatewayConditions(ctx context.Context, req ctrl.Request,
	modelRegistry *modelregistryv1alpha1.ModelRegistry,
	status metav1.ConditionStatus, reason string, message string) (metav1.ConditionStatus, string, string) {
	if modelRegistry.Spec.Istio != nil {
		// set Istio available condition
		if !r.SetIstioCondition(ctx, req, modelRegistry) {
			status = metav1.ConditionFalse
			reason = ReasonResourcesUnavailable
			message = "Istio resources are unavailable"
		}

		// set Gateway available condition
		if modelRegistry.Spec.Istio.Gateway != nil {
			if !r.SetGatewayCondition(ctx, req, modelRegistry) {
				status = metav1.ConditionFalse
				reason = ReasonResourcesUnavailable
				message = "Istio Gateway resources are unavailable"
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
	message := "Istio resources were successfully created"

	available := true
	// verify that virtualservice, destinationrule, authorizationpolicy are available
	name := req.NamespacedName
	message, available, reason = r.CheckIstioResourcesAvailable(ctx, name, log, message, available, reason)

	if r.CreateAuthResources {
		message, available, reason = r.CheckAuthConfigCondition(ctx, name, log, message, available, reason)
	}

	status := metav1.ConditionFalse
	if available {
		if reason == ReasonResourcesAvailable || (!r.CreateAuthResources && reason == ReasonResourcesCreated) {
			status = metav1.ConditionTrue
		}
		// additionally verify that Deployment pod has 3 containers including the istio-envoy proxy
		message, reason, status = r.CheckDeploymentPods(ctx, name, "Istio", log, message, reason, status)
	} else {
		status = metav1.ConditionFalse
		reason = ReasonResourcesUnavailable
	}
	meta.SetStatusCondition(&modelRegistry.Status.Conditions, metav1.Condition{Type: ConditionTypeIstio,
		Status: status, Reason: reason,
		Message: message})

	return status == metav1.ConditionTrue
}

func (r *ModelRegistryReconciler) SetOauthProxyCondition(ctx context.Context, req ctrl.Request,
	modelRegistry *modelregistryv1alpha1.ModelRegistry,
	status metav1.ConditionStatus, reason string, message string) (metav1.ConditionStatus, string, string) {

	log := klog.FromContext(ctx)

	if modelRegistry.Spec.OAuthProxy != nil {

		// verify that Deployment pod has 3 containers including the oauth proxy
		name := req.NamespacedName
		reason2 := ReasonResourcesCreated
		message2 := "OAuth Proxy was successfully created"
		message2, reason2, status2 := r.CheckDeploymentPods(ctx, name, "OAuth", log, message2, reason2, status)
		meta.SetStatusCondition(&modelRegistry.Status.Conditions, metav1.Condition{Type: ConditionTypeOAuthProxy,
			Status: status2, Reason: reason2,
			Message: message2})

		// set OAuth proxy available condition
		if status2 == metav1.ConditionFalse {
			status = status2
			reason = reason2
			message = "OAuth Proxy resources are unavailable"
		}
	} else {
		meta.RemoveStatusCondition(&modelRegistry.Status.Conditions, ConditionTypeOAuthProxy)
	}

	return status, reason, message
}

func (r *ModelRegistryReconciler) CheckDeploymentPods(ctx context.Context, name types.NamespacedName, proxyType string,
	log logr.Logger, message string, reason string, status metav1.ConditionStatus) (string, string, metav1.ConditionStatus) {
	pods := corev1.PodList{}
	if err := r.Client.List(ctx, &pods,
		client.MatchingLabels{"app": name.Name, "component": "model-registry"},
		client.InNamespace(name.Namespace)); err != nil {

		log.Error(err, "Failed to get model registry pods", "name", name)
		message = fmt.Sprintf("Failed to find Pods: %s", err.Error())
		reason = ReasonResourcesUnavailable
		status = metav1.ConditionFalse

		return message, reason, status
	}

	if len(pods.Items) == 0 {
		message = fmt.Sprintf("No Pods found for Deployment %s", name.Name)
		reason = ReasonResourcesUnavailable
		status = metav1.ConditionFalse

		return message, reason, status
	}

	// check that pods have 3 containers
	for _, pod := range pods.Items {
		if len(pod.Spec.Containers) != 3 {
			message = fmt.Sprintf("%s proxy unavailable in Pod %s", proxyType, pod.Name)
			reason = ReasonResourcesUnavailable
			status = metav1.ConditionFalse
			break
		}
	}

	return message, reason, status
}

func (r *ModelRegistryReconciler) CheckAuthConfigCondition(ctx context.Context, name types.NamespacedName, log logr.Logger, message string, available bool, reason string) (string, bool, string) {
	authConfig := CreateAuthConfig()
	if err := r.Get(ctx, name, authConfig); err != nil {
		log.Error(err, "Failed to get model registry Istio Authorino AuthConfig", "name", name)
		message = fmt.Sprintf("Failed to find AuthConfig: %s", err.Error())
		available = false
	}

	// check authconfig Ready condition
	if available {
		conditions, _, _ := unstructured.NestedSlice(authConfig.Object, "status", "conditions")
		for _, c := range conditions {
			switch con := c.(type) {
			case map[string]interface{}:

				condType, _, _ := unstructured.NestedString(con, "type")
				if condType == "Ready" {
					status, _, _ := unstructured.NestedString(con, "status")
					available = status == "True"
					if available {
						reason = ReasonResourcesAvailable
						message = "Istio resources are available"
					} else {
						reason = ReasonResourcesUnavailable
						condReason, _, _ := unstructured.NestedString(con, "reason")
						condMessage, _, _ := unstructured.NestedString(con, "message")
						message = fmt.Sprintf("Istio AuthConfig is not ready: {reason: %s, message: %s}", condReason, condMessage)
					}
					break
				}
			}
		}
	}
	return message, available, reason
}

func (r *ModelRegistryReconciler) CheckIstioResourcesAvailable(ctx context.Context, name types.NamespacedName,
	log logr.Logger, message string, available bool, reason string) (string, bool, string) {

	var resource client.Object
	resource = &v1beta1.VirtualService{}
	if err := r.Get(ctx, name, resource); err != nil {
		log.Error(err, "Failed to get model registry Istio VirtualService", "name", name)
		message = fmt.Sprintf("Failed to find VirtualService: %s", err.Error())
		available = false
		reason = ReasonResourcesUnavailable
	}
	resource = &v1beta1.DestinationRule{}
	if err := r.Get(ctx, name, resource); err != nil {
		log.Error(err, "Failed to get model registry Istio DestinationRule", "name", name)
		message = fmt.Sprintf("Failed to find DestinationRule: %s", err.Error())
		available = false
		reason = ReasonResourcesUnavailable
	}
	if r.CreateAuthResources {
		resource = &v1beta12.AuthorizationPolicy{}
		policyName := name
		policyName.Name = policyName.Name + "-authorino"
		if err := r.Get(ctx, policyName, resource); err != nil {
			log.Error(err, "Failed to get model registry Istio AuthorizationPolicy", "name", policyName)
			message = fmt.Sprintf("Failed to find AuthorizationPolicy %s: %s", policyName, err.Error())
			available = false
			reason = ReasonResourcesUnavailable
		}
	}

	return message, available, reason
}

func (r *ModelRegistryReconciler) SetGatewayCondition(ctx context.Context, req ctrl.Request,
	modelRegistry *modelregistryv1alpha1.ModelRegistry) bool {

	log := klog.FromContext(ctx)

	reason := ReasonResourcesCreated
	message := "Istio Gateway resources were successfully created"

	available := true
	// verify that gateway is available
	name := req.NamespacedName
	resource := &v1beta1.Gateway{}
	if err := r.Get(ctx, name, resource); err != nil {
		log.Error(err, "Failed to get model registry Istio Gateway", "name", name)
		message = fmt.Sprintf("Failed to find Gateway: %s", err.Error())
		available = false
	}

	// check routes Ingress Admitted condition
	if available && r.IsOpenShift {
		message, available = r.CheckGatewayRoutes(ctx, modelRegistry, name, log, message, available)

		// set Gateway condition true if routes are available
		if available {
			reason = ReasonResourcesAvailable
			message = "Istio Gateway resources are available"
		}
	}

	status := metav1.ConditionFalse
	if available {
		if reason == ReasonResourcesAvailable || (!r.IsOpenShift && reason == ReasonResourcesCreated) {
			status = metav1.ConditionTrue
		}
	} else {
		status = metav1.ConditionFalse
		reason = ReasonResourcesUnavailable
	}
	meta.SetStatusCondition(&modelRegistry.Status.Conditions, metav1.Condition{Type: ConditionTypeGateway,
		Status: status, Reason: reason,
		Message: message})

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
			message = fmt.Sprintf("Failed to find Routes: %s", err.Error())
			available = false
		}

		// check Ingress Admitted condition
		if available {

			// look for conditions in all ingresses in all routes
			available = r.CheckRouteIngressConditions(routes, available, routeAvailable, routeMessage)

			// check that expected routes are available
			restError := false
			if restRouteEnabled && !routeAvailable["rest"] {
				restError = true
				message = routeMessage["rest"]
				if len(message) == 0 {
					available = false
					message = "Istio Gateway REST Route missing"
				}
			}
			if grpcRouteEnabled && !routeAvailable["grpc"] {
				if restError {
					message = fmt.Sprintf("%s, %s", message, routeMessage["grpc"])
				} else {
					message = routeMessage["grpc"]
				}
				if len(message) == 0 {
					available = false
					message = "Istio Gateway GRPC Route missing"
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
					routeAdmitted := c.Status == corev1.ConditionTrue

					routeName := route.Name
					routeType := routeName[strings.LastIndex(routeName, "-")+1:]
					routeAvailable[routeType] = routeAdmitted

					if !routeAdmitted {
						available = false
						routeMessage[routeType] = fmt.Sprintf("Host %s in Route %s is unavailable: {reason: %s, message: %s}", ingress.Host, routeName, c.Reason, c.Message)
					}
					break
				}
			}
		}
	}

	return available
}
