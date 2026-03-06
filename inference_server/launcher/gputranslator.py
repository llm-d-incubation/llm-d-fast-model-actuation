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
import json
import logging
import os
from typing import Dict, Optional

import pynvml
from kubernetes import client, config

logger = logging.getLogger(__name__)


# VLLM process manager
class GpuTranslator:
    def __init__(
        self,
        mock_gpus: bool = False,
        mock_gpu_count: int = 8,
        node_name: Optional[str] = None,
    ):
        """
        Initialize GPU Translator

        Args:
            mock_gpus: If True, skip pynvml and use mock mode for testing
            mock_gpu_count: Number of mock GPUs to create (default: 8)
            node_name: Kubernetes node name for ConfigMap-based GPU discovery
        """
        self.mapping = {}
        self.reverse_mapping = {}
        self.device_count = 0
        self.mock_mode = mock_gpus
        self.mock_gpu_count = mock_gpu_count
        self.node_name = node_name or os.getenv("NODE_NAME")
        if not self.mock_mode:
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

    def _load_gpu_map_from_configmap(self) -> Optional[Dict[str, int]]:
        """
        Load GPU mapping from Kubernetes ConfigMap 'gpu-map'.

        Returns:
            Dict[str, int]: GPU UUID to index mapping, or None if ConfigMap not
            available
        """
        if not self.node_name:
            logger.info("No node name provided, skipping ConfigMap GPU discovery")
            return None

        try:
            # Try to load in-cluster config first, fall back to kubeconfig
            try:
                config.load_incluster_config()
            except config.ConfigException:
                config.load_kube_config()

            v1 = client.CoreV1Api()

            # Read the ConfigMap
            namespace = os.getenv("NAMESPACE", "default")
            cm = v1.read_namespaced_config_map(name="gpu-map", namespace=namespace)

            if not cm.data or self.node_name not in cm.data:
                logger.warning(
                    "Node '%s' not found in ConfigMap 'gpu-map' in namespace '%s'",
                    self.node_name,
                    namespace,
                )
                return None

            # Parse the JSON mapping for this node
            node_gpu_data = cm.data[self.node_name]
            gpu_mapping = json.loads(node_gpu_data)

            logger.info(
                "Loaded GPU mapping from ConfigMap for node '%s': %s",
                self.node_name,
                gpu_mapping,
            )
            return gpu_mapping

        except Exception as e:
            logger.warning(
                "Failed to load GPU mapping from ConfigMap: %s. Falling back to "
                "mock mode.",
                e,
            )
            return None

    def _populate_mapping(self):
        """
        Creates mapping and reverse_mapping for the GPU Translator.
        Priority order:
        1. ConfigMap 'gpu-map' based mock if mock mode enabled and node_name available
        2. Naive mock with GPU-0, GPU-1, etc. if mock mode is enabled
        3. Real GPUs via pynvml
        """
        # Try ConfigMap first if in mock mode and node_name is available
        if self.mock_mode and self.node_name:
            configmap_mapping = self._load_gpu_map_from_configmap()
            if configmap_mapping:
                self.mapping = configmap_mapping
                self.reverse_mapping = {v: k for k, v in self.mapping.items()}
                self.device_count = len(self.mapping)
                logger.info(
                    "GPU Translator initialized from ConfigMap with "
                    "%d GPUs for node '%s'",
                    self.device_count,
                    self.node_name,
                )
                return

        # Fall back to hardcoded mock mode
        if self.mock_mode:
            # Pre-populate with mock GPUs following the test pattern: GPU-0, GPU-1, etc.
            for index in range(self.mock_gpu_count):
                uuid = f"GPU-{index}"
                self.mapping[uuid] = index
                self.reverse_mapping[index] = uuid
            self.device_count = self.mock_gpu_count
            logger.info(
                "GPU Translator initialized in mock mode with %d mock GPUs",
                self.mock_gpu_count,
            )
            return

        # Use real GPUs via pynvml
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
            logger.info(
                "GPU Translator initialized with %d real GPUs", self.device_count
            )

        except pynvml.NVMLError as error:
            logger.error("Failed to initialize pynvml: %s", error)

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
