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


import pytest
from launcher import VllmConfig
from pydantic import ValidationError


def test_vllmconfig_init_with_options():
    config = VllmConfig(options="test_options")
    assert config.options == "test_options"
    assert config.env_var is None


def test_vllmconfig_init_with_options_and_env_var():
    env_var_data = {"TEST_ENV_VAR": "test_value"}
    config = VllmConfig(options="test_options", env_var=env_var_data)
    assert config.options == "test_options"
    assert config.env_var == env_var_data


def test_vllmconfig_init_without_options():
    with pytest.raises(ValidationError):
        VllmConfig(env_var={"TEST_ENV_VAR": "test_value"})


def test_vllmconfig_env_var_type_check():
    with pytest.raises(ValidationError):
        VllmConfig(options=123)  # options should be a string


def test_vllmconfig_env_var_dict_type_check():
    with pytest.raises(ValidationError):
        VllmConfig(
            options="test_options", env_var="not_a_dict"
        )  # env_var should be a dict


# Run tests with: python -m pytest test_launcher.py -v
