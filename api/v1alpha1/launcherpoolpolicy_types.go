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

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// PoolPolicy defines the proactive provisioning policy for idle launcher pods.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=launcherpoolpolicies,scope=Cluster

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
	//    # Optional: also support capacity (if total capacity—not just allocatable—is desired)
	//    # capacity:
	//    #   memory: ">=256Gi"
	EnhancedNodeSelector EnhancedNodeSelector `json:"enhancedNodeSelector"`

	// CountForLauncher is the total number of launcher for each LauncherConfig
	// to maintain on each matching node.
	// If two different counts are specified for the same (Node, LauncherConfig),
	// the higher count is used and will be populated into LauncherPoolPolicyStatus.
	// If no CountForLauncher applies to a given (Node, LauncherConfig), this Node
	// will be ignored for this LauncherConfig.
	// +kubebuilder:validation:MinItems=1
	CountForLauncher []CountForLauncher `json:"countForLauncher"`
}

// ResourceRequirementSpec defines resource requirements for a node.
type ResourceRequirementSpec struct {
	// Allocatable defines the allocatable resources for a node.
	// +kubebuilder:validation:Required
	Allocatable map[string]string `json:"allocatable,omitempty"`
	// Capacity defines the capacity resources for a node.
	// +kubebuilder:validation:Optional
	Capacity map[string]string `json:"capacity,omitempty"`
}

// EnhancedNodeSelector defines node selector with label selector and resource requirements.
type EnhancedNodeSelector struct {
	// LabelSelector defines the label selector for a node.
	// +kubebuilder:validation:Required
	LabelSelector *metav1.LabelSelector `json:"labelSelector,omitempty"`
	// ResourceRequirements defines the resource requirements for a node.
	// +kubebuilder:validation:Optional
	ResourceRequirements *ResourceRequirementSpec `json:"resourceRequirements,omitempty"`
}

type CountForLauncher struct {
	// LauncherConfigName references the name of the LauncherConfig this policy applies to.
	// +kubebuilder:validation:Required
	LauncherConfigName string `json:"launcherConfigName"`

	// LauncherCount is the total number of launcher pods to maintain.
	// +kubebuilder:validation:Required
	LauncherCount int32 `json:"launcherCount"`
}

type LauncherPoolPolicyStatus struct {
	// `observedGeneration` is the `metadata.generation` last seen by the controller.
	// +optional
	ObservedGeneration int32 `json:"observedGeneration,omitempty"`

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

func init() {
	SchemeBuilder.Register(&LauncherPoolPolicy{}, &LauncherPoolPolicyList{})
}
