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
	"flag"
	"fmt"

	"github.com/spf13/pflag"

	kubeinformers "k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"

	dpctlr "github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/dual-pods-controller"
)

func main() {
	numWorkers := 2

	klog.InitFlags(flag.CommandLine)
	pflag.CommandLine.AddGoFlagSet(flag.CommandLine)
	pflag.CommandLine.IntVar(&numWorkers, "num-workers", numWorkers, "number of queue worker goroutines")

	pflag.Parse()
	ctx := context.Background()
	logger := klog.FromContext(ctx)

	pflag.CommandLine.VisitAll(func(f *pflag.Flag) {
		logger.V(1).Info("Flag", "name", f.Name, "value", f.Value.String())
	})

	restConfig, err := getRestConfig(ctx)
	if err != nil {
		klog.Fatal(err)
	}
	if len(restConfig.UserAgent) == 0 {
		restConfig.UserAgent = dpctlr.ControllerName
	} else {
		restConfig.UserAgent += "/" + dpctlr.ControllerName
	}

	kubeClient := kubernetes.NewForConfigOrDie(restConfig)
	kubePreInformers := kubeinformers.NewSharedInformerFactory(kubeClient, 0)

	ctlr, err := dpctlr.NewController(
		logger,
		kubeClient.CoreV1(),
		kubePreInformers.Core().V1(),
		numWorkers,
	)
	if err != nil {
		klog.Fatal(err)
	}
	kubePreInformers.Start(ctx.Done())
	err = ctlr.Start(ctx)
	if err != nil {
		klog.Fatal(err)
	}
	<-ctx.Done()
}

func getRestConfig(ctx context.Context) (*rest.Config, error) {
	logger := klog.FromContext(ctx)
	if config, err := rest.InClusterConfig(); err == nil {
		logger.V(1).Info("Successfully loaded in-cluster config")
		return config, nil
	}

	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	overrides := &clientcmd.ConfigOverrides{}
	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, overrides).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to load kubeconfig: %w", err)
	}

	logger.V(1).Info("Successfully loaded out-of-cluster kubeconfig")
	return config, nil
}
