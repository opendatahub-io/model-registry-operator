/*
Copyright 2026.

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

package cmd

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/opendatahub-io/model-registry-operator/internal/setup"
)

// Readiness must not require leader election: a replacement pod can never
// acquire the lease while the old leader still runs, so leader-gated readiness
// deadlocks rolling updates (new pod never ready, old pod never terminates).
func TestReadinessRunnableNotLeaderGated(t *testing.T) {
	healthSrv, err := setup.NewHealthServer("127.0.0.1:0")
	if err != nil {
		t.Fatalf("NewHealthServer() error = %v", err)
	}
	go func() {
		_ = healthSrv.Start()
	}()
	runnable := readinessRunnable{healthSrv: healthSrv}

	if runnable.NeedLeaderElection() {
		t.Fatal("readinessRunnable must not require leader election")
	}

	readyz := func() int {
		t.Helper()
		resp, err := http.Get("http://" + healthSrv.Addr() + "/readyz")
		if err != nil {
			t.Fatalf("GET /readyz: %v", err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		if err := runnable.Start(ctx); err != nil {
			t.Errorf("Start() error = %v", err)
		}
		close(done)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for readyz() != http.StatusOK {
		if time.Now().After(deadline) {
			t.Fatal("Start() must flip /readyz to 200")
		}
		time.Sleep(5 * time.Millisecond)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Start() did not return after context cancel")
	}
	if code := readyz(); code != http.StatusServiceUnavailable {
		t.Fatalf("GET /readyz after cancel = %d, want %d", code, http.StatusServiceUnavailable)
	}
}
