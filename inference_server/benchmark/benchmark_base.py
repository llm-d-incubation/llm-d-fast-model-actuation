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
from typing import Any, Dict, List, Optional

# Local imports
from utils import BaseLogger


class DualPodsBenchmark:
    """Benchmark class for dual-pod inference server readineness."""

    def __init__(
        self,
        op_mode: str = "kind",
        simulation_delays: Optional[Dict[str, float]] = None,
    ):
        """
        Initialize the benchmark class.

        :param op_mode: The operational mode for the benchmark (one of remote, kind, or
                        simulated)
        :param simulation_delays: Customized delays in secs for the simulated mode
                                  depending on the scenario
        """
        self.logger = BaseLogger(self.__name__)
        self.op_mode = op_mode
        if op_mode == "kind":  # Default
            self.logger.info("Operating with kind cluster.")
            # Set context with a kind cluster.
        elif op_mode == "remote":
            self.logger.info("Operating with remote cluster.")
            # Load config for the remote cluster.
        elif op_mode == "simulated":
            self.logger.info("Operating in simulated moder.")
            # Load simulation parameters for the particular scenario.
        else:
            raise ValueError("Mode must be one of [kind, remote, simulated]")

        self.parsed_inputs = None
        self.results: List[Dict[str, Any]] = []

    def parse_inputs(self) -> tuple:
        """
        Parse user inputs from the CLI.
        """
        pass

    def configure_scenario(self, scenario: str, **kwargs) -> Dict[str, Any]:
        """
        Configure benchmark settings based on the given scenario.

        :param scenario: Scenario name (e.g., "Fast Replica Scale Up")
        :param kwargs: Scenario-specific params such as number of GPUs, variants, etc.
        """
        pass

    def run_benchmark(
        self, scenario: str, iterations: int = 1, timeout: int = 600, **scenario_kwargs
    ) -> List[Dict[str, Any]]:
        """
        Run the benchmark for a given scenario.

        :param scenario: The scenario name.
        :param iterations: Number of iterations for run.
        :param timeout: Timeout for each run in seconds.
        :param scenario_kwargs: Parameters for configuring the scenario.
        :return: List of result dictionaries.
        """
        pass

    def get_results(self) -> Dict[str, Any]:
        """
        Aggregate and return the benchmark results.

        :return: Dict with summary of stats (e.g., average, min, max, etc)
        """
        pass

    def cleanup_resources(self):
        """Clean up any remaining resources in kind or remote cluster."""
        pass
