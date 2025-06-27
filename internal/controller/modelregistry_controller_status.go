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

	"github.com/opendatahub-io/model-registry-operator/internal/controller/config"

	jsonpatch "github.com/evanphx/json-patch/v5"
	"github.com/go-logr/logr"
	"github.com/opendatahub-io/model-registry-operator/api/v1beta1"
	routev1 "github.com/openshift/api/route/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
	ReasonResourcesAlert       = "ResourcesAlert"

	grpcContainerName       = "grpc-container"
	restContainerName       = "rest-container"
	containerCreatingReason = "ContainerCreating"
)

// errRegexp is based on the CHECK_EQ macro output used by mlmd container.
// For more details on Abseil logging and CHECK_EQ macro see [Abseil documentation].
//
// [Abseil documentation]: https://abseil.io/docs/cpp/guides/logging#CHECK
var errRegexp = regexp.MustCompile("Check failed: absl::OkStatus\\(\\) == status \\(OK vs. ([^)]+)\\) (.*)")

func (r *ModelRegistryReconciler) setRegistryStatus(ctx context.Context, req ctrl.Request, params *ModelRegistryParams, operationResult OperationResult) (bool, error) {
	log := klog.FromContext(ctx)

	modelRegistry := &v1beta1.ModelRegistry{}
	if err := r.Get(ctx, req.NamespacedName, modelRegistry); err != nil {
		log.Error(err, "Failed to re-fetch modelRegistry")
		return false, err
	}

	r.setRegistryStatusHosts(req, params, modelRegistry)
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
			// remove deprecated Istio and Gateway conditions
			meta.RemoveStatusCondition(&modelRegistry.Status.Conditions, ConditionTypeIstio)
			meta.RemoveStatusCondition(&modelRegistry.Status.Conditions, ConditionTypeGateway)
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

func (r *ModelRegistryReconciler) setRegistryStatusHosts(req ctrl.Request, params *ModelRegistryParams, registry *v1beta1.ModelRegistry) {

	var hosts []string

	oAuthProxy := registry.Spec.OAuthProxy
	name := req.Name
	if oAuthProxy != nil && oAuthProxy.ServiceRoute == config.RouteEnabled {
		// use domain from the reconciled registry with runtime defaults
		domain := params.Spec.OAuthProxy.Domain
		hosts = append(hosts, fmt.Sprintf("%s-rest.%s", name, domain))
	}
	namespace := req.Namespace
	hosts = append(hosts, fmt.Sprintf("%s.%s.svc.cluster.local", name, namespace))
	hosts = append(hosts, fmt.Sprintf("%s.%s", name, namespace))
	hosts = append(hosts, name)

	registry.Status.Hosts = hosts
	registry.Status.HostsStr = strings.Join(hosts, ",")
}

func (r *ModelRegistryReconciler) setRegistryStatusSpecDefaults(registry *v1beta1.ModelRegistry, spec *v1beta1.ModelRegistrySpec) error {
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
	err := r.Client.List(ctx, &pods, client.MatchingLabels{"app": req.Name, "component": "model-registry"}, client.InNamespace(req.Namespace))
	if err != nil {
		// log K8s error
		r.Log.Error(err, "failed to get grpc container error")
	}
	for _, p := range pods.Items {
		// look for not ready container status first
		failedContainers := make(map[string]string)
		for _, s := range p.Status.ContainerStatuses {
			if !s.Ready {
				// look for MLMD container errors, make sure it has also been created
				if s.Name == grpcContainerName && s.State.Waiting != nil && s.State.Waiting.Reason != containerCreatingReason {
					// check container log for MLMD errors
					dbError, err := r.getContainerDBerror(ctx, p, grpcContainerName)
					if err != nil {
						// log K8s error
						r.Log.Error(err, "failed to get grpc container error")
					}
					if dbError != nil {
						if strings.Contains(dbError.Error(), "{{ALERT}}") {
							reason = ReasonResourcesAlert
							message = fmt.Sprintf("grpc container alert: %s", dbError)
							return available, reason, message
						}
						// MLMD errors take priority
						reason = ReasonConfigurationError
						message = fmt.Sprintf("metadata database configuration error: %s", dbError)
						return available, reason, message
					}
				}
				// check for schema migration errors within rest containers
				if s.Name == restContainerName && s.State.Waiting != nil && s.State.Waiting.Reason != containerCreatingReason {
					// check container log for schema migration errors
					dbError, err := r.getContainerDBerror(ctx, p, restContainerName)
					if err != nil {
						// log K8s error
						r.Log.Error(err, "failed to get rest container error")
					}
					if dbError != nil {
						if strings.Contains(dbError.Error(), "{{ALERT}}") {
							reason = ReasonResourcesAlert
							message = fmt.Sprintf("rest container alert: %s", dbError)
							return available, reason, message
						}
						// if not a schema migration error, return a generic configuration error
						reason = ReasonConfigurationError
						message = fmt.Sprintf("metadata database configuration error: %s", dbError)
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

// getContainerDBerror scrapes container log and returns a database connection error if it exists in the logs
// it also returns a k8s API error if it cannot read the container log
func (r *ModelRegistryReconciler) getContainerDBerror(ctx context.Context, pod corev1.Pod, containerTypeName string) (error, error) {
	request := r.ClientSet.CoreV1().Pods(pod.Namespace).GetLogs(pod.Name, &corev1.PodLogOptions{Container: containerTypeName})
	podLogs, err := request.Stream(ctx)
	if err != nil {
		return nil, err
	}
	defer podLogs.Close()

	scanner := bufio.NewScanner(podLogs)
	for scanner.Scan() {
		line := scanner.Text()

		// priority check for alert errors
		if strings.Contains(line, "{{ALERT}}") {
			return fmt.Errorf("%s", line), nil
		}
		// then check for generic errors
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

func (r *ModelRegistryReconciler) SetOauthProxyCondition(ctx context.Context, req ctrl.Request,
	modelRegistry *v1beta1.ModelRegistry,
	status metav1.ConditionStatus, reason string, message string) (metav1.ConditionStatus, string, string) {

	log := klog.FromContext(ctx)

	if modelRegistry.Spec.OAuthProxy != nil {

		// verify that Deployment pod has 3 containers including the oauth proxy
		name := req.NamespacedName
		reason2 := ReasonResourcesCreated
		message2 := "OAuth Proxy was successfully created"
		message2, reason2, status2 := r.CheckDeploymentPods(ctx, name, "OAuth", log, message2, reason2, status)

		// set OAuth proxy available condition
		if status2 == metav1.ConditionTrue {

			reason2 = ReasonResourcesAvailable

			// also check Oauth proxy route if enabled
			if modelRegistry.Spec.OAuthProxy.ServiceRoute == config.RouteEnabled {
				// get proxy route
				var routeList routev1.RouteList
				err := r.Client.List(ctx, &routeList, client.InNamespace(req.Namespace), client.MatchingLabels(map[string]string{
					"app": req.Name, "component": "model-registry",
				}))
				if err != nil {
					// log K8s error
					r.Log.Error(err, "failed to get oauth proxy route")
				}
				routeAvailable := make(map[string]bool)
				routeMessage := make(map[string]string)
				r.CheckRouteIngressConditions(&routeList, true, routeAvailable, routeMessage)
				routeName := modelRegistry.Name + "-https"
				routeType := "https"
				if !routeAvailable[routeType] {
					status2 = metav1.ConditionFalse
					reason2 = ReasonResourcesUnavailable
					message2 = fmt.Sprintf("OAuthProxy Route %s is unavailable: %s", routeName, routeMessage[routeType])
				}
			}
		}

		meta.SetStatusCondition(&modelRegistry.Status.Conditions, metav1.Condition{Type: ConditionTypeOAuthProxy,
			Status: status2, Reason: reason2, Message: message2})

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
