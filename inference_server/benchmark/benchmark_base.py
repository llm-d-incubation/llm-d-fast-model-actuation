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
from time import sleep, time
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
        cluster_name: str = "fmabenchmark",
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

            self.logger.info(f"Operating with kind cluster: {cluster_name}")

            # Set context with a kind cluster.
            self.k8_ops = KindKubernetesOps(self.logger, cluster_name)

        elif op_mode == "remote":
            self.logger.info("Operating with remote cluster.")
            # Load config for the remote cluster.
            self.k8_ops = RemoteKubernetesOps(self.logger)

        elif op_mode == "simulated":
            self.logger.info("Operating in simulated mode.")
            # Load simulation parameters for the particular scenario.
            self.k8_ops = SimKubernetesOps(self.logger)
        else:
            raise ValueError("Mode must be one of [kind, remote, simulated]")

        self.results: List[Dict[str, Any]] = []
        self.intermediate_files: List[str] = []
        self.template_files: List[str] = []

        self.parsed_inputs = self.parse_inputs()
        input_str = self.describe_inputs()
        self.logger.info(input_str)

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

        # Track the template file for cleanup
        self.template_files.append(str(request_yaml_file))

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
        sed_cmd = "s#${REPLICASET_NAME}#" + rs_name + "#"
        rs_name_yaml = rs_name + ".yaml"
        with Path(rs_name_yaml).open(mode="wb") as rs_yaml_fd:
            invoke_shell(
                ["sed", "-e", sed_cmd, yaml_template_file],
                stdout=rs_yaml_fd,
                check=False,
            )

        return rs_name_yaml


    def run_benchmark(
        self, iterations: int = 1, timeout: int = 1000, scenario: str = "baseline"
    ) -> List[Dict[str, Any]]:
        """
        Run the benchmark.

        :param iterations: Number of iterations for run.
        :param timeout: Timeout for each run in seconds.
        :param scenario: Benchmark scenario ("baseline" or "scaling")
        :return: List of result dictionaries.
        """
        ns, yaml_file, pod_label, image = self.parsed_inputs

        if scenario == "scaling":
            return self._run_scaling_scenario(ns, yaml_file, timeout)

        return self._run_standard_scenario(iterations, timeout, scenario, ns, yaml_file)

    def _run_standard_scenario(
        self, iterations: int, timeout: int, scenario: str, ns: str, yaml_file: str
    ) -> List[Dict[str, Any]]:
        """Run the standard benchmark scenario with multiple iterations."""
        self.results = []
        try:
            for i in range(iterations):
                iter_num = str(i + 1)
                self.logger.info(f"Running iteration {iter_num}")

                # Generate a unique replicaset YAML for the iteration.
                rs_name = "my-request-" + f"{iter_num}-" + str(int(time()))
                self.logger.info(f"ReplicaSet Name: {rs_name}")
                request_yaml = self.create_request_yaml(rs_name, yaml_file)
                self.intermediate_files.append(request_yaml)

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
                        "scenario": scenario,
                        "rq_time": rq_ready,
                        "prv_time": prv_ready,
                        "availability_mode": prv_mode,
                        "success": True,
                    }
                except Exception as e:
                    self.logger.error(f"Iteration {i+1} failed with error: {e}")
                    result = {
                        "iteration": i + 1,
                        "scenario": scenario,
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
        finally:
            # Clean up intermediate YAML files created during benchmark
            self._cleanup_intermediate_files()

            # Delete the associated cluster for a kind benchmark.
            if self.op_mode == "kind":
                self.k8_ops.clean_up_cluster()

        return self.results

    def _run_scaling_scenario(self, ns: str, yaml_file: str, timeout: int) -> List[Dict[str, Any]]:
        """Run the scaling scenario: 0→1, 1→2, 2→1, 1→2 replica scaling."""
        self.results = []

        try:
            # Generate a unique replicaset YAML for the scaling scenario
            rs_name = "scale-request-" + str(int(time()))
            self.logger.info(f"ReplicaSet Name for scaling: {rs_name}")
            request_yaml = self.create_request_yaml(rs_name, yaml_file)
            self.intermediate_files.append(request_yaml)

            # Apply the initial deployment at 0 replicas
            self.logger.info(f"Applying initial YAML: {request_yaml}")
            self.k8_ops.apply_yaml(request_yaml)

            self.logger.info("=== Scaling from 0 to 1 replica ===")
            self.k8_ops.scale_replicaset(request_yaml, 1)

            rq_ready, prv_ready, prv_mode = self.k8_ops.wait_for_dual_pods_ready(
                ns, rs_name, timeout
            )

            result = {
                "iteration": 1,
                "scenario": "scaling",
                "phase": "0_to_1",
                "rq_time": rq_ready,
                "prv_time": prv_ready,
                "availability_mode": prv_mode,
                "success": rq_ready is not None and prv_ready is not None
            }
            self.results.append(result)

            self.logger.info("=== Scaling from 1 to 2 replicas ===")
            self.k8_ops.scale_replicaset(request_yaml, 2)

            rq_ready, prv_ready, prv_mode = self.k8_ops.wait_for_dual_pods_ready(
                ns, rs_name, timeout, 2
            )

            result = {
                "iteration": 2,
                "scenario": "scaling",
                "phase": "1_to_2",
                "rq_time": rq_ready,
                "prv_time": prv_ready,
                "availability_mode": prv_mode,
                "success": rq_ready is not None and prv_ready is not None
            }
            self.results.append(result)

            self.logger.info("=== Scaling from 2 to 1 replica ===")
            self.k8_ops.scale_replicaset(request_yaml, 1)

            # For scale down, we don't wait for new pods, just record the operation
            result = {
                "iteration": 3,
                "scenario": "scaling",
                "phase": "2_to_1",
                "rq_time": None,  # No new pod timing for scale down
                "prv_time": None,
                "availability_mode": "scale_down",
                "success": True
            }
            self.results.append(result)

            # Slow down to ensure any goner requester pods do not taint number of initial
            # ready pods for the scale up from 1-2 again.
            slowdown = 10
            self.logger.info(f"Slowing down by {slowdown} secs for stale pods to go away")
            sleep(slowdown)

            self.logger.info("=== Scaling from 1 to 2 replicas (again) ===")
            self.k8_ops.scale_replicaset(request_yaml, 2)

            try:
                rq_ready, prv_ready, prv_mode = self.k8_ops.wait_for_dual_pods_ready(
                    ns, rs_name, timeout, 2
                )
                success = rq_ready is not None and prv_ready is not None
            except TimeoutError:
                self.logger.warning("Scale up timed out")
                rq_ready, prv_ready, prv_mode = None, None, "timeout"
                success = False

            result = {
                "iteration": 4,
                "scenario": "scaling",
                "phase": "1_to_2_again",
                "rq_time": rq_ready,
                "prv_time": prv_ready,
                "availability_mode": prv_mode,
                "success": success
            }
            self.results.append(result)

        finally:
            # Delete the YAML resources from the cluster
            self.logger.info(f"Finally deleting YAML file: {request_yaml}")
            self.k8_ops.delete_yaml(request_yaml)

            # Clean up intermediate YAML files created during scaling scenario
            self._cleanup_intermediate_files()

            # Delete the associated cluster for a kind benchmark.
            if self.op_mode == "kind":
                self.k8_ops.clean_up_cluster()

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

        # Compute the number of pods awoken.
        hits = len([run for run in success_runs if run["availability_mode"] == "Hit"])
        hit_runs = [run for run in success_runs if run["availability_mode"] == "Hit"]
        hit_prv_times = [
            run["prv_time"] for run in hit_runs if run["prv_time"] is not None
        ]

        summary = {
            "total_runs": len(self.results),
            "successful_runs": len(success_runs),
            "hits": hits,
            "hit_percent": int((hits / len(success_runs)) * 100) if success_runs else 0,
            "failed_runs": len(self.results) - len(success_runs),
            "rq_min": min(rq_times) if rq_times else None,
            "rq_max": max(rq_times) if rq_times else None,
            "rq_avg": (sum(rq_times) / len(rq_times) if rq_times else None),
            "prv_min": min(prv_times) if prv_times else None,
            "prv_max": max(prv_times) if prv_times else None,
            "prv_avg": (sum(prv_times) / len(prv_times) if prv_times else None),
            "hit_prv_min": min(hit_prv_times) if hit_prv_times else None,
            "hit_prv_max": max(hit_prv_times) if hit_prv_times else None,
            "hit_prv_avg": (sum(hit_prv_times) / len(hit_prv_times) if hit_prv_times else None),
            "all_results": self.results,
        }

        return summary


    def pretty_print_results(self):
        """Log the results in a human readable format."""
        summary = self.get_results()
        total_runs = summary["total_runs"]
        success_runs = summary["successful_runs"]
        failed_runs = summary["failed_runs"]
        hits = summary["hits"]
        hit_percent = summary["hit_percent"]
        rq_min = summary["rq_min"]
        rq_max = summary["rq_max"]
        rq_avg = summary["rq_avg"]
        prv_min = summary["prv_min"]
        prv_max = summary["prv_max"]
        prv_avg = summary["prv_avg"]
        hit_prv_min = summary["hit_prv_min"]
        hit_prv_max = summary["hit_prv_max"]
        hit_prv_avg = summary["hit_prv_avg"]

        run_str = (
            "---------------------------------------------------------------------"
        )
        run_str += (
            f"\n\nTotal Runs: {total_runs}\n" + f"Successful Runs: {success_runs}\n"
        )
        run_str += f"Failed Runs: {failed_runs}\n"
        rq_stats = f"Requester Pods \n\tMin: {rq_min}s, \n\tMax: {rq_max}s"
        rq_stats += f"\n\tAverage: {rq_avg}s\n"
        prv_stats = f"Provider Pods \n\tMin: {prv_min}s, \n\tMax: {prv_max}s"
        prv_stats += f"\n\tAverage: {prv_avg}s\n"
        avail_stats = f"Hits: {hits}/{success_runs} ({hit_percent}%)\n"

        if hits > 0:
            hit_stats = f"Hit Wake-up Times \n\tMin: {hit_prv_min}s, \n\tMax: {hit_prv_max}s"
            hit_stats += f"\n\tAverage: {hit_prv_avg}s\n"
            avail_stats += hit_stats

        summary_str = "".join([run_str, rq_stats, prv_stats, avail_stats])
        self.logger.info(summary_str)

    def _cleanup_intermediate_files(self):
        """Clean up intermediate YAML files created during benchmark iterations."""
        for yaml_file in self.intermediate_files:
            try:
                Path(yaml_file).unlink(missing_ok=True)
                self.logger.debug(f"Cleaned up intermediate file: {yaml_file}")
            except Exception as e:
                self.logger.warning(f"Failed to clean up {yaml_file}: {e}")

        # Also clean up template files created during input parsing
        for template_file in self.template_files:
            try:
                Path(template_file).unlink(missing_ok=True)
                self.logger.debug(f"Cleaned up template file: {template_file}")
            except Exception as e:
                self.logger.warning(f"Failed to clean up template file {template_file}: {e}")

    def cleanup_resources(self):
        """Clean up any remaining resources in kind or remote cluster."""
        if self.parsed_inputs:
            _, yaml_file, _, _ = self.parsed_inputs
            self.logger.info(f"Deleting YAML file: {yaml_file}")
            # TODO: Implement cleanup for kind v remote v simulation.
            # delete_yaml(yaml_file)


if __name__ == "__main__":
    # kind_log_path = "kind_logger.log"
    # kind_benchmark = DualPodsBenchmark("kind", log_output_file=kind_log_path)
    # sim_log_path = "sim_logger.log"
    # sim_benchmark = DualPodsBenchmark("simulated", log_output_file=sim_log_path)
    remote_log_path = "remote_logger.log"
    remote_benchmark = DualPodsBenchmark("remote", log_output_file=remote_log_path)
    all_benchmarks = [remote_benchmark]

    # Run example benchmarks
    for benchmark in all_benchmarks:
        # Run baseline scenario
        #results = benchmark.run_benchmark(4, scenario="baseline")
        #benchmark.pretty_print_results()

        # Run scaling scenario
        results = benchmark.run_benchmark(1, scenario="scaling")
        benchmark.pretty_print_results()
