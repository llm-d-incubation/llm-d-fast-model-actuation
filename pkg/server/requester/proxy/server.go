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
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"k8s.io/klog/v2"

	stubapi "github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/spi"
)

// ProxyConfig holds configuration for the proxy server
type ProxyConfig struct {
	// DialTimeout is the timeout for dialing backend connections
	DialTimeout time.Duration
}

// DefaultProxyConfig provides sensible defaults
var DefaultProxyConfig = ProxyConfig{
	DialTimeout: 10 * time.Second,
}

// forwarder is a TCP proxy that forwards connections to a target address.
type forwarder struct {
	mu     sync.Mutex
	dialer *net.Dialer
	target string // set by Configure, empty when not configured
}

func (f *forwarder) getTarget() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.target
}

// singleton instance initialized once at startup
var instance = forwarder{
	dialer: &net.Dialer{},
}

// Run starts the TCP proxy server on the given port
func Run(ctx context.Context, port string) error {
	return RunWithConfig(ctx, port, DefaultProxyConfig)
}

// RunWithConfig starts the TCP proxy server with custom configuration
func RunWithConfig(ctx context.Context, port string, cfg ProxyConfig) error {
	logger := klog.FromContext(ctx).WithName("proxy-server")
	logger.Info("Starting TCP proxy server")

	instance.mu.Lock()
	instance.dialer.Timeout = cfg.DialTimeout
	instance.mu.Unlock()

	// Listen for incoming TCP connections
	listener, err := net.Listen("tcp", fmt.Sprintf(":%s", port))
	if err != nil {
		return fmt.Errorf("failed to listen on port %s: %w", port, err)
	}

	// Start shutdown goroutine
	go func() {
		<-ctx.Done()
		logger.Info("Shutting down TCP proxy server")
		_ = listener.Close()
	}()

	logger.Info("TCP proxy server started", "port", port)

	for {
		conn, err := listener.Accept()
		if err != nil {
			// Check if this is due to shutdown
			select {
			case <-ctx.Done():
				logger.Info("TCP proxy server stopped")
				return nil
			default:
				logger.Error(err, "Failed to accept connection")
				continue
			}
		}

		go handleConnection(ctx, conn, &instance, logger)
	}
}

// handleConnection forwards a single TCP connection to the backend.
func handleConnection(ctx context.Context, clientConn net.Conn, f *forwarder, logger klog.Logger) {
	defer func() { _ = clientConn.Close() }()

	target := f.getTarget()
	if target == "" {
		logger.Info("Rejecting connection: proxy not configured")
		return
	}

	// Dial a fresh backend connection for this client connection
	targetConn, err := f.dialer.DialContext(ctx, "tcp", target)
	if err != nil {
		logger.Error(err, "Failed to dial backend", "target", target)
		return
	}
	defer func() { _ = targetConn.Close() }()

	// Bidirectional forwarding using two goroutines
	done := make(chan struct{}, 2)

	go func() {
		_, err := io.Copy(targetConn, clientConn)
		if err != nil {
			logger.V(4).Info("Client-to-target copy finished", "err", err)
		}
		done <- struct{}{}
	}()

	go func() {
		_, err := io.Copy(clientConn, targetConn)
		if err != nil {
			logger.V(4).Info("Target-to-client copy finished", "err", err)
		}
		done <- struct{}{}
	}()

	// Wait for either direction to finish or context to be cancelled
	select {
	case <-done:
		// One side finished, close the other side
	case <-ctx.Done():
		// Context cancelled
	}
}

// Configure handles the proxy configuration HTTP endpoint.
// This is the handler for the resource at ProxyConfigPath.
// GET returns the current configuration (200) or 404 if not configured.
// PUT configures the proxy with a new target address and port.
func Configure(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		target := instance.getTarget()
		if target == "" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte("proxy not configured"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		addr, portStr, _ := net.SplitHostPort(target)
		var portVal int16
		if p, err := net.LookupPort("tcp", portStr); err == nil {
			portVal = int16(p)
		}
		config := stubapi.ProxyConfig{Address: addr, Port: portVal}
		_ = json.NewEncoder(w).Encode(config)
		return
	}

	if r.Method != http.MethodPut {
		http.Error(w, "method not allowed, use GET or PUT", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to read request body: %v", err), http.StatusBadRequest)
		return
	}
	defer func() { _ = r.Body.Close() }()

	var config stubapi.ProxyConfig
	if err := json.Unmarshal(body, &config); err != nil {
		http.Error(w, fmt.Sprintf("failed to parse JSON: %v", err), http.StatusBadRequest)
		return
	}

	if config.Address == "" {
		http.Error(w, "address is required", http.StatusBadRequest)
		return
	}

	if config.Port <= 0 {
		http.Error(w, "invalid port", http.StatusBadRequest)
		return
	}

	targetAddr := net.JoinHostPort(config.Address, fmt.Sprintf("%d", config.Port))

	instance.mu.Lock()
	oldTarget := instance.target
	instance.target = targetAddr
	instance.mu.Unlock()

	w.WriteHeader(http.StatusOK)
	if oldTarget != "" {
		_, _ = fmt.Fprintf(w, "reconfigured proxy: %s -> %s", oldTarget, targetAddr)
	} else {
		_, _ = fmt.Fprintf(w, "configured proxy to: %s", targetAddr)
	}
}
