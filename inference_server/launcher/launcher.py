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
vLLM Launcher
"""

import logging
import multiprocessing
import os
from http import HTTPStatus  # HTTP Status Codes
from typing import Any, Dict, Optional

import uvloop
from fastapi import FastAPI, HTTPException
from fastapi.responses import JSONResponse
from pydantic import BaseModel
from vllm.entrypoints.openai.api_server import run_server
from vllm.entrypoints.openai.cli_args import make_arg_parser, validate_parsed_serve_args
from vllm.entrypoints.utils import cli_env_setup
from vllm.utils import FlexibleArgumentParser


# Define a the expected JSON structure in dataclass
class VllmConfig(BaseModel):
    options: str
    env_vars: Optional[Dict[str, Any]] = None


# VLLM process manager
class VllmProcessManager:
    def __init__(self):
        self.process: Optional[multiprocessing.Process] = None
        self.config: Optional[VllmConfig] = None

    def start_process(self, vllm_config: VllmConfig) -> dict:
        """
        Start new vLLM instance
        :param vllm_config: parameters for the vLLM process.
        :return: Status of the process and its PID.
        """

        # Start new process
        self.process = multiprocessing.Process(target=vllm_kickoff, args=(vllm_config,))
        self.process.start()
        self.config = vllm_config

        return {
            "status": "started",
            "pid": self.process.pid,
        }

    def stop_process(self, timeout: int = 10) -> dict:
        """
        Stop existing vLLM instance
        :param timeout: waits for the process to stop, defaults to 10
        :return: a dictionary with the status "terminated" and the process ID
        """
        if not self.process or not self.process.is_alive():
            return {"status": "no_process_to_stop"}

        pid = self.process.pid

        # Graceful termination
        self.process.terminate()
        self.process.join(timeout=timeout)

        # Force kill if needed
        if self.process.is_alive():
            self.process.kill()
            self.process.join()

        return {"status": "terminated", "pid": pid}

    def is_running(self) -> bool:
        """
        Returns if the process in the manager is running or not.
        :return: True is running, `False` otherwise.
        """
        return self.process is not None and self.process.is_alive()

    def get_status(self) -> dict:
        """
        Returns the status of the process and its PID or the no process
        :return: Status and PID of the running process.
        """
        if not self.process:
            return {"status": "no_process", "pid": None}

        return {
            "status": "running" if self.process.is_alive() else "stopped",
            "pid": self.process.pid,
        }


# Create global manager instance
vllm_manager = VllmProcessManager()

# Create FastAPI application
app = FastAPI(
    title="REST API Service", version="1.0", description="vLLM Management API"
)

# Setup logging
logger = logging.getLogger(__name__)


############################################################
# Health Endpoint
############################################################
@app.get("/health")
async def health():
    """Health Status"""
    return JSONResponse(content={"status": "OK"}, status_code=HTTPStatus.OK)


######################################################################
# GET INDEX
######################################################################
@app.get("/")
async def index():
    """Root URL response"""
    return JSONResponse(
        content={"name": "REST API Service", "version": "1.0"},
        status_code=HTTPStatus.OK,
    )


######################################################################
# vLLM MANAGEMENT ENDPOINTS
######################################################################
@app.post("/v1/vllm")
async def create_vllm(vllm_config: VllmConfig):
    """Create/swap in a new vLLM instance"""

    if vllm_manager.is_running():
        raise HTTPException(
            status_code=409, detail="A vLLM instance is already running."
        )

    result = vllm_manager.start_process(vllm_config)
    return JSONResponse(
        content=result,
        status_code=HTTPStatus.OK,
    )


@app.delete("/v1/vllm")
async def delete_vllm():
    """Delete/swap out the vLLM instance"""

    logger.info("Swap out vLLM instance")
    if not vllm_manager.is_running():
        raise HTTPException(status_code=404, detail="No running vLLM process found")

    result = vllm_manager.stop_process()
    return JSONResponse(content=result, status_code=HTTPStatus.ACCEPTED)


# Define a function to be executed by the child process
def vllm_kickoff(vllm_config: VllmConfig):
    """
    Child function to kickoff vllm instance
    :param vllm_config: vLLM configuration parameters and env variables
    """

    logger.info(f"VLLM process (PID: {os.getpid()}) started.")
    # Set env vars in the current process
    if vllm_config.env_vars:
        set_env_vars(vllm_config.env_vars)

    # prepare args
    receive_args = vllm_config.options.split()

    cli_env_setup()
    parser = FlexibleArgumentParser(
        description="vLLM OpenAI-Compatible RESTful API server."
    )
    parser = make_arg_parser(parser)
    args = parser.parse_args(receive_args)
    validate_parsed_serve_args(args)

    uvloop.run(run_server(args))


def set_env_vars(env_vars: Dict[str, Any]):
    """
    Set environment variables from a dictionary
    :param env_vars: Dict with environment var name as keys and value as values
    """
    # Set environment variables from a dictionary
    for key, value in env_vars.items():
        os.environ[key] = str(value)


if __name__ == "__main__":
    import uvicorn

    uvicorn.run(app, host="0.0.0.0", port=8001, log_level="info")
