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

// ProxyConfig holds the proxy server's runtime parameters that are provided
// at requester startup, via the requester command line. It is distinct from
// stubapi.ProxyTargetConfig, which holds the runtime parameters provided
// later by the dual-pods controller (the backend the proxy forwards to).
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
func (cfg *ProxyConfig) AddFlags(fs *pflag.FlagSet) {
	fs.Uint16Var(&cfg.Port, "proxy-port", cfg.Port, "port for TCP proxy")
	fs.DurationVar(&cfg.DialTimeout, "proxy-dial-timeout", cfg.DialTimeout, "timeout for proxy backend dial")
}

// Module-level coordination state. Run and Configure may run on different
// goroutines and in either order; the channels handle the hand-off between
// them. configCh carries the ProxyTargetConfig from Configure to Run;
// startedCh is closed by Run once the proxy is actively listening (or has
// failed to start), so Configure does not return until that point.
var (
	initOnce  sync.Once
	configCh  chan *stubapi.ProxyTargetConfig
	startedCh chan struct{}

	stateMu  sync.Mutex
	target   *stubapi.ProxyTargetConfig // set by the first successful Configure PUT
	startErr error                      // set by Run before closing startedCh on a Start failure
)

// initState lazily initializes the module-level coordination channels.
func initState() {
	initOnce.Do(func() {
		configCh = make(chan *stubapi.ProxyTargetConfig, 1)
		startedCh = make(chan struct{})
	})
}

// Run starts the TCP proxy server. It blocks waiting for Configure to deliver
// the backend target, then constructs and starts a tcpproxy.Proxy listening on
// cfg.Port. It returns when the context is cancelled or the proxy stops.
//
// Run and Configure may be invoked in either order from different goroutines.
func Run(ctx context.Context, cfg ProxyConfig) error {
	logger := klog.FromContext(ctx).WithName("proxy-server")
	logger.Info("Starting TCP proxy server", "config", cfg)

	initState()

	var tgt *stubapi.ProxyTargetConfig
	select {
	case tgt = <-configCh:
		logger.V(2).Info("Received proxy target from Configure", "address", tgt.Address, "port", tgt.Port)
	case <-ctx.Done():
		logger.V(2).Info("Context cancelled before proxy was configured")
		return nil
	}

	targetAddr := net.JoinHostPort(tgt.Address, fmt.Sprintf("%d", tgt.Port))
	p := &tcpproxy.Proxy{}
	p.AddRoute(fmt.Sprintf(":%d", cfg.Port), &tcpproxy.DialProxy{
		Addr:        targetAddr,
		DialTimeout: cfg.DialTimeout,
	})

	if err := p.Start(); err != nil {
		stateMu.Lock()
		startErr = fmt.Errorf("failed to start proxy listener on port %d: %w", cfg.Port, err)
		stateMu.Unlock()
		close(startedCh)
		logger.Error(err, "Failed to start TCP proxy listener", "port", cfg.Port)
		return startErr
	}
	close(startedCh)
	logger.Info("TCP proxy server listening", "port", cfg.Port, "target", targetAddr)

	go func() {
		<-ctx.Done()
		logger.V(2).Info("Shutting down TCP proxy server")
		_ = p.Close()
	}()

	waitErr := p.Wait()
	if ctx.Err() != nil {
		logger.V(2).Info("TCP proxy server stopped after context cancellation", "waitErr", waitErr)
		return nil
	}
	if waitErr != nil {
		logger.Error(waitErr, "TCP proxy server stopped unexpectedly")
		return waitErr
	}
	logger.Info("TCP proxy server stopped")
	return nil
}

// Configure handles the proxy configuration HTTP endpoint at ProxyConfigPath.
//
// GET returns the configured target (200) or 404 if not yet configured.
// PUT delivers the backend target. PUT may succeed only once; subsequent PUTs
// return 409. PUT does not return until the proxy is actively listening, so
// callers can rely on a 200 response to mean the proxy is ready to accept
// connections.
func Configure(w http.ResponseWriter, r *http.Request) {
	initState()
	logger := klog.FromContext(r.Context()).WithName("proxy-server")

	switch r.Method {
	case http.MethodGet:
		stateMu.Lock()
		cur := target
		stateMu.Unlock()
		if cur == nil {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte("proxy not configured"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(cur)
		return
	case http.MethodPut:
		// fall through
	default:
		http.Error(w, "method not allowed, use GET or PUT", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to read request body: %v", err), http.StatusBadRequest)
		return
	}
	defer func() { _ = r.Body.Close() }()

	var cfg stubapi.ProxyTargetConfig
	if err := json.Unmarshal(body, &cfg); err != nil {
		http.Error(w, fmt.Sprintf("failed to parse JSON: %v", err), http.StatusBadRequest)
		return
	}
	if cfg.Address == "" {
		http.Error(w, "address is required", http.StatusBadRequest)
		return
	}
	if cfg.Port == 0 {
		http.Error(w, "invalid port", http.StatusBadRequest)
		return
	}

	stateMu.Lock()
	if target != nil {
		stateMu.Unlock()
		http.Error(w, "proxy already configured", http.StatusConflict)
		return
	}
	target = &cfg
	stateMu.Unlock()

	logger.V(2).Info("Delivering proxy target to Run", "address", cfg.Address, "port", cfg.Port)

	select {
	case configCh <- &cfg:
	case <-r.Context().Done():
		http.Error(w, "request cancelled before proxy could receive target", http.StatusServiceUnavailable)
		return
	}

	select {
	case <-startedCh:
		stateMu.Lock()
		sErr := startErr
		stateMu.Unlock()
		if sErr != nil {
			http.Error(w, fmt.Sprintf("proxy failed to start: %v", sErr), http.StatusInternalServerError)
			return
		}
	case <-r.Context().Done():
		http.Error(w, "request cancelled before proxy started listening", http.StatusServiceUnavailable)
		return
	}

	targetAddr := net.JoinHostPort(cfg.Address, fmt.Sprintf("%d", cfg.Port))
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(w, "configured proxy to: %s", targetAddr)
}
