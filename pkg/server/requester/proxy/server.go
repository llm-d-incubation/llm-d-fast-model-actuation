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

// ProxyConfig holds the proxy server's own runtime parameters (listening port,
// dial timeout, etc.). This is distinct from spi.ProxyTargetConfig, which
// describes the backend target the proxy forwards traffic to.
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

// lazyTarget implements tcpproxy.Target. It acts as a placeholder
// registered with tcpproxy.Proxy before the backend target is known.
// HandleConn rejects connections until Configure() delivers the DialProxy.
type lazyTarget struct {
	mu          sync.Mutex
	dialProxy   *tcpproxy.DialProxy    // nil until Configure() delivers
	proxyTarget *stubapi.ProxyTargetConfig
	configured  bool                   // true once setConfig has been called
}

// HandleConn checks if the backend is configured. If not, the connection is
// rejected immediately. Otherwise it delegates to tcpproxy.DialProxy.HandleConn.
func (l *lazyTarget) HandleConn(conn net.Conn) {
	l.mu.Lock()
	dp := l.dialProxy
	configured := l.configured
	l.mu.Unlock()

	if !configured || dp == nil {
		_ = conn.Close()
		return
	}
	dp.HandleConn(conn)
}

// setConfig delivers the DialProxy and signals readiness to HandleConn calls.
func (l *lazyTarget) setConfig(dp *tcpproxy.DialProxy, target *stubapi.ProxyTargetConfig) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.configured {
		return
	}
	l.configured = true
	l.dialProxy = dp
	l.proxyTarget = target
}

// config returns the current target config, or nil if not yet configured.
func (l *lazyTarget) config() *stubapi.ProxyTargetConfig {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.proxyTarget
}

var (
	// Global state shared between Run() and Configure().
	proxyMu     sync.Mutex
	proxyTarget *lazyTarget        // placeholder target registered with tcpproxy.Proxy
	proxy       *tcpproxy.Proxy
	proxyCfg    ProxyConfig       // set by Run() before proxy starts
	proxyStarted chan struct{}    // closed when proxy.Run() has been called
)

// Run starts the TCP proxy server with the given configuration.
// It uses tcpproxy.Proxy as the underlying framework — the proxy creates its
// own listener and dispatches connections to the configured backend.
//
// Run and Configure may be called in either order. Run registers a placeholder
// route and starts the proxy; Configure delivers the backend target. Incoming
// connections wait briefly for the target to be configured before being forwarded.
//
// It blocks until the context is cancelled or a fatal error occurs.
func Run(ctx context.Context, cfg ProxyConfig) error {
	logger := klog.FromContext(ctx).WithName("proxy-server")
	logger.Info("Starting TCP proxy server", "config", cfg)

	proxyMu.Lock()
	proxyTarget = &lazyTarget{}
	proxy = &tcpproxy.Proxy{}
	proxy.AddRoute(fmt.Sprintf(":%d", cfg.Port), proxyTarget)
	proxyCfg = cfg
	proxyStarted = make(chan struct{})
	proxyMu.Unlock()

	// Start the proxy in a goroutine (it creates its own listener and blocks).
	// Signal started immediately after so Configure() can proceed.
	proxyErr := make(chan error, 1)
	go func() {
		proxyErr <- proxy.Run()
	}()
	close(proxyStarted)

	// Wait for context cancellation, then shut down.
	<-ctx.Done()
	logger.Info("Shutting down TCP proxy server")
	_ = proxy.Close()
	return <-proxyErr
}

// Configure handles the proxy configuration HTTP endpoint.
// This is the handler for the resource at ProxyConfigPath.
// GET returns the current configuration (200) or 404 if not configured.
// PUT configures the proxy with a new target address and port (one-time only, 409 if already configured).
func Configure(w http.ResponseWriter, r *http.Request) {
	// Wait for Run() to have registered the proxy.
	proxyMu.Lock()
	ps := proxyStarted
	lt := proxyTarget
	proxyMu.Unlock()

	if ps != nil {
		<-ps
	}

	if lt == nil {
		http.Error(w, "proxy server not started", http.StatusServiceUnavailable)
		return
	}

	if r.Method == http.MethodGet {
		target := lt.config()
		if target == nil {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte("proxy not configured"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(target)
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

	// Check whether already configured (must hold proxyMu for this).
	proxyMu.Lock()
	defer proxyMu.Unlock()

	if lt.config() != nil {
		http.Error(w, "proxy already configured", http.StatusConflict)
		return
	}

	dp := &tcpproxy.DialProxy{
		Addr:        targetAddr,
		DialTimeout: proxyCfg.DialTimeout,
	}

	lt.setConfig(dp, &config)

	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(w, "configured proxy to: %s", targetAddr)
}
