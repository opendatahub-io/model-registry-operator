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

package setup

import (
	"errors"
	"net/http"
	"testing"
	"time"
)

func TestHealthServerProbes(t *testing.T) {
	h, err := NewHealthServer("127.0.0.1:0")
	if err != nil {
		t.Fatalf("NewHealthServer() error = %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- h.Start() }()

	client := &http.Client{Timeout: 2 * time.Second}
	base := "http://" + h.Addr()

	get := func(path string) int {
		t.Helper()
		resp, err := client.Get(base + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}

	if code := get("/healthz"); code != http.StatusOK {
		t.Fatalf("GET /healthz before ready = %d, want %d", code, http.StatusOK)
	}
	if code := get("/readyz"); code != http.StatusServiceUnavailable {
		t.Fatalf("GET /readyz before ready = %d, want %d", code, http.StatusServiceUnavailable)
	}

	h.SetReady(true)
	if code := get("/readyz"); code != http.StatusOK {
		t.Fatalf("GET /readyz after SetReady(true) = %d, want %d", code, http.StatusOK)
	}

	h.SetReady(false)
	if code := get("/readyz"); code != http.StatusServiceUnavailable {
		t.Fatalf("GET /readyz after SetReady(false) = %d, want %d", code, http.StatusServiceUnavailable)
	}

	h.Close()
	select {
	case err := <-done:
		if !errors.Is(err, http.ErrServerClosed) {
			t.Fatalf("Start() after Close() = %v, want http.ErrServerClosed", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Start() did not return after Close()")
	}
}

func TestHealthServerDisabled(t *testing.T) {
	for _, addr := range []string{"", "0"} {
		h, err := NewHealthServer(addr)
		if err != nil {
			t.Fatalf("NewHealthServer(%q) error = %v", addr, err)
		}
		if err := h.Start(); err != nil {
			t.Fatalf("Start() on disabled server = %v, want nil", err)
		}
		if h.Addr() != "" {
			t.Fatalf("Addr() on disabled server = %q, want empty", h.Addr())
		}
		h.SetReady(true) // must be a safe no-op
		h.Close()        // must be a safe no-op
	}
}
