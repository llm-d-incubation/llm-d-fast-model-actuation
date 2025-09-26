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

from typing import Dict

import pynvml


# VLLM process manager
class GpuTranslator:

    def __init__(self):
        """
        Initialize GPU Translator
        """
        self.device_count = 0
        self.mapping = {}
        # Create reverse mapping
        self.reverse_mapping = {}

    def _populate_mapping(self):
        """
        Creates mapping and reverse_mapping for the GPU Translator
        """
        pynvml.nvmlInit()
        self.device_count = pynvml.nvmlDeviceGetCount()
        for index in range(self.device_count):
            handle = pynvml.nvmlDeviceGetHandleByIndex(index)
            uuid = pynvml.nvmlDeviceGetUUID(handle).decode("utf-8")
            self.mapping[uuid] = index

        pynvml.nvmlShutdown()

        # Create reverse mapping
        self.reverse_mapping = {v: k for k, v in self.mapping.items()}

    def get_gpu_uuid_to_index_mapping(self) -> Dict[str, int]:
        """
        Get a mapping from GPU UUID to GPU index.

        Returns:
            Dict[str, int]: Dictionary mapping GPU UUID to index

        """
        if self.mapping is None or self.device_count == 0:
            self._populate_mapping()
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
        if self.mapping is None or self.device_count == 0:
            self._populate_mapping()
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
        if self.reverse_mapping is None or self.device_count == 0:
            self._populate_mapping()
        if index not in self.reverse_mapping:
            available_indices = list(self.reverse_mapping.keys())
            raise ValueError(
                f"GPU index {index} not found. Available indices: {available_indices}"
            )

        return self.reverse_mapping[index]
