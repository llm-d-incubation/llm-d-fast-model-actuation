package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// InferenceServerConfigSpec defines the configuration parameters required to launch the vLLM process inside the launcher pod
type InferenceServerConfigSpec struct {
	// ModelName is the name of the model
	// +kubebuilder:validation:Required
	ModelName string `json:"modelName"`

	// ModelServerConfig defines the configuration for the model server
	// +kubebuilder:validation:Required
	ModelServerConfig ModelServerConfig `json:"modelServerConfig"`

	// ServerProviderConfigRef is a reference to the ServerProviderConfig that this InferenceServerConfig belongs to
	// +kubebuilder:validation:Required
	TemplateRef ServerProviderConfigReference `json:"templateRef,omitempty"`
}

// ModelServerConfig defines the configuration for a model server
type ModelServerConfig struct {
	// Options are the vLLM startup options
	// +optional
	Options string `json:"options,omitempty"`

	// EnvVars are the environment variables for the vLLM instance
	// +optional
	EnvVars     map[string]string `json:"env_vars,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

// ServerProviderConfigReference is a reference to an ServerProviderConfig resource
type ServerProviderConfigReference struct {
	// Name of the referenced ModelServerPool
	// +kubebuilder:validation:Required
	Name string `json:"name"`
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
