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
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"k8s.io/klog/v2"

	stubapi "github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/stub/api"
)

// gpuHandler responds with the list of allocated GPU UUIDs
func gpuHandler(gpuUUIDs []string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if len(gpuUUIDs) == 0 {
			http.Error(w, "no GPUs found", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(gpuUUIDs); err != nil {
			http.Error(w, fmt.Sprintf("error encoding response: %v", err), http.StatusInternalServerError)
			return
		}
	}
}

func getGpuUUIDs() ([]string, error) {
	cmd := exec.Command("nvidia-smi", "--query-gpu=uuid", "--format=csv,noheader")
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	var uuids []string
	for _, line := range lines {
		if line != "" {
			uuids = append(uuids, line)
		}
	}
	if len(uuids) == 0 {
		return nil, fmt.Errorf("no GPUs found")
	}

	return uuids, nil
}

// Run starts an HTTP server managing the endpoints
// consumed by the dual-pod controller.
func Run(ctx context.Context, port string, ready *atomic.Bool) error {
	logger := klog.FromContext(ctx).WithName("spi-server")

	gpuUUIDs, err := getGpuUUIDs()
	if err != nil {
		logger.Error(err, "failed to get GPU UUIDs")
	}
	if len(gpuUUIDs) == 0 {
		logger.Error(fmt.Errorf("no GPUs found"), "no GPU UUIDs available")
	} else {
		logger.Info("Got GPU UUIDs", "uuids", gpuUUIDs)
	}
	return RunWithGPUUUIDs(ctx, port, ready, gpuUUIDs)
}

func gpuMemoryHandler(logger klog.Logger) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close() //nolint:errcheck
		usageMap := map[string]int64{}
		errs := []string{}
		cmd := exec.Command("nvidia-smi", "--query-gpu=uuid,memory.used", "--format=csv,csv,noheader,nounits")
		output, err := cmd.Output()
		if err != nil {
			errs = []string{err.Error()}
		} else {
			lines := strings.Split(strings.TrimSpace(string(output)), "\n")
			for _, line := range lines {
				if line == "" {
					continue
				}
				parts := strings.Split(line, ",")
				if len(parts) != 2 {
					errs = append(errs, fmt.Sprintf("got line %q with wrong number of data", line))
				} else {
					usedStr := strings.TrimSpace(parts[1])
					memUsed, err := strconv.ParseInt(usedStr, 10, 64)
					if err != nil {
						errs = append(errs, fmt.Sprintf("failed to parse int %q on line %q: %s", usedStr, line, err.Error()))
					} else {
						usageMap[parts[0]] = memUsed
					}
				}
			}
		}
		usageJSON, err := json.Marshal(usageMap)
		if err != nil {
			errs = append(errs, fmt.Sprintf("failed to marshal usage map %#v: %s", usageMap, err.Error()))
		}
		if len(errs) > 0 {
			logger.Error(nil, "Returning errors to memory query", "errs", errs)
			w.WriteHeader(http.StatusInternalServerError)
			for _, err := range errs {
				_, _ = fmt.Fprintln(w, err)
			}
		} else {
			logger.Info("Returning memory usage", "usageMap", usageMap)
			w.WriteHeader(http.StatusOK) // not strictly necessary, but explicit
			_, _ = w.Write(usageJSON)
		}
	}
}

func newSetReadyHandler(logger klog.Logger, ready *atomic.Bool, newReady bool) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close() //nolint:errcheck
		logger.Info("Setting ready", "newReady", newReady)
		ready.Store(newReady)
		w.WriteHeader(http.StatusOK) // not strictly necessary, but explicit
		fmt.Fprintln(w, "OK")        //nolint:errcheck
	}
}

func RunWithGPUUUIDs(ctx context.Context, port string, ready *atomic.Bool, gpuUUIDs []string) error {
	logger := klog.FromContext(ctx).WithName("spi-server")
	mux := http.NewServeMux()
	mux.HandleFunc(strings.Join([]string{"GET", stubapi.AcceleratorQueryPath}, " "), gpuHandler(gpuUUIDs))
	mux.HandleFunc(strings.Join([]string{"GET", stubapi.AcceleratorMemoryQueryPath}, " "), gpuMemoryHandler(logger))
	mux.HandleFunc("POST "+stubapi.BecomeReadyPath, newSetReadyHandler(logger, ready, true))
	mux.HandleFunc("POST "+stubapi.BecomeUnreadyPath, newSetReadyHandler(logger, ready, false))

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
