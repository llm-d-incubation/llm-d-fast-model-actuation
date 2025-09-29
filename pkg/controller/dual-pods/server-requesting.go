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

package dualpods

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"text/template"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	"k8s.io/klog/v2"
	"sigs.k8s.io/yaml"

	"github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/api"
	stubapi "github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/stub/api"
)

func (ctl *controller) processServerRequestingPod(ctx context.Context, requestingPod *corev1.Pod) (error, bool) {
	logger := klog.FromContext(ctx)
	logger.V(5).Info("Processing server-requesting pod", "name", requestingPod.Name)

	serverPatch := requestingPod.Annotations[api.ServerPatchAnnotationName]
	if serverPatch == "" {
		logger.V(5).Info("No server patch annotation found", "name", requestingPod.Name)
		return nil, true
	}
	logger.V(5).Info("Found server patch annotation", "name", requestingPod.Name, "patch", serverPatch)

	// get allocated gpu
	ip := requestingPod.Status.PodIP
	if ip == "" {
		return fmt.Errorf("pod %q has no PodIP yet", requestingPod.Name), true
	}
	port := requestingPod.Annotations[api.AdminPortAnnotationName]
	if port == "" {
		port = api.AdminPortDefaultValue
	}
	logger.V(5).Info("Querying accelerators", "ip", ip, "port", port)
	url := fmt.Sprintf("http://%s:%s%s", ip, port, stubapi.AcceleratorQueryPath)
	gpuUUIDs, err := getGPUUUIDs(url)
	if err != nil {
		logger.V(5).Info("Not able to get GPU UUIDs at this time", "url", url, "error", err)
		return err, true
	}
	if len(gpuUUIDs) == 0 {
		logger.V(5).Info("No GPUs found for Pod", "name", requestingPod.Name)
		return nil, true
	}
	logger.V(5).Info("Found GPUs for Pod", "name", requestingPod.Name, "gpuUUIDs", gpuUUIDs)
	gpuIndices, err := ctl.mapToGPUIndices(requestingPod.Spec.NodeName, gpuUUIDs)
	if err != nil {
		return err, true
	}

	// use the server patch to build the server-running pod
	logger.V(5).Info("Building server-running pod from patch", "name", requestingPod.Name, "patch", serverPatch)
	serverRunningPod, err := composeServerRunningPod(requestingPod, gpuIndices, api.RunnerData{
		NodeName: requestingPod.Spec.NodeName,
	})
	if err != nil {
		logger.Error(err, "Failed to build server-running pod from patch", "name", requestingPod.Name, "patch", serverPatch)
		return err, true
	}

	got, err := ctl.podLister.Pods(serverRunningPod.Namespace).Get(serverRunningPod.Name)
	if err != nil && !apierrors.IsNotFound(err) {
		logger.Error(err, "Failed to get existing server-running pod", "name", serverRunningPod.Name)
		return err, true
	}
	if got != nil {
		logger.V(5).Info("Server-running pod exists", "name", serverRunningPod.Name)

		// TODO: we should reconcile the existing server-running pod with the one we just built
		return nil, false
	}

	logger.V(2).Info("Creating server-running pod", "name", serverRunningPod.Name, "namespace", serverRunningPod.Namespace)
	_, err = ctl.coreclient.Pods(serverRunningPod.Namespace).Create(ctx, serverRunningPod, metav1.CreateOptions{})
	if err != nil {
		logger.Error(err, "Failed to create server-running pod", "name", serverRunningPod.Name)
		return err, true
	}
	logger.V(2).Info("Created server-running pod", "name", serverRunningPod.Name)

	logger.V(5).Info("Processed server-requesting pod", "name", requestingPod.Name)
	return nil, false
}

func composeServerRunningPod(reqPod *corev1.Pod, gpuIndices string, data api.RunnerData) (*corev1.Pod, error) {
	rawTmpl, ok := reqPod.Annotations[api.ServerPatchAnnotationName]
	if !ok {
		return nil, fmt.Errorf("annotation %q not found", api.ServerPatchAnnotationName)
	}

	tmpl, err := template.New("serverPatch").Option("missingkey=error").Parse(rawTmpl)
	if err != nil {
		return nil, fmt.Errorf("parse template: %w", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("execute template: %w", err)
	}
	renderedPatch := buf.Bytes()

	patchJSON, err := yaml.YAMLToJSON(renderedPatch)
	if err != nil {
		return nil, fmt.Errorf("yaml to json: %w", err)
	}

	// marshal into json
	origJSON, err := json.Marshal(reqPod)
	if err != nil {
		return nil, fmt.Errorf("marshal server-requesting pod: %w", err)
	}

	// apply strategic merge patch
	modifiedJSON, err := strategicpatch.StrategicMergePatch(origJSON, patchJSON, &corev1.Pod{})
	if err != nil {
		return nil, fmt.Errorf("apply patch: %w", err)
	}

	// decode back into Pod
	var pod corev1.Pod
	if err := json.Unmarshal(modifiedJSON, &pod); err != nil {
		return nil, fmt.Errorf("unmarshal patched pod: %w", err)
	}

	// ensure the correct role annotation
	if pod.Annotations == nil {
		pod.Annotations = map[string]string{}
	}
	pod.Annotations[api.PodRoleAnnotationName] = api.PodRoleAnnotationValueRunning

	// identify the inference server container
	cIdx := slices.IndexFunc(pod.Spec.Containers, func(c corev1.Container) bool {
		return c.Name == api.InferenceServerContainerName
	})
	if cIdx == -1 {
		return nil, fmt.Errorf("container %q not found", api.InferenceServerContainerName)
	}

	// ensure the value of CUDA_VISIBLE_DEVICES envar for the inference server container
	eIdx := slices.IndexFunc(pod.Spec.Containers[cIdx].Env, func(e corev1.EnvVar) bool {
		return e.Name == "CUDA_VISIBLE_DEVICES"
	})
	if eIdx == -1 {
		pod.Spec.Containers[cIdx].Env = append(pod.Spec.Containers[cIdx].Env, corev1.EnvVar{
			Name:  "CUDA_VISIBLE_DEVICES",
			Value: gpuIndices,
		})
	} else {
		pod.Spec.Containers[cIdx].Env[eIdx].Value = gpuIndices
	}

	// set the inference server container's gpu limits and requests to zero to bypass the nvidia device plugin
	if pod.Spec.Containers[cIdx].Resources.Limits == nil {
		pod.Spec.Containers[cIdx].Resources.Limits = corev1.ResourceList{}
	}
	pod.Spec.Containers[cIdx].Resources.Limits[corev1.ResourceName("nvidia.com/gpu")] = resource.Quantity{}
	if pod.Spec.Containers[cIdx].Resources.Requests == nil {
		pod.Spec.Containers[cIdx].Resources.Requests = corev1.ResourceList{}
	}
	pod.Spec.Containers[cIdx].Resources.Requests[corev1.ResourceName("nvidia.com/gpu")] = resource.Quantity{}

	// connect dual pods
	pod.Name = reqPod.Name + api.ServerRunningPodNameSuffix
	pod.OwnerReferences = []metav1.OwnerReference{
		*metav1.NewControllerRef(reqPod, corev1.SchemeGroupVersion.WithKind("Pod")),
	}

	// clean up
	delete(pod.Annotations, api.AdminPortAnnotationName)
	delete(pod.Annotations, api.ServerPatchAnnotationName)
	pod.ResourceVersion = ""
	pod.UID = ""
	pod.CreationTimestamp = metav1.Time{}

	return &pod, nil
}

func getGPUUUIDs(url string) ([]string, error) {
	client := &http.Client{
		Timeout: 5 * time.Second,
	}

	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("http get %q: %w", url, err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	var uuids []string
	if err := json.Unmarshal(body, &uuids); err != nil {
		return nil, fmt.Errorf("unmarshal uuids: %w", err)
	}

	return uuids, nil
}

// findGPUIndices maps GPU UUIDs to GPU indices.
// This is a stub implementation that just returns "0".
// The real implementation is planned to be done in a component other than the controller.
// This func will be moved into that component once that component exists.
func (ctl *controller) mapToGPUIndices(nodeName string, gpuUUIDs []string) (string, error) {
	gpuMap := *ctl.gpuMap.Load()
	var otherErrors []string
	indices, unknowns := SliceMap(gpuUUIDs, func(uuid string) (string, bool) {
		loc, have := gpuMap[uuid]
		if !have {
			return "", false
		} else if loc.Node != nodeName {
			otherErrors = append(otherErrors, fmt.Sprintf("UUID %s is on Node %s, not %s", uuid, loc.Node, nodeName))
			return "", true
		} else {
			return strconv.FormatUint(uint64(loc.Index), 10), true
		}
	})
	indicesStr := strings.Join(indices, ",")
	if len(unknowns) > 0 {
		return indicesStr, fmt.Errorf("some GPU UUID(s) are not known: %v", unknowns)
	}
	if len(otherErrors) > 0 {
		return indicesStr, errors.New(strings.Join(otherErrors, ", "))
	}
	return indicesStr, nil
}

func SliceMap[Domain, Range any](slice []Domain, mapFn func(Domain) (Range, bool)) ([]Range, []Domain) {
	var mapped []Range
	var failed []Domain
	for _, dom := range slice {
		rng, have := mapFn(dom)
		if have {
			mapped = append(mapped, rng)
		} else {
			failed = append(failed, dom)
		}
	}
	return mapped, failed
}
