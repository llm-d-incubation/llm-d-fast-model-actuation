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
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"text/template"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	k8sserializer "k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/yaml"

	"github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/api"
	stubapi "github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/stub/api"
)

func (ctl *controller) processServerRequestingPod(ctx context.Context, requestingPod *corev1.Pod, serverPatch string) (error, bool) {
	logger := klog.FromContext(ctx)
	reqDat := ctl.getRequesterData(requestingPod.Name, requestingPod.UID, true)
	logger.V(5).Info("Processing server-requesting pod", "name", requestingPod.Name, "reqDat", reqDat)

	if requestingPod.DeletionTimestamp != nil {
		logger.V(5).Info("Nothing to do because server-requesting Pod is being deleted and that will cascade to th server-running Pod", "name", requestingPod.Name)
		return nil, false
	}

	// get allocated gpu
	ip := requestingPod.Status.PodIP
	if ip == "" {
		return ctl.ensureReqStatus(ctx, requestingPod, "no IP assigned yet")
	}
	// Getting an IP normally comes after scheduling, but check just to be sure.
	if requestingPod.Spec.NodeName == "" {
		return ctl.ensureReqStatus(ctx, requestingPod, "not scheduled yet")
	}
	node, err := ctl.nodeLister.Get(requestingPod.Spec.NodeName)
	if err != nil {
		if apierrors.IsNotFound(err) {
			node = nil
		} else { // BTW, impossible
			return err, true
		}
	}

	if node == nil || node.DeletionTimestamp != nil {
		// Node is gone or going away, do nothing to maintain server-running Pod.
		logger.V(3).Info("Ignoring server-requesting Pod on absent or departing Node", "node", requestingPod.Spec.NodeName)
		return nil, false
	}

	port := requestingPod.Annotations[api.AdminPortAnnotationName]
	if port == "" {
		port = api.AdminPortDefaultValue
	}
	if reqDat.GPUIndices == nil {
		logger.V(5).Info("Querying accelerators", "ip", ip, "port", port)
		url := fmt.Sprintf("http://%s:%s%s", ip, port, stubapi.AcceleratorQueryPath)
		gpuUUIDs, err := getGPUUUIDs(url)
		if err != nil {
			return ctl.ensureReqStatus(ctx, requestingPod, fmt.Sprintf("GET %q fails: %s", url, err.Error()))
		}
		if len(gpuUUIDs) == 0 {
			return ctl.ensureReqStatus(ctx, requestingPod, "the assigned set of GPUs is empty")
		}
		logger.V(5).Info("Found GPUs for Pod", "name", requestingPod.Name, "gpuUUIDs", gpuUUIDs)
		gpuIndices, err := ctl.mapToGPUIndices(requestingPod.Spec.NodeName, gpuUUIDs)
		if err != nil {
			return ctl.ensureReqStatus(ctx, requestingPod, err.Error())
		}
		reqDat.GPUIndices = &gpuIndices
	}

	// use the server patch to build the server-running pod
	logger.V(5).Info("Building server-running pod from patch", "name", requestingPod.Name, "patch", serverPatch)
	serverRunningPod, err := composeServerRunningPod(ctx, requestingPod, serverPatch, *reqDat.GPUIndices, api.RunnerData{
		NodeName: requestingPod.Spec.NodeName,
	})
	if err != nil {
		return ctl.ensureReqStatus(ctx, requestingPod, fmt.Sprintf("failed to construct the nominal server-running Pod: %s", err.Error()))
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

	if node.Spec.Unschedulable {
		// Reflect the inability to serve back to the client/user
		logger.V(2).Info("Deleting server-requesting Pod because it is bound to an unschedulable Node and has no server-running Pod", "pod", requestingPod.Name, "node", requestingPod.Spec.NodeName)
		err := ctl.coreclient.Pods(requestingPod.Namespace).Delete(ctx, requestingPod.Name, metav1.DeleteOptions{})
		return err, false
	}

	logger.V(2).Info("Creating server-running pod", "name", serverRunningPod.Name, "namespace", serverRunningPod.Namespace, "annotations", serverRunningPod.Annotations, "labels", serverRunningPod.Labels)
	echo, err := ctl.coreclient.Pods(serverRunningPod.Namespace).Create(ctx, serverRunningPod, metav1.CreateOptions{})
	if err != nil {
		errMsg := err.Error()
		if invalidPodRE.MatchString(errMsg) {
			return ctl.ensureReqStatus(ctx, requestingPod, "the nominal server-running "+errMsg)
		}
		innerErr, _ := ctl.ensureReqStatus(ctx, requestingPod, fmt.Sprintf("failed to create server-running Pod: %s", errMsg))
		if innerErr != nil {
			return errors.Join(err, innerErr), true
		}
		return err, true
	}
	logger.V(5).Info("Created server-running pod", "name", serverRunningPod.Name, "annotations", echo.Annotations, "labels", echo.Labels)

	return ctl.ensureReqStatus(ctx, requestingPod)
}

var invalidPodRE = regexp.MustCompile(`^Pod "[a-z0-9.-]*" is invalid`)

func composeServerRunningPod(ctx context.Context, reqPod *corev1.Pod, rawTmpl string, gpuIndices string, data api.RunnerData) (*corev1.Pod, error) {
	logger := klog.FromContext(ctx)

	tmpl, err := template.New("serverPatch").Option("missingkey=error").Parse(rawTmpl)
	if err != nil {
		return nil, fmt.Errorf("parse template: %w", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("failed to execute server patch template: %w", err)
	}
	renderedPatch := buf.Bytes()

	patchJSON, err := yaml.YAMLToJSON(renderedPatch)
	if err != nil {
		return nil, fmt.Errorf("failed to convert server patch yaml to json: %w", err)
	}

	basePod := &corev1.Pod{
		TypeMeta: reqPod.TypeMeta,
		ObjectMeta: metav1.ObjectMeta{
			Labels:    reqPod.Labels,
			Namespace: reqPod.Namespace,
		},
		Spec: reqPod.Spec,
	}
	// marshal into json
	baseJSON, err := json.Marshal(basePod)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal server-requesting pod: %w", err)
	}
	logger.V(5).Info("Before StrategicMergePatch", "reqPodName", reqPod.Name, "baseJSON", baseJSON)
	// apply strategic merge patch
	modifiedJSON, err := strategicpatch.StrategicMergePatch(baseJSON, patchJSON, &corev1.Pod{})
	if err != nil {
		return nil, fmt.Errorf("failed to apply server patch: %w", err)
	}

	// Decode back into Pod.
	// Use a real Kubernetes decoder that will complain about spurious fields,
	// to catch common errors here (before sending to apiserver).
	var pod corev1.Pod
	_, _, err = podDecoder.Decode(modifiedJSON, nil, &pod)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal patched pod: %w", err)
	}

	nodeSelector := pod.Spec.NodeSelector
	if nodeSelector == nil {
		nodeSelector = map[string]string{}
		pod.Spec.NodeSelector = nodeSelector
	}
	nodeSelector["kubernetes.io/hostname"] = reqPod.Spec.NodeName

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
	ownerRef := *metav1.NewControllerRef(reqPod, corev1.SchemeGroupVersion.WithKind("Pod"))
	ownerRef.BlockOwnerDeletion = ptr.To(false)
	pod.OwnerReferences = []metav1.OwnerReference{ownerRef}

	return &pod, nil
}

func (ctl *controller) ensureReqStatus(ctx context.Context, requestingPod *corev1.Pod, errors ...string) (error, bool) {
	status := api.ServerRequestingPodStatus{Errors: errors}
	logger := klog.FromContext(ctx)
	newStatusBytes, err := json.Marshal(status)
	if err != nil { // impossible; handle by infinite retry
		return fmt.Errorf("failed to marshal status (%#v): %w", status, err), true
	}
	newStatusStr := string(newStatusBytes)
	oldStatusStr := requestingPod.Annotations[api.ServerPatchAnnotationErrorsName]
	if oldStatusStr == string(newStatusStr) {
		logger.V(5).Info("No need to update status", "serverRequestingPod", requestingPod.Name, "status", status)
		return nil, false
	}
	requestingPod = requestingPod.DeepCopy()
	if requestingPod.Annotations == nil {
		requestingPod.Annotations = map[string]string{}
	}
	requestingPod.Annotations[api.ServerPatchAnnotationErrorsName] = newStatusStr
	_, err = ctl.coreclient.Pods(requestingPod.Namespace).Update(ctx, requestingPod, metav1.UpdateOptions{FieldManager: ctl.ControllerName})
	if err == nil {
		logger.V(2).Info("Set status", "serverRequestingPod", requestingPod.Name, "status", status)
	} else {
		logger.V(3).Info("Failed to set status", "serverRequestingPod", requestingPod.Name, "status", status)
	}
	return err, false
}

var coreScheme *k8sruntime.Scheme
var codecFactory k8sserializer.CodecFactory
var podDecoder k8sruntime.Decoder

func init() {
	coreScheme = k8sruntime.NewScheme()
	err := corev1.AddToScheme(coreScheme)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Failed to corev1.AddToScheme: "+err.Error())
	}
	codecFactory = k8sserializer.NewCodecFactory(coreScheme, k8sserializer.EnableStrict)
	podDecoder = codecFactory.UniversalDecoder(corev1.SchemeGroupVersion)
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
	indices, errs := SliceMap(gpuUUIDs, func(uuid string) (string, error) {
		loc, have := gpuMap[uuid]
		if !have {
			return "", fmt.Errorf("UUID %s is not known", uuid)
		} else if loc.Node != nodeName {
			return "", fmt.Errorf("UUID %s is on Node %s, not %s", uuid, loc.Node, nodeName)
		} else {
			return strconv.FormatUint(uint64(loc.Index), 10), nil
		}
	})
	return strings.Join(indices, ","), errors.Join(errs...)
}

func SliceMap[Domain, Range any](slice []Domain, mapFn func(Domain) (Range, error)) ([]Range, []error) {
	var mapped []Range
	var errors []error
	for _, dom := range slice {
		rng, err := mapFn(dom)
		if err == nil {
			mapped = append(mapped, rng)
		} else {
			errors = append(errors, err)
		}
	}
	return mapped, errors
}
