/*
Copyright 2023.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package webhook

import (
	"context"
	"fmt"
	"github.com/opendatahub-io/model-registry-operator/api/v1alpha1"
	ctrl "sigs.k8s.io/controller-runtime"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// log is for logging in this package.
var modelregistrylog = logf.Log.WithName("modelregistryresource")

const (
	// default ports
	DefaultHttpPort  = 80
	DefaultHttpsPort = 443

	tagSeparator = ":"
	emptyValue   = ""
)

func SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		WithDefaulter(&modelRegistryDefaulter{Client: mgr.GetClient()}).
		WithValidator(&modelRegistryValidator{Client: mgr.GetClient()}).
		For(&v1alpha1.ModelRegistry{}). // for conversion webhook on spoke
		Complete()
}

// TODO(user): EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!

//+kubebuilder:webhook:path=/mutate-modelregistry-opendatahub-io-modelregistry,mutating=true,failurePolicy=fail,sideEffects=None,groups=modelregistry.opendatahub.io,resources=modelregistries,verbs=create;update,versions=v1alpha1;v1alpha2,name=mmodelregistry.opendatahub.io,admissionReviewVersions=v1

var (
	httpsPort int32 = DefaultHttpsPort
	httpPort  int32 = DefaultHttpPort
)

type GenericModelRegistry interface {
	Default()
	ValidateRegistry() (warnings admission.Warnings, err error)
	ValidateName(ctx context.Context, client client.Client) (warnings admission.Warnings, err error)
	GetName() string
}

func toModelRegistry(obj runtime.Object) (GenericModelRegistry, error) {
	r, ok := obj.(GenericModelRegistry)
	if !ok {
		return nil, errors.NewBadRequest(fmt.Sprintf("unsupported type %s", obj.GetObjectKind().GroupVersionKind().String()))
	}
	return r, nil
}

// TODO(user): change verbs to "verbs=create;update;delete" if you want to enable deletion validation.
//+kubebuilder:webhook:path=/validate-modelregistry-opendatahub-io-modelregistry,mutating=false,failurePolicy=fail,sideEffects=None,groups=modelregistry.opendatahub.io,resources=modelregistries,verbs=create;update,versions=v1alpha1;v1alpha2,name=vmodelregistry.opendatahub.io,admissionReviewVersions=v1

// NOTE: this type MUST not be exported, or kubebuilder thinks its a CRD and tried to generate code for it!!!
type modelRegistryDefaulter struct {
	client.Client
}

var _ webhook.CustomDefaulter = &modelRegistryDefaulter{}

// Default implements webhook.CustomDefaulter
func (v *modelRegistryDefaulter) Default(ctx context.Context, obj runtime.Object) error {
	r, err := toModelRegistry(obj)
	if err != nil {
		return err
	}
	modelregistrylog.Info("default", "name", r.GetName())
	return nil
}

type modelRegistryValidator struct {
	client.Client
}

var _ webhook.CustomValidator = &modelRegistryValidator{}

// ValidateCreate validates the object on creation. The optional warnings will be added to the response as warning messages. Return an error if the object is invalid.
func (v *modelRegistryValidator) ValidateCreate(ctx context.Context, obj runtime.Object) (warnings admission.Warnings, err error) {
	r, err := toModelRegistry(obj)
	if err != nil {
		return
	}
	modelregistrylog.Info("validate create", "name", r.GetName())

	// make sure registry name is unique in the cluster
	warnings, err = r.ValidateName(ctx, v.Client)
	if err != nil {
		return
	}

	return r.ValidateRegistry()
}

// ValidateUpdate validates the object on update. The optional warnings will be added to the response as warning messages. Return an error if the object is invalid.
func (v *modelRegistryValidator) ValidateUpdate(_ context.Context, _, newObj runtime.Object) (warnings admission.Warnings, err error) {
	r, err := toModelRegistry(newObj)
	if err != nil {
		return
	}
	modelregistrylog.Info("validate update", "name", r.GetName())

	return r.ValidateRegistry()
}

// ValidateDelete validates the object on deletion. The optional warnings will be added to the response as warning messages. Return an error if the object is invalid.
func (v *modelRegistryValidator) ValidateDelete(_ context.Context, obj runtime.Object) (warnings admission.Warnings, err error) {
	r, err := toModelRegistry(obj)
	if err != nil {
		return
	}
	modelregistrylog.Info("validate delete", "name", r.GetName())

	// TODO(user): fill in your validation logic upon object deletion.
	return nil, nil
}
