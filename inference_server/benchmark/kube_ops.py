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
from subprocess import CalledProcessError
from subprocess import run as invoke_shell
from time import sleep
from typing import Any, Dict, Optional

# Local imports
from dualpods_time_logs import apply_yaml, delete_yaml, wait_for_dual_pods_ready

# Third party imports.
from kubernetes import client, config
from utils import delete_yaml_resources


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

    def wait_for_dual_pods_ready(self, ns: str, podname: str, timeout: int) -> float:
        return wait_for_dual_pods_ready(
            self.v1_api, ns, podname, timeout, suffix="dual"
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
