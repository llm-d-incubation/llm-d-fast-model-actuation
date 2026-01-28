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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// InferenceServerConfigSpec defines the configuration parameters required to launch the vLLM process inside the launcher pod
type InferenceServerConfigSpec struct {
	// ModelServerConfig defines the configuration for the model server
	// +kubebuilder:validation:Required
	ModelServerConfig ModelServerConfig `json:"modelServerConfig"`

	// LauncherConfigName is the name of the LauncherConfig that this InferenceServerConfig belongs to
	// +kubebuilder:validation:Required
	LauncherConfigName string `json:"launcherConfigName"`
}

// ModelServerConfig defines the configuration for a model server
type ModelServerConfig struct {
	// Port is the port on which the vLLM server will listen
	// Particularly, management of vLLM instances' sleep state is done through this port
	Port int32 `json:"port"`

	// Options are the vLLM startup options, excluding Port
	// +optional
	Options string `json:"options,omitempty"`

	// EnvVars are the environment variables for the vLLM instance
	// +optional
	EnvVars     map[string]string `json:"env_vars,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

// InferenceServerConfigStatus represents the current status.
type InferenceServerConfigStatus struct {
	// `observedGeneration` is the `metadata.generation` last seen by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// `errors` reports problems seen in the desired state of this object;
	// in particular, in the version reported by `observedGeneration`.
	// +optional
	Errors []string `json:"errors,omitempty"`
}

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
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
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type InferenceServerConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	// Items is the list of InferenceServerConfig resources.
	Items []InferenceServerConfig `json:"items"`
}
