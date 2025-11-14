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

	apitypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/server/requester/coordination"
	"github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/server/requester/probes"
	"sigs.k8s.io/controller-runtime/pkg/manager/signals"

	"k8s.io/klog/v2"
)

// This variant requester emulates the GPU allocation behavior of
// the Kubernetes scheduler+kubelet.
// It takes a Node name and Pod IUD on the command line
// and keeps allocation decisions in a ConfigMap named "gpu-allocs".
// The `.data` of this ConfigMap maps GPU UID to the JSON seialization
// of a `GPUHolder`.
// Old allocations for the identified Pod, and any Pod that does not currently exist, are swept away.
// This main will create the allocation decisions ConfigMap if
// it does not already exist, and will keep trying to make
// the allocation until it succeeds.

const allocMapName = "gpu-allocs"

var agentName string

func main() {
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	overrides := &clientcmd.ConfigOverrides{}
	var nodeName, podUID string
	numGPUs := uint(1)
	probesPort := int16(8080)
	spiPort := int16(8081)

	klog.InitFlags(nil)
	pflag.CommandLine.AddGoFlagSet(flag.CommandLine)
	AddKubectlFlags(*pflag.CommandLine, loadingRules, overrides)
	pflag.CommandLine.StringVar(&nodeName, "node", nodeName, "name of this Pod's Node")
	pflag.CommandLine.StringVar(&podUID, "pod-uid", podUID, "UID of this Pod")
	pflag.CommandLine.UintVar(&numGPUs, "num-gpus", numGPUs, "number of GPUs to allocate")
	pflag.CommandLine.Int16Var(&probesPort, "probes-port", probesPort, "port number for /ready")
	pflag.CommandLine.Int16Var(&spiPort, "spi-port", spiPort, "port for dual-pods requests")

	pflag.Parse()

	if len(overrides.Context.Namespace) == 0 {
		fmt.Fprintln(os.Stderr, "Namespace must not be the empty string")
		os.Exit(1)
	}
	if len(nodeName) == 0 {
		fmt.Fprintln(os.Stderr, "--node must not be the empty string")
		os.Exit(1)
	}
	if len(podUID) == 0 {
		fmt.Fprintln(os.Stderr, "--pod-uid must not be the empty string")
		os.Exit(1)
	}

	// set up signals so we handle the shutdown signal gracefully
	ctx := signals.SetupSignalHandler()
	logger := klog.FromContext(ctx)

	pflag.CommandLine.VisitAll(func(f *pflag.Flag) {
		logger.V(1).Info("Flag", "name", f.Name, "value", f.Value.String())
	})

	restConfig, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, overrides).ClientConfig()
	if err != nil {
		klog.Fatal(err)
	}
	agentName = "test-requester/" + nodeName + "/" + strconv.FormatInt(time.Now().Unix(), 10)
	if len(restConfig.UserAgent) == 0 {
		restConfig.UserAgent = agentName
	} else {
		restConfig.UserAgent += "/" + agentName
	}

	kubeClient := kubernetes.NewForConfigOrDie(restConfig)
	gpuUUIDs := allocateGPUs(ctx, kubeClient.CoreV1(), nodeName, overrides.Context.Namespace, apitypes.UID(podUID), numGPUs)

	var ready atomic.Bool

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()

		err := coordination.RunWithGPUUUIDs(ctx, strconv.FormatInt(int64(spiPort), 10), &ready, gpuUUIDs)
		if err != nil {
			logger.Error(err, "failed to start requester SPI server")
		}
	}()

	// Start the readiness probe server
	go func() {
		defer wg.Done()

		err := probes.Run(ctx, strconv.FormatInt(int64(probesPort), 10), &ready)
		if err != nil {
			logger.Error(err, "failed to start requester probes server")
		}
	}()

	wg.Wait()
}

func AddKubectlFlags(flags pflag.FlagSet, loadingRules *clientcmd.ClientConfigLoadingRules, overrides *clientcmd.ConfigOverrides) {
	flags.StringVar(&loadingRules.ExplicitPath, "kubeconfig", loadingRules.ExplicitPath, "Path to the kubeconfig file to use")
	flags.StringVar(&overrides.CurrentContext, "context", overrides.CurrentContext, "The name of the kubeconfig context to use")
	flags.StringVar(&overrides.Context.AuthInfo, "user", overrides.Context.AuthInfo, "The name of the kubeconfig user to use")
	flags.StringVar(&overrides.Context.Cluster, "cluster", overrides.Context.Cluster, "The name of the kubeconfig cluster to use")
	flags.StringVarP(&overrides.Context.Namespace, "namespace", "n", overrides.Context.Namespace, "The name of the Kubernetes Namespace to work in (NOT optional)")
}
