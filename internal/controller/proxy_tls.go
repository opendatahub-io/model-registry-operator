package controller

import (
	"context"
	"reflect"

	catalogv1alpha1 "github.com/opendatahub-io/model-registry-operator/api/catalog/v1alpha1"
	"github.com/opendatahub-io/model-registry-operator/api/v1beta1"
	configv1 "github.com/openshift/api/config/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// Only the singleton's TLS fields affect operand TLS; status/metadata updates
// must not enqueue every workload. Creation and deletion also change fallback.
func proxyTLSChangedPredicate() predicate.Predicate {
	isCluster := func(obj client.Object) bool { return obj != nil && obj.GetName() == "cluster" }
	return predicate.Funcs{
		CreateFunc:  func(e event.CreateEvent) bool { return isCluster(e.Object) },
		DeleteFunc:  func(e event.DeleteEvent) bool { return isCluster(e.Object) },
		GenericFunc: func(event.GenericEvent) bool { return false },
		UpdateFunc: func(e event.UpdateEvent) bool {
			before, okBefore := e.ObjectOld.(*configv1.APIServer)
			after, okAfter := e.ObjectNew.(*configv1.APIServer)
			return okBefore && okAfter && isCluster(after) &&
				(before.Spec.TLSAdherence != after.Spec.TLSAdherence || !reflect.DeepEqual(before.Spec.TLSSecurityProfile, after.Spec.TLSSecurityProfile))
		},
	}
}

func (r *ModelRegistryReconciler) registriesForTLSChange(ctx context.Context, _ client.Object) []reconcile.Request {
	list := &v1beta1.ModelRegistryList{}
	if err := r.List(ctx, list); err != nil {
		log.FromContext(ctx).Error(err, "unable to enqueue registries for TLS policy change")
		return nil
	}
	requests := make([]reconcile.Request, 0, len(list.Items))
	for i := range list.Items {
		requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&list.Items[i])})
	}
	return requests
}

func (r *CatalogReconciler) catalogsForTLSChange(ctx context.Context, _ client.Object) []reconcile.Request {
	list := &catalogv1alpha1.CatalogList{}
	if err := r.List(ctx, list); err != nil {
		log.FromContext(ctx).Error(err, "unable to enqueue catalogs for TLS policy change")
		return nil
	}
	requests := make([]reconcile.Request, 0, len(list.Items))
	for i := range list.Items {
		requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&list.Items[i])})
	}
	return requests
}
