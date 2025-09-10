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
// just a stub. A dual-pod controller manages
// server-running Pods that actually run the inference servers.

// The server-requesting Pod
// can be queried to discover the set of accelerators (e.g., GPUs)
// that it is associated with.
// Once the server-requesting Pod's container named "inference-server"
// is running, the controller will rapidly poll that container until this query
// successfully returns a result.

// ServerPatchAnnotationName is the name of the annotation on the
// server-requesting Pod that defines how to transform it into the
// corresponding server-running Pod. The value of the annotation is a
// [Golang template](https://pkg.go.dev/text/template) that expands to a patch
// in Kubernetes YAML (which subsumes JSON). In particular, it is a
// strategic merge patch, as described in
// https://kubernetes.io/docs/tasks/manage-kubernetes-objects/update-api-object-kubectl-patch/#use-strategic-merge-patch-to-update-a-deployment-using-the-retainkeys-strategy .
// The server-requesting Pod's spec and label and annotation metadata are transformed
// --- by the following procedure ---
// into the spec and label and annotation metadata given to the kube-apiserver
// to define the server-running Pod.
// 1. Remove the annotations whose name begins with "dual-pod.llm-d.ai/".
// 2. Apply the patch

const ServerPatchAnnotationName = "dual-pod.llm-d.ai/server-patch"

// AdminPortAnnotationName is the name of an annotation whose value
// is the name of the port on the "inference-server" container to be
// queried to get the set of associated accelerators.
const AdminPortAnnotationName = "dual-pod.llm-d.ai/admin-port"

// RunnerData is the data made available to the server patch.
type RunnerData struct {
  // NodeName is the name of the Node to which the Pod is bound
  NodeName string

  // LocalVolume is the name of the PVC that is dedicated to storage specific
  // to that node.
  LocalVolume string
}
