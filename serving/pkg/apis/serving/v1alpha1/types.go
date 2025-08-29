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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// InferenceServer represents set of inference servers (eg. vLLM inference server)
type InferenceServerSet struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec InferenceServerSetSpec `json:"spec"`

	// +optional
	Status InferenceServerSetStatus `json:"status,omitempty"`
}

// InferenceServerSetSpec is the desired state of the inference server.
type InferenceServerSetSpec struct {
	// Replicas is the number of desired replicas of inference servers
	Replicas string `json:"replicas"`

	// Template is the object that describes the inference server that will be created
	// if insufficient replicas are detected.
	Template *corev1.PodTemplateSpec `json:"template"`
}

// InferenceServerSetStatus reports on the inference servers status.
type InferenceServerSetStatus struct {
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// InferenceServerSetList is a list of InferenceServerSet objects (without a level of pointer indirection, oddly enough).
type InferenceServerSetList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []InferenceServerSet `json:"items"`
}
