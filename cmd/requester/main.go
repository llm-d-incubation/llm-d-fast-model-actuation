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
	"sync"

	"github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/server/requester/probes"
	"github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/server/requester/spi"
	"sigs.k8s.io/controller-runtime/pkg/manager/signals"

	"k8s.io/klog/v2"
)

func main() {
	klog.InitFlags(nil)
	flag.Parse()

	// set up signals so we handle the shutdown signal gracefully
	ctx := signals.SetupSignalHandler()
	logger := klog.FromContext(ctx)

	// Read ports from environment variables, fallback to defaults
	probesPort := os.Getenv("PROBES_PORT")
	if probesPort == "" {
		probesPort = "8080"
	}

	spiPort := os.Getenv("SPI_PORT")
	if spiPort == "" {
		spiPort = "8081"
	}

	ipCh := make(chan string)

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()

		err := spi.Start(ctx, spiPort, ipCh)
		if err != nil {
			logger.Error(err, "failed to start requester SPI server")
		}
	}()

	// Start the readiness probe server
	go func() {
		defer wg.Done()

		err := probes.Start(ctx, probesPort, ipCh)
		if err != nil {
			logger.Error(err, "failed to start requester probes server")
		}
	}()

	wg.Wait()
}
