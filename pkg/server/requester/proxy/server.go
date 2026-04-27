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
	"sync/atomic"
	"time"

	"k8s.io/klog/v2"

	stubapi "github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/spi"
)

// forwarder is a lazy TCP proxy that only starts forwarding after receiving
// the first configuration request
type forwarder struct {
	mu          sync.RWMutex
	targetAddr  string
	initialized atomic.Bool
}

// singleton instance initialized once at startup
var instance forwarder

// ProxyConfig holds configuration for the proxy server
type ProxyConfig struct {
	// UninitDelay is the delay before closing a connection when proxy is not initialized
	UninitDelay time.Duration
	// DialTimeout is the timeout for connecting to the target server
	DialTimeout time.Duration
}

// DefaultProxyConfig provides sensible defaults
var DefaultProxyConfig = ProxyConfig{
	UninitDelay: 100 * time.Millisecond,
	DialTimeout: 10 * time.Second,
}

// Run starts the TCP proxy server on the given port
func Run(ctx context.Context, port string) error {
	return RunWithConfig(ctx, port, DefaultProxyConfig)
}

// RunWithConfig starts the TCP proxy server with custom configuration
func RunWithConfig(ctx context.Context, port string, cfg ProxyConfig) error {
	logger := klog.FromContext(ctx).WithName("proxy-server")
	logger.Info("starting TCP proxy server")

	// Listen for incoming TCP connections
	listener, err := net.Listen("tcp", fmt.Sprintf(":%s", port))
	if err != nil {
		return fmt.Errorf("failed to listen on port %s: %w", port, err)
	}

	go func() {
		<-ctx.Done()
		logger.Info("shutting down TCP proxy server")
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
				logger.Error(err, "failed to accept connection")
				continue
			}
		}

		go handleConnection(ctx, conn, &instance, logger, cfg)
	}
}

// handleConnection forwards a single TCP connection to the target
func handleConnection(ctx context.Context, clientConn net.Conn, f *forwarder, logger klog.Logger, cfg ProxyConfig) {
	defer func() { _ = clientConn.Close() }()

	// Check if proxy is initialized
	if !f.initialized.Load() {
		// Proxy not initialized, close connection after a short delay
		logger.Info("rejecting connection: proxy not initialized")
		time.Sleep(cfg.UninitDelay)
		return
	}

	// Get target address
	f.mu.RLock()
	target := f.targetAddr
	f.mu.RUnlock()

	if target == "" {
		logger.Info("rejecting connection: target address not set")
		return
	}

	// Connect to target server
	targetConn, err := net.DialTimeout("tcp", target, cfg.DialTimeout)
	if err != nil {
		logger.Error(err, "failed to connect to target", "target", target)
		return
	}
	defer func() { _ = targetConn.Close() }()

	// Bidirectional forwarding
	done := make(chan struct{}, 2)

	go func() {
		_, err := io.Copy(targetConn, clientConn)
		if err != nil {
			logger.V(4).Info("client->target copy finished", "err", err)
		}
		done <- struct{}{}
	}()

	go func() {
		_, err := io.Copy(clientConn, targetConn)
		if err != nil {
			logger.V(4).Info("target->client copy finished", "err", err)
		}
		done <- struct{}{}
	}()

	// Wait for either direction to finish or context to be cancelled
	select {
	case <-done:
	case <-ctx.Done():
	}
}

// Initialize handles proxy initialization and configuration
func Initialize(w http.ResponseWriter, r *http.Request) {
	// Get proxy status
	if r.Method == http.MethodGet {
		if instance.initialized.Load() {
			instance.mu.RLock()
			target := instance.targetAddr
			instance.mu.RUnlock()
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, "proxying to %s", target)
		} else {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte("proxy not initialized"))
		}
		return
	}

	if r.Method != http.MethodPut {
		http.Error(w, "invalid method, use PUT", http.StatusMethodNotAllowed)
		return
	}

	// Acquire write lock and check again
	instance.mu.Lock()
	defer instance.mu.Unlock()

	if instance.initialized.Load() {
		http.Error(w, "proxy already initialized", http.StatusConflict)
		return
	}

	// Parse configuration from request body
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to read request body: %v", err), http.StatusBadRequest)
		return
	}
	defer func() { _ = r.Body.Close() }()

	var config stubapi.ProxyConfigRequest
	if err := json.Unmarshal(body, &config); err != nil {
		http.Error(w, fmt.Sprintf("failed to parse JSON: %v", err), http.StatusBadRequest)
		return
	}

	if config.Address == "" {
		http.Error(w, "address is required", http.StatusBadRequest)
		return
	}

	if config.Port <= 0 || config.Port > 65535 {
		http.Error(w, "invalid port", http.StatusBadRequest)
		return
	}

	// Set target address
	instance.targetAddr = net.JoinHostPort(config.Address, fmt.Sprintf("%d", config.Port))
	instance.initialized.Store(true)

	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(w, "initialized proxy to: %s", instance.targetAddr)
}
