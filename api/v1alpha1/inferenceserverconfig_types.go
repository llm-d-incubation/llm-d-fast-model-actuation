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

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// InferenceServerConfigSpec defines the configuration parameters required to launch the vLLM process inside the launcher pod
type InferenceServerConfigSpec struct {
	// TODO
}

// InferenceServerConfigStatus represents the current status.
type InferenceServerConfigStatus struct {
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=isc

// InferenceServerConfig is the Schema for the InferenceServerConfigs API.
// It represents the configuration parameters required to launch the vLLM process inside the launcher pod.
type InferenceServerConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Spec defines the desired state of the InferenceServerConfig.
	//
	// +required
	Spec InferenceServerConfigSpec `json:"spec,omitempty"`

	// Status represents the observed status of the InferenceServerConfig.
	//
	// +optional
	Status InferenceServerConfigStatus `json:"status,omitempty"`
}

// InferenceServerConfigList contains a list of InferenceServerConfig resources.
// +kubebuilder:object:root=true
type InferenceServerConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	// Items is the list of InferenceServerConfig resources.
	Items []InferenceServerConfig `json:"items"`
}

func init() {
	SchemeBuilder.Register(&InferenceServerConfig{}, &InferenceServerConfigList{})
}
