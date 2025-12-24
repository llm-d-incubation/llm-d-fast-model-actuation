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

	// 创建一个带取消信号的上下文
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 设置信号处理，用于优雅关闭
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

	// 启动 informers
	kubePreInformers.Start(ctx.Done())
	fmaPreInformers.Start(ctx.Done())

	// 在 goroutine 中启动控制器
	go func() {
		if err := ctlr.Start(ctx); err != nil {
			klog.ErrorS(err, "Controller failed to start")
			cancel() // 发生错误时取消上下文
		}
	}()

	// 等待终止信号或上下文取消
	select {
	case <-ctx.Done():
		logger.Info("Context cancelled, shutting down...")
	case sig := <-sigChan:
		logger.Info("Received signal, shutting down...", "signal", sig)
		cancel() // 收到信号时取消上下文
	}

	// 等待一段时间让资源优雅关闭
	time.Sleep(5 * time.Second)
}

func AddFlags(flags pflag.FlagSet, loadingRules *clientcmd.ClientConfigLoadingRules, overrides *clientcmd.ConfigOverrides) {
	flags.StringVar(&overrides.CurrentContext, "context", overrides.CurrentContext, "The name of the kubeconfig context to use")
	flags.StringVar(&overrides.Context.AuthInfo, "user", overrides.Context.AuthInfo, "The name of the kubeconfig user to use")
	flags.StringVar(&overrides.Context.Cluster, "cluster", overrides.Context.Cluster, "The name of the kubeconfig cluster to use")
	flags.StringVarP(&overrides.Context.Namespace, "namespace", "n", overrides.Context.Namespace, "The name of the Kubernetes Namespace to work in (NOT optional)")
}
