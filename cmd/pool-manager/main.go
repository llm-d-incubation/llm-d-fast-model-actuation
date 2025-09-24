/*
Copyright 2025 The llm-d Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing,
software distributed under the License is distributed on an
"AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND,
either express or implied.  See the License for the specific
language governing permissions and limitations under the License.
*/

package main

import (
	"context"
	"flag"
	"log"

	"github.com/spf13/pflag"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"

	pmctlr "github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/controller/poolmanager"
)

func main() {
	numLaunchers := 5
	port := 8080
	namespace := "default"

	klog.InitFlags(flag.CommandLine)
	pflag.CommandLine.AddGoFlagSet(flag.CommandLine)
	pflag.CommandLine.StringVar(&namespace, "namespace", namespace, "Namespace for idle launcher pool")
	pflag.CommandLine.IntVar(&numLaunchers, "num-launchers", numLaunchers, "Number of idle launcher pods in the pool")
	pflag.CommandLine.IntVar(&port, "port", port, "Port for pool manager HTTP server")

	pflag.Parse()
	ctx := context.Background()
	logger := klog.FromContext(ctx)

	pflag.CommandLine.VisitAll(func(f *pflag.Flag) {
		logger.V(1).Info("Flag", "name", f.Name, "value", f.Value.String())
	})

	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	overrides := &clientcmd.ConfigOverrides{}
	restConfig, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, overrides).ClientConfig()
	if err != nil {
		klog.Fatal(err)
	}
	if len(restConfig.UserAgent) == 0 {
		restConfig.UserAgent = pmctlr.ManagerName
	} else {
		restConfig.UserAgent += "/" + pmctlr.ManagerName
	}

	kubeClient := kubernetes.NewForConfigOrDie(restConfig)

	mgr, err := pmctlr.NewPoolManager(
		logger,
		kubeClient.CoreV1(),
		namespace,
		numLaunchers)
	if err != nil {
		klog.Fatal(err)
	}

	log.Printf("Starting Pool Manager with pool size %d", numLaunchers)
	if err := mgr.Serve(port); err != nil {
		logger.V(1).Error(err, "pool manager failed")
		klog.Fatal(err)
	}
}
