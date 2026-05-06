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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	stubapi "github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/spi"
)

// waitForTestReady blocks until the proxy listener is ready.
// Used by tests that don't call Configure (which auto-waits).
func waitForTestReady() {
	for {
		targetMu.RLock()
		ch := ready
		targetMu.RUnlock()
		if ch != nil {
			<-ch
			return
		}
		time.Sleep(time.Millisecond)
	}
}

// resetInstance clears the module-level state so tests don't interfere with each other.
func resetInstance(t *testing.T) {
	t.Helper()
	targetMu.Lock()
	dialProxy = nil
	dialTimeout = DefaultProxyConfig.DialTimeout
	closedCh := make(chan struct{})
	close(closedCh)
	ready = closedCh
	targetMu.Unlock()
}

// startTestEchoServer starts a TCP server that echoes back any data it receives.
func startTestEchoServer(t *testing.T) (addr string, port uint16, closer func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start echo server: %v", err)
	}
	tcpAddr := ln.Addr().(*net.TCPAddr)
	addr = tcpAddr.String()
	port = uint16(tcpAddr.Port)

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				_, _ = io.Copy(c, c)
				_ = c.Close()
			}(conn)
		}
	}()

	return addr, port, func() { _ = ln.Close() }
}

// findFreePort returns a free TCP port.
func findFreePort(t *testing.T) uint16 {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to find free port: %v", err)
	}
	port := uint16(ln.Addr().(*net.TCPAddr).Port)
	_ = ln.Close()
	return port
}

func TestProxy_EchoRoundTrip(t *testing.T) {
	resetInstance(t)

	_, backendPort, backendCloser := startTestEchoServer(t)
	defer backendCloser()

	proxyPort := findFreePort(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	// Reset ready so Run can set it
	targetMu.Lock()
	ready = nil
	targetMu.Unlock()

	cfg := ProxyConfig{
		Port:        proxyPort,
		DialTimeout: 2 * time.Second,
	}
	go func() {
		err := Run(ctx, cfg)
		if err != nil {
			t.Logf("proxy Run error: %v", err)
		}
	}()

	// Wait for the proxy listener to be ready
	waitForTestReady()

	// Configure the proxy
	body := stubapi.ProxyTargetConfig{
		Address: "127.0.0.1",
		Port:    backendPort,
	}
	jsonBody, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPut, stubapi.ProxyConfigPath, bytes.NewReader(jsonBody))
	w := httptest.NewRecorder()
	Configure(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Configure failed: %d — %s", w.Code, w.Body.String())
	}

	// Connect to proxy and verify echo
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", proxyPort), 2*time.Second)
	if err != nil {
		t.Fatalf("failed to dial proxy: %v", err)
	}
	defer func() { _ = conn.Close() }()

	// Send a message and verify echo
	testMsg := "hello proxy\n"
	_, err = conn.Write([]byte(testMsg))
	if err != nil {
		t.Fatalf("failed to write to proxy: %v", err)
	}

	reader := bufio.NewReader(conn)
	resp, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("failed to read echo: %v", err)
	}
	if resp != testMsg {
		t.Errorf("expected echo of %q, got %q", testMsg, resp)
	}
}

func TestProxy_RejectBeforeConfigure(t *testing.T) {
	resetInstance(t)

	// Before Configure is called, connections should be rejected
	proxyPort := findFreePort(t)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	// Reset ready so Run can set it
	targetMu.Lock()
	ready = nil
	targetMu.Unlock()

	cfg := ProxyConfig{
		Port:        proxyPort,
		DialTimeout: DefaultProxyConfig.DialTimeout,
	}
	go func() {
		_ = Run(ctx, cfg)
	}()

	// Wait for proxy to be listening
	waitForTestReady()

	// Dial the proxy — it should accept but reject (close immediately) since not configured
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", proxyPort), 2*time.Second)
	if err != nil {
		t.Fatalf("failed to dial proxy: %v", err)
	}
	defer func() { _ = conn.Close() }()

	// Try to read — connection should be closed by proxy since it's not configured
	_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	buf := make([]byte, 1024)
	_, err = conn.Read(buf)
	if err == nil {
		t.Error("expected read error since proxy is not configured, but got data")
	}
}

func TestConfigure_GetStatus(t *testing.T) {
	resetInstance(t)

	// Before configure, GET should return 404
	req1 := httptest.NewRequest(http.MethodGet, stubapi.ProxyConfigPath, nil)
	w1 := httptest.NewRecorder()
	Configure(w1, req1)

	if w1.Code != http.StatusNotFound {
		t.Errorf("GET before configure should return 404, got %d", w1.Code)
	}

	// Now configure and check status again
	_, backendPort, backendCloser := startTestEchoServer(t)
	defer backendCloser()

	body := stubapi.ProxyTargetConfig{
		Address: "127.0.0.1",
		Port:    backendPort,
	}
	jsonBody, _ := json.Marshal(body)
	req2 := httptest.NewRequest(http.MethodPut, stubapi.ProxyConfigPath, bytes.NewReader(jsonBody))
	w2 := httptest.NewRecorder()
	Configure(w2, req2)

	if w2.Code != http.StatusOK {
		t.Fatalf("Configure failed: %d — %s", w2.Code, w2.Body.String())
	}

	// GET should now return 200 with config
	req3 := httptest.NewRequest(http.MethodGet, stubapi.ProxyConfigPath, nil)
	w3 := httptest.NewRecorder()
	Configure(w3, req3)

	if w3.Code != http.StatusOK {
		t.Errorf("GET after configure should return 200, got %d", w3.Code)
	}

	var config stubapi.ProxyTargetConfig
	if err := json.Unmarshal(w3.Body.Bytes(), &config); err != nil {
		t.Fatalf("failed to parse response JSON: %v", err)
	}
	if config.Address != "127.0.0.1" {
		t.Errorf("expected address 127.0.0.1, got %q", config.Address)
	}
	expectedPort := backendPort
	if config.Port != expectedPort {
		t.Errorf("expected port %d, got %d", expectedPort, config.Port)
	}
}

func TestConfigure_ReconfigureReturnsConflict(t *testing.T) {
	resetInstance(t)

	_, backendPort, backendCloser := startTestEchoServer(t)
	defer backendCloser()

	// Configure
	body := stubapi.ProxyTargetConfig{
		Address: "127.0.0.1",
		Port:    backendPort,
	}
	jsonBody, _ := json.Marshal(body)
	req1 := httptest.NewRequest(http.MethodPut, stubapi.ProxyConfigPath, bytes.NewReader(jsonBody))
	w1 := httptest.NewRecorder()
	Configure(w1, req1)

	if w1.Code != http.StatusOK {
		t.Fatalf("first Configure failed: %d — %s", w1.Code, w1.Body.String())
	}

	// Verify GET returns the correct config
	reqGet := httptest.NewRequest(http.MethodGet, stubapi.ProxyConfigPath, nil)
	wGet := httptest.NewRecorder()
	Configure(wGet, reqGet)

	if wGet.Code != http.StatusOK {
		t.Fatalf("GET after configure failed: %d — %s", wGet.Code, wGet.Body.String())
	}
	var cfg stubapi.ProxyTargetConfig
	if err := json.Unmarshal(wGet.Body.Bytes(), &cfg); err != nil {
		t.Fatalf("failed to parse GET response: %v", err)
	}
	if cfg.Address != "127.0.0.1" || cfg.Port != backendPort {
		t.Errorf("GET returned wrong config: want {127.0.0.1, %d}, got {%s, %d}", backendPort, cfg.Address, cfg.Port)
	}

	// Reconfigure should return 409 Conflict
	req2 := httptest.NewRequest(http.MethodPut, stubapi.ProxyConfigPath, bytes.NewReader(jsonBody))
	w2 := httptest.NewRecorder()
	Configure(w2, req2)

	if w2.Code != http.StatusConflict {
		t.Errorf("reconfigure should return 409, got %d — body: %s", w2.Code, w2.Body.String())
	}
}

func TestConfigure_BadRequests(t *testing.T) {
	resetInstance(t)

	tests := []struct {
		name       string
		method     string
		body       string
		expectCode int
	}{
		{
			name:       "DELETE not allowed",
			method:     http.MethodDelete,
			body:       "",
			expectCode: http.StatusMethodNotAllowed,
		},
		{
			name:       "invalid JSON",
			method:     http.MethodPut,
			body:       `{invalid json}`,
			expectCode: http.StatusBadRequest,
		},
		{
			name:       "missing address",
			method:     http.MethodPut,
			body:       `{"address":"","port":8080}`,
			expectCode: http.StatusBadRequest,
		},
		{
			name:       "invalid port zero",
			method:     http.MethodPut,
			body:       `{"address":"127.0.0.1","port":0}`,
			expectCode: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetInstance(t) // each subtest starts fresh
			req := httptest.NewRequest(tt.method, stubapi.ProxyConfigPath, bytes.NewReader([]byte(tt.body)))
			w := httptest.NewRecorder()
			Configure(w, req)

			if w.Code != tt.expectCode {
				t.Errorf("expected status %d, got %d — body: %s", tt.expectCode, w.Code, w.Body.String())
			}
		})
	}
}
