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
Unit tests for Multi-Instance vLLM Launcher
Run as:
python -m pytest tests/test_launcher.py -v
"""
import sys
from unittest.mock import MagicMock, patch

import pytest
from fastapi.testclient import TestClient
from gputranslator import GpuTranslator

# Mock vllm before importing launcher
sys.modules["vllm"] = MagicMock()
sys.modules["vllm.utils"] = MagicMock()
sys.modules["vllm.utils.argparse_utils"] = MagicMock()
sys.modules["vllm.entrypoints.openai.api_server"] = MagicMock()
sys.modules["vllm.entrypoints.openai.cli_args"] = MagicMock()
sys.modules["vllm.entrypoints.utils"] = MagicMock()

# Import the application and classes
from launcher import (  # noqa: E402
    MAX_LOG_RESPONSE_BYTES,
    MAX_QUEUE_BYTES,
    MAX_QUEUE_SIZE,
    LogRangeNotAvailable,
    QueueWriter,
    VllmConfig,
    VllmInstance,
    VllmMultiProcessManager,
    app,
    get_logs_from_queue,
    set_env_vars,
)


# Fixtures
@pytest.fixture
def vllm_config():
    """Create a sample VllmConfig for testing"""
    return VllmConfig(
        options="--model test-model --port 8000", env_vars={"TEST_VAR": "test_value"}
    )


@pytest.fixture
def gpu_translator():
    """Create a GPUTranslator for testing"""
    return GpuTranslator()


@pytest.fixture
def vllm_config_no_env():
    """Create a VllmConfig without env vars"""
    return VllmConfig(options="--model test-model --port 8001")


@pytest.fixture
def manager():
    """Create a fresh VllmMultiProcessManager for each test"""
    return VllmMultiProcessManager()


@pytest.fixture
def client():
    """Create a FastAPI test client"""
    return TestClient(app)


# Mock process for testing without actually starting vLLM
class MockProcess:
    def __init__(self):
        self._is_alive = True
        self.terminated = False
        self.killed = False

    def start(self):
        pass

    def is_alive(self):
        return self._is_alive

    def terminate(self):
        self.terminated = True
        self._is_alive = False

    def join(self, timeout=None):
        pass

    def kill(self):
        self.killed = True
        self._is_alive = False


# Tests for VllmConfig
class TestVllmConfig:
    def test_vllm_config_with_env_vars(self):
        """Test VllmConfig creation with environment variables"""
        config = VllmConfig(
            options="--model test --port 8000",
            gpu_uuids=["uuid1", "uuid2"],
            env_vars={"KEY1": "value1", "KEY2": "value2"},
        )
        assert config.options == "--model test --port 8000"
        assert config.gpu_uuids == ["uuid1", "uuid2"]
        assert config.env_vars == {"KEY1": "value1", "KEY2": "value2"}

    def test_vllm_config_without_env_vars(self):
        """Test VllmConfig creation without environment variables"""
        config = VllmConfig(options="--model test --port 8000")
        assert config.options == "--model test --port 8000"
        assert config.gpu_uuids is None
        assert config.env_vars is None


# Tests for VllmInstance
class TestVllmInstance:
    @patch("launcher.multiprocessing.Process")
    def test_instance_creation(self, vllm_config, gpu_translator):
        """Test creating a VllmInstance"""
        instance = VllmInstance("test-id", vllm_config, gpu_translator)
        assert instance.instance_id == "test-id"
        assert instance.config == vllm_config
        assert instance.process is None

    @patch("launcher.multiprocessing.Process")
    def test_instance_start(self, mock_process_class, vllm_config, gpu_translator):
        """Test starting a vLLM instance"""
        mock_process = MockProcess()
        mock_process_class.return_value = mock_process

        instance = VllmInstance("test-id", vllm_config, gpu_translator)
        result = instance.start()

        assert result["status"] == "started"
        assert result["instance_id"] == "test-id"

    @patch("launcher.multiprocessing.Process")
    def test_instance_start_already_running(
        self, mock_process_class, vllm_config, gpu_translator
    ):
        """Test starting an instance that's already running"""
        mock_process = MockProcess()
        mock_process_class.return_value = mock_process

        instance = VllmInstance("test-id", vllm_config, gpu_translator)
        instance.start()
        result = instance.start()  # Try to start again

        assert result["status"] == "already_running"
        assert result["instance_id"] == "test-id"

    @patch("launcher.multiprocessing.Process")
    def test_instance_stop(self, mock_process_class, vllm_config, gpu_translator):
        """Test stopping a running instance"""
        mock_process = MockProcess()
        mock_process_class.return_value = mock_process

        instance = VllmInstance("test-id", vllm_config, gpu_translator)
        instance.start()
        result = instance.stop()

        assert result["status"] == "terminated"
        assert result["instance_id"] == "test-id"
        assert mock_process.terminated is True

    @patch("launcher.multiprocessing.Process")
    def test_instance_stop_not_running(self, vllm_config, gpu_translator):
        """Test stopping an instance that's not running"""
        instance = VllmInstance("test-id", vllm_config, gpu_translator)
        result = instance.stop()

        assert result["status"] == "not_running"
        assert result["instance_id"] == "test-id"

    @patch("launcher.multiprocessing.Process")
    def test_instance_force_kill(self, mock_process_class, vllm_config, gpu_translator):
        """Test force killing an instance that won't terminate"""
        mock_process = MockProcess()

        # Simulate process that won't die on terminate
        def stay_alive_on_terminate():
            pass  # Don't change _is_alive

        mock_process.terminate = stay_alive_on_terminate
        mock_process_class.return_value = mock_process

        instance = VllmInstance("test-id", vllm_config, gpu_translator)
        instance.start()
        _ = instance.stop(timeout=0.1)

        assert mock_process.killed is True

    @patch("launcher.multiprocessing.Process")
    def test_instance_get_status(self, mock_process_class, vllm_config, gpu_translator):
        """Test getting instance status"""
        mock_process = MockProcess()
        mock_process_class.return_value = mock_process

        instance = VllmInstance("test-id", vllm_config, gpu_translator)

        # Running
        instance.start()
        status = instance.get_status()
        assert status["status"] == "running"

        # Stopped
        mock_process._is_alive = False
        status = instance.get_status()
        assert status["status"] == "stopped"

    @patch("launcher.multiprocessing.Process")
    def test_instance_uuid_to_index_translation(
        self, mock_process_class, gpu_translator
    ):
        """Test that GPU UUIDs are correctly translated to
        indices and CUDA_VISIBLE_DEVICES is set"""
        mock_process = MockProcess()
        mock_process_class.return_value = mock_process

        # Mock the uuid_to_index method to return predictable indices
        test_uuids = ["GPU-uuid-1234", "GPU-uuid-5678", "GPU-uuid-9abc"]
        expected_indices = [0, 2, 3]

        # Create a mock that returns indices based on the UUID
        uuid_to_index_map = dict(zip(test_uuids, expected_indices))
        gpu_translator.uuid_to_index = MagicMock(
            side_effect=lambda uuid: uuid_to_index_map[uuid]
        )

        # Create config with GPU UUIDs
        config = VllmConfig(
            options="--model test-model --port 8000", gpu_uuids=test_uuids
        )

        # Create instance (this triggers UUID translation in __init__)
        instance = VllmInstance("test-id", config, gpu_translator)

        # Verify uuid_to_index was called for each UUID
        assert gpu_translator.uuid_to_index.call_count == len(test_uuids)
        for uuid_str in test_uuids:
            gpu_translator.uuid_to_index.assert_any_call(uuid_str)

        # Verify CUDA_VISIBLE_DEVICES was set correctly
        assert "CUDA_VISIBLE_DEVICES" in instance.config.env_vars
        assert instance.config.env_vars["CUDA_VISIBLE_DEVICES"] == "0,2,3"

        # Verify the instance can be started with the translated indices
        result = instance.start()
        assert result["status"] == "started"

    @patch("launcher.multiprocessing.Process")
    def test_instance_uuid_translation_creates_env_vars_if_none(
        self, mock_process_class, gpu_translator
    ):
        """Test that env_vars dict is created when
        gpu_uuids provided but env_vars is None"""
        mock_process = MockProcess()
        mock_process_class.return_value = mock_process

        # Mock uuid_to_index
        gpu_translator.uuid_to_index = MagicMock(side_effect=[1, 3])

        # Create config WITHOUT env_vars but WITH gpu_uuids
        config = VllmConfig(
            options="--model test-model --port 8000",
            gpu_uuids=["GPU-uuid-aaa", "GPU-uuid-bbb"],
        )

        # Verify env_vars is None initially
        assert config.env_vars is None

        # Create instance
        instance = VllmInstance("test-id", config, gpu_translator)

        # Verify env_vars was created and CUDA_VISIBLE_DEVICES was set
        assert instance.config.env_vars is not None
        assert instance.config.env_vars["CUDA_VISIBLE_DEVICES"] == "1,3"

    @patch("launcher.multiprocessing.Process")
    def test_instance_uuid_translation_preserves_existing_env_vars(
        self, mock_process_class, gpu_translator
    ):
        """Test that existing env_vars are preserved when adding CUDA_VISIBLE_DEVICES"""
        mock_process = MockProcess()
        mock_process_class.return_value = mock_process

        # Mock uuid_to_index
        gpu_translator.uuid_to_index = MagicMock(return_value=0)

        # Create config with existing env_vars
        existing_env_vars = {"CUSTOM_VAR": "custom_value", "ANOTHER_VAR": "123"}
        config = VllmConfig(
            options="--model test-model --port 8000",
            gpu_uuids=["GPU-uuid-xyz"],
            env_vars=existing_env_vars.copy(),
        )

        # Create instance
        instance = VllmInstance("test-id", config, gpu_translator)

        # Verify existing env_vars are preserved
        assert instance.config.env_vars["CUSTOM_VAR"] == "custom_value"
        assert instance.config.env_vars["ANOTHER_VAR"] == "123"
        # And CUDA_VISIBLE_DEVICES was added
        assert instance.config.env_vars["CUDA_VISIBLE_DEVICES"] == "0"

    @patch("launcher.multiprocessing.Process")
    def test_instance_no_uuid_translation_when_gpu_uuids_none(
        self, mock_process_class, gpu_translator
    ):
        """Test that uuid_to_index is not called when gpu_uuids is None"""
        mock_process = MockProcess()
        mock_process_class.return_value = mock_process

        # Mock uuid_to_index to track calls
        gpu_translator.uuid_to_index = MagicMock()

        # Create config WITHOUT gpu_uuids
        config = VllmConfig(
            options="--model test-model --port 8000", env_vars={"SOME_VAR": "value"}
        )

        # Create instance
        instance = VllmInstance("test-id", config, gpu_translator)

        # Verify uuid_to_index was NOT called
        gpu_translator.uuid_to_index.assert_not_called()

        # Verify CUDA_VISIBLE_DEVICES was NOT added
        assert "CUDA_VISIBLE_DEVICES" not in instance.config.env_vars


# Tests for VllmMultiProcessManager
class TestVllmMultiProcessManager:
    @patch("launcher.multiprocessing.Process")
    def test_create_instance_auto_id(self, mock_process_class, manager, vllm_config):
        """Test creating instance with auto-generated ID"""
        mock_process = MockProcess()
        mock_process_class.return_value = mock_process

        result = manager.create_instance(vllm_config)

        assert result["status"] == "started"
        assert "instance_id" in result
        assert len(manager.instances) == 1

    @patch("launcher.multiprocessing.Process")
    def test_create_instance_custom_id(self, mock_process_class, manager, vllm_config):
        """Test creating instance with custom ID"""
        mock_process = MockProcess()
        mock_process_class.return_value = mock_process

        result = manager.create_instance(vllm_config, "custom-id")

        assert result["status"] == "started"
        assert result["instance_id"] == "custom-id"
        assert "custom-id" in manager.instances

    @patch("launcher.multiprocessing.Process")
    def test_create_instance_duplicate_id(
        self, mock_process_class, manager, vllm_config
    ):
        """Test creating instance with duplicate ID raises error"""
        mock_process = MockProcess()
        mock_process_class.return_value = mock_process

        manager.create_instance(vllm_config, "duplicate-id")

        with pytest.raises(ValueError, match="already exists"):
            manager.create_instance(vllm_config, "duplicate-id")

    @patch("launcher.multiprocessing.Process")
    def test_stop_instance(self, mock_process_class, manager, vllm_config):
        """Test stopping a specific instance"""
        mock_process = MockProcess()
        mock_process_class.return_value = mock_process

        result = manager.create_instance(vllm_config, "test-id")
        instance_id = result["instance_id"]

        stop_result = manager.stop_instance(instance_id)

        assert stop_result["status"] == "terminated"
        assert instance_id not in manager.instances  # Should be cleaned up

    @patch("launcher.multiprocessing.Process")
    def test_stop_nonexistent_instance(self, mock_process_class, manager):
        """Test stopping instance that doesn't exist"""
        with pytest.raises(KeyError, match="not found"):
            manager.stop_instance("nonexistent-id")

    @patch("launcher.multiprocessing.Process")
    def test_stop_all_instances(self, mock_process_class, manager, vllm_config):
        """Test stopping all instances"""
        mock_process = MockProcess()
        mock_process_class.return_value = mock_process

        # Create multiple instances
        manager.create_instance(vllm_config, "id-1")
        manager.create_instance(vllm_config, "id-2")
        manager.create_instance(vllm_config, "id-3")

        result = manager.stop_all_instances()

        assert result["status"] == "all_stopped"
        assert result["total_stopped"] == 3
        assert len(manager.instances) == 0

    @patch("launcher.multiprocessing.Process")
    def test_get_instance_status(self, mock_process_class, manager, vllm_config):
        """Test getting status of specific instance"""
        mock_process = MockProcess()
        mock_process_class.return_value = mock_process

        manager.create_instance(vllm_config, "test-id")
        status = manager.get_instance_status("test-id")

        assert status["status"] == "running"
        assert status["instance_id"] == "test-id"

    @patch("launcher.multiprocessing.Process")
    def test_get_instance_status_nonexistent(self, mock_process_class, manager):
        """Test getting status of nonexistent instance"""
        with pytest.raises(KeyError, match="not found"):
            manager.get_instance_status("nonexistent-id")

    @patch("launcher.multiprocessing.Process")
    def test_get_all_instances_status(self, mock_process_class, manager, vllm_config):
        """Test getting status of all instances"""
        mock_process = MockProcess()
        mock_process_class.return_value = mock_process

        manager.create_instance(vllm_config, "id-1")
        manager.create_instance(vllm_config, "id-2")

        status = manager.get_all_instances_status()

        assert status["total_instances"] == 2
        assert status["running_instances"] == 2
        assert len(status["instances"]) == 2

    @patch("launcher.multiprocessing.Process")
    def test_list_instances(self, mock_process_class, manager, vllm_config):
        """Test listing all instance IDs"""
        mock_process = MockProcess()
        mock_process_class.return_value = mock_process

        manager.create_instance(vllm_config, "id-1")
        manager.create_instance(vllm_config, "id-2")

        instances = manager.list_instances()

        assert len(instances) == 2
        assert "id-1" in instances
        assert "id-2" in instances

    @patch("launcher.multiprocessing.Process")
    @patch("launcher.get_logs_from_queue")
    @patch("launcher.multiprocessing.Queue")
    def test_get_instance_logs(
        self, mock_queue_class, mock_get_logs, mock_process_class, manager, vllm_config
    ):
        """Test getting logs from a specific instance"""
        mock_process = MockProcess()
        mock_process_class.return_value = mock_process

        mock_get_logs.return_value = "Log line 1Log line 2Log line 3"

        mock_queue = MagicMock()
        mock_queue_class.return_value = mock_queue

        manager.create_instance(vllm_config, "test-id")
        log_content = manager.get_instance_logs("test-id", start_byte=0, max_bytes=1000)

        assert isinstance(log_content, str)
        assert "Log line 1" in log_content
        assert "Log line 2" in log_content
        assert "Log line 3" in log_content

    @patch("launcher.multiprocessing.Process")
    def test_get_instance_logs_nonexistent(self, mock_process_class, manager):
        """Test getting logs from nonexistent instance raises KeyError"""
        with pytest.raises(KeyError, match="not found"):
            manager.get_instance_logs("nonexistent-id")

    @patch("launcher.multiprocessing.Process")
    @patch("launcher.get_logs_from_queue")
    @patch("launcher.multiprocessing.Queue")
    def test_get_instance_logs_respects_max_bytes(
        self, mock_queue_class, mock_get_logs, mock_process_class, manager, vllm_config
    ):
        """Test that get_logs respects max_bytes parameter"""
        mock_process = MockProcess()
        mock_process_class.return_value = mock_process

        mock_get_logs.return_value = "Log 1Log 2"

        mock_queue = MagicMock()
        mock_queue_class.return_value = mock_queue

        manager.create_instance(vllm_config, "test-id")
        log_content = manager.get_instance_logs("test-id", start_byte=0, max_bytes=100)

        # Should call get_logs_from_queue with start_byte and max_bytes
        assert isinstance(log_content, str)
        mock_get_logs.assert_called_once_with(mock_queue, 0, 100)


# Tests for API Endpoints
class TestAPIEndpoints:
    def test_health_endpoint(self, client):
        """Test health check endpoint"""
        response = client.get("/health")
        assert response.status_code == 200
        assert response.json() == {"status": "OK"}

    def test_index_endpoint(self, client):
        """Test index endpoint"""
        response = client.get("/")
        assert response.status_code == 200
        data = response.json()
        assert data["name"] == "Multi-Instance vLLM Management API"
        assert data["version"] == "2.0"
        assert "endpoints" in data
        assert len(data["endpoints"]) == 9

    @patch("launcher.vllm_manager")
    def test_create_vllm_instance(self, mock_manager, client, vllm_config):
        """Test creating vLLM instance via API"""
        mock_manager.create_instance.return_value = {
            "status": "started",
            "instance_id": "test-id",
        }

        response = client.post(
            "/v2/vllm/instances", json={"options": "--model test --port 8000"}
        )

        assert response.status_code == 201
        data = response.json()
        assert data["status"] == "started"
        assert "instance_id" in data

    @patch("launcher.vllm_manager")
    def test_create_id_vllm_instance(self, mock_manager, client):
        """Test creating vLLM instance with custom ID via API"""
        mock_manager.create_instance.return_value = {
            "status": "started",
            "instance_id": "custom-id",
        }

        response = client.put(
            "/v2/vllm/instances/custom-id", json={"options": "--model test --port 8000"}
        )

        assert response.status_code == 201
        data = response.json()
        assert data["instance_id"] == "custom-id"

    @patch("launcher.vllm_manager")
    def test_create_duplicate_instance(self, mock_manager, client):
        """Test creating instance with duplicate ID returns 409"""
        mock_manager.create_instance.side_effect = ValueError("already exists")

        response = client.put(
            "/v2/vllm/instances/duplicate-id",
            json={"options": "--model test --port 8000"},
        )

        assert response.status_code == 409

    @patch("launcher.vllm_manager")
    def test_delete_vllm_instance(self, mock_manager, client):
        """Test deleting vLLM instance via API"""
        mock_manager.stop_instance.return_value = {
            "status": "terminated",
            "instance_id": "test-id",
        }

        response = client.delete("/v2/vllm/instances/test-id")

        assert response.status_code == 200
        data = response.json()
        assert data["status"] == "terminated"

    @patch("launcher.vllm_manager")
    def test_delete_nonexistent_instance(self, mock_manager, client):
        """Test deleting nonexistent instance returns 404"""
        mock_manager.stop_instance.side_effect = KeyError("not found")

        response = client.delete("/v2/vllm/instances/nonexistent-id")

        assert response.status_code == 404

    @patch("launcher.vllm_manager")
    def test_delete_all_instances(self, mock_manager, client):
        """Test deleting all instances via API"""
        mock_manager.stop_all_instances.return_value = {
            "status": "all_stopped",
            "stopped_instances": [],
        }

        response = client.delete("/v2/vllm/instances")

        assert response.status_code == 200
        data = response.json()
        assert data["status"] == "all_stopped"

    @patch("launcher.vllm_manager")
    def test_list_instances(self, mock_manager, client):
        """Test listing instances via API"""
        mock_manager.list_instances.return_value = ["id-1", "id-2"]

        response = client.get("/v2/vllm/instances?detail=False")

        assert response.status_code == 200
        data = response.json()
        assert data["count"] == 2
        assert "id-1" in data["instance_ids"]

        mock_manager.get_all_instances_status.return_value = {
            "total_instances": 1,
            "running_instances": 1,
            "instances": {
                "status": "running",
                "instance_id": "test-id",
            },
        }

        response = client.get("/v2/vllm/instances?detail=True")

        assert response.status_code == 200
        data = response.json()
        assert data["total_instances"] == 1
        assert data["running_instances"] == 1

    @patch("launcher.vllm_manager")
    def test_get_instance_status(self, mock_manager, client):
        """Test getting specific instance status via API"""
        mock_manager.get_instance_status.return_value = {
            "status": "running",
            "instance_id": "test-id",
        }

        response = client.get("/v2/vllm/instances/test-id")

        assert response.status_code == 200
        data = response.json()
        assert data["status"] == "running"

    @patch("launcher.vllm_manager")
    def test_get_nonexistent_instance_status(self, mock_manager, client):
        """Test getting status of nonexistent instance returns 404"""
        mock_manager.get_instance_status.side_effect = KeyError("not found")

        response = client.get("/v2/vllm/instances/nonexistent-id")

        assert response.status_code == 404

    @patch("launcher.vllm_manager")
    def test_get_instance_logs_endpoint(self, mock_manager, client):
        """Test getting instance logs via API"""
        mock_manager.get_instance_logs.return_value = "Log line 1Log line 2Log line 3"

        response = client.get("/v2/vllm/instances/test-id/log")

        assert response.status_code == 200
        data = response.json()
        assert "log" in data
        assert isinstance(data["log"], str)
        assert "Log line 1" in data["log"]

    @patch("launcher.vllm_manager")
    def test_get_instance_logs_with_max_bytes(self, mock_manager, client):
        """Test getting instance logs with max_bytes parameter"""
        mock_manager.get_instance_logs.return_value = "Log 1Log 2"

        response = client.get("/v2/vllm/instances/test-id/log?max_bytes=5000")

        assert response.status_code == 200
        mock_manager.get_instance_logs.assert_called_once_with("test-id", 0, 5000)

    @patch("launcher.vllm_manager")
    def test_get_instance_logs_nonexistent_endpoint(self, mock_manager, client):
        """Test getting logs from nonexistent instance returns 404"""
        mock_manager.get_instance_logs.side_effect = KeyError("not found")

        response = client.get("/v2/vllm/instances/nonexistent-id/log")

        assert response.status_code == 404

    @patch("launcher.vllm_manager")
    def test_get_instance_logs_range_not_available(self, mock_manager, client):
        """Test getting logs with start_byte beyond available content returns 416"""
        mock_manager.get_instance_logs.side_effect = LogRangeNotAvailable(5000, 1000)

        response = client.get("/v2/vllm/instances/test-id/log?start_byte=5000")

        assert response.status_code == 416


# Tests for QueueWriter
class TestQueueWriter:
    def test_queue_writer_write_non_empty(self):
        """Test QueueWriter writes non-empty messages to queue"""
        from unittest.mock import MagicMock

        mock_queue = MagicMock()
        mock_queue.put_nowait.side_effect = None  # Simulate successful put
        writer = QueueWriter(mock_queue)

        writer.write("Test message")

        # Verify put_nowait was called with the message
        mock_queue.put_nowait.assert_called_once_with("Test message")

    def test_queue_writer_ignores_empty_messages(self):
        """Test QueueWriter ignores empty/whitespace messages"""
        from unittest.mock import MagicMock

        mock_queue = MagicMock()
        writer = QueueWriter(mock_queue)

        writer.write("")
        writer.write("   ")
        writer.write("\n")

        # put should not have been called for empty messages
        mock_queue.put.assert_not_called()

    def test_queue_writer_flush(self):
        """Test QueueWriter flush method (should do nothing)"""
        from unittest.mock import MagicMock

        mock_queue = MagicMock()
        writer = QueueWriter(mock_queue)

        # Should not raise any exception
        writer.flush()


# Tests for VllmInstance log functionality
class TestVllmInstanceLogs:
    @patch("launcher.multiprocessing.Process")
    @patch("launcher.get_logs_from_queue")
    @patch("launcher.multiprocessing.Queue")
    def test_get_logs_empty_queue(
        self,
        mock_queue_class,
        mock_get_logs,
        mock_process_class,
        vllm_config,
        gpu_translator,
    ):
        """Test get_logs returns empty string when queue is empty"""
        mock_process = MockProcess()
        mock_process_class.return_value = mock_process

        mock_queue = MagicMock()
        mock_queue_class.return_value = mock_queue
        mock_get_logs.return_value = ""

        instance = VllmInstance("test-id", vllm_config, gpu_translator)
        instance.start()

        log_content = instance.get_logs()

        assert log_content == ""

    @patch("launcher.multiprocessing.Process")
    @patch("launcher.get_logs_from_queue")
    @patch("launcher.multiprocessing.Queue")
    def test_get_logs_with_messages(
        self,
        mock_queue_class,
        mock_get_logs,
        mock_process_class,
        vllm_config,
        gpu_translator,
    ):
        """Test get_logs retrieves messages from queue"""
        mock_process = MockProcess()
        mock_process_class.return_value = mock_process

        mock_queue = MagicMock()
        mock_queue_class.return_value = mock_queue
        mock_get_logs.return_value = "Log 1Log 2"

        instance = VllmInstance("test-id", vllm_config, gpu_translator)
        instance.start()

        log_content = instance.get_logs()

        assert isinstance(log_content, str)
        assert "Log 1" in log_content
        assert "Log 2" in log_content

    @patch("launcher.multiprocessing.Process")
    @patch("launcher.get_logs_from_queue")
    @patch("launcher.multiprocessing.Queue")
    def test_get_logs_respects_max_bytes(
        self,
        mock_queue_class,
        mock_get_logs,
        mock_process_class,
        vllm_config,
        gpu_translator,
    ):
        """Test get_logs respects max_bytes parameter"""
        mock_process = MockProcess()
        mock_process_class.return_value = mock_process

        mock_queue = MagicMock()
        mock_queue_class.return_value = mock_queue
        mock_get_logs.return_value = "Log 1Log 2Log 3"

        instance = VllmInstance("test-id", vllm_config, gpu_translator)
        instance.start()

        log_content = instance.get_logs(start_byte=0, max_bytes=100)

        assert isinstance(log_content, str)
        mock_get_logs.assert_called_once_with(mock_queue, 0, 100)

    @patch("launcher.multiprocessing.Process")
    def test_get_logs_no_queue(self, mock_process_class, vllm_config, gpu_translator):
        """Test get_logs returns empty string when queue doesn't exist"""
        instance = VllmInstance("test-id", vllm_config, gpu_translator)
        # Don't start the instance, so output_queue is None

        log_content = instance.get_logs()

        assert log_content == ""


# Tests for Helper Functions
class TestHelperFunctions:
    def test_set_env_vars(self):
        """Test setting environment variables"""
        import os

        test_vars = {"TEST_VAR_1": "value1", "TEST_VAR_2": 12345, "TEST_VAR_3": True}

        set_env_vars(test_vars)

        assert os.environ["TEST_VAR_1"] == "value1"
        assert os.environ["TEST_VAR_2"] == "12345"
        assert os.environ["TEST_VAR_3"] == "True"

        # Cleanup
        for key in test_vars.keys():
            del os.environ[key]

    def test_max_queue_size_constant(self):
        """Test that MAX_QUEUE_SIZE constant is defined"""
        assert MAX_QUEUE_SIZE == 5000

    def test_max_queue_bytes_constant(self):
        """Test that MAX_QUEUE_BYTES constant is defined"""
        assert MAX_QUEUE_BYTES == 10 * 1024 * 1024  # 10 MB

    def test_max_log_response_bytes_constant(self):
        """Test that MAX_LOG_RESPONSE_BYTES constant is defined"""
        assert MAX_LOG_RESPONSE_BYTES == 1 * 1024 * 1024  # 1 MB


# Tests for get_logs_from_queue function
class TestGetLogsFromQueue:
    """Test suite for get_logs_from_queue function"""

    def test_basic_retrieval(self):
        """Test basic log retrieval"""
        from unittest.mock import MagicMock

        mock_queue = MagicMock()
        messages = ["Test message 1", "Test message 2"]
        mock_queue.empty.side_effect = [False, False, True]
        mock_queue.get_nowait.side_effect = messages
        mock_queue.put_nowait.return_value = None

        log_content = get_logs_from_queue(mock_queue, start_byte=0, max_bytes=1000)
        assert isinstance(log_content, str)
        assert "Test message 1" in log_content
        assert "Test message 2" in log_content

    def test_empty_queue(self):
        """Test getting logs from empty queue"""
        from unittest.mock import MagicMock

        mock_queue = MagicMock()
        mock_queue.empty.return_value = True

        log_content = get_logs_from_queue(mock_queue, start_byte=0, max_bytes=1000)
        assert log_content == ""

    def test_byte_limit_enforcement(self):
        """Test that byte limit is enforced"""
        from unittest.mock import MagicMock

        mock_queue = MagicMock()
        messages = [f"Message {i}" for i in range(10)]
        mock_queue.empty.side_effect = [False] * 10 + [True]
        mock_queue.get_nowait.side_effect = messages
        mock_queue.put_nowait.return_value = None

        # Request only 50 bytes
        log_content = get_logs_from_queue(mock_queue, start_byte=0, max_bytes=50)
        assert isinstance(log_content, str)
        assert len(log_content.encode("utf-8")) <= 50

    def test_max_bytes_larger_than_queue(self):
        """Test when max_bytes is larger than queue content"""
        from unittest.mock import MagicMock

        mock_queue = MagicMock()
        messages = ["Short", "Message"]
        mock_queue.empty.side_effect = [False, False, True]
        mock_queue.get_nowait.side_effect = messages
        mock_queue.put_nowait.return_value = None

        log_content = get_logs_from_queue(mock_queue, start_byte=0, max_bytes=10000)
        assert isinstance(log_content, str)
        assert "Short" in log_content
        assert "Message" in log_content

    def test_messages_returned_to_queue(self):
        """Test that messages are returned to queue after retrieval"""
        from unittest.mock import MagicMock

        mock_queue = MagicMock()
        messages = ["Message 1", "Message 2"]

        # First call
        mock_queue.empty.side_effect = [False, False, True]
        mock_queue.get_nowait.side_effect = messages.copy()
        mock_queue.put_nowait.return_value = None

        log_content = get_logs_from_queue(mock_queue, start_byte=0, max_bytes=1000)
        assert isinstance(log_content, str)

        # Verify put_nowait was called to return messages
        assert mock_queue.put_nowait.call_count == 2

    def test_unicode_handling(self):
        """Test handling of unicode characters"""
        from unittest.mock import MagicMock

        mock_queue = MagicMock()
        messages = ["Hello 世界", "Test émojis 🚀"]
        mock_queue.empty.side_effect = [False, False, True]
        mock_queue.get_nowait.side_effect = messages
        mock_queue.put_nowait.return_value = None

        log_content = get_logs_from_queue(mock_queue, start_byte=0, max_bytes=1000)
        assert isinstance(log_content, str)
        assert "世界" in log_content
        assert "🚀" in log_content

    def test_partial_retrieval_with_byte_limit(self):
        """Test that only messages within byte limit are returned"""
        from unittest.mock import MagicMock

        mock_queue = MagicMock()
        messages = ["A" * 20, "B" * 20, "C" * 20]
        mock_queue.empty.side_effect = [False, False, False, True]
        mock_queue.get_nowait.side_effect = messages
        mock_queue.put_nowait.return_value = None

        # Request only 45 bytes - should get exactly 45 bytes
        log_content = get_logs_from_queue(mock_queue, start_byte=0, max_bytes=45)
        assert isinstance(log_content, str)
        assert log_content == "A" * 20 + "B" * 20 + "C" * 5

        # Verify all messages were put back
        assert mock_queue.put_nowait.call_count == 3

    def test_start_byte_offset(self):
        """Test that start_byte returns bytes from exact position"""
        from unittest.mock import MagicMock

        mock_queue = MagicMock()
        messages = ["A" * 10, "B" * 10, "C" * 10]  # Each 10 bytes
        # Full stream: AAAAAAAAAABBBBBBBBBBCCCCCCCCCC (bytes 0-29)
        mock_queue.empty.side_effect = [False, False, False, True]
        mock_queue.get_nowait.side_effect = messages
        mock_queue.put_nowait.return_value = None

        # Start from byte 15 (middle of B)
        log_content = get_logs_from_queue(mock_queue, start_byte=15, max_bytes=100)

        # Should get bytes 15-29: last 5 B's + all 10 C's
        assert log_content == "B" * 5 + "C" * 10

    def test_start_byte_at_message_boundary(self):
        """Test that start_byte at exact message boundary works correctly"""
        from unittest.mock import MagicMock

        mock_queue = MagicMock()
        messages = ["A" * 10, "B" * 10, "C" * 10]  # Each 10 bytes
        # Full stream: AAAAAAAAAABBBBBBBBBBCCCCCCCCCC (bytes 0-29)
        mock_queue.empty.side_effect = [False, False, False, True]
        mock_queue.get_nowait.side_effect = messages
        mock_queue.put_nowait.return_value = None

        # Start from byte 10 (exactly where B starts)
        log_content = get_logs_from_queue(mock_queue, start_byte=10, max_bytes=100)

        # Should get bytes 10-29: all of B and C
        assert log_content == "B" * 10 + "C" * 10

    def test_start_byte_beyond_available_raises_error(self):
        """Test that start_byte beyond available content raises LogRangeNotAvailable"""
        from unittest.mock import MagicMock

        mock_queue = MagicMock()
        messages = ["A" * 10, "B" * 10]  # 20 bytes total
        mock_queue.empty.side_effect = [False, False, True]
        mock_queue.get_nowait.side_effect = messages
        mock_queue.put_nowait.return_value = None

        with pytest.raises(LogRangeNotAvailable) as exc_info:
            get_logs_from_queue(mock_queue, start_byte=50, max_bytes=100)

        assert exc_info.value.start_byte == 50
        assert exc_info.value.available_bytes == 20


if __name__ == "__main__":
    pytest.main([__file__, "-v"])
