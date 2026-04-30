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

// EmbeddedObjectMeta holds the subset of metav1.ObjectMeta fields that
// we want in the CRD schema, so that strict decoding accepts them.
type EmbeddedObjectMeta struct {
	// Labels for organizing and categorizing objects.
	// +optional
	Labels map[string]string `json:"labels,omitempty"`

	// Annotations for storing arbitrary non-identifying metadata.
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`
}

// EmbeddedPodTemplateSpec is a PodTemplateSpec whose metadata fields
// are explicitly declared so that the CRD schema admits them.
type EmbeddedPodTemplateSpec struct {
	// +optional
	Metadata EmbeddedObjectMeta `json:"metadata,omitempty"`

	// Spec defines the behavior of pods created from this template.
	// +optional
	Spec corev1.PodSpec `json:"spec,omitempty"`
}

// LauncherConfigSpec defines the configuration to manage the nominal server-providing pod definition.
type LauncherConfigSpec struct {
	// PodTemplate defines the pod specification for the server-providing pod.
	// +optional
	PodTemplate EmbeddedPodTemplateSpec `json:"podTemplate,omitempty"`

	// MaxSleepingInstances is the maximum number of sleeping inference engine instances allowed per launcher pod.
	// +kubebuilder:validation:Required
	MaxSleepingInstances int32 `json:"maxSleepingInstances,omitempty"`
}

// LauncherConfigStatus represents the current status
type LauncherConfigStatus struct {
	// `observedGeneration` is the `metadata.generation` last seen by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// `errors` reports problems seen in the desired state of this object.
	// +optional
	Errors []string `json:"errors,omitempty"`
}

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=lcfg

// LauncherConfig is the Schema for the LauncherConfigs API.
// It represents the configuration to manage the nominal server-providing pod definition.
type LauncherConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Spec defines the desired state of the LauncherConfig.
	//
	// +required
	Spec LauncherConfigSpec `json:"spec,omitempty"`

	// Status represents the observed status of the LauncherConfig.
	//
	// +optional
	Status LauncherConfigStatus `json:"status,omitempty"`
}

// LauncherConfigList contains a list of LauncherConfig resources.
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type LauncherConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	// Items is the list of LauncherConfig resources.
	Items []LauncherConfig `json:"items"`
}
