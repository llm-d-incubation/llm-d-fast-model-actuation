package api

// In the "dual Pod" technique, clients/users create a server-requesting Pod
// that describes one desired inference server Pod but when it runs is actually
// a reverse proxy to the real inference server. A dual-pod controller manages
// server-running Pods that actually run the inference servers.

// In addition to the reverse proxy function, the server-requesting Pod
// also can be queried to discover the set of accelerators (e.g., GPUs)
// that it is associated with.
// Once the server-requesting Pod's container named "inference-server"
// is running, the controller will rapidly poll that container until this query
// successfully returns a result.

// ServerPatchAnnotationName is the name of the annotation on the
// server-requesting Pod that defines how to transform it into the
// corresponding server-running Pod. The value of the annotation is a patch
// in Kubernetes YAML (which subsumes JSON). In particular, it is a
// strategic merge patch, as described in
// https://kubernetes.io/docs/tasks/manage-kubernetes-objects/update-api-object-kubectl-patch/#use-strategic-merge-patch-to-update-a-deployment-using-the-retainkeys-strategy .
// The server-requesting Pod's spec and label and annotation metadata are transformed
// --- by the following procedure ---
// into the spec and label and annotation metadata given to the kube-apiserver
// to define the server-running Pod.
// 1. Remove the annotations whose names begin with "dual-pod.llm-d.cncf.io/".
// 2. Apply the patch

const ServerPatchAnnotationName = "dual-pod.llm-d.cncf.io/server-patch"

// AdminPortAnnotationName is the name of an annotation whose value
// is the name of the port on the "inference-server" container to be
// queried to get the set of associated accelerators.
const AdminPortAnnotationName = "dual-pod.llm-d.cncf.io/admin-port"
