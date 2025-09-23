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

	stubapi "github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/stub/api"
)

// Run runs an HTTP server managing the /ready endpoint.
func Run(ctx context.Context, port string, ready *atomic.Bool) error {
	logger := klog.FromContext(ctx).WithName("probes-server")
	ctx = klog.NewContext(ctx, logger)

	mux := http.NewServeMux()
	mux.HandleFunc("GET "+stubapi.ReadyPath, func(w http.ResponseWriter, r *http.Request) {
		if ready.Load() {
			w.WriteHeader(http.StatusOK) // not strictly necessary, but explicit
			fmt.Fprintln(w, "OK")        //nolint:errcheck
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprintln(w, "Service Unavailable") //nolint:errcheck
		}
	})

	server := &http.Server{
		Addr:    ":" + port,
		Handler: mux,
	}

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

	logger.Info("starting server", "port", port)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("listen and serve error: %w", err)
	}

	logger.Info("server stopped")
	return nil
}
