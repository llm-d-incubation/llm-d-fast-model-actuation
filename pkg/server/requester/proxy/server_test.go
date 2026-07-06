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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	stubapi "github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/spi"
)

// CI machines can be heavily loaded, so use generous timeouts for any
// operation that crosses the proxy or talks to the network.
const (
	testDialTimeout = 10 * time.Second
	testReadTimeout = 10 * time.Second
)

// startProxy runs Run in a goroutine and returns a stop function.
// Calling stop cancels the context and waits for the goroutine to exit,
// ensuring no goroutine leaks between tests.
func startProxy(t *testing.T, srv *Server) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := srv.Run(ctx); err != nil {
			t.Logf("proxy Run error: %v", err)
		}
	}()
	return func() {
		cancel()
		wg.Wait()
	}
}

// startTestEchoServer starts a TCP server that echoes back any data it
// receives. The returned closer shuts the listener AND any accepted
// connections so tests do not leak goroutines.
func startTestEchoServer(t *testing.T) (addr string, port uint16, closer func()) {
	t.Helper()
	ln, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("failed to start echo server: %v", err)
	}
	tcpAddr := ln.Addr().(*net.TCPAddr) // ListenTCP guarantees *TCPAddr
	addr = tcpAddr.String()
	port = uint16(tcpAddr.Port)

	var (
		connsMu sync.Mutex
		conns   []net.Conn
	)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			connsMu.Lock()
			conns = append(conns, conn)
			connsMu.Unlock()
			go func(c net.Conn) {
				_, _ = io.Copy(c, c)
				_ = c.Close()
			}(conn)
		}
	}()

	return addr, port, func() {
		_ = ln.Close()
		connsMu.Lock()
		defer connsMu.Unlock()
		for _, c := range conns {
			_ = c.Close()
		}
	}
}

// findFreePort returns a free TCP port.
func findFreePort(t *testing.T) uint16 {
	t.Helper()
	ln, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("failed to find free port: %v", err)
	}
	port := uint16(ln.Addr().(*net.TCPAddr).Port)
	_ = ln.Close()
	return port
}

func TestProxy_EchoRoundTrip(t *testing.T) {
	t.Parallel()

	_, backendPort, backendCloser := startTestEchoServer(t)
	defer backendCloser()

	proxyPort := findFreePort(t)
	srv := New(ProxyConfig{Port: proxyPort, DialTimeout: testDialTimeout})
	stop := startProxy(t, srv)
	defer stop()

	body := stubapi.ProxyTargetConfig{Address: "127.0.0.1", Port: backendPort}
	jsonBody, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPut, stubapi.ProxyConfigPath, bytes.NewReader(jsonBody))
	w := httptest.NewRecorder()
	srv.Configure(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("Configure failed: %d — %s", w.Code, w.Body.String())
	}

	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", proxyPort), testDialTimeout)
	if err != nil {
		t.Fatalf("failed to dial proxy: %v", err)
	}
	defer func() { _ = conn.Close() }()

	testMsg := "hello proxy\n"
	if _, err := conn.Write([]byte(testMsg)); err != nil {
		t.Fatalf("failed to write to proxy: %v", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(testReadTimeout))
	got := make([]byte, len(testMsg))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("failed to read echo: %v", err)
	}
	if string(got) != testMsg {
		t.Errorf("expected echo of %q, got %q", testMsg, got)
	}
	_ = conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	extra := make([]byte, 1)
	if n, _ := conn.Read(extra); n > 0 {
		t.Errorf("unexpected trailing byte %q after echo", extra[:n])
	}
}

func TestProxy_NoListenerBeforeConfigure(t *testing.T) {
	t.Parallel()

	proxyPort := findFreePort(t)
	srv := New(ProxyConfig{Port: proxyPort, DialTimeout: DefaultProxyConfig.DialTimeout})
	stop := startProxy(t, srv)
	defer stop()

	// Run is blocked waiting for Configure, so the proxy port has no listener.
	time.Sleep(500 * time.Millisecond)
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", proxyPort), 2*time.Second)
	if err == nil {
		_ = conn.Close()
		t.Errorf("expected dial to fail before Configure, but it succeeded")
	}
}

func TestConfigure_GetStatus(t *testing.T) {
	t.Parallel()

	srv := New(ProxyConfig{Port: findFreePort(t), DialTimeout: testDialTimeout})

	// Before Configure, GET should return 404.
	req1 := httptest.NewRequest(http.MethodGet, stubapi.ProxyConfigPath, nil)
	w1 := httptest.NewRecorder()
	srv.Configure(w1, req1)
	if w1.Code != http.StatusNotFound {
		t.Errorf("GET before configure should return 404, got %d", w1.Code)
	}

	// PUT requires Run to be active, since Configure waits for listener readiness.
	stop := startProxy(t, srv)
	defer stop()

	_, backendPort, backendCloser := startTestEchoServer(t)
	defer backendCloser()

	body := stubapi.ProxyTargetConfig{Address: "127.0.0.1", Port: backendPort}
	jsonBody, _ := json.Marshal(body)
	req2 := httptest.NewRequest(http.MethodPut, stubapi.ProxyConfigPath, bytes.NewReader(jsonBody))
	w2 := httptest.NewRecorder()
	srv.Configure(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("Configure failed: %d — %s", w2.Code, w2.Body.String())
	}

	req3 := httptest.NewRequest(http.MethodGet, stubapi.ProxyConfigPath, nil)
	w3 := httptest.NewRecorder()
	srv.Configure(w3, req3)
	if w3.Code != http.StatusOK {
		t.Errorf("GET after configure should return 200, got %d", w3.Code)
	}

	var got stubapi.ProxyTargetConfig
	if err := json.Unmarshal(w3.Body.Bytes(), &got); err != nil {
		t.Fatalf("failed to parse response JSON: %v", err)
	}
	if got != body {
		t.Errorf("GET returned %+v, want %+v", got, body)
	}
}

func TestConfigure_DifferentConfigReturnsConflict(t *testing.T) {
	t.Parallel()

	proxyPort := findFreePort(t)
	srv := New(ProxyConfig{Port: proxyPort, DialTimeout: testDialTimeout})
	stop := startProxy(t, srv)
	defer stop()

	_, backendPort, backendCloser := startTestEchoServer(t)
	defer backendCloser()

	body := stubapi.ProxyTargetConfig{Address: "127.0.0.1", Port: backendPort}
	jsonBody, _ := json.Marshal(body)
	req1 := httptest.NewRequest(http.MethodPut, stubapi.ProxyConfigPath, bytes.NewReader(jsonBody))
	w1 := httptest.NewRecorder()
	srv.Configure(w1, req1)
	if w1.Code != http.StatusOK {
		t.Fatalf("first Configure failed: %d — %s", w1.Code, w1.Body.String())
	}

	reqGet := httptest.NewRequest(http.MethodGet, stubapi.ProxyConfigPath, nil)
	wGet := httptest.NewRecorder()
	srv.Configure(wGet, reqGet)
	if wGet.Code != http.StatusOK {
		t.Fatalf("GET after configure failed: %d — %s", wGet.Code, wGet.Body.String())
	}
	var got stubapi.ProxyTargetConfig
	if err := json.Unmarshal(wGet.Body.Bytes(), &got); err != nil {
		t.Fatalf("failed to parse GET response: %v", err)
	}
	if got != body {
		t.Errorf("GET returned %+v, want %+v", got, body)
	}

	// Reconfigure with a DIFFERENT config — should still be rejected, so the
	// 409 actually proves "reconfigure is blocked" rather than "duplicate
	// write is a no-op".
	differentBody := stubapi.ProxyTargetConfig{Address: "10.0.0.1", Port: backendPort + 1}
	diffJSON, _ := json.Marshal(differentBody)
	req2 := httptest.NewRequest(http.MethodPut, stubapi.ProxyConfigPath, bytes.NewReader(diffJSON))
	w2 := httptest.NewRecorder()
	srv.Configure(w2, req2)
	if w2.Code != http.StatusConflict {
		t.Errorf("reconfigure with different config should return 409, got %d — body: %s", w2.Code, w2.Body.String())
	}
}

func TestConfigure_BadRequests(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		method     string
		body       string
		expectCode int
	}{
		{"DELETE not allowed", http.MethodDelete, "", http.StatusMethodNotAllowed},
		{"invalid JSON", http.MethodPut, `{invalid json}`, http.StatusBadRequest},
		{"missing address", http.MethodPut, `{"address":"","port":8080}`, http.StatusBadRequest},
		{"invalid port zero", http.MethodPut, `{"address":"127.0.0.1","port":0}`, http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv := New(ProxyConfig{Port: findFreePort(t), DialTimeout: testDialTimeout})
			req := httptest.NewRequest(tt.method, stubapi.ProxyConfigPath, bytes.NewReader([]byte(tt.body)))
			w := httptest.NewRecorder()
			srv.Configure(w, req)

			if w.Code != tt.expectCode {
				t.Errorf("expected status %d, got %d — body: %s", tt.expectCode, w.Code, w.Body.String())
			}
		})
	}
}
