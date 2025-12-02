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
from json import loads
from pathlib import Path
from time import sleep, time
from typing import Any, Dict, List

# Local imports.
from benchmark_diagnostics import IterationResult, ScenarioStatus
from utils import replace_repo_variables


def run_baseline_scenario(
    benchmark: Any,
    timeout: int,
    yaml_file: str = None,
    rs_name_prefix: str = "my-request",
) -> List[Dict[str, Any]]:
    """
    Run the baseline benchmark scenario with multiple iterations.
    :param benchmark: The benchmark instance to run the scenario on.
    :param timeout: The max time to allocate for all pods to be checked.
    :param rs_name_prefix: The externally defined prefix to attach to each
    replicaset name

    :return: A list of the results for each iteration of the scenario.
    """
    benchmark.results = []
    cleanup = benchmark.cleanup_enabled
    iterations = benchmark.iterations
    scenario = benchmark.scenario
    max_replicas = benchmark.max_replicas
    yaml_template = yaml_file if yaml_file else benchmark.yaml_template_file
    try:
        for i in range(iterations):
            iter_num = str(i + 1)
            benchmark.logger.info(f"Running iteration {iter_num}")

            # Generate a unique replicaset YAML for the iteration.
            rs_name = rs_name_prefix + f"-{iter_num}-" + str(int(time()))
            benchmark.logger.debug(f"ReplicaSet Name: {rs_name}")
            request_yaml = benchmark.create_request_yaml(rs_name, yaml_template)
            benchmark.intermediate_files.append(request_yaml)

            try:
                benchmark.logger.debug(f"Applying YAML: {request_yaml}")
                benchmark.k8_ops.apply_yaml(request_yaml)

                # Scale up
                _run_scaling_phase(
                    benchmark,
                    request_yaml,
                    rs_name,
                    timeout,
                    max_replicas,
                    iter_num,
                    scenario,
                    "up",
                )
            except Exception as e:
                benchmark.logger.error(f"Iteration {i+1} failed with error: {e}")
                result = IterationResult(
                    success=False,
                    error=e.__str__(),
                    scenario=scenario,
                    phase="up",
                    iteration=iter_num,
                )
                benchmark.results.append(result)
            finally:
                if cleanup:
                    benchmark.logger.debug(
                        f"Finally deleting YAML file: {request_yaml}"
                    )
                    benchmark.k8_ops.delete_yaml(request_yaml)
                    benchmark.cleanup_resources()

    finally:
        # Clean up intermediate YAML files created during benchmark
        benchmark.cleanup_intermediate_files()

        # Delete the associated cluster for a kind benchmark.
        if benchmark.op_mode == "kind":
            benchmark.k8_ops.clean_up_cluster()

    return benchmark.results


def run_scaling_scenario(
    benchmark: Any,
    timeout: int,
    yaml_file: str = None,
    rs_name_prefix: str = "scale-request",
) -> List[Dict[str, Any]]:

    benchmark.results = []
    cleanup = benchmark.cleanup_enabled
    iterations = benchmark.iterations
    scenario = benchmark.scenario
    max_replicas = benchmark.max_replicas
    yaml_template = yaml_file if yaml_file else benchmark.yaml_template_file

    for i in range(iterations):
        iter_num = str(i + 1)
        benchmark.logger.info(f"Running iteration {iter_num}")

        try:
            rs_name = rs_name_prefix + f"-{iter_num}-" + str(int(time()))
            benchmark.logger.debug(f"ReplicaSet Name: {rs_name}")
            request_yaml = benchmark.create_request_yaml(rs_name, yaml_template)
            benchmark.intermediate_files.append(request_yaml)

            # Apply the initial deployment at 0 replicas
            benchmark.logger.debug(f"Applying initial YAML: {request_yaml}")
            benchmark.k8_ops.apply_yaml(request_yaml)

            # Scale up
            _run_scaling_phase(
                benchmark,
                request_yaml,
                rs_name,
                timeout,
                max_replicas,
                iter_num,
                scenario,
                "up",
            )

            # Scale down
            benchmark.logger.debug("=== Scaling step down to 1 replica ===")
            benchmark.k8_ops.scale_replicaset(request_yaml, 1)

            # Slow down to ensure any goner requester pods do not taint number of
            # initial ready pods for the scale up again.
            if benchmark.op_mode != "simulated":
                benchmark.logger.debug(
                    "Slowing down by 10 secs for stale pods to go away"
                )
                sleep(10)

            # Scale up again
            _run_scaling_phase(
                benchmark,
                request_yaml,
                rs_name,
                timeout,
                max_replicas,
                iter_num,
                scenario,
                "up_again",
            )

        except Exception as e:
            benchmark.logger.error(f"Iteration {iter_num} failed with error: {e}")
            result = IterationResult(
                success=False,
                error=e.__str__(),
                scenario=scenario,
                iteration=iter_num,
            )
            benchmark.results.append(result)
        finally:
            # Delete the YAML resources from the cluster
            if cleanup:
                benchmark.logger.debug(f"Finally deleting YAML file: {request_yaml}")
                benchmark.k8_ops.delete_yaml(request_yaml)
                benchmark.cleanup_resources()

    # Clean up intermediate YAML files created during scaling scenario
    benchmark.cleanup_intermediate_files()

    # Delete the associated cluster for a kind benchmark.
    if benchmark.op_mode == "kind" and cleanup:
        benchmark.k8_ops.clean_up_cluster()

    return benchmark.results


def _run_scaling_phase(
    benchmark,
    request_yaml: str,
    rs_name: str,
    timeout: int,
    expected_pods: int,
    iteration_num: str,
    scenario: str,
    phase: str,
) -> None:
    """
    Scale the replicaset, wait for readiness,
    track provider pods, and output a result dict.
    """
    benchmark.logger.debug(
        f"=== Scaling step '{phase}' to {expected_pods} replicas ==="
    )
    benchmark.k8_ops.scale_replicaset(request_yaml, expected_pods)

    # Query GPU usage only for emulated or real GPUs.
    if benchmark.op_mode != "simulated":
        benchmark.logger.info("=== Busy GPUs Before Iteration ===")
        benchmark.query_gpu_usage()

    readiness_result, err = benchmark.k8_ops.wait_for_dual_pods_ready(
        benchmark.namespace,
        rs_name,
        timeout,
        expected_pods,
    )

    # Track provider pods created in cold start mode for cleanup
    for pod in readiness_result.provider_pods:
        benchmark.logger.debug(f"Check Mode on Pod: {pod}")
        if pod.avail_mode == "Cold":
            if not hasattr(benchmark, "provider_pods"):
                benchmark.provider_pods = []
            provider_pod_name = pod.provider
            benchmark.provider_pods.append(provider_pod_name)
            benchmark.logger.debug(
                f"Added provider pod {provider_pod_name} to cleanup list"
            )
        iter_result = IterationResult(
            rq_time=pod.rq_time,
            avail_mode=pod.avail_mode,
            success=True,
            iteration=iteration_num,
            scenario=scenario,
            phase=phase,
        )
        benchmark.results.append(iter_result)

    # Check whether errors occured for any of the dual pods.
    if readiness_result.status == ScenarioStatus.FAILURE:
        benchmark.logger.warning(
            f"Scaling step '{phase}' for request {rs_name} errored: {err}"
        )

        for pod in readiness_result.unready_pods:
            if "dual" not in pod:
                iter_result = IterationResult(
                    success=False,
                    error=err.__str__(),
                    iteration=iteration_num,
                    scenario=scenario,
                    phase=phase,
                )
                benchmark.results.append(iter_result)

        # Print the intermediate results before stopping benchmark.
        if benchmark.results:
            benchmark.pretty_print_results()

        # Clean up intermediate YAML files created during scaling scenario
        benchmark.cleanup_intermediate_files()

        exit(1)


def run_new_variant_scenario(
    benchmark: Any,
    timeout: int,
    yaml_file: str = None,
    rs_name_prefix: str = "model-request",
) -> List[Dict[str, Any]]:
    """
    Run the scenario to introduce a new model variant.
    :param benchmark: The benchmark instance to run the scenario on.
    :param timeout: The timeout in seconds for the execution of the pod requests.
    :param yaml_file: The YAML template file to use (optional, uses benchmark default
        if None).

    :return: A list with the results for the different models.
    """
    results = []

    if not benchmark.model_path:
        benchmark.logger.error("model_path not provided for new_variant scenario")
        return results

    yaml_template = yaml_file if yaml_file else benchmark.yaml_template_file

    # Load the file with all the models.
    all_models = None
    models_abs_path = Path(benchmark.model_path).absolute()
    if not Path(models_abs_path).exists():
        benchmark.logger.info(f"Path to models {models_abs_path} does not exist!")
        return results
    else:
        with Path(models_abs_path).open(mode="rb") as model_json_fd:
            all_models = loads(model_json_fd.read())["models"]
            benchmark.logger.info(f"All Models: {all_models}")

    # Generate the general template with container image repository and tag.
    for model in all_models:
        model_parts = model.split("/")
        model_registry = model_parts[0]
        model_repo = model_parts[1]
        model_info = f"Model Registry: {model_registry}, Model Repo: {model_repo}"
        benchmark.logger.info(model_info)
        model_template = replace_repo_variables(
            benchmark.requester_img_tag.split(":")[0],  # image repo
            benchmark.requester_img_tag,  # full image tag
            yaml_template,
            model_registry,
            model_repo,
        )

        # Generate a unique replicaset YAML for a particular model.
        benchmark.scenario = "variant-" + model_registry + "-" + model_repo
        model_results = run_baseline_scenario(
            benchmark,
            timeout,
            model_template,
            rs_name_prefix,
        )
        results.extend(model_results)

        # Print the results and remote intermediate files.
        benchmark.pretty_print_results()

    return results
