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

// This file exists solely to hold the +kubebuilder:rbac markers for the
// standalone Catalog operator (cmd/catalog.go, internal/controller/catalog_controller.go).
// They live here, in a package scanned in isolation via `paths="./api/catalog/..."`
// (see the Makefile's `manifests` target), rather than alongside the reconciler in
// internal/controller, because controller-gen merges every marker under the scanned
// path into one ClusterRole. Putting them in internal/controller would pollute the
// ModelRegistry operator's manager-role (config/rbac/role.yaml).
//
// NOTE: There MUST be an empty newline at the end of this rbac permissions list, or
// role generation won't work!!!
// +kubebuilder:rbac:groups=aihub.opendatahub.io,resources=catalogs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=aihub.opendatahub.io,resources=catalogs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=aihub.opendatahub.io,resources=catalogs/finalizers,verbs=update
// +kubebuilder:rbac:groups=core,resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=pods;pods/log,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=services;serviceaccounts,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=endpoints,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=configmaps,verbs=create;get;list;watch;patch;update;delete
// +kubebuilder:rbac:groups=core,resources=secrets,verbs=create;get;list;watch;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=persistentvolumeclaims,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=route.openshift.io,resources=routes;routes/custom-host,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles;rolebindings;clusterrolebindings,verbs=get;list;watch;create;update;delete
// +kubebuilder:rbac:groups=authentication.k8s.io,resources=tokenreviews,verbs=create
// +kubebuilder:rbac:groups=authorization.k8s.io,resources=subjectaccessreviews,verbs=create
// +kubebuilder:rbac:groups=networking.k8s.io,resources=networkpolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apiextensions.k8s.io,resources=customresourcedefinitions,verbs=get;list;watch
// +kubebuilder:rbac:groups=services.platform.opendatahub.io,resources=auths,verbs=get;list;watch
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=httproutes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=referencegrants,verbs=get;list;watch;create

package v1alpha1
