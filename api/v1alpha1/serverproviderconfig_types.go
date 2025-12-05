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
	PodTemplate PodTemplate `json:"podTemplate,omitempty"`

	// MaxSleepingInstances is the maximum number of sleeping inference engine instances allowed per launcher pod.
	// Only applicable when Mode is "launcher".
	// +optional
	MaxSleepingInstances *int32 `json:"maxSleepingInstances,omitempty"`
}

// PodTemplate is a description of the server-providing pod template.
type PodTemplate struct {
	// List of volumes that can be mounted by containers belonging to the pod.
	// More info: https://kubernetes.io/docs/concepts/storage/volumes
	// +optional
	// +patchMergeKey=name
	// +patchStrategy=merge,retainKeys
	// +listType=map
	// +listMapKey=name
	Volumes []corev1.Volume `json:"volumes,omitempty"`
	// List of containers belonging to the pod.
	// Containers cannot currently be added or removed.
	// There must be at least one container in a Pod.
	// Cannot be updated.
	// +patchMergeKey=name
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=name
	Containers []corev1.Container `json:"containers"`
	// Optional duration in seconds the pod needs to terminate gracefully. May be decreased in delete request.
	// Value must be non-negative integer. The value zero indicates stop immediately via
	// the kill signal (no opportunity to shut down).
	// If this value is nil, the default grace period will be used instead.
	// The grace period is the duration in seconds after the processes running in the pod are sent
	// a termination signal and the time when the processes are forcibly halted with a kill signal.
	// Set this value longer than the expected cleanup time for your process.
	// Defaults to 30 seconds.
	// +optional
	TerminationGracePeriodSeconds *int64 `json:"terminationGracePeriodSeconds,omitempty" protobuf:"varint,4,opt,name=terminationGracePeriodSeconds"`
	// NodeSelector is a selector which must be true for the pod to fit on a node.
	// Selector which must match a node's labels for the pod to be scheduled on that node.
	// More info: https://kubernetes.io/docs/concepts/configuration/assign-pod-node/
	// +optional
	// +mapType=atomic
	NodeSelector map[string]string `json:"nodeSelector,omitempty" protobuf:"bytes,7,rep,name=nodeSelector"`

	// ServiceAccountName is the name of the ServiceAccount to use to run this pod.
	// More info: https://kubernetes.io/docs/tasks/configure-pod-container/configure-service-account/
	// +optional
	ServiceAccountName string `json:"serviceAccountName,omitempty" protobuf:"bytes,8,opt,name=serviceAccountName"`
	// NodeName indicates in which node this pod is scheduled.
	// If empty, this pod is a candidate for scheduling by the scheduler defined in schedulerName.
	// Once this field is set, the kubelet for this node becomes responsible for the lifecycle of this pod.
	// This field should not be used to express a desire for the pod to be scheduled on a specific node.
	// https://kubernetes.io/docs/concepts/scheduling-eviction/assign-pod-node/#nodename
	// +optional
	NodeName string `json:"nodeName,omitempty" protobuf:"bytes,10,opt,name=nodeName"`
	// Host networking requested for this pod. Use the host's network namespace.
	// When using HostNetwork you should specify ports so the scheduler is aware.
	// When `hostNetwork` is true, specified `hostPort` fields in port definitions must match `containerPort`,
	// and unspecified `hostPort` fields in port definitions are defaulted to match `containerPort`.
	// Default to false.
	// +k8s:conversion-gen=false
	// +optional
	HostNetwork bool `json:"hostNetwork,omitempty" protobuf:"varint,11,opt,name=hostNetwork"`
	// ImagePullSecrets is an optional list of references to secrets in the same namespace to use for pulling any of the images used by this PodSpec.
	// If specified, these secrets will be passed to individual puller implementations for them to use.
	// +optional
	// +patchMergeKey=name
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=name
	ImagePullSecrets []corev1.LocalObjectReference `json:"imagePullSecrets,omitempty" patchStrategy:"merge" patchMergeKey:"name" protobuf:"bytes,15,rep,name=imagePullSecrets"`
	// If specified, the pod's scheduling constraints
	// +optional
	Affinity *corev1.Affinity `json:"affinity,omitempty" protobuf:"bytes,18,opt,name=affinity"`
	// If specified, the pod will be dispatched by specified scheduler.
	// If not specified, the pod will be dispatched by default scheduler.
	// +optional
	SchedulerName string `json:"schedulerName,omitempty" protobuf:"bytes,19,opt,name=schedulerName"`
	// If specified, the pod's tolerations.
	// +optional
	// +listType=atomic
	Tolerations []corev1.Toleration `json:"tolerations,omitempty" protobuf:"bytes,22,opt,name=tolerations"`
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
