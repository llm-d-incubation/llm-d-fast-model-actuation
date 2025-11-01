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

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/api"
	"github.com/spf13/pflag"

	"k8s.io/klog/v2"
)

func main() {
	port := int16(8000)
	startupDelaySecs := int(47)

	klog.InitFlags(nil)
	pflag.CommandLine.AddGoFlagSet(flag.CommandLine)
	pflag.CommandLine.Int16Var(&port, "port", port, "port at which to listen for HTTP connections")
	pflag.CommandLine.IntVar(&startupDelaySecs, "startup-delay", startupDelaySecs, "number of seconds to delay before positive responses to /health")

	pflag.Parse()

	healthyTime := time.Now().Add(time.Duration(startupDelaySecs) * time.Second)
	healthyTimeStr := healthyTime.Format(time.RFC3339)

	ctx := context.Background()
	logger := klog.FromContext(ctx)

	pflag.CommandLine.VisitAll(func(f *pflag.Flag) {
		logger.V(1).Info("Flag", "name", f.Name, "value", f.Value.String())
	})

	var sleeping atomic.Bool
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		if time.Now().After(healthyTime) {
			w.WriteHeader(http.StatusOK) // not strictly necessary, but explicit
			w.Header().Set("Content-Type", "text/plain")
			_, _ = fmt.Fprintln(w, "OK")
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Header().Set("Content-Type", "text/plain")
			_, _ = fmt.Fprintf(w, "Service Unavailable until %s\n", healthyTimeStr)
		}
	})
	mux.HandleFunc("GET /is_sleeping", func(w http.ResponseWriter, r *http.Request) {
		ss := api.SleepState{IsSleeping: sleeping.Load()}
		ssBytes, err := json.Marshal(&ss)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			w.Header().Set("Content-Type", "text/plain")
			_, _ = fmt.Fprintf(w, "Failed to marshal state %#v: %s", ss, err.Error())
		} else {
			w.WriteHeader(http.StatusOK) // not strictly necessary, but explicit
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(ssBytes)
		}
	})
	mux.HandleFunc("POST /sleep", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK) // not strictly necessary, but explicit
		sleeping.Store(true)
		logger.Info("Set sleeping=true")
	})
	mux.HandleFunc("POST /wake_up", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK) // not strictly necessary, but explicit
		sleeping.Store(false)
		logger.Info("Set sleeping=false")
	})

	server := &http.Server{
		Addr:    ":" + strconv.FormatInt(int64(port), 10),
		Handler: mux,
	}

	logger.Info("Starting server", "port", port)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Error(err, "Listen and serve failed")
	}

	logger.Info("Server stopped")
}
