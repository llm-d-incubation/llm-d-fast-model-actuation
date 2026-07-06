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
	// Port is the port the proxy listens on.
	Port uint16
	// DialTimeout is the timeout for dialing backend connections.
	DialTimeout time.Duration
}

// DefaultProxyConfig provides sensible defaults.
var DefaultProxyConfig = ProxyConfig{
	Port:        8082,
	DialTimeout: 10 * time.Second,
}

// AddFlags registers command-line flags for all proxy configuration fields.
func (cfg *ProxyConfig) AddFlags(fs *pflag.FlagSet) {
	fs.Uint16Var(&cfg.Port, "proxy-port", cfg.Port, "port for TCP proxy")
	fs.DurationVar(&cfg.DialTimeout, "proxy-dial-timeout", cfg.DialTimeout, "timeout for proxy backend dial")
}

// Server is a TCP reverse proxy that waits for a backend target to be
// delivered via Configure, then forwards all connections to that target.
type Server struct {
	cfg ProxyConfig

	// configCh carries the target from Configure to Run; sent on at most once.
	configCh chan stubapi.ProxyTargetConfig
	// startedCh carries the result of starting the listener (nil on success).
	// Run sends exactly one value then closes: nil on success, an error on
	// Start failure or shutdown-before-configuration.
	startedCh chan error

	// target is the most recently delivered backend target; guarded by stateMu
	// because GET and the duplicate-PUT check race with the first PUT writer.
	stateMu sync.Mutex
	target  *stubapi.ProxyTargetConfig
}

// New creates a Server with the given configuration.
func New(cfg ProxyConfig) *Server {
	return &Server{
		cfg:       cfg,
		configCh:  make(chan stubapi.ProxyTargetConfig, 1),
		startedCh: make(chan error, 1),
	}
}

// Run starts the TCP proxy server. It blocks waiting for Configure to deliver
// the backend target, then constructs and starts a tcpproxy.Proxy listening on
// cfg.Port. It returns when the context is cancelled or the proxy stops.
//
// Run and Configure may be invoked in either order from different goroutines.
func (s *Server) Run(ctx context.Context) error {
	logger := klog.FromContext(ctx).WithName("proxy-server")
	logger.Info("Starting TCP proxy server", "port", s.cfg.Port, "dialTimeout", s.cfg.DialTimeout)

	var tgt stubapi.ProxyTargetConfig
	select {
	case tgt = <-s.configCh:
		logger.V(2).Info("Received proxy target from Configure", "target", tgt)
	case <-ctx.Done():
		logger.V(2).Info("Context cancelled before proxy was configured")
		s.startedCh <- fmt.Errorf("proxy shutdown cancelled before configuration")
		close(s.startedCh)
		return nil
	}

	targetAddr := tgt.String()
	p := &tcpproxy.Proxy{}
	p.AddRoute(fmt.Sprintf(":%d", s.cfg.Port), &tcpproxy.DialProxy{
		Addr:        targetAddr,
		DialTimeout: s.cfg.DialTimeout,
	})

	if err := p.Start(); err != nil {
		startErr := fmt.Errorf("failed to start proxy listener on port %d: %w", s.cfg.Port, err)
		s.startedCh <- startErr
		close(s.startedCh)
		logger.Error(err, "Failed to start TCP proxy listener", "port", s.cfg.Port)
		return startErr
	}
	s.startedCh <- nil
	close(s.startedCh)
	logger.Info("TCP proxy server listening", "port", s.cfg.Port, "target", targetAddr)

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
func (s *Server) Configure(w http.ResponseWriter, r *http.Request) {
	logger := klog.FromContext(r.Context()).WithName("proxy-server")

	switch r.Method {
	case http.MethodGet:
		s.stateMu.Lock()
		cur := s.target
		s.stateMu.Unlock()
		if cur == nil {
			http.Error(w, "proxy not configured", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(cur)
		return
	case http.MethodPut:
		// fall through to body handling below
	default:
		http.Error(w, "method not allowed, use GET or PUT", http.StatusMethodNotAllowed)
		return
	}

	defer func() { _ = r.Body.Close() }()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to read request body: %v", err), http.StatusBadRequest)
		return
	}

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

	s.stateMu.Lock()
	if s.target != nil {
		s.stateMu.Unlock()
		http.Error(w, "proxy already configured", http.StatusConflict)
		return
	}
	s.target = &cfg
	s.stateMu.Unlock()

	logger.V(2).Info("Delivering proxy target to Run", "target", cfg)

	// configCh has cap=1 and is reachable at most once per process lifetime,
	// so the send never blocks.
	s.configCh <- cfg

	// Wait for Run to confirm the listener is up. Abort if the request
	// is cancelled; Run always sends on startedCh (nil, start error, or
	// shutdown error), so this select is guaranteed to unblock.
	select {
	case startErr := <-s.startedCh:
		if startErr != nil {
			http.Error(w, fmt.Sprintf("proxy failed to start: %v", startErr), http.StatusInternalServerError)
			return
		}
	case <-r.Context().Done():
		http.Error(w, "request cancelled before proxy started listening", http.StatusServiceUnavailable)
		return
	}

	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(w, "configured proxy to: %s", cfg.String())
}
