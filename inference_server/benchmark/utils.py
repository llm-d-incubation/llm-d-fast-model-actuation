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

import argparse
import logging
from logging import DEBUG, INFO, FileHandler, Formatter, StreamHandler, getLogger
from os import getenv
from pathlib import Path
from subprocess import run as invoke_shell
from time import time
from uuid import uuid4

# ---------------- Logging setup ----------------
logger = logging.getLogger(__name__)
logger.setLevel(logging.INFO)
formatter = logging.Formatter("%(asctime)s - %(levelname)s - %(message)s")

file_handler = logging.FileHandler(f"metrics{int(time())}.log")
file_handler.setLevel(logging.DEBUG)
file_handler.setFormatter(formatter)

console_handler = logging.StreamHandler()
console_handler.setLevel(logging.INFO)
console_handler.setFormatter(formatter)

logger.addHandler(file_handler)
logger.addHandler(console_handler)


def parse_request_args():
    """
    Retrieve the arguments for launching the inference server
    request.
    """
    parser = argparse.ArgumentParser(
        description="Benchmarking the Dual-pods readiness time"
    )
    parser.add_argument(
        "--namespace",
        type=str,
        required=True,
        help="Openshift namespace to run benchmark",
    )
    parser.add_argument(
        "--yaml",
        type=str,
        default="deploy/server-request.yaml",
        help="Path to the server-requesting YAML file",
    )
    parser.add_argument(
        "--label",
        type=str,
        default="app=dp-example",
        help="Label selector for server-requesting pod",
    )
    parser.add_argument(
        "--cleanup",
        type=bool,
        default=True,
        help="Whether to clean up provider pods after benchmark completion",
    )

    # Check for a container image env variables before adding to the parser.
    requester_img = getenv("CONTAINER_IMG_REG")
    img_tag = getenv("CONTAINER_IMG_VERSION")
    if requester_img and img_tag:
        logger.info("Requester Image Loaded from ENV: {requester_img}:{img_tag}")
    else:  # Force user to pass both the image and tage as arguments.
        logger.info("CONTAINER_IMG_REG is not set locally")
        parser.add_argument(
            "--image",
            type=str,
            required=True,
            help="Repository for the requester image",
        )
        parser.add_argument(
            "--tag",
            type=str,
            required=True,
            help="Version tag for the requester image",
        )

    args = parser.parse_args()

    # Validate the path for the YAML template.
    yaml_template = args.yaml
    yaml_template_path = Path(yaml_template)
    if not (yaml_template_path).exists():
        raise FileNotFoundError(f"{yaml_template} path does not exist!")

    # Override the provided template path with the absolute version.
    if not (yaml_template_path.is_absolute()):
        args.yaml = yaml_template_path.absolute()
    else:
        args.yaml = yaml_template_path

    return args


def replace_repo_variables(
    requester_image_repo: str,
    image_tag: str,
    request_yaml_template: str,
    model_registry: str = "ibm-granite",
    model_repo: str = "granite-3.3-2b-instruct",
):
    """
    Replace the variable for the inference server container image.
    :param requester_image_repo: The repository for the inference server
                                 container images.
    :param image_tag: The particular tag to use for the container image.
    :param request_yaml_template: The local path for the inference server request
                                  template YAML file.
    :param model_registry: The name of the model registry to insert.
    :param model_repo: The name of the model repository to insert.
    """
    # Check that yaml path exists before invoking sed.
    request_yaml_path = Path(request_yaml_template)
    if not (request_yaml_path).exists():
        raise FileNotFoundError(f"{request_yaml_template} path does not exist!")

    # Invoke the replacement in the template for redirection.
    sed_script = "s#${MODEL_REGISTRY}#" + model_registry + "#\n"
    sed_script += "s#${MODEL_REPO}#" + model_repo + "#\n"
    sed_script += "s#${CONTAINER_IMG_REG}#" + requester_image_repo + "#\n"
    sed_script += "s#${CONTAINER_IMG_REG}#" + requester_image_repo + "#\n"
    sed_script += "s#${CONTAINER_IMG_VERSION}#" + image_tag + "#"
    updated_request_file = "inf-server-request-template-" + str(uuid4()) + ".yaml"
    updated_request_file_path = Path(updated_request_file)
    with Path(updated_request_file_path).open(mode="wb") as yaml_fd:
        invoke_shell(
            ["sed", "-e", sed_script, request_yaml_template],
            stdout=yaml_fd,
            check=False,
        )

    return updated_request_file


class BaseLogger:
    """Base class for a single logger that all the classes inherit from."""

    def __init__(self, log_output_file: str, owner: str = ""):
        """
        Initialize the base logger class.

        :param owner: The class or invoker of the logger for easy tracing.
        :param log_output_file: The path where to write logs if not the default.
        """
        self.logger = getLogger(owner + "Logger")
        # Set default level and formatting.
        self.logger.setLevel(INFO)
        formatter = Formatter("%(asctime)s - %(levelname)s - %(message)s")

        # Create the console and stream handler.
        self.file_handler = FileHandler(log_output_file)
        self.file_handler.setLevel(DEBUG)
        self.file_handler.setFormatter(formatter)
        self.console_handler = StreamHandler()
        self.console_handler.setLevel(INFO)
        self.console_handler.setFormatter(formatter)
        self.logger.addHandler(self.file_handler)
        self.logger.addHandler(self.console_handler)

    def get_custom_logger(self):
        """
        Get the custom logger created by the class.
        """
        return self.logger


def delete_yaml_resources(yaml_file):
    """Delete the resources created with the YAML and delete the file itself."""
    yaml_path = Path(yaml_file)
    if not yaml_path.exists():
        logger.warning(f"YAML file {yaml_file} does not exist, skipping cleanup")
        return

    logger.info(f"Cleaning up resources from {yaml_file}...")
    invoke_shell(
        ["kubectl", "delete", "-f", yaml_file, "--ignore-not-found=true"], check=False
    )
    invoke_shell(["rm", yaml_file], check=False)
