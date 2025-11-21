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
from datetime import datetime
from json import loads
from pathlib import Path
from statistics import median
from subprocess import run as invoke_shell
from typing import Any, Dict, List, Optional

# Local imports
from kube_ops import KindKubernetesOps, RemoteKubernetesOps, SimKubernetesOps
from scenarios import (
    run_baseline_scenario,
    run_new_variant_scenario,
    run_scaling_scenario,
)
from utils import BaseLogger, parse_request_args, replace_repo_variables


class DualPodsBenchmark:
    """Benchmark class for dual-pod inference server readineness."""

    def __init__(
        self,
        op_mode: str = "kind",
        simulation_delays: Optional[Dict[str, float]] = None,
        log_output_file: str = "metrics.log",
        cluster_name: str = "fmabenchmark",
        model_path: Optional[str] = None,
    ):
        """
        Initialize the benchmark class.

        :param op_mode: The operational mode for the benchmark (one of remote, kind, or
                        simulated)
        :param simulation_delays: Customized delays in secs for the simulated mode.
        :param log_output_file: File path for logging output
        :param cluster_name: Name of the cluster for kind mode
        :param model_path: Path to JSON file containing models for new_variant scenario
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
        self.provider_pods: List[str] = []

        # Parse inputs and set as class properties
        parsed_inputs = self.parse_inputs()
        self.namespace = parsed_inputs[0]
        self.yaml_template_file = parsed_inputs[1]
        self.requester_img = parsed_inputs[2]
        self.requester_img_tag = parsed_inputs[3]
        self.cleanup_enabled = parsed_inputs[4]
        self.iterations = parsed_inputs[5]
        self.cluster_domain = parsed_inputs[6]
        parsed_model_path = parsed_inputs[7]
        self.scenario = parsed_inputs[8]
        self.max_replicas = parsed_inputs[9]

        # Use model_path from parameter if provided, otherwise from parsed args
        self.model_path = model_path if model_path is not None else parsed_model_path

        input_str = self.describe_inputs()
        self.logger.info(input_str)

    def describe_inputs(self):
        """Get pretty print version of the user inputs"""
        pretty_print_str = "Namespace: {} \n".format(self.namespace)
        pretty_print_str += "Request YAML File: {}\n".format(self.yaml_template_file)
        pretty_print_str += "Requester Pod Image: {} \n".format(self.requester_img)
        pretty_print_str += "Cleanup all pods at end of run: {} \n".format(
            self.cleanup_enabled
        )
        if self.iterations > 1:
            pretty_print_str += "Requested Iterations: {} \n".format(self.iterations)
        else:
            pretty_print_str += "Default Iterations: {} \n".format(self.iterations)
        pretty_print_str += "Cluster domain: {} \n".format(self.cluster_domain)
        pretty_print_str += "Scenario: {}".format(self.scenario)

        if self.model_path:
            pretty_print_str += "\nModel Path: {}".format(self.model_path)

        if self.max_replicas > 1:
            pretty_print_str += "\nRequested Max Replicas: {}".format(self.max_replicas)

        return pretty_print_str

    def parse_inputs(self) -> tuple:
        """Parse user inputs from the CLI."""
        all_args = parse_request_args()
        ns = all_args.namespace
        yaml_template = all_args.yaml
        requester_img = all_args.image
        requester_img_tag = all_args.tag
        cleanup = all_args.cleanup
        iterations = all_args.iterations
        cluster_domain = (
            all_args.cluster_domain if hasattr(all_args, "cluster_domain") else None
        )
        model_path = getattr(all_args, "model_path", None)
        scenario = getattr(all_args, "scenario", "scaling")
        max_replicas = all_args.max_replicas

        if scenario == "new_variant" and not model_path:
            raise ValueError(
                "The --model-path argument is required when scenario=new_variant"
            )

        # Generate the request YAML from template and image details.
        request_yaml_template_file = replace_repo_variables(
            requester_img, requester_img_tag, yaml_template
        )

        # Track the template file for cleanup
        self.template_files.append(str(request_yaml_template_file))

        return (
            ns,
            request_yaml_template_file,
            requester_img,
            requester_img_tag,
            cleanup,
            iterations,
            cluster_domain,
            model_path,
            scenario,
            max_replicas,
        )

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
        self,
        timeout: int = 1000,
        scenario: str = None,
    ) -> List[Dict[str, Any]]:
        """
        Run the benchmark.

        :param timeout: Timeout for each run in seconds.
        :param scenario: Benchmark scenario ("baseline", "scaling", or "new_variant").
                        If None, uses the scenario from command line arguments.
        :return: List of result dictionaries.
        """

        # Use provided scenario or default to instance scenario
        benchmark_scenario = scenario if scenario is not None else self.scenario

        if benchmark_scenario == "scaling":
            return run_scaling_scenario(self, timeout, self.yaml_template_file)
        elif benchmark_scenario == "new_variant":
            return run_new_variant_scenario(self, timeout, self.yaml_template_file)

        return run_baseline_scenario(
            self, timeout, benchmark_scenario, self.yaml_template_file
        )

    def get_results(self) -> Dict[str, Any]:
        """
        Aggregate and return the benchmark results.

        :return: Dict with summary of stats (e.g., average, min, max, etc)
        """
        if not self.results:
            return {}

        success_runs = [run for run in self.results if run.success]
        rq_times = [run.rq_time for run in success_runs if run.rq_time is not None]

        # For scaling scenarios, only count hits from the
        # only phase that can wake up sleeping provider pods
        if any(run.scenario == "scaling" for run in self.results):
            scale_up_again_runs = [
                run for run in success_runs if run.phase == "up_again"
            ]
            hit_runs = [run for run in scale_up_again_runs if run.avail_mode == "Hit"]
            hits = len(hit_runs)
            hit_rq_times = [run.rq_time for run in hit_runs if run.rq_time is not None]
            hit_percent_base = len(scale_up_again_runs)
        else:
            # For non-scaling scenarios, use all successful runs
            hit_runs = [run for run in success_runs if run.avail_mode == "Hit"]
            hits = len(hit_runs)
            hit_rq_times = [run.rq_time for run in hit_runs if run.rq_time is not None]
            hit_percent_base = len(success_runs)

        summary = {
            "total_runs": len(self.results),
            "successful_runs": len(success_runs),
            "failed_runs": len(self.results) - len(success_runs),
            "hits": hits,
            "total_hit_runs": hit_percent_base,
            "hit_percent": (
                int((hits / hit_percent_base) * 100) if hit_percent_base > 0 else 0
            ),
            "rq_min": min(rq_times) if rq_times else None,
            "rq_max": max(rq_times) if rq_times else None,
            "rq_avg": (sum(rq_times) / len(rq_times) if rq_times else None),
            "rq_median": median(rq_times) if rq_times else None,
            "hit_prv_min": min(hit_rq_times) if hit_rq_times else None,
            "hit_prv_max": max(hit_rq_times) if hit_rq_times else None,
            "hit_prv_avg": (
                sum(hit_rq_times) / len(hit_rq_times) if hit_rq_times else None
            ),
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
        total_hit_runs = summary["total_hit_runs"]
        hit_percent = summary["hit_percent"]
        rq_min = summary["rq_min"]
        rq_max = summary["rq_max"]
        rq_avg = summary["rq_avg"]
        rq_median = summary["rq_median"]
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
        rq_stats += f"\n\tAverage: {rq_avg}s"
        rq_stats += f"\n\tMedian: {rq_median}s\n"
        avail_stats = f"Hits: {hits}/{total_hit_runs} ({hit_percent}%)\n"

        if hits > 0:
            hit_stats = (
                f"Hit Wake-up Times \n\tMin: {hit_prv_min}s, \n\tMax: {hit_prv_max}s"
            )
            hit_stats += f"\n\tAverage: {hit_prv_avg}s\n"
            avail_stats += hit_stats

        summary_str = "".join([run_str, rq_stats, avail_stats])
        self.logger.info(summary_str)

    def cleanup_intermediate_files(self):
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
                self.logger.warning(
                    f"Failed to clean up template file {template_file}: {e}"
                )

    def cleanup_resources(self):
        """Clean up any remaining resources in kind or remote cluster."""
        if hasattr(self, "provider_pods"):
            for provider_pod in self.provider_pods:
                self.logger.debug(f"Cleaning up provider pod: {provider_pod}")
                self.k8_ops.delete_pod(self.namespace, provider_pod)
            self.provider_pods.clear()

    def query_gpu_usage(self):
        """Query GPU usage from Prometheus metrics in OpenShift cluster."""
        cluster_domain = self.cluster_domain
        if not cluster_domain:
            self.logger.warning("cluster_domain not set, skipping GPU usage query")
            return []

        try:
            token_result = invoke_shell(
                ["oc", "whoami", "-t"], capture_output=True, text=True, check=True
            )
            token = token_result.stdout.strip()

            prometheus_url = (
                f"https://prometheus-k8s-openshift-monitoring.apps."
                f"{cluster_domain}/api/v1/query"
            )
            query_result = invoke_shell(
                [
                    "curl",
                    "-sSkG",
                    "-H",
                    f"Authorization: Bearer {token}",
                    "--data-urlencode",
                    "query=DCGM_FI_DEV_FB_USED",
                    prometheus_url,
                ],
                capture_output=True,
                text=True,
                check=True,
            )
            self.logger.debug(f"Query Result: \n{query_result}\n")

            gpu_data = loads(query_result.stdout)
            gpu_list = []
            for result in gpu_data.get("data", {}).get("result", []):
                gpu_info = {
                    "Hostname": result["metric"].get("Hostname"),
                    "GPU": result["metric"].get("gpu"),
                    "ID": result["metric"].get("UUID"),
                    "Assoc": result["metric"].get("exported_namespace") is not None,
                    "Mem": result["value"][1] if len(result["value"]) > 1 else None,
                }
                gpu_list.append(gpu_info)

            gpu_list.sort(key=lambda x: (x["Hostname"] or "", x["GPU"] or ""))
            for gpu in gpu_list:
                if gpu["Mem"] and float(gpu["Mem"]) > 0:
                    self.logger.info(f"GPU: {gpu}")

            return gpu_list

        except Exception as e:
            self.logger.warning(f"Failed to query GPU usage: {e}")
            return []


if __name__ == "__main__":
    # Create benchmark instance (automatically parses command line arguments)
    date_stamp = datetime.now().isoformat(timespec="minutes")
    log_output_file = f"benchmark-{date_stamp}.log"
    benchmark = DualPodsBenchmark("remote", log_output_file=log_output_file)

    # Run benchmark using scenario from command line args (defaults to "scaling")
    results = benchmark.run_benchmark(timeout=1000)
    benchmark.pretty_print_results()
