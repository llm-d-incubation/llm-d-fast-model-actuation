package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

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
	config, err := rest.InClusterConfig()
	if err == nil {
		logger.V(1).Info("Successfully loaded in-cluster config")
		return config, nil
	}

	logger.V(1).Info("In-cluster config not found, falling back to local kubeconfig")
	kubeconfigPath := os.Getenv("KUBECONFIG")
	if kubeconfigPath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("failed to get user home directory: %w", err)
		}
		kubeconfigPath = filepath.Join(home, ".kube", "config")
	}
	config, err = clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	if err != nil {
		return nil, fmt.Errorf("unable to load kubeconfig from %s: %w", kubeconfigPath, err)
	}
	logger.V(1).Info("Successfully loaded kubeconfig", "path", kubeconfigPath)
	return config, nil
}
