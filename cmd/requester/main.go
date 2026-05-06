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
	"flag"
	"fmt"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/spf13/pflag"

	"github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/server/requester/coordination"
	"github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/server/requester/probes"
	"github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/server/requester/proxy"
	"sigs.k8s.io/controller-runtime/pkg/manager/signals"

	"k8s.io/klog/v2"
)

// requesterConfig holds all configurable port parameters.
type requesterConfig struct {
	ProbesPort int16
	SPIPort    int16
	Proxy      proxy.ProxyConfig
}

func addFlags(fs pflag.FlagSet, cfg *requesterConfig) {
	fs.Int16Var(&cfg.ProbesPort, "probes-port", cfg.ProbesPort, "port number for readiness/liveness probes")
	fs.Int16Var(&cfg.SPIPort, "spi-port", cfg.SPIPort, "port for dual-pods SPI requests")
	cfg.Proxy.AddFlags(fs)
}

func main() {
	cfg := requesterConfig{
		ProbesPort: 8080,
		SPIPort:    8081,
		Proxy:      proxy.DefaultProxyConfig,
	}

	klog.InitFlags(nil)
	pflag.CommandLine.AddGoFlagSet(flag.CommandLine)
	addFlags(*pflag.CommandLine, &cfg)
	pflag.Parse()

	// Override from environment variables if not explicitly set via flags
	if p := os.Getenv("PROBES_PORT"); p != "" {
		if port, err := strconv.ParseInt(p, 10, 16); err == nil {
			cfg.ProbesPort = int16(port)
		} else {
			fmt.Fprintf(os.Stderr, "invalid PROBES_PORT environment variable: %q\n", p)
			os.Exit(1)
		}
	}
	if p := os.Getenv("SPI_PORT"); p != "" {
		if port, err := strconv.ParseInt(p, 10, 16); err == nil {
			cfg.SPIPort = int16(port)
		} else {
			fmt.Fprintf(os.Stderr, "invalid SPI_PORT environment variable: %q\n", p)
			os.Exit(1)
		}
	}
	if p := os.Getenv("PROXY_PORT"); p != "" {
		if port, err := strconv.ParseUint(p, 10, 16); err == nil {
			cfg.Proxy.Port = uint16(port)
		} else {
			fmt.Fprintf(os.Stderr, "invalid PROXY_PORT environment variable: %q\n", p)
			os.Exit(1)
		}
	}
	if d := os.Getenv("PROXY_DIAL_TIMEOUT"); d != "" {
		if dur, err := time.ParseDuration(d); err == nil {
			cfg.Proxy.DialTimeout = dur
		} else {
			fmt.Fprintf(os.Stderr, "invalid PROXY_DIAL_TIMEOUT environment variable: %q\n", d)
			os.Exit(1)
		}
	}

	// set up signals so we handle the shutdown signal gracefully
	ctx := signals.SetupSignalHandler()
	logger := klog.FromContext(ctx)

	pflag.CommandLine.VisitAll(func(f *pflag.Flag) {
		logger.V(1).Info("Config", "flag", f.Name, "value", f.Value.String())
	})

	var ready atomic.Bool

	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()

		err := coordination.Run(ctx, strconv.FormatInt(int64(cfg.SPIPort), 10), &ready, os.Stdout)
		if err != nil {
			logger.Error(err, "failed to run requester SPI server")
		}
	}()

	// Start the readiness probe server
	wg.Add(1)
	go func() {
		defer wg.Done()

		err := probes.Run(ctx, strconv.FormatInt(int64(cfg.ProbesPort), 10), &ready)
		if err != nil {
			logger.Error(err, "failed to run requester probes server")
		}
	}()

	// Start the reverse proxy server
	wg.Add(1)
	go func() {
		defer wg.Done()

		err := proxy.Run(ctx, cfg.Proxy)
		if err != nil {
			logger.Error(err, "failed to run requester proxy server")
		}
	}()

	wg.Wait()
}
