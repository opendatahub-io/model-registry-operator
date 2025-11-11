/*
Copyright 2025.

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
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
)

var _ = Describe("ClusterCapabilities Detection", func() {
	Describe("DetectClusterCapabilities", func() {
		Context("Traditional OpenShift cluster", func() {
			It("should detect both OpenShift and user API", func() {
				mockDiscovery := &mockDiscoveryClient{
					groups: &metav1.APIGroupList{
						Groups: []metav1.APIGroup{
							{Name: "route.openshift.io"},
							{Name: "user.openshift.io"},
							{Name: "config.openshift.io"},
						},
					},
					err: nil,
				}

				caps, err := DetectClusterCapabilities(mockDiscovery)

				Expect(err).NotTo(HaveOccurred())
				Expect(caps.IsOpenShift).To(BeTrue())
				Expect(caps.HasUserAPI).To(BeTrue())
				Expect(caps.HasConfigAPI).To(BeTrue())
			})
		})

		Context("BYOIDC OpenShift cluster", func() {
			It("should detect OpenShift but not user API", func() {
				// Simulate partial discovery failure - ServerGroups returns both groups AND error
				// The groups contain successfully discovered APIs, error indicates some failed
				mockDiscovery := &mockDiscoveryClient{
					groups: &metav1.APIGroupList{
						Groups: []metav1.APIGroup{
							{Name: "route.openshift.io"},
							{Name: "config.openshift.io"},
						},
					},
					err: &discovery.ErrGroupDiscoveryFailed{
						Groups: map[schema.GroupVersion]error{
							{Group: "user.openshift.io", Version: "v1"}: fmt.Errorf("the server could not find the requested resource"),
						},
					},
				}

				caps, err := DetectClusterCapabilities(mockDiscovery)

				Expect(err).NotTo(HaveOccurred())
				Expect(caps.IsOpenShift).To(BeTrue())
				Expect(caps.HasUserAPI).To(BeFalse())
				Expect(caps.HasConfigAPI).To(BeTrue())
			})
		})

		Context("Plain Kubernetes cluster", func() {
			It("should not detect OpenShift", func() {
				mockDiscovery := &mockDiscoveryClient{
					groups: &metav1.APIGroupList{
						Groups: []metav1.APIGroup{
							{Name: "apps"},
							{Name: "batch"},
							{Name: "networking.k8s.io"},
						},
					},
					err: nil,
				}

				caps, err := DetectClusterCapabilities(mockDiscovery)

				Expect(err).NotTo(HaveOccurred())
				Expect(caps.IsOpenShift).To(BeFalse())
				Expect(caps.HasUserAPI).To(BeFalse())
				Expect(caps.HasConfigAPI).To(BeFalse())
			})
		})

		Context("Edge case: user API without route API", func() {
			It("should detect user API but not OpenShift", func() {
				mockDiscovery := &mockDiscoveryClient{
					groups: &metav1.APIGroupList{
						Groups: []metav1.APIGroup{
							{Name: "user.openshift.io"},
							{Name: "apps"},
						},
					},
					err: nil,
				}

				caps, err := DetectClusterCapabilities(mockDiscovery)

				Expect(err).NotTo(HaveOccurred())
				Expect(caps.IsOpenShift).To(BeFalse())
				Expect(caps.HasUserAPI).To(BeTrue())
			})
		})

		Context("Complete discovery failure", func() {
			It("should return error", func() {
				mockDiscovery := &mockDiscoveryClient{
					groups: nil,
					err:    &discovery.ErrGroupDiscoveryFailed{},
				}

				caps, err := DetectClusterCapabilities(mockDiscovery)

				// When there are NO partial results, it should fail
				Expect(err).To(HaveOccurred())
				Expect(caps).To(Equal(ClusterCapabilities{}))
			})
		})

		Context("Transient user.openshift.io failure (not BYOIDC)", func() {
			It("should fail-fast on non-'not found' errors", func() {
				// Simulate a transient network/RBAC error for user.openshift.io
				mockDiscovery := &mockDiscoveryClient{
					groups: &metav1.APIGroupList{
						Groups: []metav1.APIGroup{
							{Name: "route.openshift.io"},
							{Name: "config.openshift.io"},
						},
					},
					err: &discovery.ErrGroupDiscoveryFailed{
						Groups: map[schema.GroupVersion]error{
							{Group: "user.openshift.io", Version: "v1"}: fmt.Errorf("connection timeout"),
						},
					},
				}

				caps, err := DetectClusterCapabilities(mockDiscovery)

				// Should fail because this is NOT a BYOIDC "not found" error
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("user.openshift.io discovery failed"))
				Expect(err.Error()).To(ContainSubstring("not BYOIDC"))
				Expect(caps).To(Equal(ClusterCapabilities{}))
			})
		})

		Context("Critical OpenShift API failure", func() {
			It("should fail-fast if route.openshift.io fails", func() {
				mockDiscovery := &mockDiscoveryClient{
					groups: &metav1.APIGroupList{
						Groups: []metav1.APIGroup{
							{Name: "config.openshift.io"},
						},
					},
					err: &discovery.ErrGroupDiscoveryFailed{
						Groups: map[schema.GroupVersion]error{
							{Group: "route.openshift.io", Version: "v1"}: fmt.Errorf("connection refused"),
						},
					},
				}

				caps, err := DetectClusterCapabilities(mockDiscovery)

				// Should fail because route.openshift.io is critical
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("critical OpenShift API"))
				Expect(err.Error()).To(ContainSubstring("route.openshift.io"))
				Expect(caps).To(Equal(ClusterCapabilities{}))
			})

			It("should fail-fast if config.openshift.io fails", func() {
				mockDiscovery := &mockDiscoveryClient{
					groups: &metav1.APIGroupList{
						Groups: []metav1.APIGroup{
							{Name: "route.openshift.io"},
						},
					},
					err: &discovery.ErrGroupDiscoveryFailed{
						Groups: map[schema.GroupVersion]error{
							{Group: "config.openshift.io", Version: "v1"}: fmt.Errorf("RBAC error"),
						},
					},
				}

				caps, err := DetectClusterCapabilities(mockDiscovery)

				// Should fail because config.openshift.io is critical
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("critical OpenShift API"))
				Expect(err.Error()).To(ContainSubstring("config.openshift.io"))
				Expect(caps).To(Equal(ClusterCapabilities{}))
			})
		})
	})
})

// mockDiscoveryClient implements only the ServerGroups method needed for testing
type mockDiscoveryClient struct {
	discovery.DiscoveryInterface // Embed to satisfy interface
	groups                       *metav1.APIGroupList
	err                          error
}

func (m *mockDiscoveryClient) ServerGroups() (*metav1.APIGroupList, error) {
	return m.groups, m.err
}
