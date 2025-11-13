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
import logging
from datetime import datetime
from json import loads
from pathlib import Path
from time import sleep, time
from typing import Any, Dict, List

# Local imports.
from benchmark_base import DualPodsBenchmark
from utils import parse_request_args, replace_repo_variables
from typing import List, Dict, Any

# ---------------- Logging setup ----------------

logger = logging.getLogger(__name__)
logger.setLevel(logging.INFO)
formatter = logging.Formatter("%(asctime)s - %(levelname)s - %(message)s")

file_handler = logging.FileHandler(f"metrics-{datetime.now()}.log")
file_handler.setLevel(logging.DEBUG)
file_handler.setFormatter(formatter)

console_handler = logging.StreamHandler()
console_handler.setLevel(logging.INFO)
console_handler.setFormatter(formatter)

logger.addHandler(file_handler)
logger.addHandler(console_handler)


def run_standard_scenario(
    benchmark: DualPodsBenchmark,
    timeout: int,
    scenario: str,
    yaml_file: str = None,
    rs_name_prefix: str = "my-request",
) -> List[Dict[str, Any]]:
    """
    Run the standard benchmark scenario with multiple iterations.
    :param benchmark: The benchmark instance to run the scenario on.
    :param timeout: The max time to allocate for all pods to be checked.
    :param scenario: Externally defined details on the scenario.
    :param rs_name_prefix: The externally defined prefix to attach to each
    replicaset name

    :return: A list of the results for each iteration of the scenario.
    """
    benchmark.results = []
    cleanup = benchmark.cleanup_enabled
    iterations = benchmark.iterations
    yaml_template = yaml_file if yaml_file else benchmark.yaml_template_file
    try:
        for i in range(iterations):
            iter_num = str(i + 1)
            benchmark.logger.info(f"Running iteration {iter_num}")

            # Generate a unique replicaset YAML for the iteration.
            rs_name = rs_name_prefix + f"{iter_num}-" + str(int(time()))
            benchmark.logger.debug(f"ReplicaSet Name: {rs_name}")
            request_yaml = benchmark.create_request_yaml(rs_name, yaml_template)
            benchmark.intermediate_files.append(request_yaml)

            try:
                # Query GPU usage before iteration
                benchmark.logger.info(f"=== GPU Usage Before Iteration {iter_num} ===")
                benchmark.query_gpu_usage()

                benchmark.logger.debug(f"Applying YAML: {request_yaml}")
                benchmark.k8_ops.apply_yaml(request_yaml)

                # Check for pod readiness.
                (
                    rq_ready,
                    prv_mode,
                    provider_pod_name,
                    node_name,
                    accelerator_info,
                ) = benchmark.k8_ops.wait_for_dual_pods_ready(benchmark.namespace, rs_name, timeout, 1)
                # Track provider pods created in cold start mode for cleanup
                if prv_mode == "Cold" and provider_pod_name:
                    if not hasattr(benchmark, "provider_pods"):
                        benchmark.provider_pods = []
                    benchmark.provider_pods.append(provider_pod_name)
                    benchmark.logger.debug(
                        f"Added provider pod {provider_pod_name} to cleanup list"
                    )

                # Compile the result.
                result = {
                    "iteration": i + 1,
                    "scenario": scenario,
                    "rq_time": rq_ready,
                    "availability_mode": prv_mode,
                    "success": True,
                }
            except Exception as e:
                benchmark.logger.error(f"Iteration {i+1} failed with error: {e}")
                result = {
                    "iteration": i + 1,
                    "scenario": scenario,
                    "rq_time": None,
                    "availability_mode": "No Server Providing Pod Available",
                    "success": False,
                    "error": e.__str__(),
                }
            finally:
                if cleanup:
                    benchmark.logger.debug(f"Finally deleting YAML file: {request_yaml}")
                    benchmark.k8_ops.delete_yaml(request_yaml)
                    benchmark.cleanup_resources()

            benchmark.results.append(result)

    finally:
        # Clean up intermediate YAML files created during benchmark
        benchmark._cleanup_intermediate_files()

        # Delete the associated cluster for a kind benchmark.
        if benchmark.op_mode == "kind":
            benchmark.k8_ops.clean_up_cluster()

    return benchmark.results


def run_scaling_scenario(
    benchmark: DualPodsBenchmark,
    timeout: int,
    yaml_file: str = None,
) -> List[Dict[str, Any]]:
    """Run the scaling scenario: 0→1, 1→2, 2→1, 1→2 replica scaling."""
    benchmark.results = []
    cleanup = benchmark.cleanup_enabled
    iterations = benchmark.iterations
    yaml_template = yaml_file if yaml_file else benchmark.yaml_template_file

    for i in range(iterations):
        iter_num = str(i + 1)
        benchmark.logger.info(f"Running iteration {iter_num}")

        try:
            # Generate a unique replicaset YAML for the scaling scenario
            rs_name = f"scale-request-{iter_num}-" + str(int(time()))
            benchmark.logger.debug(f"ReplicaSet Name: {rs_name}")
            request_yaml = benchmark.create_request_yaml(rs_name, yaml_template)
            benchmark.intermediate_files.append(request_yaml)

            # Query GPU usage before iteration
            benchmark.logger.info(
                f"=== GPU Usage Before Scaling Iteration {iter_num} ==="
            )
            benchmark.query_gpu_usage()

            # Apply the initial deployment at 0 replicas
            benchmark.logger.debug(f"Applying initial YAML: {request_yaml}")
            benchmark.k8_ops.apply_yaml(request_yaml)

            benchmark.logger.debug("=== Scaling from 0 to 1 replica ===")
            benchmark.k8_ops.scale_replicaset(request_yaml, 1)

            try:
                (
                    rq_ready,
                    prv_mode,
                    provider_pod_name,
                    node_name,
                    accelerator_info,
                ) = benchmark.k8_ops.wait_for_dual_pods_ready(benchmark.namespace, rs_name, timeout, 1)
                # Track provider pods created in cold start mode for cleanup
                if prv_mode == "Cold" and provider_pod_name:
                    if not hasattr(benchmark, "provider_pods"):
                        benchmark.provider_pods = []
                    benchmark.provider_pods.append(provider_pod_name)
                    benchmark.logger.debug(
                        f"Added provider pod {provider_pod_name} to cleanup list"
                    )

                result = {
                    "iteration": iter_num,
                    "scenario": "scaling",
                    "phase": "0_to_1",
                    "rq_time": rq_ready,
                    "availability_mode": prv_mode,
                    "success": rq_ready is not None,
                }
            except TimeoutError as e:
                benchmark.logger.warning(
                    f"Scaling 0->1 timed out for iteration {iter_num}: {e}"
                )
                result = {
                    "iteration": iter_num,
                    "scenario": "scaling",
                    "phase": "0_to_1",
                    "rq_time": None,
                    "availability_mode": "timeout",
                    "success": False,
                    "error": str(e),
                }

            benchmark.results.append(result)
            benchmark.logger.info(f"Scaling 0->1 Status: {result['success']}")

            benchmark.logger.debug("=== Scaling from 1 to 2 replicas ===")
            benchmark.k8_ops.scale_replicaset(request_yaml, 2)

            try:
                (
                    rq_ready,
                    prv_mode,
                    provider_pod_name,
                    node_name,
                    accelerator_info,
                ) = benchmark.k8_ops.wait_for_dual_pods_ready(benchmark.namespace, rs_name, timeout, 2)
                # Track provider pods created in cold start mode for cleanup
                if prv_mode == "Cold" and provider_pod_name:
                    if not hasattr(benchmark, "provider_pods"):
                        benchmark.provider_pods = []
                    benchmark.provider_pods.append(provider_pod_name)
                    benchmark.logger.debug(
                        f"Added provider pod {provider_pod_name} to cleanup list"
                    )

                result = {
                    "iteration": iter_num,
                    "scenario": "scaling",
                    "phase": "1_to_2",
                    "rq_time": rq_ready,
                    "availability_mode": prv_mode,
                    "success": rq_ready is not None,
                }
            except TimeoutError as e:
                benchmark.logger.warning(
                    f"Scaling 1->2 timed out for iteration {iter_num}: {e}"
                )
                result = {
                    "iteration": iter_num,
                    "scenario": "scaling",
                    "phase": "1_to_2",
                    "rq_time": None,
                    "availability_mode": "timeout",
                    "success": False,
                    "error": str(e),
                }

            benchmark.results.append(result)
            benchmark.logger.info(f"Scaling 1->2 Status: {result['success']}")

            benchmark.logger.debug("=== Scaling from 2 to 1 replica ===")
            benchmark.k8_ops.scale_replicaset(request_yaml, 1)

            # Slow down to ensure any goner requester pods do not taint number of
            # initial ready pods for the scale up from 1-2 again.
            benchmark.logger.debug("Slowing down by 10 secs for stale pods to go away")
            sleep(10)

            benchmark.logger.debug("=== Scaling from 1 to 2 replicas (again) ===")
            benchmark.k8_ops.scale_replicaset(request_yaml, 2)

            try:
                (
                    rq_ready,
                    prv_mode,
                    provider_pod_name,
                    node_name,
                    accelerator_info,
                ) = benchmark.k8_ops.wait_for_dual_pods_ready(benchmark.namespace, rs_name, timeout, 2)
                # Track provider pods created in cold start mode for cleanup
                if prv_mode == "Cold" and provider_pod_name:
                    if not hasattr(benchmark, "provider_pods"):
                        benchmark.provider_pods = []
                    benchmark.provider_pods.append(provider_pod_name)
                    benchmark.logger.debug(
                        f"Added provider pod {provider_pod_name} to cleanup list"
                    )

                result = {
                    "iteration": iter_num,
                    "scenario": "scaling",
                    "phase": "1_to_2_again",
                    "rq_time": rq_ready,
                    "availability_mode": prv_mode,
                    "success": rq_ready is not None,
                }
            except TimeoutError as e:
                benchmark.logger.warning(
                    f"Scaling 1->2 (again) timed out for iteration {iter_num}: {e}"
                )
                result = {
                    "iteration": iter_num,
                    "scenario": "scaling",
                    "phase": "1_to_2_again",
                    "rq_time": None,
                    "availability_mode": "timeout",
                    "success": False,
                    "error": str(e),
                }

            benchmark.results.append(result)
            benchmark.logger.info(f"Scaling 1->2 (Again) Status: {result['success']}")

        finally:
            # Delete the YAML resources from the cluster
            if cleanup:
                benchmark.logger.debug(f"Finally deleting YAML file: {request_yaml}")
                benchmark.k8_ops.delete_yaml(request_yaml)
                benchmark.cleanup_resources()

    # Clean up intermediate YAML files created during scaling scenario
    benchmark._cleanup_intermediate_files()

    # Delete the associated cluster for a kind benchmark.
    if benchmark.op_mode == "kind" and cleanup:
        benchmark.k8_ops.clean_up_cluster()

    return benchmark.results


def run_new_variant_scenario(
    benchmark: DualPodsBenchmark, timeout: int, yaml_file: str = None
) -> List[Dict[str, Any]]:
    """
    Run the scenario to introduce a new model variant.
    :param benchmark: The benchmark instance to run the scenario on.
    :param timeout: The timeout in seconds for the execution of the pod requests.
    :param yaml_file: The YAML template file to use (optional, uses benchmark default if None).

    :return: A list with the results for the different models.
    """
    results = []

    if not benchmark.model_path:
        logger.error("model_path not provided for new_variant scenario")
        return results

    yaml_template = yaml_file if yaml_file else benchmark.yaml_template_file

    # Load the file with all the models.
    all_models = None
    models_abs_path = Path(benchmark.model_path).absolute()
    if not Path(models_abs_path).exists():
        logger.info(f"Path to models {models_abs_path} does not exist!")
        return results
    else:
        with Path(models_abs_path).open(mode="rb") as model_json_fd:
            all_models = loads(model_json_fd.read())["models"]
            logger.info(f"All Models: {all_models}")

    # Generate the general template with container image repository and tag.
    for model in all_models:
        model_parts = model.split("/")
        model_registry = model_parts[0]
        model_repo = model_parts[1]
        logger.info(f"Model Registry: {model_registry}, Model Repo: {model_repo}")
        model_template = replace_repo_variables(
            benchmark.requester_img_tag.split(":")[0],  # image repo
            benchmark.requester_img_tag,  # full image tag
            yaml_template,
            model_registry,
            model_repo,
        )

        # Generate a unique replicaset YAML for a particular model.
        rs_prefix = "model-request"
        model_scenario = "variant-" + model_registry + "-" + model_repo
        model_results = run_standard_scenario(
            benchmark,
            timeout,
            model_scenario,
            model_template,
            rs_prefix,
        )
        results.extend(model_results)

        # Print the results and remote intermediate files.
        benchmark.pretty_print_results()

    return results
