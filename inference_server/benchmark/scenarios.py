# Standard imports.
import logging
from json import loads
from pathlib import Path
from time import time
from typing import Any, Dict, List

# Local imports.
from benchmark_base import DualPodsBenchmark
from utils import parse_request_args, replace_repo_variables

# ---------------- Logging setup ----------------

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


def run_new_variant_scenario(
    yaml_file: str, models: str, timeout: int
) -> List[Dict[str, Any]]:
    """
    Run the scenario to introduce a new model variant.
    :param yaml_file: The YAML template to customize for the scenario.
    :param models: A path to a JSON file containing the list of models to run.
    :param timeout: The timeout in seconds for the execution of the pod requests.

    :return: A dictionary with the results for the different models.
    """
    results = []

    # Load the file with all the models.
    all_models = None
    models_abs_path = Path(models).absolute()
    if not Path(models_abs_path).exists():
        logger.info(f"Path to models {models_abs_path} does not exist!")
        return results
    else:
        with Path(models_abs_path).open(mode="rb") as model_json_fd:
            all_models = loads(model_json_fd.read())["models"]
            logger.info(f"All Models: {all_models}")

    # Create the logger file and benchmark runner for the scenario
    model_log_path = "model_benchmark_logger-" + str(int(time())) + ".log"
    benchmark = DualPodsBenchmark("remote", model_log_path)

    # Load the user inputs for the requester image, image tag, and yaml template
    all_args = parse_request_args()
    ns = all_args.namespace
    yaml_template = all_args.yaml
    requester_img_repo = all_args.image
    requester_img_tag = all_args.tag

    # Generate the general template with container image repository and tag.
    for model in all_models:
        model_parts = model.split("/")
        model_registry = model_parts[0]
        model_repo = model_parts[1]
        logger.info(f"Model Registry: {model_registry}, Model Repo: {model_repo}")
        model_template = replace_repo_variables(
            requester_img_repo,
            requester_img_tag,
            yaml_template,
            model_registry,
            model_repo,
        )

        # Generate a unique replicaset YAML for a particular model.
        rs_prefix = "model-variant-request-"
        rs_prefix += f"{model_registry}-" + "".join(model_repo.split("-")[2:]) + "-"
        model_scenario = "variant-" + model_registry + "-" + model_repo
        results = benchmark._run_standard_scenario(
            1,
            timeout,
            scenario=model_scenario,
            ns=ns,
            yaml_file=model_template,
            rs_name_prefix=rs_prefix,
        )

        # Print the results and remote intermediate files.
        benchmark.pretty_print_results()


if __name__ == "__main__":
    models_path = "inference_server/benchmark/deploy/my_models.json"
    yaml_file = "inference_server/benchmark/deploy/server-request-generic-model.yaml"
    run_new_variant_scenario(yaml_file, models_path, 800)
