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
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// LauncherPopulationPolicy defines the policy for pro-active creation of launcher Pods
// for a given type of Node.
// Here we introduce and use a particular definition of "type" for Nodes.
// All the LauncherPopulationPolicy objects together define a map,
// from (Node, LauncherConfig) to count.
// Call this map `PopulationPolicy`.
// When multiple CountForLauncher apply to the same (Node, LauncherConfig) pair
// the maximum of their counts is what appears in `PopulationPolicy`.
// When no CountForLauncher applies to a given (Node, LauncherConfig),
// `PopulationPolicy` implicitly maps that pair to zero.
//
// The collective meaning of all the LauncherPopulationPolicy objects
// and all the server-requesting Pods is that for a given (Node, LauncherConfig)
// the number of launchers that should exist is the larger of
// (a) what `PopulationPolicy` says for that pair, and
// (b) the number needed to satisfy the server-requesting Pods.
//
// +genclient
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=lpp
type LauncherPopulationPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   LauncherPopulationPolicySpec   `json:"spec,omitempty"`
	Status LauncherPopulationPolicyStatus `json:"status,omitempty"`
}

// LauncherPopulationPolicySpec defines policy for one type of Node.
type LauncherPopulationPolicySpec struct {
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
	//  allocatableResources:
	//    cpu: {min: "16", max: "64"}
	//    memory: {min: 128Gi, max: 512Gi}
	//    "nvidia.com/gpu": {min: 8, max: 8}
	// +required
	EnhancedNodeSelector EnhancedNodeSelector `json:"enhancedNodeSelector"`

	// CountForLauncher declares the desired number of launchers on the
	// relevant Node, for various LauncherConfigs.
	// +required
	CountForLauncher []CountForLauncher `json:"countForLauncher"`
}

// EnhancedNodeSelector defines node selector with label selector and resource requirements.
type EnhancedNodeSelector struct {
	// LabelSelector defines the label selector for a node.
	// +required
	LabelSelector metav1.LabelSelector `json:"labelSelector"`
	// ResourceRequirements defines the resource requirements for a node.
	// +optional
	AllocatableResources ResourceRanges `json:"allocatableResources,omitempty"`
}

// ResourceRanges defines the required range for some resources.
// These are the same sort of resources that appear in the `.status.allocatable`
// of a Node object.
type ResourceRanges map[corev1.ResourceName]ResourceRange

// ResourceRange defines a range by inclusive minimum and maximum quantity values.
type ResourceRange struct {
	// Min specifies the minimum quantity required,
	// or is `null` to signal that there is no lower bound.
	// +optional
	Min *resource.Quantity `json:"min,omitempty"`

	// Max specifies the maximum quantity allowed,
	// or is `null` to signal that there is no upper bound.
	// +optional
	Max *resource.Quantity `json:"max,omitempty"`
}

type CountForLauncher struct {
	// LauncherConfigName is the name of the LauncherConfig this policy applies to.
	// +required
	LauncherConfigName string `json:"launcherConfigName"`

	// LauncherCount is the total number of launcher pods to maintain.
	// +required
	LauncherCount int32 `json:"launcherCount"`
}

type LauncherPopulationPolicyStatus struct {
	// `observedGeneration` is the `metadata.generation` last seen by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// `errors` reports problems seen in the desired state of this object;
	// in particular, in the version reported by `observedGeneration`.
	// +optional
	Errors []string `json:"errors,omitempty"`
	// Add status fields if needed (e.g., current idle pod counts)
}

// LauncherPopulationPolicyList contains a list of LauncherPopulationPolicy resources.
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type LauncherPopulationPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []LauncherPopulationPolicy `json:"items"`
}
