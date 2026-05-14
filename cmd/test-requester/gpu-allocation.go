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
	"math/rand/v2"
	"os"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apitypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"

	dpctlr "github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/controller/dual-pods"

	"k8s.io/klog/v2"
)

const nvidiaVisibleDevicesEnvVar = "NVIDIA_VISIBLE_DEVICES"

// visibleGPUFilter reads NVIDIA_VISIBLE_DEVICES and returns (filter, restricted).
// restricted=false means "no filter" (env unset, empty, or value "all").
// A returned empty set with restricted=true means "no GPUs visible"
// (values "void"/"none"); caller should refuse to allocate.
func visibleGPUFilter() (sets.Set[string], bool) {
	raw, present := os.LookupEnv(nvidiaVisibleDevicesEnvVar)
	if !present {
		return nil, false
	}
	switch strings.TrimSpace(raw) {
	case "", "all":
		return nil, false
	case "void", "none":
		return sets.New[string](), true
	}
	filter := sets.New[string]()
	for uid := range strings.SplitSeq(raw, ",") {
		uid = strings.TrimSpace(uid)
		if uid != "" {
			filter.Insert(uid)
		}
	}
	return filter, true
}

// This code maintains a ConfigMap named "gpu-allocs" that holds the current test allocations
// of GPUs. The data of this ConfigMap is a map from GPU UID to the JSON marshaling of a GPUHolder.

// gpuMap maps node name to nodeGPUMap
type gpuMap map[string]nodeGPUMap

// nodeGPUMap maps GPU UID to index
type nodeGPUMap map[string]int

// GPUHolder identifies a test requester that is currently allocated the use of a GPU
type GPUHolder struct {
	NodeName string
	PodUID   apitypes.UID
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

func allocateGPUs(ctx context.Context, coreClient corev1client.CoreV1Interface, nodeName, namespace string, podUID apitypes.UID, numGPUs uint) []string {
	logger := klog.FromContext(ctx)
	cmClient := coreClient.ConfigMaps(namespace)
	podClient := coreClient.Pods(namespace)
	var gpuUIDs []string
	// try once to allocate the requested number of GPUs;
	// on failure return explanatory error;
	// on success return nil.
	try := func(ctx context.Context) (err error) {
		gpuMap, err := getGPUMap(ctx, cmClient)
		if err != nil {
			return err
		}
		avail := gpuMap.onNode(nodeName)
		if filter, restricted := visibleGPUFilter(); restricted {
			for uid := range filter.Difference(avail) {
				logger.V(5).Info("Ignoring NVIDIA_VISIBLE_DEVICES entry not present on node", "gpuUID", uid, "nodeName", nodeName)
			}
			for uid := range avail.Difference(filter) {
				logger.V(5).Info("Excluding node GPU hidden by NVIDIA_VISIBLE_DEVICES", "gpuUID", uid, "nodeName", nodeName)
			}
			avail = avail.Intersection(filter)
		}
		podMap, err := getPodMap(ctx, podClient)
		if err != nil {
			return err
		}
		if _, has := podMap[podUID]; !has {
			return fmt.Errorf("pod UID %q not found among current Pods", podUID)
		}
		// Get the current allocations, as a data structure and as a ConfigMap object.
		gpuAllocMap, gpuAllocCM, err := getGPUAlloc(ctx, cmClient)
		if err != nil {
			return err
		}
		logger.V(5).Info("Read GPU allocations", "gpuAllocMap", gpuAllocMap)
		// Collect the ones used by other Pods on the same Node,
		// and remove obsolete entries from the ConfigMap.
		used := sets.New[string]()
		for gpuUID, holder := range gpuAllocMap {
			if holder.NodeName != nodeName {
				continue
			}
			if holderName, held := podMap[holder.PodUID]; !held {
				logger.V(5).Info("Removing entry for non-existent Pod", "gpuUID", gpuUID, "holderUID", holder.PodUID)
				delete(gpuAllocCM.Data, gpuUID)
			} else if holder.PodUID != podUID {
				logger.V(5).Info("Noting usage", "gpuUID", gpuUID, "holderUID", holder.PodUID, "holderName", holderName)
				used.Insert(gpuUID)
			} else {
				logger.V(5).Info("Noting availability", "gpuUID", gpuUID)
			}
		}
		// Compute the list of unused GPUs on the right Node, then shuffle it so
		// that selection is non-deterministic --- closer to what the real Kubernetes
		// scheduler + NVIDIA device plugin would do. When NVIDIA_VISIBLE_DEVICES is
		// set, `avail` has already been narrowed to that subset above.
		rem := sets.List(avail.Difference(used))
		if uint(len(rem)) < numGPUs {
			return fmt.Errorf("fewer than %d GPUs available (%v) for node %q", numGPUs, rem, nodeName)
		}
		rand.Shuffle(len(rem), func(i, j int) { rem[i], rem[j] = rem[j], rem[i] })
		gpuUIDs = rem[:numGPUs]
		for _, gpuUID := range gpuUIDs {
			holder := GPUHolder{NodeName: nodeName, PodUID: podUID}
			holderBytes, err := json.Marshal(holder)
			if err != nil {
				return fmt.Errorf("failed to marshal holder for GPU %s (%#v): %w", gpuUID, holder, err)
			}
			gpuAllocCM.Data[gpuUID] = string(holderBytes)
		}
		echo, err := cmClient.Update(ctx, gpuAllocCM, metav1.UpdateOptions{
			FieldManager: agentName,
		})
		if err != nil {
			return fmt.Errorf("failed to update GPU allocation ConfigMap: %w", err)
		}
		logger.Info("Successful allocation", "nodeName", nodeName, "podUID", podUID, "gpus", gpuUIDs, "newResourceVersion", echo.ResourceVersion)
		return nil
	}
	err := wait.PollUntilContextCancel(ctx, time.Second, true, func(ctx context.Context) (bool, error) {
		err := try(ctx)
		if err != nil {
			logger.Error(err, "Failed to allocate")
		}
		return err == nil, nil
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to allocate GPUS: %s\n", err.Error())
		os.Exit(100)
	}
	return gpuUIDs
}

// Returns map from Pod UID to name
func getPodMap(ctx context.Context, podClient corev1client.PodInterface) (map[apitypes.UID]string, error) {
	podList, err := podClient.List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	ans := make(map[apitypes.UID]string, len(podList.Items))
	for _, pod := range podList.Items {
		ans[pod.UID] = pod.Name
	}
	return ans, nil
}
