# Copyright 2025 The llm-d Authors.

# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at

# 	http://www.apache.org/licenses/LICENSE-2.0

# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.


"""
GPU Translator
"""

import importlib.metadata
import logging
from typing import Dict

import pynvml

logger = logging.getLogger(__name__)


# VLLM process manager
class GpuTranslator:
    def __init__(self):
        """
        Initialize GPU Translator
        """
        self.mapping = {}
        self._check_library()
        self._populate_mapping()

    def _check_library(self):
        """
        Makes sure that nvidia-ml-py is being used
        """
        package_name = "nvidia-ml-py"
        try:
            distribution = importlib.metadata.distribution(package_name)
            logger.info(
                "using package: %s version: %s",
                distribution.name,
                distribution.version,
            )
        except importlib.metadata.PackageNotFoundError:
            raise ModuleNotFoundError(
                f"package {package_name} not found. Please install it."
            )

    def _populate_mapping(self):
        """
        Creates mapping and reverse_mapping for the GPU Translator
        """
        try:
            pynvml.nvmlInit()
            self.device_count = pynvml.nvmlDeviceGetCount()
            for index in range(self.device_count):
                handle = pynvml.nvmlDeviceGetHandleByIndex(index)
                uuid_value = pynvml.nvmlDeviceGetUUID(handle)
                uuid = (
                    uuid_value
                    if isinstance(uuid_value, str)
                    else uuid_value.decode("utf-8")
                )
                self.mapping[uuid] = index
            pynvml.nvmlShutdown()

        except pynvml.NVMLError as error:
            logger.error(error)

        # Create reverse mapping
        self.reverse_mapping = {v: k for k, v in self.mapping.items()}

    def get_gpu_uuid_to_index_mapping(self) -> Dict[str, int]:
        """
        Get a mapping from GPU UUID to GPU index.

        Returns:
            Dict[str, int]: Dictionary mapping GPU UUID to index

        """
        return self.mapping

    def uuid_to_index(self, uuid: str) -> int:
        """
        Convert a GPU UUID to its corresponding index.

        Args:
            uuid (str): The GPU UUID to convert

        Returns:
            int: The GPU index corresponding to the UUID

        Raises:
            ValueError: If UUID is not found
        """
        if uuid not in self.mapping:
            available_uuids = list(self.mapping.keys())
            raise ValueError(
                f"GPU UUID '{uuid}' not found. Available UUIDs: {available_uuids}"
            )

        return self.mapping[uuid]

    def index_to_uuid(self, index: int) -> str:
        """
        Convert a GPU index to its corresponding UUID.

        Args:
            index (int): The GPU index to convert

        Returns:
            str: The GPU UUID corresponding to the index

        Raises:
            ValueError: If index is not found
        """
        if index not in self.reverse_mapping:
            available_indices = list(self.reverse_mapping.keys())
            raise ValueError(
                f"GPU index {index} not found. Available indices: {available_indices}"
            )

        return self.reverse_mapping[index]
