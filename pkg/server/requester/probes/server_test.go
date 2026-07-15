/*
Copyright 2025 The llm-d Authors.

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

package probes

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	stubapi "github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/spi"
)

func FuzzServer(f *testing.F) {
	f.Add(true)
	f.Add(false)
	var ready atomic.Bool
	ctx, cancel := context.WithCancel(f.Context())
	port := "28082"
	serve, err := Start(ctx, port, &ready)
	if err != nil {
		f.Fatalf("Server start failed: %s", err.Error())
	}
	go func() {
		if err := serve(); err != nil {
			f.Logf("Serve failed: %s", err.Error())
		}
	}()
	f.Fuzz(func(t *testing.T, beReady bool) {
		ready.Store(beReady)
		resp, err := http.Get("http://localhost:" + port + stubapi.ReadyPath)
		expectedStatus := http.StatusOK
		if !beReady {
			expectedStatus = http.StatusServiceUnavailable
		}
		if err != nil {
			t.Fatalf("Failed to query readiness: %s", err.Error())
		} else if resp.StatusCode != expectedStatus {
			t.Fatalf("Expected %d, got %d", expectedStatus, resp.StatusCode)
		} else {
			t.Logf("Successful test of ready=%v", beReady)
		}
	})
	cancel()
}

// TestShutdownBeforeServe covers the case where the context is canceled after
// Start binds the listener but before the returned serve func is invoked. That
// is a clean shutdown, so serve must return nil.
func TestShutdownBeforeServe(t *testing.T) {
	var ready atomic.Bool
	ctx, cancel := context.WithCancel(t.Context())
	port := "28087"
	serve, err := Start(ctx, port, &ready)
	if err != nil {
		t.Fatalf("Server start failed: %s", err.Error())
	}
	// Cancel before serving, then wait for the shutdown goroutine to run so that
	// serve deterministically observes the already-shut-down server.
	cancel()
	errCh := make(chan error, 1)
	go func() {
		// Give the shutdown goroutine time to run Shutdown and close the listener.
		time.Sleep(5 * time.Second)
		errCh <- serve()
	}()
	timer := time.NewTimer(20 * time.Second)
	defer timer.Stop()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("serve returned non-nil on clean shutdown-before-serve: %s", err.Error())
		}
		t.Log("serve returned nil on shutdown-before-serve, as expected")
	case <-timer.C:
		t.Fatalf("serve did not return in time")
	}
}
