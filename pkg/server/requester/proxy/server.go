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

// forwarder is a TCP proxy that uses a connection pool to reuse
// backend connections, reducing per-request TCP handshake latency.
type forwarder struct {
	pool  *connPool
	mu    sync.Mutex
	cfg   PoolConfig
	ready atomic.Bool
}

// singleton instance initialized once at startup
var instance forwarder

// ProxyConfig holds configuration for the proxy server
type ProxyConfig struct {
	// UninitDelay is the delay before closing a connection when proxy is not initialized
	UninitDelay time.Duration
	// DrainTimeout is how long to wait for pending data before returning a
	// backend connection to the pool
	DrainTimeout time.Duration
	// PoolConfig is forwarded to the connection pool
	PoolConfig
}

// DefaultProxyConfig provides sensible defaults
var DefaultProxyConfig = ProxyConfig{
	UninitDelay:  100 * time.Millisecond,
	DrainTimeout: 200 * time.Millisecond,
	PoolConfig:   DefaultPoolConfig,
}

// Run starts the TCP proxy server on the given port
func Run(ctx context.Context, port string) error {
	return RunWithConfig(ctx, port, DefaultProxyConfig)
}

// RunWithConfig starts the TCP proxy server with custom configuration
func RunWithConfig(ctx context.Context, port string, cfg ProxyConfig) error {
	logger := klog.FromContext(ctx).WithName("proxy-server")
	logger.Info("starting TCP proxy server")

	// Create or update the connection pool
	instance.mu.Lock()
	instance.cfg = cfg.PoolConfig
	if instance.pool == nil {
		instance.pool = newConnPool(cfg.PoolConfig)
	}
	pool := instance.pool
	instance.mu.Unlock()

	pool.StartCleaner(ctx, logger)

	// Listen for incoming TCP connections
	listener, err := net.Listen("tcp", fmt.Sprintf(":%s", port))
	if err != nil {
		return fmt.Errorf("failed to listen on port %s: %w", port, err)
	}

	// Mark proxy as ready for accepting connections
	instance.ready.Store(true)

	// Start shutdown goroutine
	go func() {
		<-ctx.Done()
		logger.Info("shutting down TCP proxy server")
		_ = listener.Close()
		pool.Close()
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

// handleConnection forwards a single TCP connection using a pooled backend connection
func handleConnection(ctx context.Context, clientConn net.Conn, f *forwarder, logger klog.Logger, cfg ProxyConfig) {
	defer func() { _ = clientConn.Close() }()

	// Check if proxy is ready to accept connections
	if !f.ready.Load() {
		logger.Info("rejecting connection: proxy not ready")
		time.Sleep(cfg.UninitDelay)
		return
	}

	// Get the pool
	pool := f.getPool()
	if pool == nil {
		logger.Info("rejecting connection: pool not configured")
		return
	}

	// Get a backend connection from the pool (or dial a new one)
	targetConn, err := pool.Get(ctx)
	if err != nil {
		logger.Error(err, "failed to get backend connection")
		return
	}
	if targetConn == nil {
		logger.Info("rejecting connection: no target configured")
		return
	}
	// NOTE: Do NOT defer targetConn.Close() here — we return it to the pool
	// after a clean drain, or close it on error.

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
		// One side finished — drain remaining data before returning to pool
		drainConn(targetConn, cfg.DrainTimeout, logger)
		pool.Put(targetConn)
	case <-ctx.Done():
		// Context cancelled — close the connection instead of pooling
		_ = targetConn.Close()
	}
}

func (f *forwarder) getPool() *connPool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pool
}

// drainConn reads any remaining data from the connection with a short timeout,
// so that the connection can be safely returned to the pool in a clean state.
func drainConn(conn net.Conn, timeout time.Duration, logger klog.Logger) {
	deadline := time.Now().Add(timeout)
	_ = conn.SetReadDeadline(deadline)

	buf := make([]byte, 4096)
	for {
		_, err := conn.Read(buf)
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				// No more data — connection is clean
				break
			}
			// EOF or other error — also clean
			break
		}
		// Read some data, keep draining
		logger.V(6).Info("drained bytes from backend connection")
	}
	// Reset deadline
	_ = conn.SetReadDeadline(time.Time{})
}

// Initialize handles proxy initialization and configuration.
// May be called before or after Run starts. If called before Run,
// the pool is created on Initialize and reused when Run starts.
func Initialize(w http.ResponseWriter, r *http.Request) {
	// Get proxy status
	if r.Method == http.MethodGet {
		if instance.pool != nil {
			target := instance.pool.Target()
			if target != "" {
				w.WriteHeader(http.StatusOK)
				_, _ = fmt.Fprintf(w, "proxying to %s", target)
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("proxy not initialized"))
		return
	}

	if r.Method != http.MethodPut {
		http.Error(w, "invalid method, use PUT", http.StatusMethodNotAllowed)
		return
	}

	instance.mu.Lock()
	defer instance.mu.Unlock()

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

	if config.Port <= 0 {
		http.Error(w, "invalid port", http.StatusBadRequest)
		return
	}

	targetAddr := net.JoinHostPort(config.Address, fmt.Sprintf("%d", config.Port))

	// Create pool if it doesn't exist (before Run starts) or reconfigure existing pool
	if instance.pool == nil {
		// Called before Run — create the pool with default config.
		// Run will reuse this pool via its own cfg.PoolConfig.
		poolCfg := DefaultPoolConfig
		instance.pool = newConnPool(poolCfg)
	}

	oldTarget := instance.pool.SetTarget(targetAddr)

	w.WriteHeader(http.StatusOK)
	if oldTarget != "" {
		_, _ = fmt.Fprintf(w, "reconfigured proxy: %s -> %s (old pool drained)", oldTarget, targetAddr)
	} else {
		_, _ = fmt.Fprintf(w, "initialized proxy to: %s", targetAddr)
	}
}
