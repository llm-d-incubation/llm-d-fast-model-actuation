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

package common

import (
	"github.com/spf13/pflag"
	"k8s.io/client-go/tools/clientcmd"
)

// AddKubernetesClientFlags adds Kubernetes client configuration flags to the provided FlagSet.
// This function allows users to specify kubeconfig file path, context, user, cluster, and namespace
// through command line flags for Kubernetes client configuration.
//
// Parameters:
//   - flags: A pflag.FlagSet to which the Kubernetes client flags will be added
//   - loadingRules: A pointer to clientcmd.ClientConfigLoadingRules that contains rules for loading kubeconfig
//   - overrides: A pointer to clientcmd.ConfigOverrides that contains configuration overrides
func AddKubernetesClientFlags(flags pflag.FlagSet, loadingRules *clientcmd.ClientConfigLoadingRules, overrides *clientcmd.ConfigOverrides) {
	if loadingRules == nil || overrides == nil {
		return
	}

	flags.StringVar(&loadingRules.ExplicitPath, "kubeconfig", loadingRules.ExplicitPath, "Path to the kubeconfig file to use")
	flags.StringVar(&overrides.CurrentContext, "context", overrides.CurrentContext, "The name of the kubeconfig context to use")
	flags.StringVar(&overrides.Context.AuthInfo, "user", overrides.Context.AuthInfo, "The name of the kubeconfig user to use")
	flags.StringVar(&overrides.Context.Cluster, "cluster", overrides.Context.Cluster, "The name of the kubeconfig cluster to use")
	flags.StringVarP(&overrides.Context.Namespace, "namespace", "n", overrides.Context.Namespace, "The name of the Kubernetes Namespace to work in (NOT optional)")
}
