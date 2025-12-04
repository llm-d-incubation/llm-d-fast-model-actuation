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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ServerProviderConfigSpec defines the configuration to manage the nominal server-providing pod definition.
type ServerProviderConfigSpec struct {
	// Mode specifies how the inference engine is launched. Valid values: "direct", "launcher".
	// +kubebuilder:validation:Enum=direct;launcher
	// +optional
	Mode string `json:"mode"`

	// PodTemplate defines the pod specification for the server-providing pod.
	// +optional
	PodTemplate corev1.PodSpec `json:"podTemplate,omitempty"`

	// MaxSleepingInstances is the maximum number of sleeping inference engine instances allowed per launcher pod.
	// Only applicable when Mode is "launcher".
	// +optional
	MaxSleepingInstances *int32 `json:"maxSleepingInstances,omitempty"`
}

// ServerProviderConfigStatus represents the current status
type ServerProviderConfigStatus struct {
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=ist

// ServerProviderConfig is the Schema for the ServerProviderConfigs API.
// It represents the configuration to manage the nominal server-providing pod definition.
type ServerProviderConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Spec defines the desired state of the ServerProviderConfig.
	//
	// +required
	Spec ServerProviderConfigSpec `json:"spec,omitempty"`

	// Status represents the observed status of the ServerProviderConfig.
	//
	// +optional
	Status ServerProviderConfigStatus `json:"status,omitempty"`
}

// ServerProviderConfigList contains a list of ServerProviderConfig resources.
// +kubebuilder:object:root=true
type ServerProviderConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	// Items is the list of ServerProviderConfig resources.
	Items []ServerProviderConfig `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ServerProviderConfig{}, &ServerProviderConfigList{})
}
