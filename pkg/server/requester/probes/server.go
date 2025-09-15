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

package probes

import (
	"context"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"

	"k8s.io/klog/v2"
)

var (
	ready atomic.Bool
)

// readyGetHandler responds with 200 OK if the service is ready,
// otherwise 503 Service Unavailable.
func readyGetHandler(w http.ResponseWriter, r *http.Request) {
	if ready.Load() {
		w.WriteHeader(http.StatusOK) // not strictly necessary, but explicit
		fmt.Fprintln(w, "OK")        //nolint:errcheck
	} else {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintln(w, "Service Unavailable") //nolint:errcheck
	}
}

// watchForIPs listens for IP addresses on the provided channel and
// sets the readiness state accordingly.
func watchForIPs(ctx context.Context, ipCh <-chan string) {
	logger := klog.FromContext(ctx)

	for {
		select {
		case <-ctx.Done():
			logger.Info("context done, stopping IP reception")
			return
		case ip := <-ipCh:
			if ip == "" {
				ready.Store(false)
				logger.Info("received empty IP, setting readiness to false")

			} else {
				ready.Store(true)
				logger.Info("received IP, setting readiness to true", "ip", ip)
			}
		}
	}
}

// Start starts an HTTP server managing the /ready endpoint.
func Start(ctx context.Context, port string, ipCh <-chan string) error {
	logger := klog.FromContext(ctx).WithName("probes-server")
	ctx = klog.NewContext(ctx, logger)

	logger.Info("starting server", "port", port)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /ready", readyGetHandler)

	server := &http.Server{
		Addr:    ":" + port,
		Handler: mux,
	}

	// Goroutine to set readiness to true or false based on IP reception
	go watchForIPs(ctx, ipCh)

	// Setup graceful termination
	go func() {
		<-ctx.Done()
		logger.Info("shutting down")

		ctx, cancelFn := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancelFn()
		if err := server.Shutdown(ctx); err != nil {
			logger.Error(err, "failed to gracefully shutdown")
		}
	}()

	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("listen and serve error: %w", err)
	}

	logger.Info("server stopped")
	return nil
}
