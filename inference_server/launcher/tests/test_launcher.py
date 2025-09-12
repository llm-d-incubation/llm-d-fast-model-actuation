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


import os

import pytest
from launcher import VllmConfig, VllmProcessManager, set_env_vars
from pydantic import ValidationError

mock_env_vars = {"TEST_VAR1": "test_value1", "TEST_VAR2": 123}


def test_set_env_vars():
    """Test setting environment variables"""
    # Capture original environment variables
    original_env = dict(os.environ)

    # Call the function
    set_env_vars(mock_env_vars)

    # Check if new environment variables are set
    assert os.environ["TEST_VAR1"] == "test_value1"
    assert os.environ["TEST_VAR2"] == "123"

    # Clean up by restoring original environment variables
    for key, value in original_env.items():
        del os.environ[key]


def test_vllmconfig_with_options():
    config = VllmConfig(options="test_options")
    assert config.options == "test_options"
    assert config.env_var is None
    env_var_data = {"TEST_ENV_VAR": "test_value"}
    config = VllmConfig(options="test_options", env_var=env_var_data)
    assert config.options == "test_options"
    assert config.env_var == env_var_data


def test_vllmconfig_validations():
    with pytest.raises(ValidationError):
        VllmConfig(env_var={"TEST_ENV_VAR": "test_value"})

    with pytest.raises(ValidationError):
        VllmConfig(options=123)  # options should be a string

    with pytest.raises(ValidationError):
        VllmConfig(
            options="test_options", env_var="not_a_dict"
        )  # env_var should be a dict


test_vllmconfig = VllmConfig(
    options="--model TinyLlama/TinyLlama-1.1B-Chat-v1.0 --port 8005"
)


def test_start_running_stop_status_process():

    # start
    pm = VllmProcessManager()
    assert not pm.is_running()
    result = pm.start_process(test_vllmconfig)
    # start
    assert result["status"] == "started"
    assert result["pid"] is not None
    # running
    assert pm.is_running()
    # status
    status = pm.get_status()
    assert status["status"] == "running"
    assert status["pid"] == pm.process.pid

    # stop
    result = pm.stop_process()
    assert result["status"] == "terminated"
    assert result["pid"] == pm.process.pid
    # running
    assert not pm.is_running()
    # status
    status = pm.get_status()
    assert status["status"] == "stopped"
    assert status["pid"] == pm.process.pid


def test_stop_process_not_running():
    pm = VllmProcessManager()
    result = pm.stop_process()
    assert result["status"] == "no_process_to_stop"


# Run tests with: python -m pytest test_launcher.py -v
