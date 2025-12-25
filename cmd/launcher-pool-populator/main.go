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
	"os"
	"os/signal"
	"syscall"
	"time"

	launcherpool "github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/controller/launcher-pool"
	"github.com/spf13/pflag"

	kubeinformers "k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"

	fmaclient "github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/generated/clientset/versioned"
	fmainformers "github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/generated/informers/externalversions"
)

func main() {
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	overrides := &clientcmd.ConfigOverrides{}

	klog.InitFlags(flag.CommandLine)
	pflag.CommandLine.AddGoFlagSet(flag.CommandLine)
	AddFlags(*pflag.CommandLine, loadingRules, overrides)
	pflag.Parse()

	// Create a context with cancellation signal
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Set up signal handling for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(sigChan)

	logger := klog.FromContext(ctx)

	logger.V(1).Info("Start", "time", time.Now())

	pflag.CommandLine.VisitAll(func(f *pflag.Flag) {
		logger.V(1).Info("Flag", "name", f.Name, "value", f.Value.String())
	})

	if len(overrides.Context.Namespace) == 0 {
		fmt.Fprintln(os.Stderr, "Namespace must not be the empty string")
		os.Exit(1)
	} else {
		logger.Info("Focusing on one namespace", "name", overrides.Context.Namespace)
	}

	restConfig, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, overrides).ClientConfig()
	if err != nil {
		klog.Fatal(err)
	}
	if len(restConfig.UserAgent) == 0 {
		restConfig.UserAgent = launcherpool.ControllerName
	} else {
		restConfig.UserAgent += "/" + launcherpool.ControllerName
	}

	kubeClient := kubernetes.NewForConfigOrDie(restConfig)
	kubePreInformers := kubeinformers.NewSharedInformerFactoryWithOptions(kubeClient, 0, kubeinformers.WithNamespace(overrides.Context.Namespace))
	fmaClient := fmaclient.NewForConfigOrDie(restConfig)
	fmaPreInformers := fmainformers.NewSharedInformerFactoryWithOptions(fmaClient, 0, fmainformers.WithNamespace(overrides.Context.Namespace))

	ctlr, err := launcherpool.NewController(
		logger,
		kubeClient.CoreV1(),
		overrides.Context.Namespace,
		kubePreInformers.Core().V1(),
		fmaPreInformers,
	)
	if err != nil {
		klog.Fatal(err)
	}

	// Start informers
	kubePreInformers.Start(ctx.Done())
	fmaPreInformers.Start(ctx.Done())

	// Start the controller in a goroutine
	go func() {
		if err := ctlr.Start(ctx); err != nil {
			klog.ErrorS(err, "Controller failed to start")
			cancel() // Cancel the context when an error occurs
		}
	}()

	// Wait for termination signal or context cancellation
	select {
	case <-ctx.Done():
		logger.Info("Context cancelled, shutting down...")
	case sig := <-sigChan:
		logger.Info("Received signal, shutting down...", "signal", sig)
		cancel() // Cancel the context when receiving a signal
	}

	// Wait for a period to allow resources to shut down gracefully
	time.Sleep(5 * time.Second)
}

func AddFlags(flags pflag.FlagSet, loadingRules *clientcmd.ClientConfigLoadingRules, overrides *clientcmd.ConfigOverrides) {
	flags.StringVar(&overrides.CurrentContext, "context", overrides.CurrentContext, "The name of the kubeconfig context to use")
	flags.StringVar(&overrides.Context.AuthInfo, "user", overrides.Context.AuthInfo, "The name of the kubeconfig user to use")
	flags.StringVar(&overrides.Context.Cluster, "cluster", overrides.Context.Cluster, "The name of the kubeconfig cluster to use")
	flags.StringVarP(&overrides.Context.Namespace, "namespace", "n", overrides.Context.Namespace, "The name of the Kubernetes Namespace to work in (NOT optional)")
}
