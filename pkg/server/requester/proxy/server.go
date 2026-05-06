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

	"github.com/inetaf/tcpproxy"
	"github.com/spf13/pflag"
	"k8s.io/klog/v2"

	stubapi "github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/spi"
)

// ProxyConfig holds configuration for the proxy server
type ProxyConfig struct {
	// Port is the port the proxy listens on
	Port uint16
	// DialTimeout is the timeout for dialing backend connections
	DialTimeout time.Duration
}

// DefaultProxyConfig provides sensible defaults
var DefaultProxyConfig = ProxyConfig{
	Port:        8082,
	DialTimeout: 10 * time.Second,
}

// AddFlags registers command-line flags for all proxy configuration fields.
func (cfg *ProxyConfig) AddFlags(fs pflag.FlagSet) {
	fs.Uint16Var(&cfg.Port, "proxy-port", cfg.Port, "port for TCP proxy")
	fs.DurationVar(&cfg.DialTimeout, "proxy-dial-timeout", cfg.DialTimeout, "timeout for proxy backend dial")
}

var (
	targetMu    sync.RWMutex
	dialProxy   *tcpproxy.DialProxy // nil until configured (set exactly once)
	dialTimeout = DefaultProxyConfig.DialTimeout
	ready       chan struct{} // closed once the proxy listener is ready
)

// Run starts the TCP proxy server with the given configuration.
// It blocks until the context is cancelled or a fatal error occurs.
func Run(ctx context.Context, cfg ProxyConfig) error {
	logger := klog.FromContext(ctx).WithName("proxy-server")
	logger.Info("Starting TCP proxy server", "config", cfg)

	// Set dial timeout for later use in Configure()
	targetMu.Lock()
	dialTimeout = cfg.DialTimeout
	targetMu.Unlock()

	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.Port))
	if err != nil {
		return fmt.Errorf("failed to listen on port %d: %w", cfg.Port, err)
	}

	// Signal that the proxy is now listening
	ready = make(chan struct{})
	close(ready)

	// Start shutdown goroutine
	go func() {
		<-ctx.Done()
		logger.Info("Shutting down TCP proxy server")
		_ = listener.Close()
	}()

	logger.Info("TCP proxy server started")

	for {
		conn, err := listener.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
				logger.Error(err, "Failed to accept connection")
				continue
			}
		}
		go handleConnection(ctx, conn)
	}
}

// handleConnection forwards a single TCP connection using the configured DialProxy.
func handleConnection(ctx context.Context, clientConn net.Conn) {
	defer func() { _ = clientConn.Close() }()

	// Monitor context cancellation to close active connections on shutdown
	go func() {
		<-ctx.Done()
		_ = clientConn.Close()
	}()

	targetMu.RLock()
	dp := dialProxy
	targetMu.RUnlock()

	if dp == nil {
		logger := klog.FromContext(ctx).WithName("proxy-server")
		logger.Info("Rejecting connection: proxy not configured")
		return
	}

	dp.HandleConn(clientConn)
}

// waitForReady blocks until the proxy listener is ready.
// Returns immediately if already ready.
// Times out after 50ms to avoid blocking indefinitely (e.g., in unit tests).
func waitForReady() {
	// Quick check
	targetMu.RLock()
	ch := ready
	targetMu.RUnlock()
	if ch != nil {
		<-ch
		return
	}
	// Wait up to 50ms for Run to start and signal ready
	deadline := time.Now().Add(50 * time.Millisecond)
	for time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
		targetMu.RLock()
		ch = ready
		targetMu.RUnlock()
		if ch != nil {
			<-ch
			return
		}
	}
}

// Configure handles the proxy configuration HTTP endpoint.
// This is the handler for the resource at ProxyConfigPath.
// GET returns the current configuration (200) or 404 if not configured.
// PUT configures the proxy with a new target address and port (one-time only, 409 if already configured).
func Configure(w http.ResponseWriter, r *http.Request) {
	// Wait for the proxy listener to be ready before handling any request
	waitForReady()

	if r.Method == http.MethodGet {
		targetMu.RLock()
		dp := dialProxy
		targetMu.RUnlock()

		if dp == nil {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte("proxy not configured"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		addr, portStr, _ := net.SplitHostPort(dp.Addr)
		var portVal uint16
		if portNum, err := net.LookupPort("tcp", portStr); err == nil {
			portVal = uint16(portNum)
		}
		config := stubapi.ProxyTargetConfig{Address: addr, Port: portVal}
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

	var config stubapi.ProxyTargetConfig
	if err := json.Unmarshal(body, &config); err != nil {
		http.Error(w, fmt.Sprintf("failed to parse JSON: %v", err), http.StatusBadRequest)
		return
	}

	if config.Address == "" {
		http.Error(w, "address is required", http.StatusBadRequest)
		return
	}

	if config.Port == 0 {
		http.Error(w, "invalid port", http.StatusBadRequest)
		return
	}

	targetAddr := net.JoinHostPort(config.Address, fmt.Sprintf("%d", config.Port))

	targetMu.Lock()
	defer targetMu.Unlock()

	if dialProxy != nil {
		http.Error(w, "proxy already configured", http.StatusConflict)
		return
	}

	dialProxy = &tcpproxy.DialProxy{
		Addr:        targetAddr,
		DialTimeout: dialTimeout,
	}

	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(w, "configured proxy to: %s", targetAddr)
}
