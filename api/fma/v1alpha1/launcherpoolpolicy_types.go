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

package v1alpha1

import (
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// LauncherPoolPolicy defines the proactive provisioning policy for the idle launcher pods.
// The LauncherPoolPolicy semantics should be defined as below:
//
// ## Multiple LauncherPoolPolicy Objects
//
// 1. **Additive Semantics**
//   - Multiple LauncherPoolPolicy objects follow additive semantics
//   - All policies across all objects are evaluated together
//   - Rules from different objects are combined to form the complete policy set
//
// 2. **Zero Objects Behavior**
//   - When no LauncherPoolPolicy objects exist, no proactive provisioning occurs
//   - System falls back to on-demand launcher pod creation
//
// ## Multiple LauncherPoolForNodeType Matching
//
// 1. **Multiple Matches**
//   - When multiple LauncherPoolForNodeType structs match a single Node (across same or different LauncherPoolPolicy objects):
//     - For each unique combination of (Node, Accelerator, LauncherConfig), select the highest LauncherCount value
//     - This forms the effective policy for that specific tuple
//
// 2. **Zero Matches**
//   - When no LauncherPoolForNodeType matches a Node:
//     - No pre-provisioning policy applies to that Node
//     - Launcher pods for that Node are created on-demand
//
// ## Multiple CountForLauncher with Same LauncherConfig
//
// 1. **Duplicate LauncherConfig Names**
//   - When multiple CountForLauncher structs specify the same LauncherConfigName for the same (Node, Accelerator) combination:
//     - Select the highest LauncherCount value among all matching entries
//     - This determines the target pool size for that LauncherConfig on that Node with that Accelerator
//
// 2. **Zero Matching CountForLauncher**
//   - When no CountForLauncher matches a specific (Node, Accelerator, LauncherConfig) tuple:
//     - No pre-provisioning occurs for that specific combination
//     - Launcher pods are created on-demand when needed
//
// +genclient
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=lpp

type LauncherPoolPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   LauncherPoolPolicySpec   `json:"spec,omitempty"`
	Status LauncherPoolPolicyStatus `json:"status,omitempty"`
}

// LauncherPoolPolicySpec defines the node-level idle pool configuration.
type LauncherPoolPolicySpec struct {
	// LauncherPoolForNodeType defines pool spec per node type.
	LauncherPoolForNodeType []LauncherPoolForNodeType `json:"launcherPoolForNodeType"`
}

// LauncherPoolForNodeType defines launcher count for a class of nodes.
type LauncherPoolForNodeType struct {
	// Selector describes the hardware characteristics of target nodes.
	// +kubebuilder:validation:Required
	// Introduce an EnhancedNodeSelector that supports combining label-based
	// matching with resource field conditions.
	// For example:
	// enhancedNodeSelector:
	//  # 1. Label selector (compatible with existing metav1.LabelSelector)
	//  labelSelector:
	//    matchLabels:
	//      nvidia.com/gpu.family: ada-lovelace
	//    matchExpressions:
	//      - key: node.kubernetes.io/instance-type
	//        operator: In
	//        values: ["gx3-48x240x2l40s", "gx3-96x480x4l40s"]
	//
	//  # 2. Resource condition selector (new capability)
	//  resourceRequirements:
	//    allocatable:
	//      memory: ">=200Gi"
	//      nvidia.com/gpu: ">=1"
	//      cpu: ">=32"
	EnhancedNodeSelector EnhancedNodeSelector `json:"enhancedNodeSelector"`

	// PerAcceleratorCount defines pre-configuration quantities for each accelerator type
	PerAcceleratorCount []PerAcceleratorCount `json:"perAcceleratorCount"`
}

// PerAcceleratorCount defines configuration for specific accelerators
type PerAcceleratorCount struct {
	// AcceleratorSelector accelerator selector
	AcceleratorSelector AcceleratorSelector `json:"acceleratorSelector"`

	// CountForLauncher is the total number of launcher for each LauncherConfig
	// to maintain on each matching node per accelerator.
	// If two different counts are specified for the same (Node, Accelerator, LauncherConfig),
	// the higher count is used and will be populated into LauncherPoolPolicyStatus.
	// If no CountForLauncher applies to a given (Node, Accelerator, LauncherConfig), this Node
	// will be ignored for this LauncherConfig.
	// +kubebuilder:validation:MinItems=1
	CountForLauncher []CountForLauncher `json:"countForLauncher"`
}

// ResourceRequirements defines resource requirements for a node.
type ResourceRequirements struct {
	// Allocatable defines the allocatable resources for a node.
	// +kubebuilder:validation:Required
	Allocatable map[string]resource.Quantity `json:"allocatable,omitempty"`
}

// EnhancedNodeSelector defines node selector with label selector and resource requirements.
type EnhancedNodeSelector struct {
	// LabelSelector defines the label selector for a node.
	// +kubebuilder:validation:Required
	LabelSelector *metav1.LabelSelector `json:"labelSelector,omitempty"`
	// ResourceRequirements defines the resource requirements for a node.
	// +kubebuilder:validation:Optional
	ResourceRequirements *ResourceRequirements `json:"resourceRequirements,omitempty"`
	// AcceleratorSelector defines accelerator-specific selection criteria
	AcceleratorSelector *AcceleratorSelector `json:"acceleratorSelector,omitempty"`
}

// AcceleratorSelector defines accelerator selection criteria
type AcceleratorSelector struct {
	// Type specifies accelerator type (e.g., nvidia.com/gpu)
	Type string `json:"type,omitempty"`

	// Memory specifies accelerator memory size requirement
	Memory *resource.Quantity `json:"memory,omitempty"`

	// Count specifies required number of accelerators
	Count *int32 `json:"count,omitempty"`
}

type CountForLauncher struct {
	// LauncherConfigName is the name of the LauncherConfig this policy applies to.
	// +kubebuilder:validation:Required
	LauncherConfigName string `json:"launcherConfigName"`

	// LauncherCount is the total number of launcher pods to maintain.
	// +kubebuilder:validation:Required
	LauncherCount int32 `json:"launcherCount"`
}

type LauncherPoolPolicyStatus struct {
	// `observedGeneration` is the `metadata.generation` last seen by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// `errors` reports problems seen in the desired state of this object;
	// in particular, in the version reported by `observedGeneration`.
	// +optional
	Errors []string `json:"errors,omitempty"`
	// Add status fields if needed (e.g., current idle pod counts)
}

// LauncherPoolPolicyList contains a list of LauncherPoolPolicy resources.
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type LauncherPoolPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []LauncherPoolPolicy `json:"items"`
}
