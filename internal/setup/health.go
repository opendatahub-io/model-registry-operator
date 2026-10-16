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
	"context"
	"net"
	"net/http"
	"sync/atomic"
	"time"
)

// HealthServer serves the /healthz and /readyz probe endpoints for the whole
// process lifetime, independently of the controller-runtime manager. Binding
// the port before any startup API call keeps the kubelet liveness probe
// passing while startup blocks on capabilities detection or TLS
// configuration, each bounded by a timeout.
//
// The manager must be created with HealthProbeBindAddress "0" so it does not
// try to bind the same port; readiness is wired to the manager by flipping
// SetReady from a manager runnable once the caches have synced.
type HealthServer struct {
	srv   *http.Server
	ln    net.Listener
	ready atomic.Bool
}

// NewHealthServer binds addr immediately. An empty addr or "0" returns a
// disabled server whose methods are safe no-ops, matching the manager's
// HealthProbeBindAddress semantics.
func NewHealthServer(addr string) (*HealthServer, error) {
	if addr == "" || addr == "0" {
		return &HealthServer{}, nil
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	h := &HealthServer{ln: ln}
	mux := http.NewServeMux()
	// Liveness means the process is alive; it must not depend on API-server
	// reachability, otherwise a partition makes the kubelet kill the pod.
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	// Readiness means the manager is serving; flipped via SetReady.
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		if h.ready.Load() {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	h.srv = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      5 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	return h, nil
}

// Start serves the probe endpoints until Close is called, then returns
// http.ErrServerClosed. On a disabled server it returns nil immediately.
func (h *HealthServer) Start() error {
	if h.srv == nil {
		return nil
	}
	return h.srv.Serve(h.ln)
}

// SetReady flips the /readyz response between 200 and 503.
func (h *HealthServer) SetReady(ready bool) {
	h.ready.Store(ready)
}

// Addr returns the bound listener address, or "" on a disabled server.
func (h *HealthServer) Addr() string {
	if h.ln == nil {
		return ""
	}
	return h.ln.Addr().String()
}

// Close marks the server not ready and stops serving. Best effort: callers
// invoke it on the way out of the process.
func (h *HealthServer) Close() {
	if h.srv == nil {
		return
	}
	h.ready.Store(false)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = h.srv.Shutdown(ctx)
}
