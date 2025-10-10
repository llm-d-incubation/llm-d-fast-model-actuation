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
"""

from unittest.mock import patch

import pytest
from fastapi.testclient import TestClient

# Import the application and classes
from launcher import (
    VllmConfig,
    VllmInstance,
    VllmMultiProcessManager,
    app,
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
        self.pid = 12345
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
            env_vars={"KEY1": "value1", "KEY2": "value2"},
        )
        assert config.options == "--model test --port 8000"
        assert config.env_vars == {"KEY1": "value1", "KEY2": "value2"}

    def test_vllm_config_without_env_vars(self):
        """Test VllmConfig creation without environment variables"""
        config = VllmConfig(options="--model test --port 8000")
        assert config.options == "--model test --port 8000"
        assert config.env_vars is None


# Tests for VllmInstance
class TestVllmInstance:
    @patch("launcher.multiprocessing.Process")
    def test_instance_creation(self, vllm_config):
        """Test creating a VllmInstance"""
        instance = VllmInstance("test-id", vllm_config)
        assert instance.instance_id == "test-id"
        assert instance.config == vllm_config
        assert instance.process is None

    @patch("launcher.multiprocessing.Process")
    def test_instance_start(self, mock_process_class, vllm_config):
        """Test starting a vLLM instance"""
        mock_process = MockProcess()
        mock_process_class.return_value = mock_process

        instance = VllmInstance("test-id", vllm_config)
        result = instance.start()

        assert result["status"] == "started"
        assert result["instance_id"] == "test-id"
        assert result["pid"] == 12345

    @patch("launcher.multiprocessing.Process")
    def test_instance_start_already_running(self, mock_process_class, vllm_config):
        """Test starting an instance that's already running"""
        mock_process = MockProcess()
        mock_process_class.return_value = mock_process

        instance = VllmInstance("test-id", vllm_config)
        instance.start()
        result = instance.start()  # Try to start again

        assert result["status"] == "already_running"
        assert result["instance_id"] == "test-id"

    @patch("launcher.multiprocessing.Process")
    def test_instance_stop(self, mock_process_class, vllm_config):
        """Test stopping a running instance"""
        mock_process = MockProcess()
        mock_process_class.return_value = mock_process

        instance = VllmInstance("test-id", vllm_config)
        instance.start()
        result = instance.stop()

        assert result["status"] == "terminated"
        assert result["instance_id"] == "test-id"
        assert result["pid"] == 12345
        assert mock_process.terminated is True

    @patch("launcher.multiprocessing.Process")
    def test_instance_stop_not_running(self, vllm_config):
        """Test stopping an instance that's not running"""
        instance = VllmInstance("test-id", vllm_config)
        result = instance.stop()

        assert result["status"] == "not_running"
        assert result["instance_id"] == "test-id"

    @patch("launcher.multiprocessing.Process")
    def test_instance_force_kill(self, mock_process_class, vllm_config):
        """Test force killing an instance that won't terminate"""
        mock_process = MockProcess()

        # Simulate process that won't die on terminate
        def stay_alive_on_terminate():
            pass  # Don't change _is_alive

        mock_process.terminate = stay_alive_on_terminate
        mock_process_class.return_value = mock_process

        instance = VllmInstance("test-id", vllm_config)
        instance.start()
        _ = instance.stop(timeout=0.1)

        assert mock_process.killed is True

    @patch("launcher.multiprocessing.Process")
    def test_instance_is_running(self, mock_process_class, vllm_config):
        """Test checking if instance is running"""
        mock_process = MockProcess()
        mock_process_class.return_value = mock_process

        instance = VllmInstance("test-id", vllm_config)
        assert instance.is_running() is False

        instance.start()
        assert instance.is_running() is True

        mock_process._is_alive = False
        assert instance.is_running() is False

    @patch("launcher.multiprocessing.Process")
    def test_instance_get_status(self, mock_process_class, vllm_config):
        """Test getting instance status"""
        mock_process = MockProcess()
        mock_process_class.return_value = mock_process

        instance = VllmInstance("test-id", vllm_config)

        # Not started
        status = instance.get_status()
        assert status["status"] == "not_started"
        assert status["pid"] is None

        # Running
        instance.start()
        status = instance.get_status()
        assert status["status"] == "running"
        assert status["pid"] == 12345

        # Stopped
        mock_process._is_alive = False
        status = instance.get_status()
        assert status["status"] == "stopped"
        assert status["pid"] == 12345


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
        assert result["pid"] == 12345
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

    @patch("launcher.vllm_manager")
    def test_create_vllm_instance(self, mock_manager, client, vllm_config):
        """Test creating vLLM instance via API"""
        mock_manager.create_instance.return_value = {
            "status": "started",
            "instance_id": "test-id",
            "pid": 12345,
        }

        response = client.put("/v2/vllm", json={"options": "--model test --port 8000"})

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
            "pid": 12345,
        }

        response = client.put(
            "/v2/vllm/custom-id", json={"options": "--model test --port 8000"}
        )

        assert response.status_code == 201
        data = response.json()
        assert data["instance_id"] == "custom-id"

    @patch("launcher.vllm_manager")
    def test_create_duplicate_instance(self, mock_manager, client):
        """Test creating instance with duplicate ID returns 409"""
        mock_manager.create_instance.side_effect = ValueError("already exists")

        response = client.put(
            "/v2/vllm/duplicate-id", json={"options": "--model test --port 8000"}
        )

        assert response.status_code == 409

    @patch("launcher.vllm_manager")
    def test_delete_vllm_instance(self, mock_manager, client):
        """Test deleting vLLM instance via API"""
        mock_manager.stop_instance.return_value = {
            "status": "terminated",
            "instance_id": "test-id",
            "pid": 12345,
        }

        response = client.delete("/v2/vllm/test-id")

        assert response.status_code == 200
        data = response.json()
        assert data["status"] == "terminated"

    @patch("launcher.vllm_manager")
    def test_delete_nonexistent_instance(self, mock_manager, client):
        """Test deleting nonexistent instance returns 404"""
        mock_manager.stop_instance.side_effect = KeyError("not found")

        response = client.delete("/v2/vllm/nonexistent-id")

        assert response.status_code == 404

    @patch("launcher.vllm_manager")
    def test_delete_all_instances(self, mock_manager, client):
        """Test deleting all instances via API"""
        mock_manager.stop_all_instances.return_value = {
            "status": "all_stopped",
            "stopped_instances": [],
            "total_stopped": 2,
        }

        response = client.delete("/v2/vllm")

        assert response.status_code == 200
        data = response.json()
        assert data["status"] == "all_stopped"

    @patch("launcher.vllm_manager")
    def test_list_instances(self, mock_manager, client):
        """Test listing instances via API"""
        mock_manager.list_instances.return_value = ["id-1", "id-2"]

        response = client.get("/v2/vllm/instances")

        assert response.status_code == 200
        data = response.json()
        assert data["count"] == 2
        assert "id-1" in data["instance_ids"]

    @patch("launcher.vllm_manager")
    def test_get_all_instances_status(self, mock_manager, client):
        """Test getting all instances status via API"""
        mock_manager.get_all_instances_status.return_value = {
            "total_instances": 2,
            "running_instances": 1,
            "instances": [],
        }

        response = client.get("/v2/vllm")

        assert response.status_code == 200
        data = response.json()
        assert data["total_instances"] == 2

    @patch("launcher.vllm_manager")
    def test_get_instance_status(self, mock_manager, client):
        """Test getting specific instance status via API"""
        mock_manager.get_instance_status.return_value = {
            "status": "running",
            "instance_id": "test-id",
            "pid": 12345,
        }

        response = client.get("/v2/vllm/test-id")

        assert response.status_code == 200
        data = response.json()
        assert data["status"] == "running"

    @patch("launcher.vllm_manager")
    def test_get_nonexistent_instance_status(self, mock_manager, client):
        """Test getting status of nonexistent instance returns 404"""
        mock_manager.get_instance_status.side_effect = KeyError("not found")

        response = client.get("/v2/vllm/nonexistent-id")

        assert response.status_code == 404


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


if __name__ == "__main__":
    pytest.main([__file__, "-v"])

# Run as:
# python -m pytest tests/test_launcher.py -v
