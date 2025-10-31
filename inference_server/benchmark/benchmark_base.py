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
from pathlib import Path
from subprocess import run as invoke_shell
from time import time
from typing import Any, Dict, List, Optional

from kube_ops import KindKubernetesOps, RemoteKubernetesOps, SimKubernetesOps

# Local imports
from utils import BaseLogger, parse_request_args, replace_repo_variable


class DualPodsBenchmark:
    """Benchmark class for dual-pod inference server readineness."""

    def __init__(
        self,
        op_mode: str = "kind",
        simulation_delays: Optional[Dict[str, float]] = None,
        log_output_file: str = "metrics.log",
        cluster_name: str = None,
    ):
        """
        Initialize the benchmark class.

        :param op_mode: The operational mode for the benchmark (one of remote, kind, or
                        simulated)
        :param simulation_delays: Customized delays in secs for the simulated mode.
        """
        logger = BaseLogger(log_output_file, self.__class__.__name__)
        self.logger = logger.get_custom_logger()
        self.logger.info("Logger Type: %s" % (self.logger.name))
        self.op_mode = op_mode
        if op_mode == "kind":  # Default

            # Check that the cluster name is specified.
            if cluster_name is None:
                raise ValueError("You must specify a kind cluster name")

            # Check that the kind cluster exists locally.
            all_clusters = invoke_shell(
                ["kind", "get", "clusters"],
                capture_output=True,
                text=True,
            ).stdout

            if not (cluster_name in all_clusters.strip("\n")):
                raise ValueError(f"Kind cluster {cluster_name} does not exist")

            self.logger.info(f"Operating with kind cluster: {cluster_name}")

            # Set context with a kind cluster.
            self.k8_ops = KindKubernetesOps(cluster_name)

        elif op_mode == "remote":
            self.logger.info("Operating with remote cluster.")
            # Load config for the remote cluster.
            self.k8_ops = RemoteKubernetesOps()

        elif op_mode == "simulated":
            self.logger.info("Operating in simulated mode.")
            # Load simulation parameters for the particular scenario.
            self.k8_ops = SimKubernetesOps(self.logger)
        else:
            raise ValueError("Mode must be one of [kind, remote, simulated]")

        self.parsed_inputs = self.parse_inputs()
        input_str = self.describe_inputs()
        self.logger.info(input_str)
        self.results: List[Dict[str, Any]] = []

    def describe_inputs(self):
        """Get pretty print version of the user inputs"""
        pretty_print_str = "Namespace: {} \n".format(self.parsed_inputs[0])
        pretty_print_str += "Request YAML File: {}\n".format(self.parsed_inputs[1])
        pretty_print_str += "Requester Pod Label: {} \n".format(self.parsed_inputs[2])
        pretty_print_str += "Requester Pod Image: {}".format(self.parsed_inputs[3])
        return pretty_print_str

    def parse_inputs(self) -> tuple:
        """Parse user inputs from the CLI."""
        all_args = parse_request_args()
        ns = all_args.namespace
        yaml_template = all_args.yaml
        requester_pod_label = all_args.label
        requester_img = all_args.image
        requester_img_tag = all_args.tag

        # Generate the request YAML from template and image details.
        request_yaml_file = replace_repo_variable(
            requester_img, requester_img_tag, yaml_template
        )

        return ns, request_yaml_file, requester_pod_label, requester_img_tag

    def create_request_yaml(self, rs_name: str, yaml_template_file: str) -> str:
        """
        Generate a request YAML file from the replicaset name.

        :param rs_name: A unique name for the replicaset.
        :param yaml_template_file: A "template" file with the container image registry
                                   and tag already filled in.
        :return: A string representing the path to the YAML file.
        """
        # Invoke the replacement with the unique replicaset name.
        sed_cmd = "s#{REQUEST_NAME}#" + rs_name + "#"
        rs_name_yaml = rs_name + ".yaml"
        with Path(rs_name_yaml).open(mode="wb") as rs_yaml_fd:
            invoke_shell(
                ["sed", "-e", sed_cmd, yaml_template_file],
                stdout=rs_yaml_fd,
                check=False,
            )

        return rs_name_yaml

    def run_benchmark(
        self, iterations: int = 1, timeout: int = 600
    ) -> List[Dict[str, Any]]:
        """
        Run the benchmark.

        :param iterations: Number of iterations for run.
        :param timeout: Timeout for each run in seconds.
        :return: List of result dictionaries.
        """
        ns, yaml_file, pod_label, image = self.parsed_inputs

        self.results = []
        for i in range(iterations):
            iter_num = str(i + 1)
            self.logger.info(f"Running iteration {iter_num}")

            # Generate a unique replicaset YAML for the iteration.
            rs_name = "my-request-" + f"{iter_num}-" + str(int(time()))
            self.logger.info(f"ReplicaSet Name: {rs_name}")
            request_yaml = self.create_request_yaml(rs_name, yaml_file)

            try:
                self.logger.info(f"Applying YAML: {request_yaml}.")
                self.k8_ops.apply_yaml(request_yaml)

                # Check for pod readiness.
                rq_ready, prv_ready, prv_mode = self.k8_ops.wait_for_dual_pods_ready(
                    ns, rs_name, timeout
                )

                # Compile the result.
                result = {
                    "iteration": i + 1,
                    # "scenario": scenario,
                    "rq_time": rq_ready,
                    "prv_time": prv_ready,
                    "availability_mode": prv_mode,
                    "success": True,
                }
            except Exception as e:
                self.logger.error(f"Iteration {i+1} failed with error: {e}")
                result = {
                    "iteration": i + 1,
                    # "scenario": scenario,
                    "rq_time": None,
                    "prv_time": None,
                    "availability_mode": "No Server Providing Pod Available",
                    "success": False,
                    "error": e.__str__(),
                }
            finally:
                self.logger.info(f"Finally deleting YAML file: {request_yaml}")
                self.k8_ops.delete_yaml(request_yaml)

            self.results.append(result)

        return self.results

    def get_results(self) -> Dict[str, Any]:
        """
        Aggregate and return the benchmark results.

        :return: Dict with summary of stats (e.g., average, min, max, etc)
        """
        if not self.results:
            return {}

        success_runs = [run for run in self.results if run["success"]]
        rq_times = [
            run["rq_time"] for run in success_runs if run["rq_time"] is not None
        ]
        prv_times = [
            run["prv_time"] for run in success_runs if run["prv_time"] is not None
        ]

        summary = {
            "Total Runs": len(self.results),
            "Successful Runs": len(success_runs),
            "Failed Runs": len(self.results) - len(success_runs),
            "Average Requester Time": (
                sum(rq_times) / len(rq_times) if rq_times else None
            ),
            "Min Requester Time": min(rq_times) if rq_times else None,
            "Max Requester Time": max(rq_times) if rq_times else None,
            "Average Provider Time": (
                sum(prv_times) / len(prv_times) if prv_times else None
            ),
            "Min Provider Time": min(prv_times) if prv_times else None,
            "Max Provider Time": max(prv_times) if prv_times else None,
            "All Results": self.results,
        }

        return summary

    def cleanup_resources(self):
        """Clean up any remaining resources in kind or remote cluster."""
        if self.parsed_inputs:
            _, yaml_file, _, _ = self.parsed_inputs
            self.logger.info(f"Deleting YAML file: {yaml_file}")
            # TODO: Implement cleanup for kind v remote v simulation.
            # delete_yaml(yaml_file)


if __name__ == "__main__":
    # kind_log_path = "kind_logger.log"
    # kind_benchmark = DualPodsBenchmark(
    #    "kind", cluster_name="fmatest", log_output_file=kind_log_path
    # )
    # sim_log_path = "sim_logger.log"
    # sim_benchmark = DualPodsBenchmark("simulated", log_output_file=sim_log_path)
    remote_log_path = "remote_logger.log"
    remote_benchmark = DualPodsBenchmark("remote", log_output_file=remote_log_path)
    # all_benchmarks = [sim_benchmark, kind_benchmark]
    all_benchmarks = [remote_benchmark]

    # Run example benchmarks
    for benchmark in all_benchmarks:
        results = benchmark.run_benchmark(4)
        for result in results:
            print(f"\nIteration: {result['iteration']}")
            print(f"Requester Ready: {result['rq_time']}")
            print(f"Provider: {result['prv_time']}")
            print(f"Availability Mode: {result['availability_mode']}\n")
        # benchmark.run_benchmark("Introducing New Variant", 3, **kwargs)
