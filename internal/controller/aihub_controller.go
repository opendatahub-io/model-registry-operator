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

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	aihubv1alpha1 "github.com/opendatahub-io/model-registry-operator/api/aihub/v1alpha1"
)

func (r *AIHubReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	return ctrl.Result{}, nil
}

func (r *AIHubReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&aihubv1alpha1.AIHub{}).
		Complete(r)
}

type AIHubReconciler struct {
	client.Client
	Scheme                *runtime.Scheme
	ManifestsTemplatePath string
}
