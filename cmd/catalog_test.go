package cmd

import (
	"testing"

	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/cache"
)

func TestCatalogCacheOptionsScopesEndpointSlices(t *testing.T) {
	const registriesNamespace = "catalog-namespace"

	cacheOptions := catalogCacheOptions(registriesNamespace)
	var endpointSliceOptions cache.ByObject
	found := false
	for object, options := range cacheOptions.ByObject {
		if _, ok := object.(*discoveryv1.EndpointSlice); ok {
			endpointSliceOptions = options
			found = true
			break
		}
	}
	if !found {
		t.Fatal("EndpointSlice cache configuration not found")
	}

	if len(endpointSliceOptions.Namespaces) != 1 {
		t.Fatalf("EndpointSlice cache namespaces = %v, want only %q", endpointSliceOptions.Namespaces, registriesNamespace)
	}
	if _, ok := endpointSliceOptions.Namespaces[registriesNamespace]; !ok {
		t.Fatalf("EndpointSlice cache does not include namespace %q", registriesNamespace)
	}
	if endpointSliceOptions.Label == nil {
		t.Fatal("EndpointSlice cache label selector is nil")
	}
	if !endpointSliceOptions.Label.Matches(labels.Set{discoveryv1.LabelServiceName: catalogServiceName}) {
		t.Fatalf("EndpointSlice cache selector does not match service %q", catalogServiceName)
	}
	if endpointSliceOptions.Label.Matches(labels.Set{discoveryv1.LabelServiceName: "another-service"}) {
		t.Fatal("EndpointSlice cache selector matches a different service")
	}
}
