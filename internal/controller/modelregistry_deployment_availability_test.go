/*
Copyright 2026.

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
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// newAvailabilityReconciler builds a ModelRegistryReconciler over a fake
// client for direct checkDeploymentAvailability calls. It builds its own
// scheme because the envtest k8sClient is only initialized inside ginkgo specs.
func newAvailabilityReconciler(objects ...client.Object) *ModelRegistryReconciler {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		panic(err)
	}
	return &ModelRegistryReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build(),
		Log:    ctrl.Log.WithName("test"),
	}
}

func availabilityDeployment(name, namespace string, transitionTime time.Time) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Status: appsv1.DeploymentStatus{
			Conditions: []appsv1.DeploymentCondition{{
				Type:               appsv1.DeploymentAvailable,
				Status:             corev1.ConditionTrue,
				LastTransitionTime: metav1.NewTime(transitionTime),
			}},
		},
	}
}

// availabilityEndpoints builds the Endpoints object the availability check
// reads; corev1.Endpoints is deprecated but still the type under test, so
// the staticcheck deprecation is suppressed on each use below.
func availabilityEndpoints(name, namespace string, ready bool) *corev1.Endpoints { //nolint:staticcheck
	endpoints := &corev1.Endpoints{ //nolint:staticcheck
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
	}
	if ready {
		endpoints.Subsets = []corev1.EndpointSubset{ //nolint:staticcheck
			{Addresses: []corev1.EndpointAddress{{IP: "10.0.0.1"}}},
		}
	} else {
		endpoints.Subsets = []corev1.EndpointSubset{ //nolint:staticcheck
			{NotReadyAddresses: []corev1.EndpointAddress{{IP: "10.0.0.1"}}},
		}
	}
	return endpoints
}

func TestCheckDeploymentAvailability(t *testing.T) {
	const name = "test-registry"
	const namespace = "test-namespace"
	ctx := context.Background()
	key := client.ObjectKey{Name: name, Namespace: namespace}

	tests := map[string]struct {
		objects     []client.Object
		wantStatus  metav1.ConditionStatus
		wantReason  string
		wantMessage string
	}{
		// The cooldown guards against the application reporting ready
		// endpoints before it actually serves requests.
		"available with fresh transition time and ready endpoints": {
			objects: []client.Object{
				availabilityDeployment(name, namespace, time.Now()),
				availabilityEndpoints(name, namespace, true),
			},
			wantStatus:  metav1.ConditionFalse,
			wantReason:  ReasonDeploymentCooldown,
			wantMessage: "Deployment only recently available",
		},
		"available with mature transition time and ready endpoints": {
			objects: []client.Object{
				availabilityDeployment(name, namespace, time.Now().Add(-time.Minute)),
				availabilityEndpoints(name, namespace, true),
			},
			wantStatus: metav1.ConditionTrue,
			wantReason: ReasonDeploymentAvailable,
		},
		"available with missing endpoints": {
			objects: []client.Object{
				availabilityDeployment(name, namespace, time.Now().Add(-time.Minute)),
			},
			wantStatus:  metav1.ConditionFalse,
			wantReason:  ReasonDeploymentUnavailable,
			wantMessage: "Service endpoints not found",
		},
		"available with endpoints that have no ready addresses": {
			objects: []client.Object{
				availabilityDeployment(name, namespace, time.Now().Add(-time.Minute)),
				availabilityEndpoints(name, namespace, false),
			},
			wantStatus:  metav1.ConditionFalse,
			wantReason:  ReasonDeploymentUnavailable,
			wantMessage: "Service endpoints not ready",
		},
		"unavailable deployment": {
			objects: []client.Object{
				&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}},
			},
			wantStatus: metav1.ConditionFalse,
			wantReason: ReasonDeploymentUnavailable,
		},
	}

	for testName, tt := range tests {
		t.Run(testName, func(t *testing.T) {
			reconciler := newAvailabilityReconciler(tt.objects...)
			condition, err := reconciler.checkDeploymentAvailability(ctx, key, name, "model-registry")
			if err != nil {
				t.Fatalf("checkDeploymentAvailability() error = %v", err)
			}
			if condition.Status != tt.wantStatus {
				t.Errorf("condition.Status = %q, want %q", condition.Status, tt.wantStatus)
			}
			if condition.Reason != tt.wantReason {
				t.Errorf("condition.Reason = %q, want %q", condition.Reason, tt.wantReason)
			}
			if tt.wantMessage != "" && !strings.Contains(condition.Message, tt.wantMessage) {
				t.Errorf("condition.Message = %q, want substring %q", condition.Message, tt.wantMessage)
			}
		})
	}
}
