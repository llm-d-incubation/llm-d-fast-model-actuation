/*
Copyright 2025 The llm-d Authors.

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
	"net/http/httputil"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"k8s.io/klog/v2"
)

// ConfigRequest is the request body to configure the proxy target
type ConfigRequest struct {
	Address string `json:"address"`
	Port    int    `json:"port"`
}

// proxy is a lazy HTTP reverse proxy that only starts after receiving
// the first configuration request
type proxy struct {
	mu          sync.RWMutex
	targetURL   *url.URL
	proxy       *httputil.ReverseProxy
	initialized atomic.Bool
}

// singleton instance initialized once at startup
var instance = &proxy{}

// Run starts the proxy server on the given port
func Run(ctx context.Context, port string) error {
	logger := klog.FromContext(ctx).WithName("proxy-server")
	logger.Info("starting proxy server")

	mux := http.NewServeMux()
	mux.HandleFunc("/", serveProxy)

	server := &http.Server{
		Addr:         fmt.Sprintf(":%s", port),
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 5 * time.Minute, // Long timeout for inference requests
		IdleTimeout:  120 * time.Second,
	}

	go func() {
		<-ctx.Done()
		logger.Info("shutting down")

		ctx, cancelFn := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancelFn()
		if err := server.Shutdown(ctx); err != nil {
			logger.Error(err, "failed to gracefully shutdown")
		}
	}()

	logger.Info("starting server", "port", port)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("listen and serve error: %w", err)
	}

	logger.Info("server stopped")
	return nil
}

// serveProxy proxies requests to the target server
func serveProxy(w http.ResponseWriter, r *http.Request) {
	if !instance.initialized.Load() {
		http.Error(w, "proxy not initialized", http.StatusServiceUnavailable)
		return
	}

	// Proxy the request
	instance.proxy.ServeHTTP(w, r)
}

// Initialize handles proxy initialization and configuration
func Initialize(w http.ResponseWriter, r *http.Request) {
	// Get proxy status
	if r.Method == http.MethodGet {
		if instance.initialized.Load() {
			targetURL := instance.targetURL
			w.WriteHeader(http.StatusOK)
			if targetURL != nil {
				_, _ = fmt.Fprintf(w, "proxying to %s", targetURL)
			} else {
				_, _ = w.Write([]byte("proxy initialized but targetURL is nil"))
			}
		} else {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("proxy not initialized"))
		}
		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, "invalid method", http.StatusMethodNotAllowed)
		return
	}

	// Try initialize server
	if instance.initialized.Load() {
		http.Error(w, "proxy already intialized", http.StatusConflict)
		return
	}

	// Need to initialize - acquire write lock
	instance.mu.Lock()
	defer instance.mu.Unlock()

	// Double-check after acquiring write lock
	if instance.initialized.Load() {
		http.Error(w, "proxy already intialized", http.StatusConflict)
		return
	}

	// Parse configuration from request body
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to read request body: %v", err), http.StatusBadRequest)
		return
	}
	defer func() { _ = r.Body.Close() }()

	var config ConfigRequest
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

	// Create target URL
	targetURL := &url.URL{
		Scheme: "http",
		Host:   net.JoinHostPort(config.Address, fmt.Sprintf("%d", config.Port)),
	}

	// Create the reverse proxy
	instance.targetURL = targetURL
	instance.proxy = httputil.NewSingleHostReverseProxy(targetURL)

	// Customize error handling
	originalErrorHandler := instance.proxy.ErrorHandler
	instance.proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		if originalErrorHandler != nil {
			originalErrorHandler(w, r, err)
		} else {
			http.Error(w, fmt.Sprintf("proxy error: %v", err), http.StatusBadGateway)
		}
	}

	instance.initialized.Store(true)
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(w, "initialized proxy to: %s", targetURL)
}
