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
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"os"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"

	dpctlr "github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/controller/dual-pods"

	"k8s.io/klog/v2"
)

// This code maintains a ConfigMap named "gpu-allocs" that holds the current test allocations
// of GPUs. The data of this ConfigMap is a map from GPU UID to the JSON marshaling of a GPUHOlder.

// gpuMap maps node name to nodeGPUMap
type gpuMap map[string]nodeGPUMap

// nodeGPUMap maps GPU UID to index
type nodeGPUMap map[string]int

// GPUHolder identifis a test requester that is currently allocated the use of a GPU
type GPUHolder struct {
	NodeName string
	PodID    string
}

// GPUAllocMap maps GPU UID to GPUHolder
type GPUAllocMap map[string]GPUHolder

func getGPUMap(ctx context.Context, cmClient corev1client.ConfigMapInterface) (gpuMap, error) {
	cm, err := cmClient.Get(ctx, dpctlr.GPUMapName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve gpu-map ConfigMap: %w", err)
	}
	ans := gpuMap{}
	for nodeName, mapStr := range cm.Data {
		nm := nodeGPUMap{}
		err := json.Unmarshal([]byte(mapStr), &nm)
		if err != nil {
			return nil, fmt.Errorf("failed to parse GPU map for node %s: %w", nodeName, err)
		}
		ans[nodeName] = nm
	}
	return ans, nil
}

func (gm gpuMap) onNode(nodeName string) sets.Set[string] {
	ngm := gm[nodeName]
	if ngm != nil {
		return sets.KeySet(ngm)
	}
	return sets.New[string]()
}

func getGPUAlloc(ctx context.Context, cmClient corev1client.ConfigMapInterface) (GPUAllocMap, *corev1.ConfigMap, error) {
	cm, err := cmClient.Get(ctx, allocMapName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			// It up to us to create it
			cmProto := corev1.ConfigMap{
				TypeMeta: metav1.TypeMeta{
					Kind:       "ConfigMap",
					APIVersion: corev1.SchemeGroupVersion.String(),
				},
				ObjectMeta: metav1.ObjectMeta{
					Name: allocMapName,
				},
			}
			cm, err = cmClient.Create(ctx, &cmProto, metav1.CreateOptions{FieldManager: agentName})
			if err != nil {
				return nil, nil, fmt.Errorf("failed to create GPU allocation ConfigMap: %w", err)
			}
		} else {
			return nil, nil, fmt.Errorf("failed to fetch GPU allocation ConfigMap: %w", err)
		}
	}
	ans := GPUAllocMap{}
	for gpuUID, holderStr := range cm.Data {
		holderReader := strings.NewReader(holderStr)
		var holder GPUHolder
		decoder := json.NewDecoder(holderReader)
		decoder.DisallowUnknownFields()
		err = decoder.Decode(&holder)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to decode GPU allocation for GPU UID %s: %w", gpuUID, err)
		}
		ans[gpuUID] = holder
	}
	cm = cm.DeepCopy()
	if cm.Data == nil {
		cm.Data = map[string]string{}
	}
	return ans, cm, nil
}

func allocateGPUs(ctx context.Context, cmClient corev1client.ConfigMapInterface, nodeName, podID string, numGPUs uint) []string {
	logger := klog.FromContext(ctx)
	var gpuUIDs []string
	try := func(ctx context.Context) (done bool, err error) {
		gpuMap, err := getGPUMap(ctx, cmClient)
		if err != nil {
			return false, err
		}
		avail := gpuMap.onNode(nodeName)
		gpuAllocMap, gpuAllocCM, err := getGPUAlloc(ctx, cmClient)
		if err != nil {
			return false, err
		}
		used := sets.New[string]()
		for gpuUID, holder := range gpuAllocMap {
			if holder.NodeName == nodeName && holder.PodID != podID {
				used.Insert(gpuUID)
			}
		}
		rem := sets.List(avail.Difference(used))
		if uint(len(rem)) < numGPUs {
			return false, fmt.Errorf("fewer than %d GPUs available (%v) for node %q", numGPUs, rem, nodeName)
		}
		gpuUIDs = rem[:numGPUs]
		for _, gpuUID := range gpuUIDs {
			holder := GPUHolder{NodeName: nodeName, PodID: podID}
			holderBytes, err := json.Marshal(holder)
			if err != nil {
				return false, fmt.Errorf("failed to marshal holder for GPU %s (%#v): %w", gpuUID, holder, err)
			}
			gpuAllocCM.Data[gpuUID] = string(holderBytes)
		}
		echo, err := cmClient.Update(ctx, gpuAllocCM, metav1.UpdateOptions{
			FieldManager: agentName,
		})
		if err != nil {
			return false, fmt.Errorf("failed to update GPU allocation ConfigMap: %w", err)
		}
		logger.Info("Successful allocation", "gpus", gpuUIDs, "newResourceVersion", echo.ResourceVersion)
		return true, nil
	}
	err := wait.PollUntilContextCancel(ctx, time.Second, true, func(ctx context.Context) (bool, error) {
		done, err := try(ctx)
		if err != nil {
			logger.Error(err, "Failed to allocate")
		}
		return done, err
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to allocate GPUS: %s\n", err.Error())
		os.Exit(100)
	}
	return gpuUIDs
}

func MapFilter2to1[Key comparable, Val, Result any](input map[Key]Val, filter func(Key, Val) (Result, bool)) iter.Seq[Result] {
	return func(yield func(Result) bool) {
		for key, val := range input {
			result, include := filter(key, val)
			if include && !yield(result) {
				return
			}
		}
	}
}
