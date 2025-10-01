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

	"github.com/spf13/pflag"

	kubeinformers "k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"

	dpctlr "github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/controller/dual-pods"
)

func main() {
	numWorkers := 2
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	overrides := &clientcmd.ConfigOverrides{}

	klog.InitFlags(flag.CommandLine)
	pflag.CommandLine.AddGoFlagSet(flag.CommandLine)
	pflag.CommandLine.IntVar(&numWorkers, "num-workers", numWorkers, "number of queue worker goroutines")
	AddFlags(*pflag.CommandLine, loadingRules, overrides)
	pflag.Parse()
	ctx := context.Background()
	logger := klog.FromContext(ctx)

	pflag.CommandLine.VisitAll(func(f *pflag.Flag) {
		logger.V(1).Info("Flag", "name", f.Name, "value", f.Value.String())
	})

	restConfig, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, overrides).ClientConfig()
	if err != nil {
		klog.Fatal(err)
	}
	if len(restConfig.UserAgent) == 0 {
		restConfig.UserAgent = dpctlr.ControllerName
	} else {
		restConfig.UserAgent += "/" + dpctlr.ControllerName
	}

	kubeClient := kubernetes.NewForConfigOrDie(restConfig)
	if len(overrides.Context.Namespace) == 0 {
		fmt.Fprint(os.Stderr, "Namespace must not be the empty string")
		os.Exit(1)
	} else {
		logger.Info("Focusing on one namespace", "name", overrides.Context.Namespace)
	}
	kubePreInformers := kubeinformers.NewSharedInformerFactoryWithOptions(kubeClient, 0, kubeinformers.WithNamespace(overrides.Context.Namespace))

	ctlr, err := dpctlr.NewController(
		logger,
		kubeClient.CoreV1(),
		overrides.Context.Namespace,
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

func AddFlags(flags pflag.FlagSet, loadingRules *clientcmd.ClientConfigLoadingRules, overrides *clientcmd.ConfigOverrides) {
	flags.StringVar(&loadingRules.ExplicitPath, "kubeconfig", loadingRules.ExplicitPath, "Path to the kubeconfig file to use")
	flags.StringVar(&overrides.CurrentContext, "context", overrides.CurrentContext, "The name of the kubeconfig context to use")
	flags.StringVar(&overrides.Context.AuthInfo, "user", overrides.Context.AuthInfo, "The name of the kubeconfig user to use")
	flags.StringVar(&overrides.Context.Cluster, "cluster", overrides.Context.Cluster, "The name of the kubeconfig cluster to use")
	flags.StringVarP(&overrides.Context.Namespace, "namespace", "n", overrides.Context.Namespace, "The name of the Kubernetes Namespace to work in (NOT optional)")
}
