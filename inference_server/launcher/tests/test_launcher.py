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
import os
import signal
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
    FileWriter,
    LogRangeNotAvailable,
    VllmConfig,
    VllmInstance,
    VllmMultiProcessManager,
    app,
    parse_range_header,
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
def manager(tmp_path):
    """Create a fresh VllmMultiProcessManager for each test"""
    return VllmMultiProcessManager(log_dir=str(tmp_path))


@pytest.fixture
def client():
    """Create a FastAPI test client"""
    return TestClient(app)


@pytest.fixture
def tmp_log_dir(tmp_path):
    """Provide a temporary directory for log files"""
    return str(tmp_path)


# Mock process for testing without actually starting vLLM
class MockProcess:
    def __init__(self):
        self._is_alive = True
        self.terminated = False
        self.killed = False
        self.pid = 12345

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
    def test_instance_creation(self, vllm_config, gpu_translator, tmp_log_dir):
        """Test creating a VllmInstance"""
        instance = VllmInstance(
            "test-id", vllm_config, gpu_translator, log_dir=tmp_log_dir
        )
        assert instance.instance_id == "test-id"
        assert instance.config == vllm_config
        assert instance.process is None

    @patch("launcher.multiprocessing.Process")
    def test_instance_start(
        self, mock_process_class, vllm_config, gpu_translator, tmp_log_dir
    ):
        """Test starting a vLLM instance"""
        mock_process = MockProcess()
        mock_process_class.return_value = mock_process

        instance = VllmInstance(
            "test-id", vllm_config, gpu_translator, log_dir=tmp_log_dir
        )
        result = instance.start()

        assert result["status"] == "started"
        assert result["instance_id"] == "test-id"
        assert os.path.exists(instance._log_file_path)

    @patch("launcher.multiprocessing.Process")
    def test_instance_start_already_running(
        self, mock_process_class, vllm_config, gpu_translator, tmp_log_dir
    ):
        """Test starting an instance that's already running"""
        mock_process = MockProcess()
        mock_process_class.return_value = mock_process

        instance = VllmInstance(
            "test-id", vllm_config, gpu_translator, log_dir=tmp_log_dir
        )
        instance.start()
        result = instance.start()  # Try to start again

        assert result["status"] == "already_running"
        assert result["instance_id"] == "test-id"

    @patch("launcher.multiprocessing.Process")
    def test_instance_stop(
        self, mock_process_class, vllm_config, gpu_translator, tmp_log_dir
    ):
        """Test stopping a running instance"""
        mock_process = MockProcess()
        mock_process_class.return_value = mock_process

        instance = VllmInstance(
            "test-id", vllm_config, gpu_translator, log_dir=tmp_log_dir
        )
        instance.start()
        result = instance.stop()

        assert result["status"] == "terminated"
        assert result["instance_id"] == "test-id"
        assert mock_process.terminated is True

    @patch("launcher.multiprocessing.Process")
    def test_instance_stop_not_running(self, vllm_config, gpu_translator, tmp_log_dir):
        """Test stopping an instance that's not running"""
        instance = VllmInstance(
            "test-id", vllm_config, gpu_translator, log_dir=tmp_log_dir
        )
        result = instance.stop()

        assert result["status"] == "not_running"
        assert result["instance_id"] == "test-id"

    @patch("launcher.os.killpg")
    @patch("launcher.multiprocessing.Process")
    def test_instance_force_kill(
        self, mock_process_class, mock_killpg, vllm_config, gpu_translator, tmp_log_dir
    ):
        """Test force killing an instance that won't terminate"""
        mock_process = MockProcess()

        # Simulate process that won't die on terminate
        def stay_alive_on_terminate():
            pass  # Don't change _is_alive

        mock_process.terminate = stay_alive_on_terminate

        # Make join after killpg finally stop the process
        call_count = 0

        def join_side_effect(timeout=None):
            nonlocal call_count
            call_count += 1
            if call_count > 1:
                mock_process._is_alive = False

        mock_process.join = join_side_effect
        mock_process_class.return_value = mock_process

        instance = VllmInstance(
            "test-id", vllm_config, gpu_translator, log_dir=tmp_log_dir
        )
        instance.start()
        _ = instance.stop(timeout=0.1)

        mock_killpg.assert_called_once_with(mock_process.pid, signal.SIGKILL)

    @patch("launcher.multiprocessing.Process")
    def test_instance_get_status(
        self, mock_process_class, vllm_config, gpu_translator, tmp_log_dir
    ):
        """Test getting instance status"""
        mock_process = MockProcess()
        mock_process_class.return_value = mock_process

        instance = VllmInstance(
            "test-id", vllm_config, gpu_translator, log_dir=tmp_log_dir
        )

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
        self, mock_process_class, gpu_translator, tmp_log_dir
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
        instance = VllmInstance("test-id", config, gpu_translator, log_dir=tmp_log_dir)

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
        self, mock_process_class, gpu_translator, tmp_log_dir
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
        instance = VllmInstance("test-id", config, gpu_translator, log_dir=tmp_log_dir)

        # Verify env_vars was created and CUDA_VISIBLE_DEVICES was set
        assert instance.config.env_vars is not None
        assert instance.config.env_vars["CUDA_VISIBLE_DEVICES"] == "1,3"

    @patch("launcher.multiprocessing.Process")
    def test_instance_uuid_translation_preserves_existing_env_vars(
        self, mock_process_class, gpu_translator, tmp_log_dir
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
        instance = VllmInstance("test-id", config, gpu_translator, log_dir=tmp_log_dir)

        # Verify existing env_vars are preserved
        assert instance.config.env_vars["CUSTOM_VAR"] == "custom_value"
        assert instance.config.env_vars["ANOTHER_VAR"] == "123"
        # And CUDA_VISIBLE_DEVICES was added
        assert instance.config.env_vars["CUDA_VISIBLE_DEVICES"] == "0"

    @patch("launcher.multiprocessing.Process")
    def test_instance_no_uuid_translation_when_gpu_uuids_none(
        self, mock_process_class, gpu_translator, tmp_log_dir
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
        instance = VllmInstance("test-id", config, gpu_translator, log_dir=tmp_log_dir)

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
    def test_get_instance_log_bytes(self, mock_process_class, manager, vllm_config):
        """Test getting log bytes from a specific instance"""
        mock_process = MockProcess()
        mock_process_class.return_value = mock_process

        manager.create_instance(vllm_config, "test-id")

        # Write log data directly to the instance's log file
        instance = manager.instances["test-id"]
        with open(instance._log_file_path, "wb") as f:
            f.write(b"Log line 1Log line 2Log line 3")

        data, total = manager.get_instance_log_bytes("test-id", start=0)

        assert isinstance(data, bytes)
        assert b"Log line 1" in data
        assert b"Log line 2" in data
        assert b"Log line 3" in data
        assert total == 30

    @patch("launcher.multiprocessing.Process")
    def test_get_instance_log_bytes_nonexistent(self, mock_process_class, manager):
        """Test getting log bytes from nonexistent instance raises KeyError"""
        with pytest.raises(KeyError, match="not found"):
            manager.get_instance_log_bytes("nonexistent-id")

    @patch("launcher.multiprocessing.Process")
    def test_get_instance_log_bytes_with_end(
        self, mock_process_class, manager, vllm_config
    ):
        """Test that get_instance_log_bytes respects end parameter"""
        mock_process = MockProcess()
        mock_process_class.return_value = mock_process

        manager.create_instance(vllm_config, "test-id")

        # Write log data directly to the instance's log file
        instance = manager.instances["test-id"]
        with open(instance._log_file_path, "wb") as f:
            f.write(b"A" * 60 + b"B" * 60)

        data, total = manager.get_instance_log_bytes("test-id", start=0, end=99)

        assert isinstance(data, bytes)
        assert len(data) == 100
        assert total == 120


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
        """Test getting instance logs without Range header returns 200"""
        mock_manager.get_instance_log_bytes.return_value = (
            b"Log line 1Log line 2Log line 3",
            29,
        )

        response = client.get("/v2/vllm/instances/test-id/log")

        assert response.status_code == 200
        assert response.headers["content-type"] == "application/octet-stream"
        assert response.content == b"Log line 1Log line 2Log line 3"
        mock_manager.get_instance_log_bytes.assert_called_once_with("test-id", 0, None)

    @patch("launcher.vllm_manager")
    def test_get_instance_logs_with_range_header(self, mock_manager, client):
        """Test getting instance logs with Range header"""
        mock_manager.get_instance_log_bytes.return_value = (b"A" * 5000, 10000)

        response = client.get(
            "/v2/vllm/instances/test-id/log",
            headers={"Range": "bytes=0-4999"},
        )

        assert response.status_code == 206
        mock_manager.get_instance_log_bytes.assert_called_once_with("test-id", 0, 4999)

    @patch("launcher.vllm_manager")
    def test_get_instance_logs_nonexistent_endpoint(self, mock_manager, client):
        """Test getting logs from nonexistent instance returns 404"""
        mock_manager.get_instance_log_bytes.side_effect = KeyError("not found")

        response = client.get("/v2/vllm/instances/nonexistent-id/log")

        assert response.status_code == 404

    @patch("launcher.vllm_manager")
    def test_get_instance_logs_range_not_available(self, mock_manager, client):
        """Test getting logs with start beyond available content returns 416"""
        mock_manager.get_instance_log_bytes.side_effect = LogRangeNotAvailable(
            5000, 1000
        )

        response = client.get(
            "/v2/vllm/instances/test-id/log",
            headers={"Range": "bytes=5000-"},
        )

        assert response.status_code == 416
        assert response.headers["content-range"] == "bytes */1000"

    @patch("launcher.vllm_manager")
    def test_get_instance_logs_partial_content_206(self, mock_manager, client):
        """Test 206 Partial Content with correct Content-Range header"""
        mock_manager.get_instance_log_bytes.return_value = (b"ABCDE", 100)

        response = client.get(
            "/v2/vllm/instances/test-id/log",
            headers={"Range": "bytes=10-14"},
        )

        assert response.status_code == 206
        assert response.content == b"ABCDE"
        assert response.headers["content-range"] == "bytes 10-14/100"

    @patch("launcher.vllm_manager")
    def test_get_instance_logs_open_ended_range(self, mock_manager, client):
        """Test open-ended Range: bytes=100-"""
        mock_manager.get_instance_log_bytes.return_value = (b"rest of log", 200)

        response = client.get(
            "/v2/vllm/instances/test-id/log",
            headers={"Range": "bytes=100-"},
        )

        assert response.status_code == 206
        mock_manager.get_instance_log_bytes.assert_called_once_with(
            "test-id", 100, None
        )
        assert response.headers["content-range"] == "bytes 100-110/200"

    @patch("launcher.vllm_manager")
    def test_get_instance_logs_invalid_range(self, mock_manager, client):
        """Test malformed Range header returns 400"""
        response = client.get(
            "/v2/vllm/instances/test-id/log",
            headers={"Range": "invalid"},
        )

        assert response.status_code == 400

    @patch("launcher.vllm_manager")
    def test_get_instance_logs_suffix_range_rejected(self, mock_manager, client):
        """Test suffix range bytes=-500 returns 400"""
        response = client.get(
            "/v2/vllm/instances/test-id/log",
            headers={"Range": "bytes=-500"},
        )

        assert response.status_code == 400


# Tests for FileWriter
class TestFileWriter:
    def test_file_writer_write_non_empty(self, tmp_log_dir):
        """Test FileWriter writes non-empty messages to file"""
        log_path = os.path.join(tmp_log_dir, "test.log")
        writer = FileWriter(log_path, sys.stdout)

        writer.write("Test message")

        with open(log_path, "rb") as f:
            content = f.read()
        assert content == b"Test message"

    def test_file_writer_ignores_empty_messages(self, tmp_log_dir):
        """Test FileWriter ignores empty/whitespace messages"""
        log_path = os.path.join(tmp_log_dir, "test.log")
        writer = FileWriter(log_path, sys.stdout)

        writer.write("")
        writer.write("   ")
        writer.write("\n")

        with open(log_path, "rb") as f:
            content = f.read()
        assert content == b""

    def test_file_writer_flush(self, tmp_log_dir):
        """Test FileWriter flush does not raise"""
        log_path = os.path.join(tmp_log_dir, "test.log")
        writer = FileWriter(log_path, sys.stdout)

        # flush should not raise
        writer.flush()

    def test_file_writer_multiple_writes(self, tmp_log_dir):
        """Test FileWriter accumulates multiple writes"""
        log_path = os.path.join(tmp_log_dir, "test.log")
        writer = FileWriter(log_path, sys.stdout)

        writer.write("Hello ")
        writer.write("World")

        with open(log_path, "rb") as f:
            content = f.read()
        assert content == b"Hello World"

    def test_file_writer_returns_msg_length(self, tmp_log_dir):
        """Test FileWriter.write returns the length of the message"""
        log_path = os.path.join(tmp_log_dir, "test.log")
        writer = FileWriter(log_path, sys.stdout)

        result = writer.write("Test")
        assert result == 4

        result = writer.write("")
        assert result == 0


# Tests for VllmInstance log functionality
class TestVllmInstanceLogs:
    def _make_instance(self, gpu_translator, log_dir):
        """Helper to create a VllmInstance without starting a real process"""
        config = VllmConfig(options="--model test --port 8000")
        instance = VllmInstance("test-id", config, gpu_translator, log_dir=log_dir)
        return instance

    def test_get_log_bytes_no_file(self, gpu_translator, tmp_log_dir):
        """Test get_log_bytes returns empty bytes when log file doesn't exist"""
        instance = self._make_instance(gpu_translator, tmp_log_dir)

        data, total = instance.get_log_bytes()

        assert data == b""
        assert total == 0

    def test_get_log_bytes_empty_file(self, gpu_translator, tmp_log_dir):
        """Test get_log_bytes returns empty bytes when log file is empty"""
        instance = self._make_instance(gpu_translator, tmp_log_dir)
        open(instance._log_file_path, "wb").close()

        data, total = instance.get_log_bytes()

        assert data == b""
        assert total == 0

    def test_get_log_bytes_with_content(self, gpu_translator, tmp_log_dir):
        """Test get_log_bytes retrieves content from log file"""
        instance = self._make_instance(gpu_translator, tmp_log_dir)
        with open(instance._log_file_path, "wb") as f:
            f.write(b"Log 1Log 2")

        data, total = instance.get_log_bytes()

        assert isinstance(data, bytes)
        assert b"Log 1" in data
        assert b"Log 2" in data
        assert total == 10

    def test_get_log_bytes_with_end(self, gpu_translator, tmp_log_dir):
        """Test get_log_bytes respects end parameter"""
        instance = self._make_instance(gpu_translator, tmp_log_dir)
        with open(instance._log_file_path, "wb") as f:
            f.write(b"A" * 50 + b"B" * 50 + b"C" * 50)

        data, total = instance.get_log_bytes(start=0, end=99)

        assert isinstance(data, bytes)
        assert len(data) == 100
        assert total == 150

    def test_get_log_bytes_persists_across_calls(self, gpu_translator, tmp_log_dir):
        """Test that log file accumulates across multiple reads"""
        instance = self._make_instance(gpu_translator, tmp_log_dir)

        with open(instance._log_file_path, "wb") as f:
            f.write(b"Hello World")
        data, total = instance.get_log_bytes(start=0)
        assert data == b"Hello World"
        assert total == 11

        with open(instance._log_file_path, "ab") as f:
            f.write(b"!")
        data, total = instance.get_log_bytes(start=0)
        assert data == b"Hello World!"
        assert total == 12


# Tests for Helper Functions
class TestHelperFunctions:
    def test_set_env_vars(self):
        """Test setting environment variables"""
        test_vars = {"TEST_VAR_1": "value1", "TEST_VAR_2": 12345, "TEST_VAR_3": True}

        set_env_vars(test_vars)

        assert os.environ["TEST_VAR_1"] == "value1"
        assert os.environ["TEST_VAR_2"] == "12345"
        assert os.environ["TEST_VAR_3"] == "True"

        # Cleanup
        for key in test_vars.keys():
            del os.environ[key]

    def test_max_log_response_bytes_constant(self):
        """Test that MAX_LOG_RESPONSE_BYTES constant is defined"""
        assert MAX_LOG_RESPONSE_BYTES == 1 * 1024 * 1024  # 1 MB


# Tests for VllmInstance file-based log behavior
class TestLogFile:
    """Test suite for VllmInstance file-based log retrieval"""

    def _make_instance(self, gpu_translator, log_dir):
        """Helper to create a VllmInstance without starting a real process"""
        config = VllmConfig(options="--model test --port 8000")
        instance = VllmInstance("test-id", config, gpu_translator, log_dir=log_dir)
        return instance

    def test_basic_retrieval(self, gpu_translator, tmp_log_dir):
        """Test basic log retrieval"""
        instance = self._make_instance(gpu_translator, tmp_log_dir)
        with open(instance._log_file_path, "wb") as f:
            f.write(b"Test message 1Test message 2")

        data, total = instance.get_log_bytes(start=0)
        assert isinstance(data, bytes)
        assert b"Test message 1" in data
        assert b"Test message 2" in data
        assert total == 28

    def test_empty_file(self, gpu_translator, tmp_log_dir):
        """Test getting logs from empty file"""
        instance = self._make_instance(gpu_translator, tmp_log_dir)
        open(instance._log_file_path, "wb").close()

        data, total = instance.get_log_bytes(start=0)
        assert data == b""
        assert total == 0

    def test_end_limit_enforcement(self, gpu_translator, tmp_log_dir):
        """Test that end parameter limits returned bytes"""
        instance = self._make_instance(gpu_translator, tmp_log_dir)
        content = "".join(f"Message {i}" for i in range(10))
        with open(instance._log_file_path, "wb") as f:
            f.write(content.encode("utf-8"))

        data, total = instance.get_log_bytes(start=0, end=49)
        assert isinstance(data, bytes)
        assert len(data) == 50

    def test_end_larger_than_content(self, gpu_translator, tmp_log_dir):
        """Test when end is beyond available content (truncates to EOF)"""
        instance = self._make_instance(gpu_translator, tmp_log_dir)
        with open(instance._log_file_path, "wb") as f:
            f.write(b"ShortMessage")

        data, total = instance.get_log_bytes(start=0, end=9999)
        assert isinstance(data, bytes)
        assert b"Short" in data
        assert b"Message" in data
        assert total == 12

    def test_unicode_bytes(self, gpu_translator, tmp_log_dir):
        """Test that raw bytes with unicode are returned as-is"""
        instance = self._make_instance(gpu_translator, tmp_log_dir)
        raw = "Hello 世界Test émojis 🚀".encode("utf-8")
        with open(instance._log_file_path, "wb") as f:
            f.write(raw)

        data, total = instance.get_log_bytes(start=0)
        assert isinstance(data, bytes)
        assert data == raw
        assert total == len(raw)

    def test_partial_retrieval_with_end(self, gpu_translator, tmp_log_dir):
        """Test that only bytes up to end are returned"""
        instance = self._make_instance(gpu_translator, tmp_log_dir)
        with open(instance._log_file_path, "wb") as f:
            f.write(b"A" * 20 + b"B" * 20 + b"C" * 20)

        data, total = instance.get_log_bytes(start=0, end=44)
        assert data == b"A" * 20 + b"B" * 20 + b"C" * 5
        assert total == 60

    def test_start_offset(self, gpu_translator, tmp_log_dir):
        """Test that start returns bytes from exact position"""
        instance = self._make_instance(gpu_translator, tmp_log_dir)
        with open(instance._log_file_path, "wb") as f:
            f.write(b"A" * 10 + b"B" * 10 + b"C" * 10)

        data, total = instance.get_log_bytes(start=15)
        assert data == b"B" * 5 + b"C" * 10
        assert total == 30

    def test_start_at_message_boundary(self, gpu_translator, tmp_log_dir):
        """Test that start at exact boundary works correctly"""
        instance = self._make_instance(gpu_translator, tmp_log_dir)
        with open(instance._log_file_path, "wb") as f:
            f.write(b"A" * 10 + b"B" * 10 + b"C" * 10)

        data, total = instance.get_log_bytes(start=10)
        assert data == b"B" * 10 + b"C" * 10
        assert total == 30

    def test_start_beyond_available_raises_error(self, gpu_translator, tmp_log_dir):
        """Test that start beyond available content raises LogRangeNotAvailable"""
        instance = self._make_instance(gpu_translator, tmp_log_dir)
        with open(instance._log_file_path, "wb") as f:
            f.write(b"A" * 10 + b"B" * 10)

        with pytest.raises(LogRangeNotAvailable) as exc_info:
            instance.get_log_bytes(start=50)

        assert exc_info.value.start_byte == 50
        assert exc_info.value.available_bytes == 20

    def test_start_equal_to_length_raises_error(self, gpu_translator, tmp_log_dir):
        """Test that start equal to file size raises LogRangeNotAvailable"""
        instance = self._make_instance(gpu_translator, tmp_log_dir)
        with open(instance._log_file_path, "wb") as f:
            f.write(b"A" * 10)

        with pytest.raises(LogRangeNotAvailable) as exc_info:
            instance.get_log_bytes(start=10)

        assert exc_info.value.start_byte == 10
        assert exc_info.value.available_bytes == 10

    def test_no_file_with_start_raises_error(self, gpu_translator, tmp_log_dir):
        """Test that requesting logs with start > 0 when no file exists raises"""
        instance = self._make_instance(gpu_translator, tmp_log_dir)

        with pytest.raises(LogRangeNotAvailable) as exc_info:
            instance.get_log_bytes(start=10)

        assert exc_info.value.start_byte == 10
        assert exc_info.value.available_bytes == 0


# Tests for log file cleanup
class TestLogFileCleanup:
    """Test suite for log file cleanup on stop"""

    def _make_instance(self, gpu_translator, log_dir):
        config = VllmConfig(options="--model test --port 8000")
        return VllmInstance("test-id", config, gpu_translator, log_dir=log_dir)

    @patch("launcher.multiprocessing.Process")
    def test_stop_terminated_cleans_up_log_file(
        self, mock_process_class, gpu_translator, tmp_log_dir
    ):
        """Test that stop() removes the log file after terminating"""
        mock_process = MockProcess()
        mock_process_class.return_value = mock_process

        instance = self._make_instance(gpu_translator, tmp_log_dir)
        instance.start()
        assert os.path.exists(instance._log_file_path)

        instance.stop()
        assert not os.path.exists(instance._log_file_path)

    @patch("launcher.multiprocessing.Process")
    def test_stop_not_running_cleans_up_log_file(
        self, mock_process_class, gpu_translator, tmp_log_dir
    ):
        """Test that stop() removes the log file when process is not running"""
        instance = self._make_instance(gpu_translator, tmp_log_dir)
        # Create a log file manually
        open(instance._log_file_path, "wb").close()
        assert os.path.exists(instance._log_file_path)

        instance.stop()
        assert not os.path.exists(instance._log_file_path)

    def test_cleanup_missing_file_no_error(self, gpu_translator, tmp_log_dir):
        """Test that _cleanup_log_file does not raise if file doesn't exist"""
        instance = self._make_instance(gpu_translator, tmp_log_dir)
        # Should not raise
        instance._cleanup_log_file()


class TestParseRangeHeader:
    """Tests for the parse_range_header helper function"""

    def test_full_range(self):
        """Test parsing bytes=0-99"""
        assert parse_range_header("bytes=0-99") == (0, 99)

    def test_open_ended_range(self):
        """Test parsing bytes=100-"""
        assert parse_range_header("bytes=100-") == (100, None)

    def test_large_values(self):
        """Test parsing large byte values"""
        assert parse_range_header("bytes=1048576-2097151") == (1048576, 2097151)

    def test_suffix_range_rejected(self):
        """Test that suffix range bytes=-500 raises ValueError"""
        with pytest.raises(ValueError, match="Unsupported or malformed"):
            parse_range_header("bytes=-500")

    def test_invalid_unit(self):
        """Test that non-bytes unit raises ValueError"""
        with pytest.raises(ValueError, match="Unsupported or malformed"):
            parse_range_header("items=0-99")

    def test_garbage_input(self):
        """Test that garbage input raises ValueError"""
        with pytest.raises(ValueError, match="Unsupported or malformed"):
            parse_range_header("not-a-range")

    def test_end_less_than_start(self):
        """Test that end < start raises ValueError"""
        with pytest.raises(ValueError, match="must be >= start"):
            parse_range_header("bytes=100-50")


if __name__ == "__main__":
    pytest.main([__file__, "-v"])
