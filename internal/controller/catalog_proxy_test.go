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
	catalogv1alpha1 "github.com/opendatahub-io/model-registry-operator/api/catalog/v1alpha1"
	"github.com/opendatahub-io/model-registry-operator/internal/controller/config"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	configv1 "github.com/openshift/api/config/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apiruntime "k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// These tests use a standalone fake client (not the shared envtest k8sClient)
// because they need the OpenShift config.openshift.io/v1 Proxy type registered,
// which the controller suite's scheme does not otherwise require.
var _ = Describe("catalogProxyOrDefault", func() {
	It("returns the CR's proxy unmodified when set", func() {
		config.SetDefaultDomain("", nil, true)
		defer config.SetDefaultDomain("", nil, false)
		proxy := &catalogv1alpha1.ProxyConfig{HTTPProxy: "http://cr-proxy:3128"}

		Expect(catalogProxyOrDefault(proxy)).To(BeIdenticalTo(proxy))
	})

	It("treats an explicit empty ProxyConfig as opt-out, not falling back to the cluster", func() {
		config.SetDefaultDomain("", nil, true)
		defer config.SetDefaultDomain("", nil, false)
		empty := &catalogv1alpha1.ProxyConfig{}

		Expect(catalogProxyOrDefault(empty)).To(BeIdenticalTo(empty))
	})

	It("returns nil when unset and not on OpenShift", func() {
		config.SetDefaultDomain("", nil, false)
		defer config.SetDefaultDomain("", nil, false)

		Expect(catalogProxyOrDefault(nil)).To(BeNil())
	})

	It("falls back to the cluster Proxy when unset and on OpenShift", func() {
		scheme := apiruntime.NewScheme()
		Expect(configv1.AddToScheme(scheme)).To(Succeed())
		clusterProxy := &configv1.Proxy{
			ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
			Spec: configv1.ProxySpec{
				HTTPProxy: "http://cluster-proxy:3128",
				NoProxy:   "cluster-noproxy",
			},
		}
		fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(clusterProxy).Build()
		config.SetDefaultDomain("", fakeClient, true)
		defer config.SetDefaultDomain("", nil, false)

		result := catalogProxyOrDefault(nil)
		Expect(result).NotTo(BeNil())
		Expect(result.HTTPProxy).To(Equal("http://cluster-proxy:3128"))
		Expect(result.HTTPSProxy).To(BeEmpty())
		Expect(result.NoProxy).To(Equal("cluster-noproxy"))
	})

	It("returns nil when unset, on OpenShift, but no cluster Proxy exists", func() {
		scheme := apiruntime.NewScheme()
		Expect(configv1.AddToScheme(scheme)).To(Succeed())
		fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
		config.SetDefaultDomain("", fakeClient, true)
		defer config.SetDefaultDomain("", nil, false)

		Expect(catalogProxyOrDefault(nil)).To(BeNil())
	})
})
