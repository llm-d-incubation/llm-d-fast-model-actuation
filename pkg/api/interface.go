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

package api

// In the "dual Pod" technique, clients/users create a server-requesting Pod
// that describes one desired inference server Pod but when it runs is actually
// just a stub. A dual-pods controller manages
// server-providing Pods that actually run the inference servers.

// The server-requesting Pod
// can be queried to discover the set of accelerators (e.g., GPUs)
// that it is associated with.
// Once the server-requesting Pod's container named "inference-server"
// is running, the controller will rapidly poll that container until this query
// successfully returns a result.

// ServerPatchAnnotationName is the name of the annotation on the
// server-requesting Pod that defines how to transform it into the
// corresponding server-providing Pod. The value of the annotation is a
// [Golang template](https://pkg.go.dev/text/template) that expands to a patch
// in Kubernetes YAML (which subsumes JSON). In particular, it is a
// strategic merge patch, as described in
// https://kubernetes.io/docs/tasks/manage-kubernetes-objects/update-api-object-kubectl-patch/#use-strategic-merge-patch-to-update-a-deployment-using-the-retainkeys-strategy .
// The server-requesting Pod's spec and label and annotation metadata are transformed
// --- by the following procedure ---
// into the spec and label and annotation metadata given to the kube-apiserver
// to define the server-providing Pod.
// 1. Remove all annotations;
// 2. Apply the patch

const ServerPatchAnnotationName = "dual-pods.llm-d.ai/server-patch"

// ServerPatchAnnotationErrorsName is the name of an annotation that the dual-pods controller
// maintains reporting the ServerRequestingPodStatus. The value of this annotation is the
// JSON rendering of the status.
const ServerPatchAnnotationErrorsName = "dual-pods.llm-d.ai/status"

// ServerRequestingPodStatus is the status of a server-requesting Pod with respect
// to the dual-pods technique.
type ServerRequestingPodStatus struct {
	// Errors reports problems in the input state.
	Errors []string
}

// InferenceServerContainerName is the name of the container which is described by the server patch.
// This container is expected to run the inference server using vLLM.
const InferenceServerContainerName = "inference-server"

// AdminPortAnnotationName is the name of an annotation whose value
// is the name of the port on the "inference-server" container to be
// queried to get the set of associated accelerators.
const AdminPortAnnotationName = "dual-pods.llm-d.ai/admin-port"

// AdminPortDefaultValue is the default port number of the server-requesting pod
// to be queried to get the set of associated accelerators.
const AdminPortDefaultValue = "8081"

// ProviderData is the data made available to the server patch.
type ProviderData struct {
	// NodeName is the name of the Node to which the Pod is bound
	NodeName string

	// LocalVolume is the name of the PVC that is dedicated to storage specific
	// to that node.
	LocalVolume string
}

// AcceleratorsAnnotationName is the name of an annotation that the dual-pods controller
// maintains on both server-requesting and server-providing Pods.
// This annotation is purely FYI emitted by the dual-pods controller
// (it does not rely on this label for anything).
const AcceleratorsAnnotationName string = "dual-pods.llm-d.ai/accelerators"

// DualLabelName is the name of a label that the dual-pods controller
// maintains on the server-requesting and server-providing Pods.
// While bound, this label is present and its value is the name of the
// corresponding other Pod;
// while unbound, this label is absent.
// This label is purely FYI emitted by the dual-pods controller
// (it does not rely on this label for anything).
const DualLabelName string = "dual-pods.llm-d.ai/dual"

// SleepingLabelName is the name of a label that the dual-pods controller
// maintains on server-providing Pods.
// This value of this label is "true" or "false",
// according to what the controller knows.
// This label is purely FYI emitted by the dual-pods controller
// (it does not rely on this label for anything).
const SleepingLabelName string = "dual-pods.llm-d.ai/sleeping"

// SleepState is what HTTP GET /is_sleeping on an inference server
// returns (as JSON).
type SleepState struct {
	IsSleeping bool `json:"is_sleeping"`
}
