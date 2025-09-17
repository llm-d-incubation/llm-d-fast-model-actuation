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

package spi

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"k8s.io/klog/v2"
)

// ipPutHandler receives the IP address of the inference server pod,
// and sends it to the provided channel.
func ipPutHandler(ipCh chan<- string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Type") != "text/plain" {
			http.Error(w, "Unsupported Media Type", http.StatusUnsupportedMediaType)
			return
		}

		reader, err := io.ReadAll(r.Body)
		defer r.Body.Close() //nolint:errcheck

		if err != nil {
			http.Error(w, "Error reading body", http.StatusBadRequest)
			return
		}

		ip := strings.TrimSpace(string(reader))

		if net.ParseIP(ip) == nil {
			http.Error(w, fmt.Sprintf("Invalid IP address %s", ip), http.StatusBadRequest)
			return
		}

		ipCh <- ip

		w.WriteHeader(http.StatusOK) // not strictly necessary, but explicit
		fmt.Fprintln(w, "OK")        //nolint:errcheck
	}
}

// ipDeleteHandler unsets the IP address of the inference server pod
// by sending an empty string to the provided channel.
func ipDeleteHandler(ipCh chan<- string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ipCh <- ""

		w.WriteHeader(http.StatusOK)
	}
}

// Start starts an HTTP server managing the endpoints
// consumed by the dual-pod controller.
func Start(ctx context.Context, port string, ipCh chan<- string) error {
	logger := klog.FromContext(ctx).WithName("spi-server")

	logger.Info("starting server", "port", port)

	mux := http.NewServeMux()
	mux.HandleFunc("PUT /ip", ipPutHandler(ipCh))
	mux.HandleFunc("DELETE /ip", ipDeleteHandler(ipCh))

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

	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("listen and serve error: %w", err)
	}

	logger.Info("server stopped")
	return nil
}
