package common

import (
	"github.com/spf13/pflag"
	"k8s.io/client-go/tools/clientcmd"
)

func AddKubernetesClientFlags(flags pflag.FlagSet, loadingRules *clientcmd.ClientConfigLoadingRules, overrides *clientcmd.ConfigOverrides) {
	if loadingRules == nil {
		return
	}
	if overrides == nil {
		return
	}

	// Check if the flag already exists to avoid duplicate definition
	if flags.Lookup("kubeconfig") == nil {
		flags.StringVar(&loadingRules.ExplicitPath, "kubeconfig", loadingRules.ExplicitPath, "Path to the kubeconfig file to use")
	}
	if flags.Lookup("context") == nil {
		flags.StringVar(&overrides.CurrentContext, "context", overrides.CurrentContext, "The name of the kubeconfig context to use")
	}
	if flags.Lookup("user") == nil {
		flags.StringVar(&overrides.Context.AuthInfo, "user", overrides.Context.AuthInfo, "The name of the kubeconfig user to use")
	}
	if flags.Lookup("cluster") == nil {
		flags.StringVar(&overrides.Context.Cluster, "cluster", overrides.Context.Cluster, "The name of the kubeconfig cluster to use")
	}
	if flags.Lookup("namespace") == nil {
		flags.StringVarP(&overrides.Context.Namespace, "namespace", "n", overrides.Context.Namespace, "The name of the Kubernetes Namespace to work in (NOT optional)")
	}
}
