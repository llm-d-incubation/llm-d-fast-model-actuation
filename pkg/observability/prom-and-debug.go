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

package observability

import (
	"context"
	"fmt"
	"net"
	"net/http"
	_ "net/http/pprof"

	"github.com/spf13/pflag"

	"k8s.io/component-base/metrics/legacyregistry"
	_ "k8s.io/component-base/metrics/prometheus/clientgo"
	_ "k8s.io/component-base/metrics/prometheus/version"
	"k8s.io/klog/v2"
)

type Options struct {
	MetricsPort uint16
	DebugPort   uint16
}

func DefaultOptions() Options {
	return Options{
		MetricsPort: 8002,
		DebugPort:   8003,
	}
}

func (opts *Options) AddToFlagSet(fs *pflag.FlagSet) {
	fs.Uint16Var(&opts.MetricsPort, "metrics-port", opts.MetricsPort, "HTTP port that serves Prometheus /metrics")
	fs.Uint16Var(&opts.DebugPort, "debug-port", opts.DebugPort, "HTTP port for Go /debug/pprof")
}

func (opts *Options) Start(ctx context.Context) {
	logger := klog.FromContext(ctx)
	metMux := http.NewServeMux()
	metMux.Handle("GET /metrics", legacyregistry.Handler())
	metricsServer := http.Server{
		Addr:        fmt.Sprintf(":%d", opts.MetricsPort),
		Handler:     metMux,
		BaseContext: func(l net.Listener) context.Context { return ctx },
	}
	go func() {
		err := metricsServer.ListenAndServe()
		if err != nil {
			logger.Error(err, "Failed to serve Prometheus metrics", "port", opts.MetricsPort)
		}
	}()

	// debMux := http.NewServeMux()
	// debMux.Handle("GET /debug/pprof", http.HandlerFunc(pprof.Index))
	debugServer := http.Server{
		Addr:        fmt.Sprintf(":%d", opts.DebugPort),
		Handler:     http.DefaultServeMux,
		BaseContext: func(l net.Listener) context.Context { return ctx },
	}
	go func() {
		err := debugServer.ListenAndServe()
		if err != nil {
			logger.Error(err, "Failed to serve /debug/pprof", "port", opts.DebugPort)
		}
	}()

}
