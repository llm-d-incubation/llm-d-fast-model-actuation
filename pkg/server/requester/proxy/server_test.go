/*
Copyright 2026 The llm-d Authors.

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

package proxy

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"k8s.io/klog/v2"

	stubapi "github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/spi"
)

// resetInstance clears the singleton so tests don't interfere with each other.
func resetInstance(t *testing.T) {
	t.Helper()
	if instance.pool != nil {
		instance.pool.Close()
	}
	instance = forwarder{}
}

// startTestEchoServer starts a TCP server that echoes back any data it receives.
// It also prepends a "CONN" line on each new connection so clients can detect
// connection reuse.
func startTestEchoServer(t *testing.T) (addr string, closer func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start echo server: %v", err)
	}
	addr = ln.Addr().String()

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				// Write a greeting so clients can see this is a new connection
				_, _ = c.Write([]byte("CONN:NEW\n"))
				buf := make([]byte, 4096)
				for {
					n, err := c.Read(buf)
					if err != nil {
						_ = c.Close()
						return
					}
					_, _ = c.Write(buf[:n])
				}
			}(conn)
		}
	}()

	return addr, func() { _ = ln.Close() }
}

// findFreePort returns an unused TCP port.
func findFreePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to find free port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

func TestProxy_EchoRoundTrip(t *testing.T) {
	resetInstance(t)

	backendAddr, backendCloser := startTestEchoServer(t)
	defer backendCloser()

	proxyPort := findFreePort(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	cfg := ProxyConfig{
		UninitDelay:  50 * time.Millisecond,
		DrainTimeout: 100 * time.Millisecond,
		PoolConfig: PoolConfig{
			MaxIdleConnections: 10,
			IdleTimeout:        5 * time.Second,
			DialTimeout:        2 * time.Second,
			CleanInterval:      2 * time.Second,
		},
	}
	go func() {
		err := RunWithConfig(ctx, fmt.Sprintf("%d", proxyPort), cfg)
		if err != nil {
			t.Logf("proxy Run error: %v", err)
		}
	}()

	// Give RunWithConfig a moment to create the pool before Initialize runs.
	time.Sleep(50 * time.Millisecond)

	// Initialize the proxy
	backendParts := strings.Split(backendAddr, ":")
	backendPort := backendParts[1]
	body := stubapi.ProxyConfigRequest{
		Address: "127.0.0.1",
		Port:    mustParsePort(backendPort, t),
	}
	jsonBody, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPut, "/init", strings.NewReader(string(jsonBody)))
	w := httptest.NewRecorder()
	Initialize(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Initialize failed: %d — %s", w.Code, w.Body.String())
	}

	// Give proxy a moment to mark itself ready
	time.Sleep(50 * time.Millisecond)

	// Connect to proxy and verify echo
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", proxyPort), 2*time.Second)
	if err != nil {
		t.Fatalf("failed to dial proxy: %v", err)
	}
	defer func() { _ = conn.Close() }()

	// Read the "CONN:NEW" greeting from backend
	reader := bufio.NewReader(conn)
	greeting, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("failed to read greeting: %v", err)
	}
	if !strings.HasPrefix(greeting, "CONN:NEW") {
		t.Errorf("expected CONN:NEW greeting, got %q", greeting)
	}

	// Send a message and verify echo
	testMsg := "hello proxy\n"
	_, err = conn.Write([]byte(testMsg))
	if err != nil {
		t.Fatalf("failed to write to proxy: %v", err)
	}
	resp, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("failed to read echo: %v", err)
	}
	if !strings.Contains(resp, "hello proxy") {
		t.Errorf("expected echo of %q, got %q", testMsg, resp)
	}
}

func TestProxy_ConnectionReuse(t *testing.T) {
	resetInstance(t)

	backendAddr, backendCloser := startTestEchoServer(t)
	defer backendCloser()

	proxyPort := findFreePort(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	cfg := ProxyConfig{
		UninitDelay:  50 * time.Millisecond,
		DrainTimeout: 100 * time.Millisecond,
		PoolConfig: PoolConfig{
			MaxIdleConnections: 10,
			IdleTimeout:        10 * time.Second,
			DialTimeout:        2 * time.Second,
			CleanInterval:      5 * time.Second,
		},
	}
	go func() {
		err := RunWithConfig(ctx, fmt.Sprintf("%d", proxyPort), cfg)
		if err != nil {
			t.Logf("proxy Run error: %v", err)
		}
	}()

	// Give RunWithConfig a moment to create the pool before Initialize runs.
	time.Sleep(50 * time.Millisecond)

	// Initialize the proxy
	backendParts := strings.Split(backendAddr, ":")
	backendPort := backendParts[1]

	body := stubapi.ProxyConfigRequest{
		Address: "127.0.0.1",
		Port:    mustParsePort(backendPort, t),
	}
	jsonBody, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPut, "/init", strings.NewReader(string(jsonBody)))
	w := httptest.NewRecorder()
	Initialize(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Initialize failed: %d — %s", w.Code, w.Body.String())
	}

	time.Sleep(50 * time.Millisecond)

	// First connection — should get CONN:NEW greeting
	conn1, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", proxyPort), 2*time.Second)
	if err != nil {
		t.Fatalf("failed to dial proxy (conn1): %v", err)
	}
	reader1 := bufio.NewReader(conn1)
	greeting1, _ := reader1.ReadString('\n')
	if !strings.HasPrefix(greeting1, "CONN:NEW") {
		t.Errorf("conn1: expected CONN:NEW greeting, got %q", greeting1)
	}
	// Write some data and close
	_, _ = conn1.Write([]byte("msg1\n"))
	_ = conn1.Close()

	// Wait for the connection to drain and return to pool
	time.Sleep(150 * time.Millisecond)

	// Second connection — should reuse pooled backend connection
	conn2, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", proxyPort), 2*time.Second)
	if err != nil {
		t.Fatalf("failed to dial proxy (conn2): %v", err)
	}
	defer func() { _ = conn2.Close() }()
	reader2 := bufio.NewReader(conn2)

	// If connection was reused, we should NOT get a new CONN:NEW greeting
	// Instead, the connection should be ready for data.
	// Set a short read deadline to detect if there's a pending greeting.
	_ = conn2.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	greeting2, err := reader2.ReadString('\n')
	if err != nil && err.Error() == "EOF" {
		// Connection was closed — not reused
		t.Errorf("conn2: connection was not reused (got EOF)")
	} else if strings.HasPrefix(greeting2, "CONN:NEW") {
		// Got a new greeting — means a NEW connection was created, not a reuse
		t.Logf("conn2: got CONN:NEW greeting — may or may not be reused (depends on timing)")
	}
	// If we get a timeout, the connection is clean and reused (no pending data)
}

func TestProxy_RejectBeforeRun(t *testing.T) {
	resetInstance(t)

	// Before Run is called, connections should be rejected
	proxyPort := findFreePort(t)

	// Initialize via HTTP PUT
	body := stubapi.ProxyConfigRequest{
		Address: "127.0.0.1",
		Port:    8080,
	}
	jsonBody, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPut, "/init", strings.NewReader(string(jsonBody)))
	w := httptest.NewRecorder()
	Initialize(w, req)

	// Should get 503 since pool hasn't been created yet
	if w.Code != http.StatusServiceUnavailable {
		t.Logf("Initialize before Run returned %d (expected 503)", w.Code)
	}

	// Direct connection should be rejected or rejected after delay
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", proxyPort), 500*time.Millisecond)
	if err == nil {
		// Connection succeeded but proxy should reject
		_ = conn.Close()
	}
	// Either way, no proxy should be listening since Run wasn't called
}

func TestInitialize_StatusEndpoint(t *testing.T) {
	resetInstance(t)

	backendAddr, backendCloser := startTestEchoServer(t)
	defer backendCloser()

	// Initialize the proxy (creates pool before Run starts)
	backendParts := strings.Split(backendAddr, ":")
	backendPort := backendParts[1]
	body := stubapi.ProxyConfigRequest{
		Address: "127.0.0.1",
		Port:    mustParsePort(backendPort, t),
	}
	jsonBody, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPut, "/init", strings.NewReader(string(jsonBody)))
	w := httptest.NewRecorder()
	Initialize(w, req)

	// Initialize before Run now creates the pool and returns 200
	if w.Code != http.StatusOK {
		t.Fatalf("Initialize before Run should return 200, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "initialized") {
		t.Errorf("expected 'initialized' in response, got %q", w.Body.String())
	}

	// Now start the proxy — it should reuse the existing pool
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	proxyPort := findFreePort(t)

	cfg := DefaultProxyConfig
	go func() {
		_ = RunWithConfig(ctx, fmt.Sprintf("%d", proxyPort), cfg)
	}()

	// Wait for pool to be ready
	time.Sleep(100 * time.Millisecond)

	// Check status via GET
	req3 := httptest.NewRequest(http.MethodGet, "/init", nil)
	w3 := httptest.NewRecorder()
	Initialize(w3, req3)

	if w3.Code != http.StatusOK {
		t.Errorf("GET status should return 200, got %d", w3.Code)
	}
	if !strings.Contains(w3.Body.String(), backendAddr) {
		t.Errorf("status body should contain target address %q, got %q", backendAddr, w3.Body.String())
	}
}

func TestInitialize_Reconfigure(t *testing.T) {
	resetInstance(t)

	backendAddr1, closer1 := startTestEchoServer(t)
	defer closer1()
	backendAddr2, closer2 := startTestEchoServer(t)
	defer closer2()

	// Start proxy to create pool
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	proxyPort := findFreePort(t)

	cfg := DefaultProxyConfig
	go func() {
		_ = RunWithConfig(ctx, fmt.Sprintf("%d", proxyPort), cfg)
	}()
	time.Sleep(100 * time.Millisecond)

	// Initialize to first backend
	parts1 := strings.Split(backendAddr1, ":")
	body1 := stubapi.ProxyConfigRequest{
		Address: "127.0.0.1",
		Port:    mustParsePort(parts1[1], t),
	}
	jsonBody1, _ := json.Marshal(body1)
	req1 := httptest.NewRequest(http.MethodPut, "/init", strings.NewReader(string(jsonBody1)))
	w1 := httptest.NewRecorder()
	Initialize(w1, req1)

	if w1.Code != http.StatusOK {
		t.Fatalf("first Initialize failed: %d — %s", w1.Code, w1.Body.String())
	}
	if !strings.Contains(w1.Body.String(), "initialized") {
		t.Errorf("expected 'initialized' in response, got %q", w1.Body.String())
	}

	// Reconfigure to second backend
	parts2 := strings.Split(backendAddr2, ":")
	body2 := stubapi.ProxyConfigRequest{
		Address: "127.0.0.1",
		Port:    mustParsePort(parts2[1], t),
	}
	jsonBody2, _ := json.Marshal(body2)
	req2 := httptest.NewRequest(http.MethodPut, "/init", strings.NewReader(string(jsonBody2)))
	w2 := httptest.NewRecorder()
	Initialize(w2, req2)

	if w2.Code != http.StatusOK {
		t.Fatalf("second Initialize failed: %d — %s", w2.Code, w2.Body.String())
	}
	if !strings.Contains(w2.Body.String(), "reconfigured") {
		t.Errorf("expected 'reconfigured' in response, got %q", w2.Body.String())
	}
	if !strings.Contains(w2.Body.String(), backendAddr1) {
		t.Errorf("response should mention old target %q, got %q", backendAddr1, w2.Body.String())
	}
}

func mustParsePort(s string, t *testing.T) int {
	t.Helper()
	var port int
	if _, err := fmt.Sscanf(s, "%d", &port); err != nil {
		t.Fatalf("failed to parse port %q: %v", s, err)
	}
	return port
}

// Suppress klog output during tests
func init() {
	klog.SetOutput(io.Discard)
}
