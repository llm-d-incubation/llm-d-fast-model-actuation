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
//     - For each unique combination of (Node, AcceleratorSet, LauncherConfig), select the highest LauncherCount value
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
//   - When multiple CountForLauncher structs specify the same LauncherConfigName for the same (Node, AcceleratorSet) combination:
//     - Select the highest LauncherCount value among all matching entries
//     - This determines the target pool size for that LauncherConfig on that Node with that Accelerator
//
// 2. **Zero Matching CountForLauncher**
//   - When no CountForLauncher matches a specific (Node, AcceleratorSet, LauncherConfig) tuple:
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
	//
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
	//      cpu:
	//        min: "16"
	//        max: "64"
	//      memory:
	//        min: "128Gi"
	//        max: "512Gi"
	//      accelerators:
	//        nvidia.com/gpu:
	//          min: "2"
	//	        max: "8"
	//  acceleratorSelector:
	//    type: "nvidia.com/gpu"
	//    memory: "48Gi"
	//    count: 4
	// +required
	EnhancedNodeSelector EnhancedNodeSelector `json:"enhancedNodeSelector"`

	// CountForLauncher defines pre-configuration quantities for each LauncherConfig
	// to maintain on each matching node. Each entry may optionally include
	// an AcceleratorSelector to restrict the entry to nodes that have a
	// matching accelerator (if omitted, apply regardless of accelerator sets).
	// +required
	CountForLauncher []CountForLauncher `json:"countForLauncher"`
}

// EnhancedNodeSelector defines node selector with label selector and resource requirements.
type EnhancedNodeSelector struct {
	// LabelSelector defines the label selector for a node.
	// +required
	LabelSelector *metav1.LabelSelector `json:"labelSelector,omitempty"`
	// ResourceRequirements defines the resource requirements for a node.
	// +optional
	ResourceRequirements *ResourceRequirements `json:"resourceRequirements,omitempty"`
	// AcceleratorSelector defines accelerator-specific selection criteria at the
	// node level. When omitted, node matching does not filter based on accelerator
	// (convenient for homogeneous clusters).
	// +optional
	AcceleratorSelector *AcceleratorSelector `json:"acceleratorSelector,omitempty"`
}

type CountForLauncher struct {
	// LauncherConfigName is the name of the LauncherConfig this policy applies to.
	// +required
	LauncherConfigName string `json:"launcherConfigName"`

	// LauncherCount is the total number of launcher pods to maintain.
	// +required
	LauncherCount int32 `json:"launcherCount"`

	// Apply this count only to accelerators on the node that
	// match this selector. When omitted, the count applies to the node regardless
	// of accelerator sets (convenient for homogeneous clusters).
	// +optional
	AcceleratorSelector *AcceleratorSelector `json:"acceleratorSelector,omitempty"`
}

// ResourceRequirements defines resource requirements for a node.
type ResourceRequirements struct {
	// Allocatable defines the allocatable resources for a node with min/max ranges.
	// +required
	Allocatable ResourceRanges `json:"allocatable,omitempty"`
}

// ResourceRanges defines min/max ranges for various resources of a Node.
type ResourceRanges struct {
	// CPU defines the CPU resource range requirement.
	// +optional
	CPU ResourceRange `json:"cpu,omitempty"`

	// Memory defines the memory resource range requirement.
	// +optional
	Memory ResourceRange `json:"memory,omitempty"`

	// Accelerators defines the GPU resource range requirements keyed by GPU type.
	// +optional
	Accelerators map[string]ResourceRange `json:"accelerators,omitempty"`
}

// ResourceRange defines a range with minimum and maximum quantity values.
type ResourceRange struct {
	// Min specifies the minimum quantity required.
	// +optional
	Min resource.Quantity `json:"min,omitempty"`

	// Max specifies the maximum quantity allowed.
	// +optional
	Max resource.Quantity `json:"max,omitempty"`
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
