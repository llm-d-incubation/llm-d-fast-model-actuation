# Cluster Sharing

FMA is designed to support mostly independent concurrent development
and testing in a shared cluster. This is tricky because FMA has some
cluster-scoped objects. Every cluster-scoped object is problematic
because ordinary users are not typically authorized to
create/modify/delete such objects. Additionally, concurrent dev/test
activity potentially involves conflicting contents for the same
object.

## The Cluster-Scoped Objects

- CustomResourceDefinition objects. These have an additional
  difficulty: Kubernetes requires that the CRD for a given kind of
  object is named `<plural resource name>.<API group>`, so content
  conflicts cannot be resolved by different users using different
  names for these CRD objects.

- ValidatingAdmissionPolicy[Binding] objects.

- A ClusterRole for reading Node objects.

- A ClusterRoleBinding that binds the node-reading ClusterRole to an
  FMA ServiceAccount.

- A ClusterRoleBinding that binds ClusterRole `view` to an FMA ServiceAccount.

- A Namespace that FMA is installed in.

## Solution for the CustomResourceDefinition Objects

(One design goal is to minimize chores for administrators of shared clusters.)

- As development progresses, we never change the definitions in an
  existing version of the `fma.llm-d.ai` API group; we only add new
  versions. Old versions may be deleted only once we are sure there is
  no further dev/test activity using them.

- During development of a PR that adds a version of the API group,
  successive revisions of the PR's head branch can change the
  definitions in the API group version being introduced. (The GHA
  workflow that does E2E test in the shared OpenShift cluster
  suppresses concurrent runs for the same PR.)

- Principle: each Git commit uses just one version of that API group,
  while recognizing that other users/tests in the same cluster may be
  using a different version.

- Corollary: a PR that adds a version of the API group changes the
  version used in all of the code and scripting.

- Only one open PR at a time proposes to add a particular new version
  of the API group.

- The GitHub Actions workflow that does E2E test in the shared
  OpenShift cluster conditionally uses `kubectl apply` to set the CRD
  objects to the values in the commit that the workflow is
  testing. That setting is done if and only if either the object is
  absent from the cluster or the commit's desired API group version is
  missing from the object in the cluster.

- Similarly, scripting for benchmarking and other shared clusters does
  the same: `kubectl apply` the CRD objects IFF either they are
  missing or lack the desired version. We publish the names of the CRD
  objects, and tell the admin of such shared clusters to authorize
  such users to perform CRUD on the CRD objects with those particular
  names. To make that easy, we maintain [a YAML
  file](../config/fma-cluster-admin/fma-crd-admin-clusterrole.yaml)
  holding the definition of a ClusterRole that declares that privilege.

- If/when it is desired to remove FMA from a shared cluster, an
  authorized person can use `kubectl delete -f config/crd` to remove
  all the FMA CRD objects.

- The Helm chart does nothing about the CRD objects.

- Optionally, we may improve our controllers to specifically detect
  and clearly report as such the condition of the relevant custom
  resources not being defined. This would be a fatal error as far as
  the controller is concerned, remediation is not the controller's
  job.

## Solution for the ValidatingAdmissionPolicy[Binding] Objects

(One design goal is to minimize chores for administrators of shared clusters.)

- These policy objects have fixed names.

- In the ValidatingAdmissionPolicy objects the test on ServiceAccount
  names matches any that ends with "-fma-controllers". This has the
  disadvantage of enabling hostile ServiceAccounts in the same
  namespace to make changes that would otherwise be forbidden. I think
  that this is OK because, as I understand it, the use case of a
  namespace running llm-d is managed infrastructure for running a
  higher level workload.

- We aim to evolve these policy objects in backward-compatible
  ways. That means that old tests succeed with the new
  definitions. This is not a strict requirement. When we must make an
  incompatible change: we insist (as usual) that the tests in
  non-shared clusters (e.g., `kind`) pass, and are willing to merge a PR
  whose only CI failure is in test cases that fail because of the
  change in these policy objects.

- Only one open PR at a time proposes to change these policy objects.

- The GitHub Actions workflow that does E2E test in the shared
  OpenShift cluster uses `kubectl apply` to set these policy objects
  to the values in the commit that the workflow is testing, without
  regard to whether these objects already exist or what their contents
  are (only conditioned on whether the cluster supports these kinds of
  objects).

- Similarly, scripting for benchmarking and other shared clusters does
  the same: `kubectl apply` the policy object YAMLs, without regard to
  whether these objects already exist or what their contents are. We
  publish the names of these policy objects, and tell the admin of
  such shared clusters to authorize such users to read and write these
  individual objects. To make that easy, we maintain [a YAML
  file](../config/fma-cluster-admin/fma-policy-admin-clusterrole.yaml)
  holding the definition of a ClusterRole that declares that
  privilege.

    The following demonstrates where these YAMLs are found and what
    are the names of the objects defined in them.

    ```console
    me@mymac llm-d-fast-model-actuation % (cd config/validating-admission-policies; grep name: *)
    bind-fma-bound-serverreqpod.yaml:  name: bind-fma-bound-serverreqpod
    bind-fma-immutable-fields.yaml:  name: bind-fma-immutable-fields
    fma-bound-serverreqpod.yaml:  name: fma-bound-serverreqpod
    fma-immutable-fields.yaml:  name: fma-immutable-fields
    ```

- If/when it is desired to remove FMA from a shared cluster, an
  authorized person can use `kubectl delete -f
  config/validating-admission-policies` to remove all the FMA
  ValidatingAdmissionPolicy[Binding] objects.

- The Helm chart does nothing about these policy objects.
