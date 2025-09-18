/*
Copyright 2025 The llm-d Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

	http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strings"

	stubapi "github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/stub/api"
)

var GPUUUIDs []string

func getGPUUUIDs() error {
	cmd := exec.Command("nvidia-smi", "--query-gpu=uuid", "--format=csv,noheader")
	output, err := cmd.Output()
	if err != nil {
		return err
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	var uuids []string
	for _, line := range lines {
		if line != "" {
			uuids = append(uuids, line)
		}
	}
	if len(uuids) == 0 {
		return fmt.Errorf("no GPUs found")
	}

	GPUUUIDs = uuids
	return nil
}

func gpuHandler(w http.ResponseWriter, r *http.Request) {
	if len(GPUUUIDs) == 0 {
		http.Error(w, "no GPUs found", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(GPUUUIDs); err != nil {
		http.Error(w, fmt.Sprintf("error encoding response: %v", err), http.StatusInternalServerError)
		return
	}
}

// Note:
// There are at least two types of 'readiness' in the server-requesting pod.
// 1. The readiness to accept queries asking which GPU(s) are allocated.
// This readiness is a prerequisite of the corresponding server-running pod
// because the server-running pod uses the allocated GPU(s).
// 2. The relayed readiness of the corresponding server-running pod.
// This readiness is part of the interface for a user to manage his/her
// inference servers. More broadly, a user uses the server-requseting pod
// as the interface to a) specify the 'desired state' via the server-requesting
// pod's annotations, b) monitor the readiness of the inference server.
//
// Here we are dealing with the 1st type of readiness.
func readyHandler(w http.ResponseWriter, r *http.Request) {
	if len(GPUUUIDs) == 0 {
		http.Error(w, "not ready", http.StatusServiceUnavailable)
		return
	}

	w.WriteHeader(http.StatusOK)
	_, err := w.Write([]byte("ready"))
	if err != nil {
		http.Error(w, fmt.Sprintf("error writing response: %v", err), http.StatusInternalServerError)
		return
	}
}

func main() {
	err := getGPUUUIDs()
	if err != nil {
		fmt.Printf("Error getting GPU UUIDs: %v\n", err)
	}

	http.HandleFunc("/v1"+stubapi.AcceleratorQueryPath, gpuHandler)
	http.HandleFunc("/readyz", readyHandler)

	if err := http.ListenAndServe(":8080", nil); err != nil {
		panic(err)
	}
}
