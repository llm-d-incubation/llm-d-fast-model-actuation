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

// InferenceServerTemplateSpec defines the configuration to manage the nominal server-providing pod definition.
type InferenceServerTemplateSpec struct {
	// TODO
}

// InferenceServerTemplateStatus represents the current status
type InferenceServerTemplateStatus struct {
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=ist

// InferenceServerTemplate is the Schema for the InferenceServerTemplates API.
// It represents the configuration to manage the nominal server-providing pod definition.
type InferenceServerTemplate struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Spec defines the desired state of the InferenceServerTemplate.
	//
	// +required
	Spec InferenceServerTemplateSpec `json:"spec,omitempty"`

	// Status represents the observed status of the InferenceServerTemplate.
	//
	// +optional
	Status InferenceServerTemplateStatus `json:"status,omitempty"`
}

// InferenceServerTemplateList contains a list of InferenceServerTemplate resources.
// +kubebuilder:object:root=true
type InferenceServerTemplateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	// Items is the list of InferenceServerTemplate resources.
	Items []InferenceServerTemplate `json:"items"`
}

func init() {
	SchemeBuilder.Register(&InferenceServerTemplate{}, &InferenceServerTemplateList{})
}
