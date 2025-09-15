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
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func Test_readyGetHandler_ReadyTrue(t *testing.T) {
	ready.Store(true) // simulate ready state

	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	rr := httptest.NewRecorder()

	readyGetHandler(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200 OK, got %d", rr.Code)
	}

	body, _ := io.ReadAll(rr.Body)
	if !bytes.Contains(body, []byte("OK")) {
		t.Errorf("expected body to contain 'OK', got %s", string(body))
	}
}

func Test_readyGetHandler_ReadyFalse(t *testing.T) {
	ready.Store(false) // simulate not ready

	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	rr := httptest.NewRecorder()

	readyGetHandler(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 Service Unavailable, got %d", rr.Code)
	}

	body, _ := io.ReadAll(rr.Body)
	if !bytes.Contains(body, []byte("Service Unavailable")) {
		t.Errorf("expected body to contain 'Service Unavailable', got %s", string(body))
	}
}

func Test_watchForIPs_ReceiveValidIP(t *testing.T) {
	ready.Store(false) // reset state

	ipCh := make(chan string, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go watchForIPs(ctx, ipCh)

	ipCh <- "192.168.1.100"

	time.Sleep(50 * time.Millisecond) // give goroutine time to process

	if !ready.Load() {
		t.Errorf("expected ready = true after receiving IP")
	}
}

func Test_watchForIPs_ReceiveEmptyIP(t *testing.T) {
	ready.Store(true) // simulate ready state

	ipCh := make(chan string, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go watchForIPs(ctx, ipCh)

	ipCh <- ""

	time.Sleep(50 * time.Millisecond)

	if ready.Load() {
		t.Errorf("expected ready = false after receiving empty IP")
	}
}

func Test_watchForIPs_ContextCancel(t *testing.T) {
	// This test ensures the goroutine exits cleanly when context is canceled
	ipCh := make(chan string)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})

	go func() {
		watchForIPs(ctx, ipCh)
		close(done)
	}()

	cancel()

	select {
	case <-done:
		// Success: goroutine exited
	case <-time.After(1 * time.Second):
		t.Errorf("watchForIPs did not exit after context cancellation")
	}
}
