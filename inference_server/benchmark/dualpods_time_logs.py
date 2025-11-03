# Copyright 2025 The llm-d Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
# 	http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

import logging
import subprocess
from time import perf_counter, sleep

from kubernetes import client, config, watch
from utils import parse_request_args

# ---------------- Logging setup ----------------
logger = logging.getLogger(__name__)
logger.setLevel(logging.INFO)
formatter = logging.Formatter("%(asctime)s - %(levelname)s - %(message)s")

file_handler = logging.FileHandler("metrics.log")
file_handler.setLevel(logging.DEBUG)
file_handler.setFormatter(formatter)

console_handler = logging.StreamHandler()
console_handler.setLevel(logging.INFO)
console_handler.setFormatter(formatter)

logger.addHandler(file_handler)
logger.addHandler(console_handler)

DUAL_POD_TOTAL = 2


# ---------------- Helper functions ----------------
def apply_yaml(yaml_file):
    logger.info(f"Applying {yaml_file}...")
    subprocess.run(["kubectl", "apply", "-f", yaml_file], check=True)


def delete_yaml(yaml_file):
    logger.info(f"Cleaning up resources from {yaml_file}...")
    subprocess.run(
        ["kubectl", "delete", "-f", yaml_file, "--ignore-not-found=true"], check=False
    )


def get_pods_with_label(api, namespace, label_selector):
    pods = api.list_namespaced_pod(
        namespace=namespace, label_selector=label_selector
    ).items
    return pods


def wait_for_dual_pods_ready(v1, namespace, podname, timeout=600, suffix="server"):
    start = perf_counter()
    elapsed = 0
    # Server-requesting and server-providing pods
    # target_pods = {podname, f"{podname}-{suffix}"}
    ready_pods = set()

    logger.info(f"Waiting for pods of ReplicaSet: {podname}")

    def check_ready(pod):
        if pod.status.phase == "Running":
            for cond in pod.status.conditions or []:
                if cond.type == "Ready" and cond.status == "True":
                    return True
        return False

    # Initialize the variables to be returned
    rq_ready = None
    prv_ready = None
    # Defaulting to cold for now.
    prv_mode = "Cold"

    while elapsed < timeout:
        try:
            w = watch.Watch()
            for event in w.stream(
                v1.list_namespaced_pod,
                namespace=namespace,
                timeout_seconds=min(300, timeout - int(elapsed)),
            ):
                pod = event["object"]
                name = pod.metadata.name
                labels = pod.metadata.labels

                # Filter the pods.
                if (podname in name) and (suffix not in name):
                    logger.info(f"Checking Readiness of Requester Pod: {name}")
                    if check_ready(pod):
                        rq_ready = int(perf_counter() - start)
                        ready_pods.add(name)
                elif suffix in name:  # Any provider pods that can be bound.
                    logger.info(f"Checking Readiness of Provider Pod: {name}")

                    # Get the server-requesting it is bound to, if any.
                    if "dual-pods.llm-d.ai/dual" in labels:
                        dual_pod = labels["dual-pods.llm-d.ai/dual"]
                        logger.info(
                            f"Checking Ready of Provider for Pair <{dual_pod}>:<{name}>"
                        )

                        # Set the return variables for the ready pod.
                        if check_ready(pod):
                            binding_match = podname in dual_pod
                            if (name not in ready_pods) and binding_match:
                                prv_ready = int(perf_counter() - start)
                                ready_pods.add(name)
                                prv_mode = "Cold"
                            elif (name not in ready_pods) and not binding_match:
                                prv_ready = int(perf_counter() - start)
                                ready_pods.add(name)
                                prv_mode = "Hit"

                if len(ready_pods) == DUAL_POD_TOTAL:
                    w.stop()
                    end = perf_counter()
                    logger.info(f"✅ Both pods Ready after {end - start:.2f}s")
                    return rq_ready, prv_ready, prv_mode

            elapsed = perf_counter() - start

        except Exception as e:
            logger.warning(
                f"⚠️ Watch interrupted ({type(e).__name__}: {e}), retrying..."
            )
            sleep(2)
            elapsed = perf_counter() - start

    raise TimeoutError(f"Timed out after {timeout}s waiting for both pods to be Ready.")


# ---------------- Main ----------------
def main():
    config.load_kube_config()
    v1 = client.CoreV1Api()

    namespace, yaml_file, label, image = parse_request_args()

    delete_yaml(yaml_file)

    logger.info(f"Namespace: {namespace}")
    logger.info(f"YAML file: {yaml_file}")

    start_time = perf_counter()
    apply_yaml(yaml_file)

    logger.info("Waiting for server-requesting pod to appear...")
    requester_pod = None
    for _ in range(60):
        pods = get_pods_with_label(v1, namespace, label)
        if pods:
            requester_pod = pods[0]
            break
        sleep(2)

    if not requester_pod:
        logger.info("❌ No requester pod appeared within 120s.")
        return

    requester_name = requester_pod.metadata.name
    logger.info(f"Requester pod detected: {requester_name}")

    logger.info("Waiting for server-providing pod to relay readiness to requester...")
    ready_time = wait_for_dual_pods_ready(v1, namespace, requester_name)

    total_time = ready_time - start_time
    logger.info(f"🚀 Readiness time: {total_time:.2f} seconds\n")

    delete_yaml(yaml_file)


if __name__ == "__main__":
    main()
