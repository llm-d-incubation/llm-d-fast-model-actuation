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

# Standard imports.
from abc import ABC, abstractmethod
from logging import Logger
from random import randint
from subprocess import run as invoke_shell, CalledProcessError
from time import perf_counter, sleep
from typing import Any, Dict, Optional

# Third party imports.
from kubernetes import client, config, watch

# Local imports
from utils import delete_yaml_resources

# ---------------- Logging setup ----------------
import logging
logger = logging.getLogger(__name__)
logger.setLevel(logging.INFO)
formatter = logging.Formatter("%(asctime)s - %(levelname)s - %(message)s")

file_handler = logging.FileHandler("metrics.log")
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


def wait_for_dual_pods_ready(
    v1: client.CoreV1Api, namespace: str, rs_name, timeout=600, expected_replicas=1,
    suffix="dual",
):
    """
    Wait for both dual pods to be ready and return timing information.
    :param v1: A reference to a CoreV1Api object for the REST calls.
    :param namespace: The namespace where the replicaset is deployed.
    :param rs_name: The name of the replicaset whose pods are to be waited.
    :param timeout: The max time to wait for all the pods to be ready.
    :param expected_replicas: The number of replicas expected for scaling.
    :param suffix: The suffix added to distinguish requester pods from provider pods.
    """
    start = perf_counter()
    elapsed = 0
    ready_pods = set()

    logger.info(f"Waiting for pods of ReplicaSet: {rs_name}")

    def check_ready(pod):
        if pod.status.phase == "Running":
            for cond in pod.status.conditions or []:
                if cond.type == "Ready" and cond.status == "True":
                    return True
        return False

    # Initialize the variables to be returned
    rq_ready = None
    prv_ready = None
    prv_mode = COLD_START_MODE

    # Track pods that were already ready when we started watching
    initial_ready_pods = set()
    try:
        # Get initial state of pods
        pods = v1.list_namespaced_pod(namespace=namespace).items
        for pod in pods:
            if rs_name in pod.metadata.name and check_ready(pod):
                initial_ready_pods.add(pod.metadata.name)
                # Add them to ready pods for total cardinality of expected pods.
                ready_pods.add(pod.metadata.name)
        logger.info(f"Pods already ready at start: {initial_ready_pods}")
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

                # Skip any pods that were in the intial set of ready pods.
                if podname in initial_ready_pods:
                    logger.info(f"Skipping initially ready pod: {podname}")
                    continue

                # Get the labels to filter out provider pods.
                labels = pod.metadata.labels

                # Filter the requester pods.
                if (rs_name in podname) and (suffix not in podname):
                    logger.info(f"Checking Readiness of Requester Pod: {podname}")
                    if check_ready(pod) and (podname not in ready_pods) and (podname not in initial_ready_pods):
                        rq_ready = int(perf_counter() - start)
                        ready_pods.add(podname)

                # Filter any provider pods that are bound to a requester pod.
                elif suffix in podname and "dual-pods.llm-d.ai/dual" in labels:

                    # Get the server-requesting it is bound to, if any.
                    dual_pod = labels["dual-pods.llm-d.ai/dual"]
                    logger.info(
                        f"Checking Ready of Provider for Pair <{dual_pod}>:<{podname}>"
                    )

                    # Set the return variables for the ready pod.
                    if check_ready(pod) and (podname not in ready_pods) and (podname not in initial_ready_pods):
                        binding_match = rs_name in dual_pod and rs_name in podname
                        if binding_match:
                            prv_ready = int(perf_counter() - start)
                            ready_pods.add(podname)
                            prv_mode = COLD_START_MODE
                            logger.info(f"{dual_pod}:{podname} bound through a Cold start")
                        elif not binding_match:
                            prv_ready = int(perf_counter() - start)
                            ready_pods.add(podname)
                            prv_mode = HIT_MODE
                            logger.info(f"{dual_pod}:{podname} bound through a Hit")

                if len(ready_pods) == DUAL_POD_TOTAL * expected_replicas:
                    end = perf_counter()
                    w.stop()
                    logger.info(f"✅ Both pods Ready after {end - start:.2f}s")
                    return rq_ready, prv_ready, prv_mode

            elapsed = perf_counter() - start

        except Exception as e:
            logger.warning(
                f"⚠️ Watch interrupted ({type(e).__name__}: {e}), retrying..."
            )
            sleep(1)  # Quick retry
            elapsed = perf_counter() - start

    raise TimeoutError(f"Timed out after {timeout}s waiting for both pods to be Ready.")


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
    def wait_for_dual_pods_ready(self, ns: str, podname: str, timeout: int) -> float:
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

    def wait_for_dual_pods_ready(self, ns: str, podname: str, timeout: int) -> float:
        return wait_for_dual_pods_ready(self.v1_api, ns, podname, timeout)

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

        # Deploy the helm chart for the dual pod controller in the cluster.
        full_registry = dpc_controller_registry + f"/dual-pods-controller:{dpc_tag}"
        self.logger.info(f"Deploying DPC Image {full_registry} in Kind Cluster")
        try:
            invoke_shell(
                [
                    "helm",
                    "upgrade",
                    "--install",
                    "dpctlr",
                    "charts/dpctlr",
                    "--set",
                    f"Image={full_registry}",
                    "--set",
                    "NodeViewClusterRole=node-viewer",
                    "--set",
                    "SleeperLimit=2",
                    "--set",
                    "Local=true",
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
        self, ns: str, podname: str, timeout: int, replicas=1
    ) -> float:
        return wait_for_dual_pods_ready(
            self.v1_api, ns, podname, timeout, expected_replicas=replicas, suffix="dual"
        )


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

    def wait_for_dual_pods_ready(
        self, ns: str, podname: str, timeout: int, context: Dict[str, Any] = None
    ) -> float:
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
        prv_delay = rq_delay + 1
        self.logger.info(
            f"[SIMULATED] Waiting for pods in {ns}... Ready after {rq_delay}s"
        )

        # Sleep a tiny amount to simulate async behavior.
        sleep(0.01)

        return rq_delay, prv_delay, mode
