import pytest
from pydantic import ValidationError
from service.launcher import VllmConfig


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


# Run tests with: pytest test_launcher.py -v
