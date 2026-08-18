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
	"os"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/spf13/pflag"

	"sigs.k8s.io/controller-runtime/pkg/manager/signals"

	"k8s.io/klog/v2"

	"github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/server/requester/config"
	"github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/server/requester/coordination"
	"github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/server/requester/probes"
	"github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/server/requester/proxy"
)

func main() {
	cfg := config.NewDefault()

	klog.InitFlags(nil)
	pflag.CommandLine.AddGoFlagSet(flag.CommandLine)
	cfg.AddToFlagSet(pflag.CommandLine)
	pflag.Parse()

	// set up signals so we handle the shutdown signal gracefully
	ctx := signals.SetupSignalHandler()
	logger := klog.FromContext(ctx)

	pflag.CommandLine.VisitAll(func(f *pflag.Flag) {
		logger.V(1).Info("Flag", "name", f.Name, "value", f.Value.String())
	})

	var ready atomic.Bool
	proxySrv := proxy.New(cfg.Proxy)

	var wg sync.WaitGroup

	serveSPI, err := coordination.Start(ctx, strconv.FormatUint(uint64(cfg.SPIPort), 10), &ready, os.Stdout, proxySrv.Configure)
	if err != nil {
		logger.Error(err, "Failed to start requester SPI server")
		os.Exit(10)
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := serveSPI(); err != nil {
			logger.Error(err, "Failed to run requester SPI server")
		}
	}()

	// Start the readiness probe server
	serveProbes, err := probes.Start(ctx, strconv.FormatUint(uint64(cfg.ProbesPort), 10), &ready)
	if err != nil {
		logger.Error(err, "Failed to start requester probes server")
		os.Exit(11)
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := serveProbes(); err != nil {
			logger.Error(err, "failed to run requester probes server")
		}
	}()

	// Start the reverse proxy. Unlike the two servers above it has no listener
	// to open yet: it blocks until the controller PUTs a target.
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := proxySrv.Run(ctx); err != nil {
			logger.Error(err, "Failed to run requester proxy server")
		}
	}()

	wg.Wait()
}
