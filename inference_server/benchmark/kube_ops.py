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
from random import randint
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

    def __init__(self, cluster_name: str):
        config.load_kube_config()
        self.v1_api = client.CoreV1Api()
        self.cluster_name = cluster_name

    def apply_yaml(self, yaml_file: str) -> None:
        apply_yaml(yaml_file)

    def delete_yaml(self, yaml_file: str) -> None:
        delete_yaml_resources(yaml_file)

    def wait_for_dual_pods_ready(self, ns: str, podname: str, timeout: int) -> float:
        return wait_for_dual_pods_ready(self.v1_api, ns, podname, timeout)

    def load_images_to_cluster(self):
        pass

    def clean_up_cluster(self):
        """Remove the kind cluster after benchmark is completed."""
        invoke_shell(
            ["kind", "delete", "cluster", "--name", self.cluster_name], check=False
        )


class RemoteKubernetesOps(KubernetesOps):
    """Kubernetes operations for testing with a live, remote cluster."""

    def __init__(self):
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

    def __init__(self, logger, simulated_delays: Optional[Dict[str, float]] = None):
        """Set default simulated delays for different setups based on prior data."""
        self.simulated_delays = simulated_delays or {
            "Cold Start": 400,
            "Cached": 82,
            "Hit": 6,
        }
        self.logger = logger

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
