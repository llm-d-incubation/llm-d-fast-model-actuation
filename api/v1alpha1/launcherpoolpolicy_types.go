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
	NodeSelector []metav1.LabelSelector `json:"nodeSelector"`

	// CountForLauncher is the total number of launcher for each LauncherConfig
	// to maintain on each matching node.
	// If two different counts are specified for the same (Node, LauncherConfig),
	// the higher count is used and will be populated into LauncherPoolPolicyStatus.
	// If no CountForLauncher applies to a given (Node, LauncherConfig), this Node
	// will be ignored for this LauncherConfig.
	CountForLauncher []CountForLauncher `json:"countForLauncher"`
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
	// Add status fields if needed (e.g., current idle pod counts)
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type LauncherPoolPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []LauncherPoolPolicy `json:"items"`
}
