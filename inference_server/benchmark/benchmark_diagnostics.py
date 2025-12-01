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

# Standard imports
from dataclasses import dataclass
from enum import Enum
from logging import Logger
from pathlib import Path
from subprocess import run as invoke_shell
from typing import List


class ScenarioStatus(Enum):
    SUCCESS = 1
    FAILURE = 2


@dataclass
class BoundProviderPodInfo:
    """A datastore for information on server providing pod in ready state."""

    requester: str
    provider: str
    rq_time: int
    avail_mode: str
    node: str
    accelerator_info: str


@dataclass
class IterationResult:
    """A datastore for the result of a benchmark iteration."""

    success: bool
    # Defaults to empty when all goes well.
    error: str = ""
    scenario: str = "scaling"
    phase: str = ""
    iteration: str = ""
    rq_time: int = 0
    avail_mode: str = ""


@dataclass
class ScenarioResult:
    """A datastore for the status and results of a benchmark scenario."""

    # Status is updated in case of failure, otherwise defaults to Success.
    status: ScenarioStatus
    # The list of bound provider pods.
    provider_pods: List
    # Empty set when all goes well.
    unready_pods: set = ()
    # Defaults to empty (no need to pass back) when all goes well.
    namespace: str = ""
    # Defaults to empty (no need to pass back) when all goes well.
    dual_pod_controller: str = ""
    # Defaults to empty string when all goes well.
    failed_rs_name: str = ""


class BenchmarkDiagnosis:
    """A diagnostic class to collect info on a failing benchmark before exiting."""

    def __init__(self, logger: Logger):
        """
        Initialize the diagnosis class.

        :param logger: The inherited logger to use, if any.
        """
        self.logger = logger

    def collect_diagnostics(self, result: ScenarioResult):
        """
        Collect logs on a failing pod and dual pod controller.
        :param result: The data structure with details on the failed iteration.
        """
        # Create a directory to house the logs for a failed iteration.
        rs_name = result.failed_rs_name
        rs_dir_name = str(Path.cwd().absolute()) + "/" + rs_name + "-failure-logs"
        Path(rs_dir_name).mkdir()
        self.logger.info(f"Dumping error logs in {rs_dir_name}")

        # Collect the logs of the dual pod controller pod.
        dp_log_name = rs_dir_name + "/" + result.dual_pod_controller + ".log"
        Path(dp_log_name).touch()
        with Path(dp_log_name).open(mode="wb") as dp_log_fd:
            invoke_shell(
                ["kubectl", "logs", "-n", result.namespace, result.dual_pod_controller],
                stdout=dp_log_fd,
                check=False,
            )
        self.logger.info(f"Dumped DPC logs at {dp_log_name}")

        # Collect the logs of all the pods that never reached ready status.
        for unready_pod in result.unready_pods:
            pod_log_name = rs_dir_name + "/" + unready_pod + ".log"
            Path(pod_log_name).touch()
            with Path(pod_log_name).open(mode="wb") as pod_log_fd:
                invoke_shell(
                    ["kubectl", "logs", "-n", result.namespace, unready_pod],
                    stdout=pod_log_fd,
                    check=False,
                )
            self.logger.info(f"Dumped Pod log for {unready_pod} at {pod_log_name}")
