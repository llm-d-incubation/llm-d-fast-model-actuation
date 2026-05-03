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
	instance = forwarder{
		dialer: &net.Dialer{},
	}
}

// startTestEchoServer starts a TCP server that echoes back any data it receives.
// Uses ports in range 10000-20000 to stay within int16 range.
func startTestEchoServer(t *testing.T) (addr string, closer func()) {
	t.Helper()
	for port := 10000; port <= 20000; port++ {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			continue
		}
		addr = ln.Addr().String()

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

		return addr, func() { _ = ln.Close() }
	}
	t.Fatalf("no free port found in range 10000-20000")
	return "", nil
}

// findFreePort returns an unused TCP port in the range 10000-20000,
// ensuring it fits in int16.
func findFreePort(t *testing.T) int16 {
	t.Helper()
	for port := 10000; port <= 20000; port++ {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			continue
		}
		_ = ln.Close()
		return int16(port)
	}
	t.Fatalf("no free port found in range 10000-20000")
	return 0
}

func TestProxy_EchoRoundTrip(t *testing.T) {
	resetInstance(t)

	backendAddr, backendCloser := startTestEchoServer(t)
	defer backendCloser()

	proxyPort := findFreePort(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	cfg := ProxyConfig{
		DialTimeout: 2 * time.Second,
	}
	go func() {
		err := RunWithConfig(ctx, fmt.Sprintf("%d", proxyPort), cfg)
		if err != nil {
			t.Logf("proxy Run error: %v", err)
		}
	}()

	// Wait for listener to be ready
	time.Sleep(50 * time.Millisecond)

	// Configure the proxy
	backendParts := strings.Split(backendAddr, ":")
	backendPort := backendParts[1]
	body := stubapi.ProxyConfig{
		Address: "127.0.0.1",
		Port:    mustParsePort(backendPort, t),
	}
	jsonBody, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPut, stubapi.ProxyConfigPath, strings.NewReader(string(jsonBody)))
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
	if !strings.Contains(resp, "hello proxy") {
		t.Errorf("expected echo of %q, got %q", testMsg, resp)
	}
}

func TestProxy_RejectBeforeConfigure(t *testing.T) {
	resetInstance(t)

	// Before Configure is called, connections should be rejected
	proxyPort := findFreePort(t)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	cfg := DefaultProxyConfig
	go func() {
		_ = RunWithConfig(ctx, fmt.Sprintf("%d", proxyPort), cfg)
	}()
	time.Sleep(50 * time.Millisecond)

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
	backendAddr, backendCloser := startTestEchoServer(t)
	defer backendCloser()

	backendParts := strings.Split(backendAddr, ":")
	backendPort := backendParts[1]
	body := stubapi.ProxyConfig{
		Address: "127.0.0.1",
		Port:    mustParsePort(backendPort, t),
	}
	jsonBody, _ := json.Marshal(body)
	req2 := httptest.NewRequest(http.MethodPut, stubapi.ProxyConfigPath, strings.NewReader(string(jsonBody)))
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

	var config stubapi.ProxyConfig
	if err := json.Unmarshal(w3.Body.Bytes(), &config); err != nil {
		t.Fatalf("failed to parse response JSON: %v", err)
	}
	if config.Address != "127.0.0.1" {
		t.Errorf("expected address 127.0.0.1, got %q", config.Address)
	}
	expectedPort := mustParsePort(backendPort, t)
	if config.Port != expectedPort {
		t.Errorf("expected port %d, got %d", expectedPort, config.Port)
	}
}

func TestConfigure_Reconfigure(t *testing.T) {
	resetInstance(t)

	backendAddr1, closer1 := startTestEchoServer(t)
	defer closer1()
	backendAddr2, closer2 := startTestEchoServer(t)
	defer closer2()

	parts1 := strings.Split(backendAddr1, ":")
	parts2 := strings.Split(backendAddr2, ":")

	// Configure to first backend
	body1 := stubapi.ProxyConfig{
		Address: "127.0.0.1",
		Port:    mustParsePort(parts1[1], t),
	}
	jsonBody1, _ := json.Marshal(body1)
	req1 := httptest.NewRequest(http.MethodPut, stubapi.ProxyConfigPath, strings.NewReader(string(jsonBody1)))
	w1 := httptest.NewRecorder()
	Configure(w1, req1)

	if w1.Code != http.StatusOK {
		t.Fatalf("first Configure failed: %d — %s", w1.Code, w1.Body.String())
	}
	if !strings.Contains(w1.Body.String(), "configured proxy to") {
		t.Errorf("expected 'configured proxy to' in response, got %q", w1.Body.String())
	}

	// Reconfigure to second backend
	body2 := stubapi.ProxyConfig{
		Address: "127.0.0.1",
		Port:    mustParsePort(parts2[1], t),
	}
	jsonBody2, _ := json.Marshal(body2)
	req2 := httptest.NewRequest(http.MethodPut, stubapi.ProxyConfigPath, strings.NewReader(string(jsonBody2)))
	w2 := httptest.NewRecorder()
	Configure(w2, req2)

	if w2.Code != http.StatusOK {
		t.Fatalf("second Configure failed: %d — %s", w2.Code, w2.Body.String())
	}
	if !strings.Contains(w2.Body.String(), "reconfigured proxy") {
		t.Errorf("expected 'reconfigured proxy' in response, got %q", w2.Body.String())
	}
	if !strings.Contains(w2.Body.String(), backendAddr1) {
		t.Errorf("response should mention old target %q, got %q", backendAddr1, w2.Body.String())
	}
	if !strings.Contains(w2.Body.String(), backendAddr2) {
		t.Errorf("response should mention new target %q, got %q", backendAddr2, w2.Body.String())
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
			req := httptest.NewRequest(tt.method, stubapi.ProxyConfigPath, strings.NewReader(tt.body))
			w := httptest.NewRecorder()
			Configure(w, req)

			if w.Code != tt.expectCode {
				t.Errorf("expected status %d, got %d — body: %s", tt.expectCode, w.Code, w.Body.String())
			}
		})
	}
}

func TestRunWithConfig_ContextCancellation(t *testing.T) {
	resetInstance(t)

	proxyPort := findFreePort(t)
	ctx, cancel := context.WithCancel(t.Context())

	cfg := ProxyConfig{
		DialTimeout: 2 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- RunWithConfig(ctx, fmt.Sprintf("%d", proxyPort), cfg)
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Errorf("RunWithConfig should return nil on clean shutdown, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunWithConfig did not stop after context cancellation")
	}
}

func mustParsePort(s string, t *testing.T) int16 {
	t.Helper()
	var port int
	if _, err := fmt.Sscanf(s, "%d", &port); err != nil {
		t.Fatalf("failed to parse port %q: %v", s, err)
	}
	if port < 0 || port > 32767 {
		t.Fatalf("port %d out of int16 range", port)
	}
	return int16(port)
}

// Suppress klog output during tests
func init() {
	klog.SetOutput(io.Discard)
}
