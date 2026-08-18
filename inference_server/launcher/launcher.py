#!/usr/bin/env python3
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

import asyncio
import glob
import logging
import multiprocessing
import os
import re
import select
import shutil
import signal
import stat
import subprocess
import sys
import time
import uuid
from contextlib import asynccontextmanager
from http import HTTPStatus  # HTTP Status Codes
from typing import Dict, List, Optional

import uvloop
from fastapi import FastAPI, Header, HTTPException, Path, Query
from fastapi.responses import JSONResponse, Response, StreamingResponse
from gputranslator import GpuTranslator
from pydantic import BaseModel
from vllm.entrypoints.openai.api_server import run_server
from vllm.entrypoints.openai.cli_args import make_arg_parser, validate_parsed_serve_args
from vllm.entrypoints.serve.utils.api_utils import cli_env_setup
from vllm.utils.argparse_utils import FlexibleArgumentParser

MAX_LOG_RESPONSE_BYTES = 1 * 1024 * 1024  # 1 MB default for API response
_MAX_BROADCASTER_EVENTS = 1000
LOG_FORMAT = "%(asctime)s - %(name)s - %(levelname)s - %(message)s"

logging.basicConfig(level=logging.INFO, format=LOG_FORMAT, force=True)


class LogRangeNotAvailable(Exception):
    """Raised when the requested start_byte is beyond available log content"""

    def __init__(self, start_byte, available_bytes):
        self.start_byte = start_byte
        self.available_bytes = available_bytes
        super().__init__(
            f"start_byte {start_byte} is beyond available content "
            f"({available_bytes} bytes available)"
        )


# Define a the expected JSON structure in dataclass
class VllmConfig(BaseModel):
    options: str
    gpu_uuids: Optional[List[str]] = None
    env_vars: Optional[Dict[str, str]] = None
    annotations: Optional[Dict[str, str]] = None


class WatchEvent(BaseModel):
    """Represents an instance lifecycle event for the watch stream."""

    type: str  # "CREATED", "STOPPED", "DELETED"
    object: dict


class RevisionTooOld(Exception):
    """Raised when a requested watch revision has fallen out of the buffer."""

    def __init__(self, requested: int, oldest: int):
        super().__init__()
        self.requested = requested
        self.oldest = oldest


class EventBroadcaster:
    """Fans out WatchEvents to all connected watchers using asyncio.Condition.

    Each watcher maintains its own cursor (revision) so late joiners can
    catch up with events still in the buffer.
    """

    def __init__(self):
        self._condition = asyncio.Condition()
        self._events: List[WatchEvent] = []
        # Tracks the revision of the most recently published event
        # (0 = no events published yet).  Revision numbers are assigned
        # by the VllmMultiProcessManager; the broadcaster only records them.
        self._revision: int = 0

    @property
    def revision(self) -> int:
        return self._revision

    @property
    def oldest_revision(self) -> int:
        """Exclusive lower bound: events in the buffer have revision
        strictly greater than this value."""
        return self._revision - len(self._events)

    def _append(self, event: WatchEvent):
        """Synchronously buffer event and advance the revision cursor."""
        self._revision = event.object["revision"]
        self._events.append(event)
        if len(self._events) > _MAX_BROADCASTER_EVENTS:
            self._events = self._events[-_MAX_BROADCASTER_EVENTS:]

    async def _notify(self):
        """Acquire condition lock and wake all watchers."""
        async with self._condition:
            self._condition.notify_all()

    async def watch(self, since_revision: int = 0):
        """Async generator that yields WatchEvents as they arrive.

        :param since_revision: Resume from this revision.  Events with
            revision > since_revision are yielded.
        :raises RevisionTooOld: If/When since_revision is older than the
            oldest event still in the buffer.
        """
        oldest = self.oldest_revision
        if since_revision < oldest:
            raise RevisionTooOld(since_revision, oldest)
        pos = since_revision
        while True:
            async with self._condition:
                while pos >= self._revision:
                    await self._condition.wait()
                offset = pos - self.oldest_revision
                if offset < 0:
                    raise RevisionTooOld(pos, self.oldest_revision)
                new_events = self._events[offset:]
                pos = self._revision
            for event in new_events:
                yield event


class HalfMade(Exception):
    """Raised when something other than start is the first op on a VllmInstance"""

    def __init__(self, instance_id):
        super().__init__()
        self.instance_id = instance_id

class AttachedProcess:
    """Process-like handle for a CRIU-restored launcher child."""

    def __init__(self, pid: int, name: str):
        self.pid = pid
        self.name = name
        self._exitcode: Optional[int] = None
        self._pidfd = os.pidfd_open(pid)
        self._closed = False

    @property
    def sentinel(self) -> int:
        if self._closed:
            raise ValueError("Process handle is closed")
        return self._pidfd

    @property
    def exitcode(self) -> Optional[int]:
        self._poll()
        return self._exitcode

    def _poll(self) -> None:
        if self._exitcode is not None:
            return

        try:
            waited_pid, status = os.waitpid(
                self.pid,
                os.WNOHANG,
            )
        except ChildProcessError:
            # The child may already have been reaped elsewhere.
            try:
                os.kill(self.pid, 0)
            except ProcessLookupError:
                self._exitcode = 0
            return

        if waited_pid == self.pid:
            self._exitcode = os.waitstatus_to_exitcode(status)

    def is_alive(self) -> bool:
        self._poll()
        return self._exitcode is None

    def join(self, timeout: Optional[float] = None) -> None:
        self._poll()

        if self._exitcode is not None:
            return

        if timeout is None:
            try:
                waited_pid, status = os.waitpid(self.pid, 0)
            except ChildProcessError:
                self._poll()
                return

            if waited_pid == self.pid:
                self._exitcode = os.waitstatus_to_exitcode(status)

            return

        poller = select.poll()
        poller.register(self._pidfd, select.POLLIN)
        poller.poll(max(0, int(timeout * 1000)))

        self._poll()

    def terminate(self) -> None:
        try:
            os.kill(self.pid, signal.SIGTERM)
        except ProcessLookupError:
            pass

    def kill(self) -> None:
        try:
            os.kill(self.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass

    def close(self) -> None:
        if self._closed:
            return

        if self.is_alive():
            raise ValueError("Cannot close a running process handle")

        os.close(self._pidfd)
        self._closed = True

def _pid_is_running(pid: Optional[int]) -> bool:
    if pid is None:
        return False

    try:
        with open(
            f"/proc/{pid}/stat",
            "r",
            encoding="utf-8",
        ) as file:
            fields = file.read().split()
    except FileNotFoundError:
        return False
    except OSError:
        return True

    # Field 3 is the process state. Z means zombie: already exited.
    return len(fields) >= 3 and fields[2] != "Z"

def _signal_pid(
    pid: Optional[int],
    sig: signal.Signals,
) -> None:
    if pid is None:
        return

    try:
        os.kill(pid, sig)
    except ProcessLookupError:
        pass

def _reclaim_launcher_terminal() -> None:
        """Make the launcher process group foreground on its terminal again."""

        try:
            terminal_fd = sys.stdin.fileno()
        except (AttributeError, OSError, ValueError):
            return

        if not os.isatty(terminal_fd):
            return

        launcher_pgid = os.getpgrp()

        try:
            if os.tcgetpgrp(terminal_fd) == launcher_pgid:
                return
        except OSError:
            return

        # tcsetpgrp() from a background process group can generate SIGTTOU.
        previous_sigttou = signal.getsignal(signal.SIGTTOU)
        signal.signal(signal.SIGTTOU, signal.SIG_IGN)

        try:
            os.tcsetpgrp(terminal_fd, launcher_pgid)
        finally:
            signal.signal(signal.SIGTTOU, previous_sigttou)

        logger.info(
            "Reclaimed terminal foreground process group for launcher: pgid=%s",
            launcher_pgid,
        )

class VllmInstance:
    """Represents a single vLLM instance"""

    def __init__(
        self,
        instance_id: str,
        config: VllmConfig,
        gpu_translator: GpuTranslator,
        log_dir: str = "",
    ):
        """
        Initialize VllmInstance object
        :param instance_id: Instance id (autogenerated or custom)
        :param config: VllmConfig object
        :param gpu_translator: GpuTranslator object
        :param log_dir: Directory for log files (defaults to /tmp)
        """

        # Check for CUDA device UUIDs and set CUDA_VISIBLE_DEVICES accordingly
        if config.gpu_uuids:
            cuda_indices = []
            for uuid_str in config.gpu_uuids:
                index = gpu_translator.uuid_to_index(uuid_str)
                cuda_indices.append(str(index))
            logger.info(
                f"Translated GPU UUIDs {config.gpu_uuids} to indices {cuda_indices}."
            )

            if config.env_vars is None:
                config.env_vars = {}
            config.env_vars["CUDA_VISIBLE_DEVICES"] = ",".join(cuda_indices)
            logger.info(
                "Set CUDA_VISIBLE_DEVICES to %s based on UUIDs.",
                config.env_vars["CUDA_VISIBLE_DEVICES"],
            )

        # Initialize instance variables
        self.instance_id = instance_id
        self.config = config
        self.process: Optional[multiprocessing.Process | AttachedProcess] = None
        self.last_revision: Optional[int] = None
        self._sentinel_active = False
        self.engine_pid: Optional[int] = None
        self.cuda_checkpointed = False
        self.criu_checkpointed = False
        self._log_file_path = os.path.join(
            log_dir or "/tmp",
            f"launcher-{os.getpid()}-vllm-{instance_id}.log",
        )

    def _make_state(self, status: str) -> dict:
        return {
            "status": status,
            "instance_id": self.instance_id,
            "revision": self.last_revision,
            "cuda_checkpointed": self.cuda_checkpointed,
            "criu_checkpointed": self.criu_checkpointed,
            **self.config.model_dump(exclude_none=True),
        }

    def start(self) -> dict:
        """
        Start this vLLM instance
        :return: Status of the process.
        """
        if self.process and self.process.is_alive():
            return self._make_state("already_running")

        # Create empty log file before spawning the child process
        open(self._log_file_path, "wb").close()

        self.process = multiprocessing.Process(
            target=vllm_kickoff, args=(self.config, self._log_file_path)
        )
        self.process.start()

        return self._make_state("started")

    def stop(self, timeout: int = 10) -> dict:
        """Stop and reap the vLLM root and EngineCore processes."""

        _reclaim_launcher_terminal()

        process = self.process

        if process is None:
            if self.criu_checkpointed:
                self.criu_checkpointed = False
                self.cuda_checkpointed = False
                self.engine_pid = None
                self._cleanup_log_file()
                return self._make_state("terminated")

            raise HalfMade(self.instance_id)

        root_pid = process.pid
        engine_pid = self.engine_pid

        logger.info(
            "Stopping vLLM instance %s: root_pid=%s engine_pid=%s",
            self.instance_id,
            root_pid,
            engine_pid,
        )

        # Gracefully stop both processes.
        _signal_pid(root_pid, signal.SIGTERM)
        _signal_pid(engine_pid, signal.SIGTERM)

        # join() is required to reap the restored root.
        process.join(timeout=timeout)

        # The root is genuinely still running only when is_alive() remains true
        # after join()/waitpid().
        if process.is_alive():
            logger.warning(
                "vLLM root PID %s did not stop; sending SIGKILL",
                root_pid,
            )
            _signal_pid(root_pid, signal.SIGKILL)
            _signal_pid(engine_pid, signal.SIGKILL)
            process.join(timeout=3)

        if process.is_alive():
            raise RuntimeError(
                f"Failed to stop and reap vLLM root PID {root_pid}"
            )

        # The root has now been reaped. Check EngineCore separately.
        if _pid_is_running(engine_pid):
            _signal_pid(engine_pid, signal.SIGKILL)

            deadline = time.monotonic() + 3
            while (
                time.monotonic() < deadline
                and _pid_is_running(engine_pid)
            ):
                time.sleep(0.1)

        if _pid_is_running(engine_pid):
            raise RuntimeError(
                f"Failed to stop EngineCore PID {engine_pid}"
            )

        process.close()

        self.process = None
        self.engine_pid = None
        self.cuda_checkpointed = False
        self.criu_checkpointed = False

        self._cleanup_log_file()

        return self._make_state("terminated")

    def _on_sentinel_exit(self):
        """Handle process exit detected by the sentinel fd.

        Removes the reader, collects the exit code, and invokes the
        registered *_on_exit_callback(instance_id, exitcode)*.
        """
        loop = asyncio.get_running_loop()
        loop.remove_reader(self.process.sentinel)
        self._sentinel_active = False
        self._on_exit_callback(self.instance_id, self.process.exitcode)

    def start_sentinel_watcher(self, on_exit_callback):
        """Register a sentinel fd on the event loop to detect process exit.

        When the child process terminates, the kernel makes the sentinel fd
        readable.  The handler invokes *on_exit_callback(instance_id, exitcode)*.
        """
        if self.process is None:
            raise HalfMade(self.instance_id)

        self._on_exit_callback = on_exit_callback
        loop = asyncio.get_running_loop()
        loop.add_reader(self.process.sentinel, self._on_sentinel_exit)
        self._sentinel_active = True

    def cancel_sentinel_watcher(self):
        """Remove the sentinel reader if it is still registered."""
        if self._sentinel_active and self.process is not None:
            try:
                loop = asyncio.get_running_loop()
                loop.remove_reader(self.process.sentinel)
            except RuntimeError:
                pass
            self._sentinel_active = False

    def _cleanup_log_file(self):
        """Remove the log file if it exists."""
        try:
            os.unlink(self._log_file_path)
        except FileNotFoundError:
            pass

    def get_status(self) -> dict:
        """Return the current instance status."""

        if self.criu_checkpointed:
            return self._make_state("criu_checkpointed")

        if self.process is None:
            raise HalfMade(self.instance_id)

        return self._make_state(
            "running" if self.process.is_alive() else "stopped"
        )

    def get_log_bytes(
        self, start: int = 0, end: int | None = None
    ) -> tuple[bytes, int]:
        """
        Retrieve log bytes from the child process.
        :param start: First byte to read (inclusive, 0-based).
        :param end: Last byte to read (inclusive, must be >= start).
                    None means up to start + MAX_LOG_RESPONSE_BYTES - 1
                    or EOF, whichever comes first.
        :return: (content_bytes, current_total_log_length)
        :raises LogRangeNotAvailable: If start is beyond available content
        """
        try:
            total = os.path.getsize(self._log_file_path)
        except FileNotFoundError:
            total = 0

        if start >= total:
            raise LogRangeNotAvailable(start, total)

        if end is None:
            read_end = min(start + MAX_LOG_RESPONSE_BYTES - 1, total - 1)
        else:
            read_end = min(end, total - 1)

        nbytes = read_end - start + 1
        with open(self._log_file_path, "rb") as f:
            f.seek(start)
            data = f.read(nbytes)
        return (data, total)

    def _get_descendant_pids(self, parent_pid: int) -> list[int]:
        descendants = []
        pending = [parent_pid]

        while pending:
            current_pid = pending.pop()

            result = subprocess.run(
                [
                    "pgrep",
                    "-P",
                    str(current_pid),
                ],
                capture_output=True,
                text=True,
                check=False,
            )

            if result.returncode not in (0, 1):
                raise RuntimeError(
                    result.stderr.strip()
                    or f"Failed to find children of PID {current_pid}"
                )

            child_pids = [
                int(line)
                for line in result.stdout.splitlines()
                if line.strip()
            ]

            descendants.extend(child_pids)
            pending.extend(child_pids)

        return descendants

    def _refresh_engine_pid(self, timeout: float = 10.0) -> int:
        """Find and cache the EngineCore PID after CRIU restore."""

        self.engine_pid = None
        deadline = time.monotonic() + timeout
        last_error = None

        while time.monotonic() < deadline:
            try:
                self.engine_pid = self._get_engine_pid()
                return self.engine_pid
            except RuntimeError as exc:
                last_error = exc
                time.sleep(0.2)

        raise RuntimeError(
            f"Restored vLLM root PID {self.process.pid} is running, "
            f"but EngineCore was not found: {last_error}"
        )
    
    def _get_engine_pid(self) -> int:
        if self.process is None or not self.process.is_alive():
            raise RuntimeError(
                f"vLLM instance {self.instance_id} is not running"
            )

        if self.engine_pid is not None:
            return self.engine_pid

        root_pid = self.process.pid
        descendant_pids = self._get_descendant_pids(root_pid)

        result = subprocess.run(
            [
                "nvidia-smi",
                "--query-compute-apps=pid",
                "--format=csv,noheader,nounits",
            ],
            capture_output=True,
            text=True,
            check=False,
        )

        if result.returncode != 0:
            raise RuntimeError(
                result.stderr.strip()
                or "nvidia-smi failed"
            )

        gpu_pids = {
            int(line.strip())
            for line in result.stdout.splitlines()
            if line.strip().isdigit()
        }

        matching_pids = [
            pid
            for pid in descendant_pids
            if pid in gpu_pids
        ]

        if not matching_pids:
            raise RuntimeError(
                f"No CUDA-owning EngineCore process found under "
                f"vLLM root PID {root_pid}"
            )

        if len(matching_pids) > 1:
            raise RuntimeError(
                f"Multiple CUDA-owning child processes found: "
                f"{matching_pids}"
            )

        self.engine_pid = matching_pids[0]
        return self.engine_pid

    def toggle_cuda_checkpoint(self) -> dict:
        engine_pid = self._get_engine_pid()

        result = subprocess.run(
            [
                "cuda-checkpoint",
                "--toggle",
                "--pid",
                str(engine_pid),
            ],
            capture_output=True,
            text=True,
            check=False,
        )

        if result.returncode != 0:
            raise RuntimeError(
                result.stderr.strip()
                or result.stdout.strip()
                or "cuda-checkpoint --toggle failed"
            )

        self.cuda_checkpointed = not self.cuda_checkpointed

        return {
            "instance_id": self.instance_id,
            "vllm_root_pid": self.process.pid,
            "engine_pid": engine_pid,
            "cuda_checkpointed": self.cuda_checkpointed,
            "stdout": result.stdout.strip(),
            "stderr": result.stderr.strip(),
        }

    def _get_criu_paths(self) -> tuple[str, str, str]:
        checkpoint_root = os.getenv(
            "FMA_CRIU_ROOT",
            "/var/lib/fma-criu",
        )

        instance_dir = os.path.join(
            checkpoint_root,
            self.instance_id,
        )

        images_dir = os.path.join(instance_dir, "images")
        bundle_dir = os.path.join(instance_dir, "bundle")

        return instance_dir, images_dir, bundle_dir
    
    def _read_path_list(self, path: str) -> list[str]:
        if not os.path.exists(path):
            return []

        with open(path, "r", encoding="utf-8") as file:
            return [
                line.strip()
                for line in file
                if line.strip()
            ]
    
    def _remove_shm_paths(self, paths: list[str]) -> None:
        for relative_path in paths:
            absolute_path = "/" + relative_path.lstrip("/")

            try:
                os.remove(absolute_path)
            except FileNotFoundError:
                pass
    
    def criu_checkpoint(self) -> dict:
        if self.process is None:
            raise RuntimeError(
                f"vLLM instance {self.instance_id} has not started"
            )

        if not self.process.is_alive():
            raise RuntimeError(
                f"vLLM instance {self.instance_id} is not running"
            )

        root_pid = self.process.pid

        os.kill(root_pid, signal.SIGUSR1)
        time.sleep(1)

        instance_dir, images_dir, bundle_dir = (
            self._get_criu_paths()
        )

        # Replace any old checkpoint for this instance.
        shutil.rmtree(instance_dir, ignore_errors=True)

        os.makedirs(images_dir, exist_ok=True)
        os.makedirs(bundle_dir, exist_ok=True)

        # Prevent the normal child-exit watcher from treating the CRIU dump
        # as an unexpected vLLM termination.
        self.cancel_sentinel_watcher()

        criu_bin = os.getenv("CRIU_BIN", "criu")

        result = subprocess.run(
            [
                "criu",
                "dump",
                "--tree",
                str(root_pid),
                "--images-dir",
                str(images_dir),
                "--tcp-close",
                "--file-locks",
                "--link-remap",
                "--shell-job",
                "-v4",
                "-o",
                "dump.log",
            ],
            capture_output=True,
            text=True,
            check=False,
        )

        if result.returncode != 0:
            raise RuntimeError(
                result.stderr.strip()
                or result.stdout.strip()
                or f"CRIU dump failed; see {images_dir}/dump.log"
            )

        # CRIU dump terminates the original multiprocessing child.
        old_process = self.process
        old_process.join(timeout=10)

        if old_process.is_alive():
            raise RuntimeError(
                f"Original vLLM process {root_pid} remained alive "
                "after CRIU dump"
            )

        old_process.close()
        self.process = None

        link_remap_paths = sorted(
            "dev/shm/" + os.path.basename(path)
            for path in glob.glob("/dev/shm/link_remap.*")
            if os.path.isfile(path)
        )

        dump_log_path = os.path.join(
            images_dir,
            "dump.log",
        )

        with open(
            dump_log_path,
            "r",
            encoding="utf-8",
            errors="replace",
        ) as file:
            dump_log = file.read()

        sem_paths = sorted(
            set(
                re.findall(
                    r"dev/shm/sem\.[A-Za-z0-9._-]+",
                    dump_log,
                )
            )
        )

        link_paths_file = os.path.join(
            bundle_dir,
            "link_remap_paths.txt",
        )

        sem_paths_file = os.path.join(
            bundle_dir,
            "sem_paths.txt",
        )

        with open(
            link_paths_file,
            "w",
            encoding="utf-8",
        ) as file:
            for path in link_remap_paths:
                file.write(path + "\n")

        with open(
            sem_paths_file,
            "w",
            encoding="utf-8",
        ) as file:
            for path in sem_paths:
                file.write(path + "\n")

        archive_path = os.path.join(
            bundle_dir,
            "link_remaps.tar",
        )

        if link_remap_paths:
            tar_bin = os.getenv(
                "TAR_BIN",
                "/usr/bin/tar",
            )

            tar_result = subprocess.run(
                [
                    tar_bin,
                    "--numeric-owner",
                    "-C",
                    "/",
                    "-cpf",
                    archive_path,
                    "-T",
                    link_paths_file,
                ],
                capture_output=True,
                text=True,
                check=False,
            )

            if tar_result.returncode != 0:
                raise RuntimeError(
                    tar_result.stderr.strip()
                    or tar_result.stdout.strip()
                    or "Failed to archive link_remap files"
                )

        # Restore always begins from a known clean /dev/shm state.
        self._remove_shm_paths(sem_paths)
        self._remove_shm_paths(link_remap_paths)

        self.criu_checkpointed = True
        self.engine_pid = None
        self.cuda_checkpointed = False

        return {
            "instance_id": self.instance_id,
            "checkpointed_root_pid": root_pid,
            "criu_checkpointed": True,
            "images_dir": images_dir,
            "bundle_dir": bundle_dir,
            "link_remap_paths": link_remap_paths,
            "sem_paths": sem_paths,
        }
    
    def criu_restore(self) -> dict:
        if not self.criu_checkpointed:
            raise RuntimeError(
                f"Instance {self.instance_id} has no CRIU checkpoint"
            )

        _, images_dir, bundle_dir = self._get_criu_paths()

        if not os.path.isdir(images_dir):
            raise RuntimeError(
                f"CRIU images directory not found: {images_dir}"
            )

        link_paths_file = os.path.join(
            bundle_dir,
            "link_remap_paths.txt",
        )

        sem_paths_file = os.path.join(
            bundle_dir,
            "sem_paths.txt",
        )

        archive_path = os.path.join(
            bundle_dir,
            "link_remaps.tar",
        )

        pid_file = os.path.join(
            bundle_dir,
            "restore.pid",
        )

        link_remap_paths = self._read_path_list(
            link_paths_file
        )

        sem_paths = self._read_path_list(
            sem_paths_file
        )

        # Remove stale sidecars left by a failed or previous restore.
        self._remove_shm_paths(sem_paths)
        self._remove_shm_paths(link_remap_paths)

        # Recreate exactly the link_remap files captured after dump.
        if os.path.exists(archive_path):
            tar_bin = os.getenv(
                "TAR_BIN",
                "/usr/bin/tar",
            )

            tar_result = subprocess.run(
                [
                    tar_bin,
                    "--numeric-owner",
                    "-C",
                    "/",
                    "-xpf",
                    archive_path,
                ],
                capture_output=True,
                text=True,
                check=False,
            )

            if tar_result.returncode != 0:
                raise RuntimeError(
                    tar_result.stderr.strip()
                    or tar_result.stdout.strip()
                    or "Failed to restore link_remap files"
                )

        # sem.* targets must remain absent before CRIU restore.
        self._remove_shm_paths(sem_paths)

        try:
            os.remove(pid_file)
        except FileNotFoundError:
            pass

        criu_bin = os.getenv("CRIU_BIN", "criu")

        try:
            result = subprocess.run(
                [
                    "criu",
                    "restore",
                    "--images-dir",
                    str(images_dir),
                    "--tcp-close",
                    "--file-locks",
                    "--link-remap",
                    "--shell-job",
                    "--restore-detached",
                    "--restore-sibling",
                    "--pidfile",
                    str(pid_file),
                    "-v4",
                    "-o",
                    "restore.log",
                ],
                capture_output=True,
                text=True,
                check=False,
            )
        finally:
            # --shell-job may leave the restored process group in the
            # foreground of the launcher's terminal.
            _reclaim_launcher_terminal()

        if result.returncode != 0:
            raise RuntimeError(
                result.stderr.strip()
                or result.stdout.strip()
                or f"CRIU restore failed; see {images_dir}/restore.log"
            )

        with open(
            pid_file,
            "r",
            encoding="utf-8",
        ) as file:
            restored_root_pid = int(file.read().strip())

        self.process = AttachedProcess(
            pid=restored_root_pid,
            name=f"vllm-{self.instance_id}",
        )

        restored_engine_pid = self._refresh_engine_pid()

        self.cuda_checkpointed = False
        self.criu_checkpointed = False

        return {
            "instance_id": self.instance_id,
            "restored_root_pid": restored_root_pid,
            "restored_engine_pid": restored_engine_pid,
            "criu_checkpointed": False,
            "cuda_checkpointed": self.cuda_checkpointed,
            "images_dir": images_dir,
            "restore_log": os.path.join(
                images_dir,
                "restore.log",
            ),
        }


# Multi-instance vLLM process manager
class VllmMultiProcessManager:
    def __init__(
        self,
        mock_gpus: bool = False,
        mock_gpu_count: int = 8,
        node_name: Optional[str] = None,
        namespace: Optional[str] = None,
        log_dir: str = "",
    ):
        self.instances: Dict[str, VllmInstance] = {}
        self.broadcaster = EventBroadcaster()
        # Monotonically increasing counter owned by the manager.
        # The manager stamps every event before handing it to the
        # broadcaster, so revision assignment is synchronous and
        # race-free with respect to the returned API response.
        self._revision: int = 0
        self.gpu_translator = GpuTranslator(
            mock_gpus=mock_gpus,
            node_name=node_name,
            namespace=namespace,
            mock_gpu_count=mock_gpu_count,
        )
        self.log_dir = log_dir

    @property
    def revision(self) -> int:
        return self._revision

    def _next_revision(self) -> int:
        self._revision += 1
        return self._revision

    def _on_instance_stopped(self, instance_id: str, exitcode):
        """Sentinel callback: assign revision and publish a STOPPED event."""
        revision = self._next_revision()
        instance = self.instances[instance_id]
        instance.last_revision = revision
        obj = instance.get_status()
        obj["exit_code"] = exitcode
        event = WatchEvent(type="STOPPED", object=obj)
        self.broadcaster._append(event)
        loop = asyncio.get_running_loop()
        loop.create_task(self.broadcaster._notify())

    def create_instance(
        self, vllm_config: VllmConfig, instance_id: Optional[str] = None
    ) -> dict:
        """Create and start a new vLLM instance"""
        if instance_id is None:
            instance_id = str(uuid.uuid4())

        if instance_id in self.instances:
            logger.warning(
                "Rejecting request to create vLLM instance: id=%s already exists",
                instance_id,
            )
            raise ValueError(f"Instance with ID {instance_id} already exists")

        logger.info("Accepted request to create vLLM instance with id=%s", instance_id)

        instance = VllmInstance(
            instance_id, vllm_config, self.gpu_translator, self.log_dir
        )
        self.instances[instance_id] = instance

        try:
            instance.start()
        except Exception:
            self.instances.pop(instance_id, None)
            raise

        revision = self._next_revision()
        instance.last_revision = revision
        result = instance.get_status()
        event = WatchEvent(type="CREATED", object=result)
        self.broadcaster._append(event)
        try:
            loop = asyncio.get_running_loop()
            instance.start_sentinel_watcher(self._on_instance_stopped)
            loop.create_task(self.broadcaster._notify())
        except RuntimeError:
            pass  # No running event loop (e.g. in sync tests)
        return result

    def stop_instance(self, instance_id: str, timeout: int = 10) -> dict:
        """Stop a specific vLLM instance"""
        if instance_id not in self.instances:
            raise KeyError(f"Instance {instance_id} not found")

        instance = self.instances[instance_id]
        instance.cancel_sentinel_watcher()
        instance.stop(timeout)

        revision = self._next_revision()
        instance.last_revision = revision
        result = instance._make_state("terminated")

        del self.instances[instance_id]

        event = WatchEvent(type="DELETED", object=result)
        self.broadcaster._append(event)
        try:
            loop = asyncio.get_running_loop()
            loop.create_task(self.broadcaster._notify())
        except RuntimeError:
            pass  # No running event loop (e.g. in sync tests)
        return result

    def stop_all_instances(self, timeout: int = 10) -> dict:
        """Stop all running vLLM instances"""
        results = []
        instance_ids = list(self.instances.keys())

        for instance_id in instance_ids:
            try:
                result = self.stop_instance(instance_id, timeout)
                results.append(result)
            except KeyError:
                continue  # Instance was already removed

        return {
            "status": "all_stopped",
            "stopped_instances": results,
            "total_stopped": len(results),
        }

    def get_instance_status(self, instance_id: str) -> dict:
        """Get status of a specific instance"""
        if instance_id not in self.instances:
            raise KeyError(f"Instance {instance_id} not found")

        return self.instances[instance_id].get_status()

    def get_all_instances_status(self) -> dict:
        """Get status of all instances"""
        instances_status = []
        running_count = 0

        for instance in self.instances.values():
            status = instance.get_status()
            instances_status.append(status)
            if status["status"] == "running":
                running_count += 1

        return {
            "revision": self._revision,
            "total_instances": len(self.instances),
            "running_instances": running_count,
            "instances": instances_status,
        }

    def list_instances(self) -> List[str]:
        """List all instance IDs"""
        return list(self.instances.keys())

    def get_instance_log_bytes(
        self,
        instance_id: str,
        start: int = 0,
        end: int | None = None,
    ) -> tuple[bytes, int]:
        """
        Get log bytes from a specific instance.
        :param instance_id: ID of the instance
        :param start: First byte to read (inclusive, 0-based)
        :param end: Last byte to read (inclusive), or None for default limit
        :return: (content_bytes, total_file_size)
        :raises LogRangeNotAvailable: If start is beyond available content
        """
        if instance_id not in self.instances:
            raise KeyError(f"Instance {instance_id} not found")
        return self.instances[instance_id].get_log_bytes(start, end)
    
    def toggle_instance_cuda_checkpoint(
        self,
        instance_id: str,
    ) -> dict:
        if instance_id not in self.instances:
            raise KeyError(instance_id)

        return self.instances[
            instance_id
        ].toggle_cuda_checkpoint()

    def checkpoint_instance_criu(
        self,
        instance_id: str,
    ) -> dict:
        if instance_id not in self.instances:
            raise KeyError(instance_id)

        return self.instances[
            instance_id
        ].criu_checkpoint()
    
    def restore_instance_criu(
        self,
        instance_id: str,
    ) -> dict:
        if instance_id not in self.instances:
            raise KeyError(instance_id)

        instance = self.instances[instance_id]
        result = instance.criu_restore()

        try:
            instance.start_sentinel_watcher(
                self._on_instance_stopped
            )
        except RuntimeError:
            # No running event loop, such as in synchronous tests.
            pass

        return result

# Create global manager instance
vllm_manager = VllmMultiProcessManager()

# Setup logging
logger = logging.getLogger(__name__)


@asynccontextmanager
async def lifespan(application: FastAPI):
    """Manage application lifecycle: clean up all vLLM instances on shutdown."""
    yield
    logger.info("Launcher shutting down, stopping all vLLM instances...")
    vllm_manager.stop_all_instances()


# Create FastAPI application
app = FastAPI(
    title="Multi-Instance vLLM Management API",
    version="2.0",
    description="REST API for managing multiple vLLM instances",
    lifespan=lifespan,
)


_RANGE_RE = re.compile(r"^bytes=(\d+)-(\d+)?$")


def parse_range_header(range_header: str) -> tuple[int, int | None]:
    """Parse an HTTP Range header value.

    Accepts ``bytes=START-END`` (both inclusive) or ``bytes=START-``
    (open-ended).  Returns ``(start, end)`` where *end* may be ``None``.

    Raises :class:`ValueError` for unsupported or malformed values
    (e.g. suffix ranges like ``bytes=-500``).
    """
    m = _RANGE_RE.match(range_header)
    if m is None:
        raise ValueError(f"Unsupported or malformed Range header: {range_header}")
    start = int(m.group(1))
    # group(2) is the end value; absent in open-ended ranges like "bytes=100-"
    end = int(m.group(2)) if m.group(2) else None
    if end is not None and end < start:
        raise ValueError(f"Range end ({end}) must be >= start ({start})")
    return (start, end)


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
        content={
            "name": "Multi-Instance vLLM Management API",
            "version": "2.0",
            "endpoints": {
                "index": "GET /",
                "health": "GET /health",
                "create_instance": "POST /v2/vllm/instances",
                "create_named_instance": "PUT /v2/vllm/instances/{instance_id}",
                "delete_instance": "DELETE /v2/vllm/instances/{instance_id}",
                "delete_all_instances": "DELETE /v2/vllm/instances",
                "get_instance_status": "GET /v2/vllm/instances/{instance_id}",
                "get_all_instances": "GET /v2/vllm/instances",
                "get_instance_logs": "GET /v2/vllm/instances/{instance_id}/log",
                "watch_instances": "GET /v2/vllm/instances/watch",
            },
        },
        status_code=HTTPStatus.OK,
    )


######################################################################
# vLLM MANAGEMENT ENDPOINTS
######################################################################


@app.get("/v2/vllm/instances/watch")
async def watch_instances(
    since: Optional[int] = Query(
        None,
        description="Resume watching from this revision. "
        "If omitted, the stream begins with CREATED events for every "
        "instance that currently exists, followed by live events.",
    ),
):
    """Stream instance lifecycle events as NDJSON (Kubernetes watch-style)."""

    if since is not None:
        oldest = vllm_manager.broadcaster.oldest_revision
        if since < oldest:
            raise HTTPException(
                status_code=HTTPStatus.GONE,
                detail=f"Requested revision {since} is no longer available. "
                f"Oldest available: {oldest}.",
            )

    async def event_stream():
        if since is None:
            # Initial state: emit a CREATED event for every existing instance
            # and start streaming from the current revision.
            start_revision = vllm_manager.revision
            for instance in vllm_manager.instances.values():
                state = instance.get_status()
                initial = WatchEvent(
                    type="CREATED",
                    object=state,
                )
                yield initial.model_dump_json() + "\n"
        else:
            start_revision = since

        try:
            async for event in vllm_manager.broadcaster.watch(start_revision):
                yield event.model_dump_json() + "\n"
        except RevisionTooOld:
            return

    return StreamingResponse(
        event_stream(),
        media_type="application/x-ndjson",
        headers={"X-Content-Type-Options": "nosniff"},
    )


@app.post("/v2/vllm/instances")
async def create_vllm_instance(vllm_config: VllmConfig):
    """Create a new vLLM instance with random instance ID"""

    try:
        result = vllm_manager.create_instance(vllm_config)
        return JSONResponse(content=result, status_code=HTTPStatus.CREATED)
    except Exception as e:
        logger.error(f"Failed to create vLLM instance: {e}")
        raise HTTPException(status_code=500, detail=str(e))


@app.put("/v2/vllm/instances/{instance_id}")
async def create_id_vllm_instance(
    vllm_config: VllmConfig,
    instance_id: str = Path(..., description="Custom instance ID"),
):
    """Create a new vLLM instance with instance ID"""
    try:
        result = vllm_manager.create_instance(vllm_config, instance_id)
        return JSONResponse(content=result, status_code=HTTPStatus.CREATED)
    except ValueError as e:
        raise HTTPException(status_code=409, detail=str(e))
    except Exception as e:
        logger.error(f"Failed to create vLLM instance {instance_id}: {e}")
        raise HTTPException(status_code=500, detail=str(e))


@app.delete("/v2/vllm/instances/{instance_id}")
async def delete_vllm_instance(
    instance_id: str = Path(..., description="Instance ID to delete")
):
    """Delete a specific vLLM instance"""
    try:
        result = vllm_manager.stop_instance(instance_id)
        return JSONResponse(content=result, status_code=HTTPStatus.OK)
    except KeyError:
        raise HTTPException(status_code=404, detail=f"Instance {instance_id} not found")
    except Exception as e:
        logger.error(f"Failed to delete vLLM instance {instance_id}: {e}")
        raise HTTPException(status_code=500, detail=str(e))


@app.delete("/v2/vllm/instances")
async def delete_all_vllm_instances():
    """Delete all vLLM instances"""
    try:
        result = vllm_manager.stop_all_instances()
        return JSONResponse(content=result, status_code=HTTPStatus.OK)
    except Exception as e:
        logger.error(f"Failed to delete all vLLM instances: {e}")
        raise HTTPException(status_code=500, detail=str(e))


@app.get("/v2/vllm/instances")
async def get_all_vllm_instances(detail: bool = True):
    """
    Get information about all vLLM instances

    Query Parameters:
    - detail: If True (default), returns full status of all instances.
              If False, returns only instance IDs.
    """
    if detail:
        result = vllm_manager.get_all_instances_status()
    else:
        instances = vllm_manager.list_instances()
        result = {
            "revision": vllm_manager.revision,
            "instance_ids": instances,
            "count": len(instances),
        }

    return JSONResponse(content=result, status_code=HTTPStatus.OK)


@app.get("/v2/vllm/instances/{instance_id}")
async def get_vllm_instance_status(
    instance_id: str = Path(..., description="Instance ID")
):
    """Get status of a specific vLLM instance"""
    try:
        result = vllm_manager.get_instance_status(instance_id)
        return JSONResponse(content=result, status_code=HTTPStatus.OK)
    except KeyError:
        raise HTTPException(status_code=404, detail=f"Instance {instance_id} not found")


@app.get("/v2/vllm/instances/{instance_id}/log")
async def get_vllm_instance_logs(
    instance_id: str = Path(..., description="Instance ID"),
    range: str | None = Header(None, alias="Range"),
):
    """
    Get logs from a specific vLLM instance.

    Supports range requests per RFC 9110 §14 (Range Requests).

    Without a Range header the full log (up to 1 MB) is returned with
    200 OK.  With ``Range: bytes=START-END`` or ``Range: bytes=START-``
    the requested slice is returned with 206 Partial Content.  In both
    cases the response includes a ``Content-Range`` header indicating the byte range
    and current total log length.
    """
    try:
        if range is None:
            start, end = 0, None
            partial = False
        else:
            try:
                start, end = parse_range_header(range)
            except ValueError as exc:
                raise HTTPException(status_code=400, detail=str(exc))
            partial = True

        data, total = vllm_manager.get_instance_log_bytes(instance_id, start, end)

        actual_end = start + len(data) - 1
        headers = {
            "Accept-Ranges": "bytes",
            "Content-Range": f"bytes {start}-{actual_end}/{total}",
        }
        if partial:
            status_code = HTTPStatus.PARTIAL_CONTENT
        else:
            status_code = HTTPStatus.OK

        return Response(
            content=data,
            status_code=status_code,
            media_type="application/octet-stream",
            headers=headers,
        )
    except KeyError:
        raise HTTPException(status_code=404, detail=f"Instance {instance_id} not found")
    except LogRangeNotAvailable as e:
        return Response(
            content=b"",
            status_code=HTTPStatus.REQUESTED_RANGE_NOT_SATISFIABLE,
            media_type="application/octet-stream",
            headers={"Content-Range": f"bytes */{e.available_bytes}"},
        )
    except HTTPException:
        raise
    except Exception as e:
        logger.error(f"Failed to get logs for instance {instance_id}: {e}")
        raise HTTPException(status_code=500, detail=str(e))

@app.post("/v2/vllm/instances/{instance_id}/cuda-checkpoint/toggle")
async def toggle_cuda_checkpoint(
    instance_id: str,
):
    try:
        return vllm_manager.toggle_instance_cuda_checkpoint(
            instance_id
        )

    except KeyError:
        raise HTTPException(
            status_code=404,
            detail=f"Instance {instance_id} not found",
        )

    except RuntimeError as exc:
        raise HTTPException(
            status_code=500,
            detail=str(exc),
        )
    
@app.post("/v2/vllm/instances/{instance_id}/criu/checkpoint")
async def checkpoint_vllm_instance_criu(
    instance_id: str,
):
    try:
        return vllm_manager.checkpoint_instance_criu(
            instance_id
        )

    except KeyError:
        raise HTTPException(
            status_code=404,
            detail=f"Instance {instance_id} not found",
        )

    except RuntimeError as exc:
        raise HTTPException(
            status_code=500,
            detail=str(exc),
        )

@app.post("/v2/vllm/instances/{instance_id}/criu/restore")
async def restore_vllm_instance_criu(
    instance_id: str,
):
    try:
        return vllm_manager.restore_instance_criu(
            instance_id
        )

    except KeyError:
        raise HTTPException(
            status_code=404,
            detail=f"Instance {instance_id} not found",
        )

    except RuntimeError as exc:
        raise HTTPException(
            status_code=500,
            detail=str(exc),
        )

######################################################################
# HELPER FUNCTIONS
######################################################################

def _close_inherited_sockets():
    """Close every socket fd inherited from the parent across fork().

    Iterates /proc/self/fd and uses fstat to identify sockets, leaving
    pipes, regular files, character devices, and anything else
    untouched. fd 0/1/2 are skipped on principle even if they happened
    to be sockets.
    """
    try:
        fd_names = os.listdir("/proc/self/fd")
    except OSError:
        return
    for name in fd_names:
        try:
            fd = int(name)
        except ValueError:
            continue
        if fd <= 2:
            continue
        try:
            if stat.S_ISSOCK(os.fstat(fd).st_mode):
                os.close(fd)
        except OSError:
            # fd was already closed or unstatable; ignore.
            pass

def _close_current_process_nvidia_fds(signum=None, frame=None):
    """Close NVIDIA FDs in this process only."""
    closed_fds = []

    try:
        fd_names = os.listdir("/proc/self/fd")
    except OSError:
        return

    for fd_name in fd_names:
        try:
            fd = int(fd_name)
        except ValueError:
            continue

        if fd <= 2:
            continue

        try:
            target = os.readlink(f"/proc/self/fd/{fd}")
        except OSError:
            continue

        if not target.startswith("/dev/nvidia"):
            continue

        try:
            os.close(fd)
            closed_fds.append(fd)
        except OSError:
            pass

    logger.info(
        "Closed NVIDIA FDs in PID %s: %s",
        os.getpid(),
        closed_fds,
    )

def _close_inherited_eventpoll_fds():
    """Close epoll FDs inherited from the launcher across fork()."""
    try:
        fd_names = os.listdir("/proc/self/fd")
    except OSError:
        return

    for name in fd_names:
        try:
            fd = int(name)
        except ValueError:
            continue

        if fd <= 2:
            continue

        try:
            target = os.readlink(f"/proc/self/fd/{fd}")
        except OSError:
            continue

        if target != "anon_inode:[eventpoll]":
            continue

        try:
            os.close(fd)
        except OSError:
            pass

# Function to be executed by the child process
def vllm_kickoff(vllm_config: VllmConfig, log_file_path: str):
    """
    Child function to kickoff vllm instance
    :param vllm_config: vLLM configuration parameters and env variables
    :param log_file_path: Path to the log file for capturing stdout/stderr
    """

    # Isolate this process tree into its own process group so that
    # signals (SIGINT/SIGTERM) sent to the launcher's group do not
    # propagate to the vLLM server or its EngineCore child process.
    os.setpgrp()

    signal.signal(
        signal.SIGUSR1,
        _close_current_process_nvidia_fds,
    )

    _close_inherited_eventpoll_fds()

    # Close socket fds inherited from the launcher across fork(). The
    # child must not hold duplicate references to uvicorn's listening
    # socket on :8001 nor to any in-flight TCP connections — the kernel
    # keeps a socket open as long as any process has an fd to it, so a
    # leaked client fd in this child wedges the connection long after
    # uvicorn has closed its own end (see issue #550). Pipes (including
    # multiprocessing's parent-child sentinel pipe used by
    # start_sentinel_watcher) and regular files are deliberately left
    # alone.
    _close_inherited_sockets()

    # Redirect stdout and stderr at the OS level using dup2 so that
    # all output (including from vLLM/uvicorn internal logging and any
    # C extensions writing to fd 1/2) is captured in the log file.
    log_fd = os.open(log_file_path, os.O_WRONLY | os.O_CREAT | os.O_APPEND, 0o644)
    os.dup2(log_fd, sys.stdout.fileno())
    os.dup2(log_fd, sys.stderr.fileno())
    os.close(log_fd)
    sys.stdout = os.fdopen(1, "w")
    sys.stderr = os.fdopen(2, "w")

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

# Function to set env variables
def set_env_vars(env_vars: Dict[str, str]):
    """
    Set environment variables from a dictionary
    :param env_vars: Dict with environment var name as keys and string values
    """

    # Set environment variables from a dictionary
    for key, value in env_vars.items():
        os.environ[key] = value


if __name__ == "__main__":
    import argparse

    import uvicorn

    parser = argparse.ArgumentParser(description="vLLM Launcher Service")
    parser.add_argument(
        "--mock-gpus",
        action="store_true",
        help="Enable mock GPU mode for CPU-only testing environments",
    )
    parser.add_argument(
        "--mock-gpu-count",
        type=int,
        default=8,
        help="Number of mock GPUs to create in mock mode (default: 8)",
    )
    parser.add_argument(
        "--host",
        type=str,
        default="0.0.0.0",
        help="Host to bind the server to (default: 0.0.0.0)",
    )
    parser.add_argument(
        "--port",
        type=int,
        default=8001,
        help="Port to bind the server to (default: 8001)",
    )
    parser.add_argument(
        "--log-level",
        type=str,
        default="info",
        choices=["critical", "error", "warning", "info", "debug"],
        help="Logging level (default: info)",
    )

    args = parser.parse_args()

    logging.getLogger().setLevel(getattr(logging, args.log_level.upper()))

    # Get node name from environment variable
    node_name = os.getenv("NODE_NAME")
    namespace = os.getenv("NAMESPACE")

    logger.info(
        "Launcher starting with args: mock_gpus=%s, mock_gpu_count=%d, "
        "host=%s, port=%d, log_level=%s, node_name=%s, namespace=%s",
        args.mock_gpus,
        args.mock_gpu_count,
        args.host,
        args.port,
        args.log_level,
        node_name,
        namespace,
    )

    # Reinitialize the global manager with mock mode settings
    vllm_manager = VllmMultiProcessManager(
        mock_gpus=args.mock_gpus,
        mock_gpu_count=args.mock_gpu_count,
        node_name=node_name,
        namespace=namespace,
    )

    uvicorn.run(
        app, host=args.host, port=args.port, log_level=args.log_level, log_config=None
    )
