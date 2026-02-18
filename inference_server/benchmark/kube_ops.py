# Copyright 2025 The llm-d Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
# 	http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# ---------------- Logging setup ----------------
import logging

# Standard imports.
from abc import ABC, abstractmethod
from datetime import datetime
from logging import Logger
from random import randint
from subprocess import CalledProcessError
from subprocess import run as invoke_shell
from time import perf_counter, sleep
from typing import Any, Dict, Optional
from uuid import uuid4

from benchmark_diagnostics import (
    BenchmarkDiagnosis,
    BoundProviderPodInfo,
    ScenarioResult,
    ScenarioStatus,
)

# Third party imports.
from kubernetes import client, config, watch

# Local imports
from utils import delete_yaml_resources

logger = logging.getLogger(__name__)
logger.setLevel(logging.DEBUG)
formatter = logging.Formatter("%(asctime)s - %(levelname)s - %(message)s")

file_handler = logging.FileHandler(f"metrics-{datetime.now()}.log")
file_handler.setLevel(logging.DEBUG)
file_handler.setFormatter(formatter)

console_handler = logging.StreamHandler()
console_handler.setLevel(logging.INFO)
console_handler.setFormatter(formatter)

logger.addHandler(file_handler)
logger.addHandler(console_handler)


# Constants for provider modes
COLD_START_MODE = "Cold"
HIT_MODE = "Hit"

# Constants for pod counts
DUAL_POD_TOTAL = 2
DUAL_LABEL_KEY = "dual-pods.llm-d.ai/dual"
REQUESTER_PATCH_ANNOTATION = "dual-pod.llm-d.ai/server-patch"
ACCELERATOR_ANNOTATION = "dual-pods.llm-d.ai/accelerators"


# ---------------- Helper functions ----------------
def apply_yaml(yaml_file):
    """Apply a YAML file to the cluster."""
    invoke_shell(["kubectl", "apply", "-f", yaml_file], check=True)


def delete_yaml(yaml_file):
    """Delete resources from a YAML file."""
    invoke_shell(
        ["kubectl", "delete", "-f", yaml_file, "--ignore-not-found=true"],
        check=False,
    )


def scale_replicaset(yaml_file: str, replicas: int):
    """Scale the ReplicaSet in the YAML file to the specified number of replicas."""
    invoke_shell(
        ["kubectl", "scale", "--replicas", str(replicas), "-f", yaml_file],
        check=True,
    )


def delete_pod(namespace: str, pod_name: str):
    """Delete a pod by name in the specified namespace."""
    invoke_shell(
        [
            "kubectl",
            "delete",
            "pod",
            pod_name,
            "-n",
            namespace,
            "--ignore-not-found=true",
        ],
        check=False,
    )


def wait_for_dual_pods_ready(
    v1: client.CoreV1Api,
    namespace: str,
    rs_name,
    timeout=600,
    expected_replicas=1,
):
    """
    Wait for both dual pods to be ready and return timing information.
    :param v1: A reference to a CoreV1Api object for the REST calls.
    :param namespace: The namespace where the replicaset is deployed.
    :param rs_name: The name of the replicaset whose pods are to be waited.
    :param timeout: The max time to wait for all the pods to be ready.
    :param expected_replicas: The number of replicas expected for scaling.
    """
    start = perf_counter()
    elapsed = 0
    ready_pods = set()
    provider_pods = []

    # Track the set of pods that do not reach ready state in case of failure.
    unready_pods = set()

    # Track the dual pod controller pod in case of failure.
    dual_pod_controller = None

    logger.info(f"Waiting for pods of ReplicaSet: {rs_name}")

    def check_ready(pod):
        if pod.status.phase == "Running":
            for cond in pod.status.conditions or []:
                if cond.type == "Ready" and cond.status == "True":
                    return True
        return False

    # Initialize the variables to be returned
    node_name = None
    accelerator_info = None

    # Track pods that were already ready when we started watching
    initial_ready_pods = set()
    try:
        # Get initial state of pods
        pods = v1.list_namespaced_pod(namespace=namespace).items
        for pod in pods:
            ex_podname = pod.metadata.name

            if dual_pod_controller is None and "dpctlr" in ex_podname:
                dual_pod_controller = ex_podname

            pod_annotations = pod.metadata.annotations
            is_requester = REQUESTER_PATCH_ANNOTATION in pod_annotations
            if rs_name in ex_podname and check_ready(pod) and is_requester:
                initial_ready_pods.add(ex_podname)

                # Add them to ready pods for total cardinality of expected replicas.
                ready_pods.add(ex_podname)
                logger.debug(f"Initially ready pod {ex_podname}")
        logger.debug(f"Pods already ready at start: {initial_ready_pods}")
    except Exception as e:
        logger.warning(f"Could not get initial pod state: {e}")

    while elapsed < timeout:
        try:
            w = watch.Watch()
            for event in w.stream(
                v1.list_namespaced_pod,
                namespace=namespace,
                timeout_seconds=30,  # Frequent checks to reduce interruption impact
            ):
                pod = event["object"]
                podname = pod.metadata.name

                # Skip any pods that were in the initial set of ready pods or new pods
                # that have already been accounted for as ready.
                if podname in initial_ready_pods:
                    logger.debug(f"Skipping INITIALLY ready pod: {podname}")
                    continue
                elif podname in ready_pods:
                    logger.debug(f"Skipping NEWLY ready pod: {podname}")
                    continue

                # Add pod if it is not already in the list of unready pods.
                is_relevant_pod = podname not in ready_pods
                if (
                    (rs_name in podname)
                    and is_relevant_pod
                    and (podname not in unready_pods)
                    and is_requester
                ):
                    unready_pods.add(podname)
                    logger.debug(f"UNREADY pod added: {podname}")
                    logger.debug(f"Updated UNREADY pods: {unready_pods}")

                # Get the labels to filter out provider pods.
                labels = pod.metadata.labels

                # Filter the requester pods.
                is_requester = REQUESTER_PATCH_ANNOTATION in pod.metadata.annotations
                if (rs_name in podname) and is_requester:
                    logger.info(f"Checking readiness of Requester Pod:{podname}")
                    if check_ready(pod):
                        rq_ready = int(perf_counter() - start)
                        ready_pods.add(podname)
                        logger.debug(f"\nUpdated ready pods {ready_pods}")

                        # Capture node and accelerator info
                        node_name = pod.spec.node_name if pod.spec else None
                        accelerator_info = (
                            pod.metadata.annotations.get(ACCELERATOR_ANNOTATION)
                            if pod.metadata.annotations
                            else None
                        )

                        logger.info(
                            f"Requester Pod:{podname} ready after {rq_ready}s "
                            f"on node:{node_name} using GPU:{accelerator_info}"
                        )

                        # Checking availability mode.
                        dual_pod = labels[DUAL_LABEL_KEY]
                        binding_match = podname in dual_pod
                        if binding_match:
                            ready_pods.add(podname)
                            avail_mode = COLD_START_MODE
                            logger.info(
                                f"{dual_pod}:{podname} bound through a COLD START."
                            )
                        else:
                            ready_pods.add(podname)
                            avail_mode = HIT_MODE
                            logger.info(f"{dual_pod}:{podname} bound through a HIT.")

                        # Add the provider pod info to the list of bound pods.
                        provider_info = BoundProviderPodInfo(
                            podname,
                            dual_pod,
                            rq_ready,
                            avail_mode,
                            node_name,
                            accelerator_info,
                        )
                        provider_pods.append(provider_info)

                        # Remove the pod pair from the unready pods.
                        unready_pods.remove(podname)
                        unready_pods.discard(dual_pod)
                        logger.debug(f"{podname}:{dual_pod} removed from UNREADY set")

                if len(ready_pods) == expected_replicas:
                    end = perf_counter()
                    w.stop()
                    logger.info(
                        f"✅ All pods {ready_pods} Ready after {end - start:.2f}s"
                    )
                    return (
                        ScenarioResult(
                            status=ScenarioStatus.SUCCESS, provider_pods=provider_pods
                        ),
                        None,
                    )

            elapsed = perf_counter() - start

        except Exception as e:
            logger.warning(
                f"⚠️ Watch interrupted ({type(e).__name__}: {e}), retrying..."
            )
            sleep(1)  # Quick retry
            elapsed = perf_counter() - start

    # Collect diagnostics data before raising the time out error.
    logger.debug(f"Unready Pods: {unready_pods}, DPC: {dual_pod_controller}")
    scenario_result = ScenarioResult(
        status=ScenarioStatus.FAILURE,
        provider_pods=provider_pods,
        unready_pods=unready_pods,
        namespace=namespace,
        dual_pod_controller=dual_pod_controller,
        failed_rs_name=rs_name,
    )
    BenchmarkDiagnosis(logger).collect_diagnostics(scenario_result)
    err = TimeoutError(f"Timed out after {timeout}s waiting for both pods to be Ready.")
    return scenario_result, err


class KubernetesOps(ABC):
    """Abstract base class for Kubernetes operations (kind vs remote vs sim)."""

    def __init__(self, logger: Logger):
        """Initiate the instance with a logger from the caller."""
        self.logger = logger

    @abstractmethod
    def apply_yaml(self, yaml_file: str) -> None:
        pass

    @abstractmethod
    def delete_yaml(self, yaml_file: str) -> None:
        pass

    @abstractmethod
    def wait_for_dual_pods_ready(
        self, ns: str, *args: Any, **kwargs: Dict[Any, Any]
    ) -> tuple:
        pass

    @abstractmethod
    def scale_replicaset(
        self,
        request_yaml: str,
        expected_replicas: int,
        *args: Any,
        **kwargs: Dict[Any, Any],
    ):
        pass

    @abstractmethod
    def delete_pod(self, namespace: str, pod_name: str) -> None:
        pass


class KindKubernetesOps(KubernetesOps):
    """Kubernetes operations using a local kind cluster for time logging functions."""

    def __init__(self, logger: Logger, cluster_name: str):
        super().__init__(logger)

        self.v1_api = client.CoreV1Api()
        self.cluster_name = cluster_name
        self.setup_cluster()
        config.load_kube_config()

    def apply_yaml(self, yaml_file: str) -> None:
        apply_yaml(yaml_file)

    def delete_yaml(self, yaml_file: str) -> None:
        delete_yaml_resources(yaml_file)

    def wait_for_dual_pods_ready(
        self, ns: str, podname: str, timeout: int, expected_replicas: int
    ) -> tuple:
        return wait_for_dual_pods_ready(
            self.v1_api, ns, podname, timeout, expected_replicas
        )

    def delete_pod(self, namespace: str, pod_name: str) -> None:
        delete_pod(namespace, pod_name)

    def setup_cluster(
        self,
        dpc_controller_registry: str = "my-registry/my-namespace",
        dpc_tag: str = "v0.2.0",
    ):
        """
        Create cluster, build appropriate images, and load them into the cluster.
        :param dpc_controller_registry: The registry for the dual-pod controller.
        :param dpc_tag: The image tag to use for the dual-pod controller.
        """
        # Invoke the script for cluster creation and image build.
        self.logger.info(f"Setting up cluster: {self.cluster_name}")
        try:
            invoke_shell(
                [
                    "./inference_server/benchmark/setup_kind_resources.sh",
                    f"{self.cluster_name}",
                    f"{dpc_tag}",
                ],
                check=True,
            )
        except CalledProcessError as cpe:
            self.logger.debug("Kind Cluster set up errored")
            self.logger.debug(f"Err: {cpe.stderr}, Output: {cpe.stdout}")
            exit(1)

        # Deploy the helm chart for the FMA controllers in the cluster.
        full_registry = dpc_controller_registry + f"/dual-pods-controller:{dpc_tag}"
        self.logger.info(f"Deploying DPC Image {full_registry} in Kind Cluster")
        try:
            invoke_shell(
                [
                    "helm",
                    "upgrade",
                    "--install",
                    "dpctlr",
                    "charts/fma-controllers",
                    "--set",
                    f"dualPodsController.image.repository={dpc_controller_registry}/dual-pods-controller",
                    "--set",
                    f"dualPodsController.image.tag={dpc_tag}",
                    "--set",
                    "global.nodeViewClusterRole=node-viewer",
                    "--set",
                    "dualPodsController.sleeperLimit=2",
                    "--set",
                    "global.local=true",
                    "--set",
                    "launcherPopulator.enabled=false",
                ]
            )
        except CalledProcessError as cpe:
            self.logger.debug("Dual Pod Controller deployment in cluster errored")
            self.logger.debug(f"Err: {cpe.stderr}, Output: {cpe.stdout}")
            exit(1)

    def clean_up_cluster(self):
        """Remove the kind cluster and associated resources after benchmark is done."""
        invoke_shell(
            ["kind", "delete", "cluster", "--name", self.cluster_name], check=False
        )


class RemoteKubernetesOps(KubernetesOps):
    """Kubernetes operations for testing with a live, remote cluster."""

    def __init__(self, logger: Logger):
        super().__init__(logger)
        config.load_kube_config()
        self.v1_api = client.CoreV1Api()

    def apply_yaml(self, yaml_file: str) -> None:
        apply_yaml(yaml_file)

    def delete_yaml(self, yaml_file: str) -> None:
        delete_yaml(yaml_file)

    def scale_replicaset(self, yaml_file: str, replicas: int) -> None:
        scale_replicaset(yaml_file, replicas)

    def wait_for_dual_pods_ready(
        self, ns: str, rs_name: str, timeout: int, expected_replicas: int
    ) -> tuple:
        return wait_for_dual_pods_ready(
            self.v1_api,
            ns,
            rs_name,
            timeout,
            expected_replicas=expected_replicas,
        )

    def delete_pod(self, namespace: str, pod_name: str) -> None:
        delete_pod(namespace, pod_name)


class SimKubernetesOps(KubernetesOps):
    """Kubernetes operations for testing without a live cluster."""

    def __init__(
        self, logger: Logger, simulated_delays: Optional[Dict[str, float]] = None
    ):
        super().__init__(logger)
        """Set default simulated delays for different setups based on prior data."""
        self.simulated_delays = simulated_delays or {
            "Cold Start": 400,
            "Cached": 82,
            "Hit": 6,
        }

    def apply_yaml(self, yaml_file: str) -> None:
        self.logger.info(f"[SIMULATED] Applying {yaml_file}...")

    def delete_yaml(self, yaml_file: str) -> None:
        self.logger.info(f"[SIMULATED] Deleting resources from {yaml_file}")

    def scale_replicaset(self, yaml_file: str, expected_replicas: int):
        self.logger.info(f"[SIMULATED] Scaled for {yaml_file} to {expected_replicas}")

    def wait_for_dual_pods_ready(
        self,
        ns: str,
        rs_name: str,
        timeout: int,
        expected_replicas: int,
        context: Dict[str, Any] = None,
    ) -> tuple:

        self.logger.info(f"[SIMULATED] Waiting on ReplicaSet: {rs_name}")

        # Simulate readiness time based on contextual delay or defaults.
        if context is not None and context["Delay"]:
            rq_delay = context["Delay"]
            mode = context["Mode"]
        else:
            # Randomly select from a cold start delay or provider pod hit.
            possible_modes = ["Cold Start", "Hit"]
            mode = possible_modes[randint(0, len(possible_modes) - 1)]
            rq_delay = self.simulated_delays[mode]

        # Set the provider pod delay to be close to the requester delay.
        self.logger.info(
            f"[SIMULATED] Waiting for pods in {ns}... Ready after {rq_delay}s"
        )

        # Sleep a tiny amount to simulate async behavior.
        sleep(0.01)

        # Generate random info for the assigned node and accelerator.
        requester_pod_name = uuid4()
        node_name = uuid4()
        provider_pod_name = [uuid4()]
        accelerator_info = uuid4()
        provider_info = BoundProviderPodInfo(
            requester_pod_name, provider_pod_name, mode, node_name, accelerator_info
        )
        provider_pods = [provider_info]

        return rq_delay, mode, provider_pods

    def delete_pod(self, namespace: str, pod_name: str) -> None:
        self.logger.info(
            f"[SIMULATED] Deleting pod {pod_name} in namespace {namespace}"
        )
