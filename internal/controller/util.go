package controller

import (
	"context"
	"fmt"
	"strings"
	"text/template"

	"github.com/banzaicloud/k8s-objectmatcher/patch"
	"k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/controller-runtime/pkg/client"
	klog "sigs.k8s.io/controller-runtime/pkg/log"
)

type TemplateApplier struct {
	Template    *template.Template
	IsOpenShift bool
}

type BaseParams struct {
	Name      string
	Namespace string
}

func (t *TemplateApplier) Apply(params any, templateName string, object any) error {
	builder := strings.Builder{}
	err := t.Template.ExecuteTemplate(&builder, templateName, params)
	if err != nil {
		return fmt.Errorf("error executing template %s: %w", templateName, err)
	}
	err = yaml.UnmarshalStrict([]byte(builder.String()), object)
	if err != nil {
		return fmt.Errorf("error creating %T from template %s: %w", object, templateName, err)
	}
	return nil
}

type ResourceManager struct {
	Client client.Client
}

func (r *ResourceManager) CreateOrUpdate(ctx context.Context, currObj client.Object, newObj client.Object) (OperationResult, error) {
	log := klog.FromContext(ctx)
	result := ResourceUnchanged

	key := client.ObjectKeyFromObject(newObj)
	gvk := newObj.GetObjectKind().GroupVersionKind()
	name := newObj.GetName()

	if err := r.Client.Get(ctx, key, currObj); err != nil {
		if client.IgnoreNotFound(err) == nil {
			result = ResourceCreated
			log.Info("creating", "kind", gvk, "name", name)
			if err := patch.DefaultAnnotator.SetLastAppliedAnnotation(newObj); err != nil {
				return result, err
			}
			return result, r.Client.Create(ctx, newObj)
		}
		return result, err
	}

	// Avoid diff on metadata.resourceVersion alone
	if rv := currObj.GetResourceVersion(); rv != "" {
		newObj.SetResourceVersion(rv)
	}

	patchResult, err := patch.DefaultPatchMaker.Calculate(currObj, newObj, patch.IgnoreStatusFields(),
		patch.IgnoreField("apiVersion"), patch.IgnoreField("kind"))
	if err != nil {
		return result, err
	}
	if !patchResult.IsEmpty() {
		result = ResourceUpdated
		log.Info("updating", "kind", gvk, "name", name)
		if err := patch.DefaultAnnotator.SetLastAppliedAnnotation(newObj); err != nil {
			return result, err
		}
		return result, r.Client.Update(ctx, newObj)
	}

	return result, nil
}

func (r *ResourceManager) CreateIfNotExists(ctx context.Context, currObj client.Object, newObj client.Object) (OperationResult, error) {
	log := klog.FromContext(ctx)
	result := ResourceUnchanged

	key := client.ObjectKeyFromObject(newObj)
	gvk := newObj.GetObjectKind().GroupVersionKind()
	name := newObj.GetName()

	if err := r.Client.Get(ctx, key, currObj); err != nil {
		if client.IgnoreNotFound(err) == nil {
			result = ResourceCreated
			log.Info("creating", "kind", gvk, "name", name)
			if err := patch.DefaultAnnotator.SetLastAppliedAnnotation(newObj); err != nil {
				return result, err
			}
			return result, r.Client.Create(ctx, newObj)
		}
		return result, err
	}
	return result, nil
}
