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

package setup

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/go-logr/logr"
	oapiconfig "github.com/openshift/api/config/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestClassifyAdherenceFetchError(t *testing.T) {
	apiServerGR := schema.GroupResource{Group: "config.openshift.io", Resource: "apiservers"}

	tests := map[string]struct {
		err            error
		wantFailClosed bool
	}{
		"not found": {
			err:            apierrors.NewNotFound(apiServerGR, "cluster"),
			wantFailClosed: false,
		},
		"no resource match": {
			err: &apimeta.NoResourceMatchError{
				PartialResource: schema.GroupVersionResource{Group: "config.openshift.io", Resource: "apiservers"},
			},
			wantFailClosed: false,
		},
		"service unavailable": {
			err:            apierrors.NewServiceUnavailable("down for maintenance"),
			wantFailClosed: false,
		},
		"timeout": {
			err:            apierrors.NewTimeoutError("timed out", 1),
			wantFailClosed: false,
		},
		"server timeout": {
			err:            apierrors.NewServerTimeout(apiServerGR, "get", 1),
			wantFailClosed: false,
		},
		"too many requests": {
			err:            apierrors.NewTooManyRequests("slow down", 1),
			wantFailClosed: false,
		},
		"context deadline exceeded": {
			err:            context.DeadlineExceeded,
			wantFailClosed: false,
		},
		"forbidden": {
			err:            apierrors.NewForbidden(apiServerGR, "cluster", errors.New("no access")),
			wantFailClosed: true,
		},
		"unexpected error": {
			err:            errors.New("boom"),
			wantFailClosed: true,
		},
		"wrapped forbidden": {
			err: fmt.Errorf("failed to get APIServer %q: %w", "cluster",
				apierrors.NewForbidden(apiServerGR, "cluster", errors.New("no access"))),
			wantFailClosed: true,
		},
		"wrapped not found": {
			err: fmt.Errorf("failed to get APIServer %q: %w", "cluster",
				apierrors.NewNotFound(apiServerGR, "cluster")),
			wantFailClosed: false,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			failClosed, reason := classifyAdherenceFetchError(tt.err)
			if failClosed != tt.wantFailClosed {
				t.Errorf("classifyAdherenceFetchError(%v) failClosed = %v, want %v", tt.err, failClosed, tt.wantFailClosed)
			}
			if reason == "" {
				t.Errorf("classifyAdherenceFetchError(%v) returned empty reason", tt.err)
			}
		})
	}
}

func TestResolveAdherencePolicy(t *testing.T) {
	apiServerGR := schema.GroupResource{Group: "config.openshift.io", Resource: "apiservers"}

	tests := map[string]struct {
		policy      oapiconfig.TLSAdherencePolicy
		err         error
		wantPolicy  oapiconfig.TLSAdherencePolicy
		wantFetched bool
		wantErr     bool
	}{
		"success": {
			policy:      oapiconfig.TLSAdherencePolicyStrictAllComponents,
			err:         nil,
			wantPolicy:  oapiconfig.TLSAdherencePolicyStrictAllComponents,
			wantFetched: true,
			wantErr:     false,
		},
		"not found": {
			err:         apierrors.NewNotFound(apiServerGR, "cluster"),
			wantPolicy:  oapiconfig.TLSAdherencePolicyNoOpinion,
			wantFetched: true,
			wantErr:     false,
		},
		"no resource match": {
			err: &apimeta.NoResourceMatchError{
				PartialResource: schema.GroupVersionResource{Group: "config.openshift.io", Resource: "apiservers"},
			},
			wantPolicy:  oapiconfig.TLSAdherencePolicyNoOpinion,
			wantFetched: true,
			wantErr:     false,
		},
		"transient": {
			err:         apierrors.NewServiceUnavailable("down for maintenance"),
			wantPolicy:  oapiconfig.TLSAdherencePolicyNoOpinion,
			wantFetched: true,
			wantErr:     false,
		},
		"forbidden": {
			err:         apierrors.NewForbidden(apiServerGR, "cluster", errors.New("no access")),
			wantPolicy:  oapiconfig.TLSAdherencePolicyNoOpinion,
			wantFetched: false,
			wantErr:     true,
		},
		"unexpected error": {
			err:         errors.New("boom"),
			wantPolicy:  oapiconfig.TLSAdherencePolicyNoOpinion,
			wantFetched: false,
			wantErr:     true,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			policy, fetched, err := resolveAdherencePolicy(tt.policy, tt.err, logr.Discard())
			if policy != tt.wantPolicy {
				t.Errorf("resolveAdherencePolicy(%v) policy = %v, want %v", tt.err, policy, tt.wantPolicy)
			}
			if fetched != tt.wantFetched {
				t.Errorf("resolveAdherencePolicy(%v) fetched = %v, want %v", tt.err, fetched, tt.wantFetched)
			}
			if (err != nil) != tt.wantErr {
				t.Errorf("resolveAdherencePolicy(%v) err = %v, wantErr %v", tt.err, err, tt.wantErr)
			}
		})
	}
}
