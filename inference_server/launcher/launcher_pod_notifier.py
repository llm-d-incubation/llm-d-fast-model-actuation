#!/usr/bin/env python3
# Copyright 2026 The llm-d Authors.

# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at

#     http://www.apache.org/licenses/LICENSE-2.0

# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

"""Publish vLLM instance-state changes onto the enclosing Pod."""

import hashlib
import json
import logging
import os
import sys
import time
import urllib.error
import urllib.request
from typing import Any

from kubernetes import client, config
from kubernetes.client import ApiException

SIGNATURE_ANNOTATION = "dual-pods.llm-d.ai/vllm-instance-signature"

DEFAULT_BASE_URL = "http://127.0.0.1:8001"
DEFAULT_POLL_INTERVAL_SECONDS = 2.0
DEFAULT_ERROR_BACKOFF_SECONDS = 5.0


logger = logging.getLogger("launcher_pod_notifier")


def configure_logging() -> None:
    logging.basicConfig(
        level=logging.INFO,
        format="%(asctime)s %(levelname)s %(name)s: %(message)s",
    )


def get_required_env(name: str) -> str:
    value = os.getenv(name)
    if not value:
        raise RuntimeError(f"missing required environment variable {name}")
    return value


def fetch_launcher_state(base_url: str) -> dict[str, Any]:
    url = f"{base_url}/v2/vllm/instances"
    with urllib.request.urlopen(url, timeout=5) as response:
        payload = json.load(response)
    if not isinstance(payload, dict):
        raise ValueError(f"launcher response is not an object: {payload!r}")
    return payload


def canonicalize_launcher_state(payload: dict[str, Any]) -> list[tuple[str, str]]:
    instances = payload.get("instances", [])
    if not isinstance(instances, list):
        raise ValueError(f"instances must be a list, got {type(instances).__name__}")
    canonical_instances: list[tuple[str, str]] = []
    for instance in instances:
        if not isinstance(instance, dict):
            raise ValueError(f"unexpected instance entry: {instance!r}")
        instance_id = str(instance.get("instance_id", ""))
        status = str(instance.get("status", ""))
        canonical_instances.append((instance_id, status))
    canonical_instances.sort()
    return canonical_instances


def compute_signature(payload: dict[str, Any]) -> str:
    canonical = canonicalize_launcher_state(payload)
    blob = json.dumps(canonical, separators=(",", ":")).encode("utf-8")
    return hashlib.sha256(blob).hexdigest()


def load_incluster_client() -> client.CoreV1Api:
    config.load_incluster_config()
    return client.CoreV1Api()


def get_pod_annotations(
    api: client.CoreV1Api, namespace: str, pod_name: str
) -> dict[str, str]:
    pod = api.read_namespaced_pod(name=pod_name, namespace=namespace)
    return pod.metadata.annotations or {}


def patch_pod_annotations(
    api: client.CoreV1Api,
    namespace: str,
    pod_name: str,
    *,
    signature: str,
) -> None:
    body = {
        "metadata": {
            "annotations": {
                SIGNATURE_ANNOTATION: signature,
            }
        }
    }
    api.patch_namespaced_pod(name=pod_name, namespace=namespace, body=body)


def patch_pod_signature(
    api: client.CoreV1Api, namespace: str, pod_name: str, signature: str
) -> None:
    patch_pod_annotations(api, namespace, pod_name, signature=signature)
    logger.info(
        "Published launcher state change",
        extra={"pod": pod_name, "signature": signature},
    )


def main() -> int:
    configure_logging()

    try:
        pod_name = get_required_env("POD_NAME")
        namespace = get_required_env("NAMESPACE")
    except RuntimeError as exc:
        logger.error("%s", exc)
        return 1

    base_url = os.getenv("LAUNCHER_BASE_URL", DEFAULT_BASE_URL).rstrip("/")
    poll_interval = DEFAULT_POLL_INTERVAL_SECONDS
    error_backoff = DEFAULT_ERROR_BACKOFF_SECONDS

    try:
        api = load_incluster_client()
    except Exception as exc:
        logger.error("Failed to initialize in-cluster Kubernetes client: %s", exc)
        return 1

    logger.info(
        "Launcher Pod notifier started for pod %s in namespace %s against %s",
        pod_name,
        namespace,
        base_url,
    )

    last_published_signature: str | None = None

    while True:
        try:
            signature = compute_signature(fetch_launcher_state(base_url))
            if signature != last_published_signature:
                patch_pod_signature(api, namespace, pod_name, signature)
                last_published_signature = signature
            time.sleep(poll_interval)
        except (
            ApiException,
            OSError,
            TimeoutError,
            ValueError,
            urllib.error.HTTPError,
            urllib.error.URLError,
        ) as exc:
            logger.warning("Notifier loop failed: %s", exc)
            time.sleep(error_backoff)
        except Exception as exc:
            logger.exception("Unexpected notifier failure: %s", exc)
            time.sleep(error_backoff)


if __name__ == "__main__":
    sys.exit(main())
