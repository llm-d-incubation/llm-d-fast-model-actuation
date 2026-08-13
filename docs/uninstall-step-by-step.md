# Step-by-step uninstallation of FMA

This document describes how to remove an installation of FMA workload
and FMA. This description is a little more structured and nuanced than
the installation documents, because the (reasonable and other) uses
cases are more widely varied. You should first read either the
[step-by-step installation document](install-step-by-step.md) or the
[scripted installation document](install-scripted.md) to understand
what is in an FMA installation.

## Removing FMA Workload

You should delete your server-requesting `Pod` objects and your
`LauncherPopulationPolicy` objects before deleting your
`LauncherConfig` and `InferenceServerConfig` objects.

Commonly the server-requesting `Pod` objects are managed by some
controller implementing some set type, such as `Deployment` or
`LeaderWorkerSet`. In this case it is the set-typed objects that need
to be deleted. If the server-requesting `Pod` objects were created
directly by a client, then those `Pod` objects need to be deleted
directly.

Delete all the `LauncherPopulationPolicy` objects.

To minimize trouble later, give the FMA controllers time to react by
deleting the server-providing `Pod` objects.

Delete all the `LauncherConfig` and `InferenceServerConfig` objects.

## Removing the FMA platform from your namespace

Delete the Helm chart instance that contains the FMA controllers.

## Stuck pods

If nothing went wrong and you waited long enough before deleting the
controllers then all the server-requesting and server-providing `Pod`
objects will be gone now.

In unhappy cases, the FMA controllers are gone but some
server-requesting and server-providing `Pod` objects are stuck in
graceful deletion because they still have FMA finalizers. (A `Pod`
object has a set of strings, called "finalizers", in its
`.metadata.finalizers`.)  The finalizers from FMA controllers are
strings that start with "dual-pods.llm-d.ai/". You will need to remove
those finalizers yourself. Once that is done, the graceful deletions
should complete.

## Removing FMA's cluster-scoped objects

These may be deleted in any order.

### Node-reading RBAC

Delete the RBAC objects that were created, if any, to enable the FMA
controllers to read `Node` objects.

### Admission policy objects

```shell
kubectl delete ValidatingAdmissionPolicy fma-bound-serverreqpod
kubectl delete ValidatingAdmissionPolicy fma-immutable-fields
kubectl delete ValidatingAdmissionPolicyBinding bind-fma-bound-serverreqpod
kubectl delete ValidatingAdmissionPolicyBinding bind-fma-immutable-fields
```

### CustomResourceDefinitions

```shell
kubectl delete crd launcherpopulationpolicies.fma.llm-d.ai
kubectl delete crd launcherconfigs.fma.llm-d.ai
kubectl delete crd inferenceserverconfigs.fma.llm-d.ai
```
