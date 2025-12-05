/*
Copyright 2025.

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

package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// PoolPolicy defines the proactive provisioning policy for idle launcher pods.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=poolpolicies,scope=Namespaced

type ServerProviderProvisionPoolPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PoolPolicySpec   `json:"spec,omitempty"`
	Status PoolPolicyStatus `json:"status,omitempty"`
}

// PoolPolicySpec defines the node-level idle pool configuration.
type PoolPolicySpec struct {
	// PoolsPerNode defines idle launcher counts per node type.
	PoolsPerNode []NodePoolSpec `json:"poolsPerNode"`
}

// NodePoolSpec defines launcher count for a class of nodes.
type NodePoolSpec struct {
	// Selector describes the hardware characteristics of target nodes.
	Selector NodeSelector `json:"selector"`

	// ServerProviderTemplateCount is the total number of launcher or vLLM pods for each ServerProviderConfig
	// to maintain on each matching node.
	ServerProviderTemplateCount []ServerProviderTemplateCount `json:"totalCountPerTemplate"`
}

type ServerProviderTemplateCount struct {
	// TemplateRef references the name of the ServerProviderConfig this policy applies to.
	// +optional
	TemplateRef ServerProviderConfigReference `json:"templateRef,omitempty"`

	// LauncherCount is the total number of launcher pods to maintain.
	LauncherCount int32 `json:"launcherCount"`
}

// NodeSelector selects nodes by hardware attributes.
type NodeSelector struct {

	// MinGPUMemoryGB is the minimum GPU memory in GB.
	MinGPUMemoryGB int32 `json:"minGPUMemoryGB"`

	// MinCPUMemoryGB is the minimum CPU memory in GB.
	MinCPUMemoryGB int32 `json:"minCPUMemoryGB"`

	// AcceleratorCount is the number of accelerators required.
	AcceleratorCount int32 `json:"acceleratorCount"`
}

type PoolPolicyStatus struct {
	// Add status fields if needed (e.g., current idle pod counts)
}

// +kubebuilder:object:root=true
type ServerProviderProvisionPoolPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ServerProviderProvisionPoolPolicy `json:"items"`
}
