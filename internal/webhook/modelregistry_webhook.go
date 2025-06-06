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
	"github.com/go-logr/logr"
	"github.com/opendatahub-io/model-registry-operator/api/v1alpha1"
	"github.com/opendatahub-io/model-registry-operator/api/v1beta1"
	admissionv1 "k8s.io/api/admission/v1"
	"k8s.io/apimachinery/pkg/util/json"
	"net/http"
	ctrl "sigs.k8s.io/controller-runtime"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

func SetupWebhookWithManager(mgr ctrl.Manager) error {
	mgr.GetWebhookServer().Register("/mutate-modelregistry-opendatahub-io-modelregistry", &webhook.Admission{
		Handler: &modelRegistryDefaulter{
			Client:  mgr.GetClient(),
			Decoder: admission.NewDecoder(mgr.GetScheme()),
		},
	})
	mgr.GetWebhookServer().Register("/validate-modelregistry-opendatahub-io-modelregistry", &webhook.Admission{
		Handler: &modelRegistryValidator{
			Client:  mgr.GetClient(),
			Decoder: admission.NewDecoder(mgr.GetScheme()),
		},
	})
	return ctrl.NewWebhookManagedBy(mgr).
		For(&v1beta1.ModelRegistry{}). // for conversion webhook on spoke
		Complete()
}

// TODO(user): EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!

//+kubebuilder:webhook:path=/mutate-modelregistry-opendatahub-io-modelregistry,mutating=true,matchPolicy=Equivalent,failurePolicy=fail,sideEffects=None,groups=modelregistry.opendatahub.io,resources=modelregistries,verbs=create;update,versions=v1alpha1;v1beta1,name=mmodelregistry.opendatahub.io,admissionReviewVersions=v1beta1;v1alpha1

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

// NOTE: this type MUST not be exported, or kubebuilder thinks it's a CRD and tries to generate code for it!!!
type modelRegistryDefaulter struct {
	client.Client
	Decoder *admission.Decoder
}

var _ admission.Handler = &modelRegistryDefaulter{}

func (v *modelRegistryDefaulter) Handle(ctx context.Context, request admission.Request) admission.Response {
	obj, err := DecodeModelRegistry(v.Decoder, request)
	if err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}
	if err := v.Default(ctx, obj); err != nil {
		return admission.Errored(http.StatusInternalServerError, err)
	}
	marshalled, err := json.Marshal(obj)
	if err != nil {
		return admission.Errored(http.StatusInternalServerError, err)
	}

	// Create the patch
	return admission.PatchResponseFromRaw(request.Object.Raw, marshalled)
}

func (v *modelRegistryDefaulter) Default(ctx context.Context, obj runtime.Object) error {
	r, err := toModelRegistry(obj)
	if err != nil {
		return err
	}
	logr.FromContextOrDiscard(ctx).Info("default", "name", r.GetName())
	r.Default()
	return nil
}

// TODO(user): change verbs to "verbs=create;update;delete" if you want to enable deletion validation.
//+kubebuilder:webhook:path=/validate-modelregistry-opendatahub-io-modelregistry,mutating=false,matchPolicy=Equivalent,failurePolicy=fail,sideEffects=None,groups=modelregistry.opendatahub.io,resources=modelregistries,verbs=create;update,versions=v1alpha1;v1beta1,name=vmodelregistry.opendatahub.io,admissionReviewVersions=v1beta1;v1alpha1

type modelRegistryValidator struct {
	client.Client
	Decoder *admission.Decoder
}

func (v *modelRegistryValidator) Handle(ctx context.Context, request admission.Request) admission.Response {
	obj, err := DecodeModelRegistry(v.Decoder, request)
	if err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}

	var warnings admission.Warnings
	switch request.Operation {
	case admissionv1.Connect:
		// no validation
	case admissionv1.Create:
		warnings, err = v.ValidateCreate(ctx, obj)
	case admissionv1.Update:
		// NOTE: we are ignoring the raw old obj in request!
		warnings, err = v.ValidateUpdate(ctx, nil, obj)
	case admissionv1.Delete:
		warnings, err = v.ValidateDelete(ctx, obj)
	default:
		return admission.Errored(http.StatusBadRequest, fmt.Errorf("unknown operation %q", request.Operation))
	}

	// Check the error message first.
	if err != nil {
		return admission.Denied(err.Error()).WithWarnings(warnings...)
	}
	// Return allowed if everything succeeded.
	return admission.Allowed("").WithWarnings(warnings...)
}

func DecodeModelRegistry(decoder *admission.Decoder, request admission.Request) (runtime.Object, error) {
	var obj runtime.Object
	kind := request.Kind
	if kind.Group != v1beta1.GroupVersion.Group || kind.Kind != "ModelRegistry" {
		return obj, fmt.Errorf("unsupported resource %s in namespace %s of type %s",
			request.Name, request.Namespace, kind)
	}

	switch kind.Version {
	case v1beta1.GroupVersion.Version:
		obj = &v1beta1.ModelRegistry{}
		if err := decoder.Decode(request, obj); err != nil {
			return obj, err
		}
	case v1alpha1.GroupVersion.Version:
		obj = &v1alpha1.ModelRegistry{}
		if err := decoder.Decode(request, obj); err != nil {
			return obj, err
		}
	default:
		return obj, fmt.Errorf("unsupported resource %s in namespace %s of type %s",
			request.Name, request.Namespace, kind)
	}
	return obj, nil
}

var _ admission.Handler = &modelRegistryValidator{}

// ValidateCreate validates the object on creation. The optional warnings will be added to the response as warning messages. Return an error if the object is invalid.
func (v *modelRegistryValidator) ValidateCreate(ctx context.Context, obj runtime.Object) (warnings admission.Warnings, err error) {
	r, err := toModelRegistry(obj)
	if err != nil {
		return
	}
	logr.FromContextOrDiscard(ctx).Info("validate create", "name", r.GetName())

	// make sure registry name is unique in the cluster
	warnings, err = r.ValidateName(ctx, v.Client)
	if err != nil {
		return
	}

	return r.ValidateRegistry()
}

// ValidateUpdate validates the object on update. The optional warnings will be added to the response as warning messages. Return an error if the object is invalid.
func (v *modelRegistryValidator) ValidateUpdate(ctx context.Context, _, newObj runtime.Object) (warnings admission.Warnings, err error) {
	r, err := toModelRegistry(newObj)
	if err != nil {
		return
	}
	logr.FromContextOrDiscard(ctx).Info("validate update", "name", r.GetName())

	return r.ValidateRegistry()
}

// ValidateDelete validates the object on deletion. The optional warnings will be added to the response as warning messages. Return an error if the object is invalid.
func (v *modelRegistryValidator) ValidateDelete(ctx context.Context, obj runtime.Object) (warnings admission.Warnings, err error) {
	r, err := toModelRegistry(obj)
	if err != nil {
		return
	}
	logr.FromContextOrDiscard(ctx).Info("validate delete", "name", r.GetName())

	// TODO(user): fill in your validation logic upon object deletion.
	return nil, nil
}
