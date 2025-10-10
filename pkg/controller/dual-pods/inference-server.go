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
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
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
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/yaml"

	"github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/api"
	stubapi "github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/stub/api"
)

func (item infSvrItem) process(ctx context.Context, ctl *controller) (error, bool) {
	logger := klog.FromContext(ctx).WithValues("serverUID", item.UID, "requesterName", item.RequesterName)
	ctx = klog.NewContext(ctx, logger)
	requesterRV := "(non existent)"
	runnerRV := "(non existent)"
	serverDat := ctl.getServerData(item.RequesterName, item.UID)

	requestingPod, err := ctl.podLister.Pods(ctl.namespace).Get(item.RequesterName)
	if err != nil {
		if apierrors.IsNotFound(err) {
			requestingPod = nil
		} else { // BTW, impossible
			logger.Error(err, "Failed to get Pod")
			return err, true
		}
	} else {
		requesterRV = requestingPod.ResourceVersion
	}

	var runningPod *corev1.Pod
	runningPodAnys, err := ctl.podInformer.GetIndexer().ByIndex(requesterIndexName, string(item.UID))
	if err != nil { //impossible
		return err, false
	}
	if len(runningPodAnys) > 0 {
		runningPod = runningPodAnys[0].(*corev1.Pod)
		runnerRV = runningPod.ResourceVersion
		if len(runningPodAnys) > 1 {
			other := runningPodAnys[1].(*corev1.Pod)
			logger.V(2).Info("Found multiple server-running Pods, using one of them", "using", runningPod.Name, "anIgnoredOne", other.Name)
		}
	}

	logger.V(5).Info("Processing inference server", "requesterResourceVersion", requesterRV, "runnerResourceVersion", runnerRV)

	podOps := ctl.coreclient.Pods(ctl.namespace)

	if requestingPod == nil && runningPod == nil {
		ctl.clearServerData(item.UID)
		logger.V(2).Info("End of life of inference server")
		return nil, false
	}

	if runningPod != nil && runningPod.DeletionTimestamp != nil {
		if requestingPod != nil && requestingPod.DeletionTimestamp == nil {
			// Reflect runningPod deletion to requestingPod deletion
			err := podOps.Delete(ctx, requestingPod.Name, metav1.DeleteOptions{
				PropagationPolicy: ptr.To(metav1.DeletePropagationBackground),
				Preconditions:     &metav1.Preconditions{UID: &item.UID, ResourceVersion: &requestingPod.ResourceVersion}})
			if err == nil {
				logger.V(2).Info("Requested deletion of server-requesting Pod because of deletion of server-running Pod")
			} else if apierrors.IsGone(err) || apierrors.IsNotFound(err) {
				logger.V(5).Info("The server-requesting Pod is already gone")
			} else {
				return fmt.Errorf("failed to delete server-requesting Pod: %w", err), false
			}
			serverDat.RequesterDeleteRequested = true
		}
		// Ensure finalizer is absent from server-running Pod so that its deletion can complete
		changed, err := ctl.ensureFinalizerAbsent(ctx, runningPod)
		if err != nil {
			return err, false
		}
		if !changed {
			logger.V(5).Info("Finalizer is absent from server-running Pod, waiting for deletions to finish")
		}
		return nil, false
	}
	// Assert: runningPod == nil || runningPod.DeletionTimestamp == nil

	if (requestingPod == nil || requestingPod.DeletionTimestamp != nil) && runningPod != nil { // time to unbind
		return ctl.ensureUnbound(ctx, serverDat, runningPod)
	}
	// Assert: requestingPod != nil

	if requestingPod.Spec.NodeName == "" {
		return ctl.ensureReqStatus(ctx, requestingPod, "not scheduled yet")
	}
	logger = logger.WithValues("node", requestingPod.Spec.NodeName)

	if requestingPod.DeletionTimestamp != nil || serverDat.RequesterDeleteRequested {
		logger.V(5).Info("Waiting for deletion of server-requesting Pod to finish")
		return nil, false
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
		logger.V(3).Info("Ignoring inference server on absent or departing Node")
		return nil, false
	}

	requesterIP := requestingPod.Status.PodIP
	if requesterIP == "" {
		return ctl.ensureReqStatus(ctx, requestingPod, "no IP assigned yet")
	}

	adminPort := requestingPod.Annotations[api.AdminPortAnnotationName]
	if adminPort == "" {
		adminPort = api.AdminPortDefaultValue
	}

	if runningPod != nil {
		// Relay readiness if not already done
		ready := isPodReady(runningPod)
		if serverDat.ReadinessRelayed == nil || ready != *serverDat.ReadinessRelayed {
			url, readiness := fmt.Sprintf("http://%s:%s", requestingPod.Status.PodIP, adminPort), ""
			if ready {
				logger.V(5).Info("Server-running pod is ready", "name", runningPod.Name)
				url += stubapi.BecomeReadyPath
				readiness = "ready"
			} else {
				logger.V(5).Info("Server-running pod is not ready", "name", runningPod.Name)
				url += stubapi.BecomeUnreadyPath
				readiness = "unready"
			}
			err = postToReadiness(url)
			if err != nil {
				logger.Error(err, "Failed to relay the readiness", "name", runningPod.Name, "readiness", readiness)
				return err, true
			}
			serverDat.ReadinessRelayed = &ready
			logger.V(5).Info("Successfully relayed the readiness", "name", runningPod.Name, "readiness", readiness)
		}
		// TODO: sync desired and actual runningPod wrt labels (spec is mostly immutable, possible mutations are allowed)
		logger.V(5).Info("Nothing more to do")
		return ctl.ensureReqStatus(ctx, requestingPod)
	}
	// Assert: runningPod == nil
	// What remains to be done is create the server-running Pod if possible

	if node.Spec.Unschedulable {
		// Reflect the inability to serve back to the client/user
		logger.V(2).Info("Deleting server-requesting Pod because it is bound to an unschedulable Node and has no server-running Pod")
		err := podOps.Delete(ctx, requestingPod.Name, metav1.DeleteOptions{PropagationPolicy: ptr.To(metav1.DeletePropagationBackground)})
		return err, false
	}

	if serverDat.GPUIndices == nil {
		logger.V(5).Info("Querying accelerators", "ip", requesterIP, "port", adminPort)
		url := fmt.Sprintf("http://%s:%s%s", requesterIP, adminPort, stubapi.AcceleratorQueryPath)
		gpuUUIDs, err := getGPUUUIDs(url)
		if err != nil {
			return ctl.ensureReqStatus(ctx, requestingPod, fmt.Sprintf("GET %q fails: %s", url, err.Error()))
		}
		if len(gpuUUIDs) == 0 {
			return ctl.ensureReqStatus(ctx, requestingPod, "the assigned set of GPUs is empty")
		}
		logger.V(5).Info("Found GPUs", "gpuUUIDs", gpuUUIDs)
		gpuIndices, err := ctl.mapToGPUIndices(requestingPod.Spec.NodeName, gpuUUIDs)
		if err != nil {
			return ctl.ensureReqStatus(ctx, requestingPod, err.Error())
		}
		serverDat.GPUIndices = &gpuIndices
	}

	serverPatch := requestingPod.Annotations[api.ServerPatchAnnotationName]
	if serverPatch == "" { // this is bad, somebody has hacked important data
		return ctl.ensureReqStatus(ctx, requestingPod, "the "+api.ServerPatchAnnotationName+" annotation is missing")
	}
	// use the server patch to build the server-running pod
	desiredRunningPod, err := serverDat.getNominalServerRunningPod(ctx, requestingPod, serverPatch, api.RunnerData{
		NodeName: requestingPod.Spec.NodeName,
	})
	if err != nil {
		return ctl.ensureReqStatus(ctx, requestingPod, fmt.Sprintf("failed to construct the nominal server-running Pod: %s", err.Error()))
	}

	logger.V(3).Info("Creating server-running pod", "labels", desiredRunningPod.Labels)
	echo, err := podOps.Create(ctx, desiredRunningPod, metav1.CreateOptions{})
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
	logger.V(2).Info("Created server-running pod", "name", echo.Name, "annotations", echo.Annotations, "labels", echo.Labels, "resourceVersion", echo.ResourceVersion)

	return ctl.ensureReqStatus(ctx, requestingPod)
}

var invalidPodRE = regexp.MustCompile(`^Pod "[a-z0-9.-]*" is invalid`)

func (ctl *controller) ensureFinalizerAbsent(ctx context.Context, runningPod *corev1.Pod) (bool, error) {
	logger := klog.FromContext(ctx)
	podOps := ctl.coreclient.Pods(ctl.namespace)
	// Ensure finalizer is absent from server-running Pod so that its deletion can complete
	if index := slices.Index(runningPod.Finalizers, runnerFinalizer); index >= 0 {
		runningPod.Finalizers = slices.Delete(runningPod.Finalizers, index, index+1)
		runningPod = runningPod.DeepCopy()
		echo, err := podOps.Update(ctx, runningPod, metav1.UpdateOptions{FieldManager: ctl.ControllerName})
		if err != nil {
			return false, fmt.Errorf("failed to remove finalizer from server-running Pod %s (RV %s): %w", runningPod.Name, runningPod.ResourceVersion, err)
		}
		logger.V(2).Info("Removed finalizer from server-running Pod", "runner", runningPod.Name, "newResourceVersion", echo.ResourceVersion)
		return true, nil // update and/or delete event will trigger more processing
	}
	return false, nil // no change
}

// Unbinds the given server-running Pod.
func (ctl *controller) ensureUnbound(ctx context.Context, serverDat *serverData, runningPod *corev1.Pod) (error, bool) {
	logger := klog.FromContext(ctx)
	// A runningPod with no IP is not scheduled, so we know that it is not awake.
	// If runningPod is stale then the update will fail.
	if (serverDat.Sleeping == nil || !*(serverDat.Sleeping)) && runningPod.Status.PodIP != "" { // need to put to sleep
		serverPort := serverDat.ServerPort
		if serverDat.NominalRunningPod == nil {
			var err error
			_, serverPort, err = getInferenceServerPort(runningPod)
			if err != nil { // Impossible, because such a runningPod would never be created by this controller
				return fmt.Errorf("unable to put server to sleep because port not known: %w", err), false
			}
		}
		sleepURL := fmt.Sprintf("http://%s:%d/sleep", runningPod.Status.PodIP, serverPort)
		resp, err := http.Post(sleepURL, "", nil)
		if err != nil {
			return fmt.Errorf("failed to put to sleep, POST %s got error: %w", sleepURL, err), false
		}
		if sc := resp.StatusCode; sc != http.StatusOK {
			return fmt.Errorf("failed to put to sleep, POST %s returned status %d", sleepURL, sc), false
		}
		serverDat.Sleeping = ptr.To(true)
		logger.V(2).Info("Put inference server to sleep")
	}
	runningPod = runningPod.DeepCopy()
	var changed bool
	// Ensure requester annotation is absent
	if _, have := runningPod.Annotations[requesterAnnotationKey]; have {
		delete(runningPod.Annotations, requesterAnnotationKey)
		changed = true
	}
	// Ensure finalizer is absent
	if index := slices.Index(runningPod.Finalizers, runnerFinalizer); index >= 0 {
		runningPod.Finalizers = slices.Delete(runningPod.Finalizers, index, index+1)
		changed = true
	}
	if changed {
		podOps := ctl.coreclient.Pods(ctl.namespace)
		echo, err := podOps.Update(ctx, runningPod, metav1.UpdateOptions{FieldManager: ControllerName})
		if err != nil {
			return fmt.Errorf("failed to unbind server-running Pod %s: %w", runningPod.Name, err), false
		}
		logger.V(2).Info("Unbound server-running Pod", "name", runningPod.Name, "newResourceVersion", echo.ResourceVersion)
	} else {
		logger.V(3).Info("Server-running Pod remains unbound", "name", runningPod.Name, "resourceVersion", runningPod.ResourceVersion)
	}
	return nil, false
}

func (serverDat *serverData) getNominalServerRunningPod(ctx context.Context, reqPod *corev1.Pod, rawTmpl string, data api.RunnerData) (*corev1.Pod, error) {
	logger := klog.FromContext(ctx)
	if serverDat.NominalRunningPod == nil {
		logger.V(5).Info("Building server-running pod from patch", "patch", rawTmpl)
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
		hasher := sha256.New()
		hasher.Write(modifiedJSON)
		hasher.Write([]byte(fmt.Sprintf(";gpus=%s", *serverDat.GPUIndices)))
		var modifiedHash [sha256.Size]byte
		modifiedHashSl := hasher.Sum(modifiedHash[:0])
		nominalHash := base64.RawStdEncoding.EncodeToString(modifiedHashSl)

		var pod = &corev1.Pod{}
		// Decode back into Pod.
		// Use a real Kubernetes decoder that will complain about spurious fields,
		// to catch common errors here (before sending to apiserver).
		_, _, err = podDecoder.Decode(modifiedJSON, nil, pod)
		if err != nil {
			return nil, fmt.Errorf("failed to unmarshal patched pod: %w", err)
		}

		nodeSelector := pod.Spec.NodeSelector
		if nodeSelector == nil {
			nodeSelector = map[string]string{}
			pod.Spec.NodeSelector = nodeSelector
		}
		nodeSelector["kubernetes.io/hostname"] = reqPod.Spec.NodeName

		cIdx, serverPort, err := getInferenceServerPort(pod)
		if err != nil {
			return nil, err
		}
		serverDat.ServerPort = serverPort
		isCtr := &pod.Spec.Containers[cIdx]

		// ensure the value of CUDA_VISIBLE_DEVICES envar for the inference server container
		eIdx := slices.IndexFunc(isCtr.Env, func(e corev1.EnvVar) bool {
			return e.Name == "CUDA_VISIBLE_DEVICES"
		})
		if eIdx == -1 {
			isCtr.Env = append(isCtr.Env, corev1.EnvVar{
				Name:  "CUDA_VISIBLE_DEVICES",
				Value: *serverDat.GPUIndices,
			})
		} else {
			isCtr.Env[eIdx].Value = *serverDat.GPUIndices
		}

		// set the inference server container's gpu limits and requests to zero to bypass the nvidia device plugin
		if isCtr.Resources.Limits == nil {
			isCtr.Resources.Limits = corev1.ResourceList{}
		}
		isCtr.Resources.Limits[corev1.ResourceName("nvidia.com/gpu")] = resource.Quantity{}
		if isCtr.Resources.Requests == nil {
			isCtr.Resources.Requests = corev1.ResourceList{}
		}
		isCtr.Resources.Requests[corev1.ResourceName("nvidia.com/gpu")] = resource.Quantity{}

		pod.GenerateName = reqPod.Name + "-dual-"
		pod.Finalizers = append(pod.Finalizers, runnerFinalizer)
		if pod.Annotations == nil {
			pod.Annotations = map[string]string{}
		}
		pod.Annotations[nominalHashAnnotationKey] = nominalHash
		pod.Annotations[requesterAnnotationKey] = string(reqPod.UID) + " " + reqPod.Name
		serverDat.NominalRunningPod = pod
		serverDat.NominalRunningPodHash = nominalHash
	}
	return serverDat.NominalRunningPod, nil
}

func getInferenceServerPort(pod *corev1.Pod) (int, int16, error) {
	// identify the inference server container
	cIdx := slices.IndexFunc(pod.Spec.Containers, func(c corev1.Container) bool {
		return c.Name == api.InferenceServerContainerName
	})
	if cIdx == -1 {
		return 0, 0, fmt.Errorf("container %q not found", api.InferenceServerContainerName)
	}
	isCtr := &pod.Spec.Containers[cIdx]
	if isCtr.ReadinessProbe == nil {
		return 0, 0, errors.New("the inference server container has no readinessProbe")
	} else if isCtr.ReadinessProbe.HTTPGet == nil {
		return 0, 0, fmt.Errorf("the readinessProbe is not an HTTPGet")
	}
	portIOS := isCtr.ReadinessProbe.HTTPGet.Port
	switch portIOS.Type {
	case intstr.Int:
		return cIdx, int16(portIOS.IntVal), nil
	case intstr.String:
		if portIOS.StrVal == "http" || portIOS.StrVal == "HTTP" {
			return cIdx, 80, nil
		} else {
			return 0, 0, fmt.Errorf("unsupported readinessProbe port %q", portIOS.StrVal)
		}
	default:
		return 0, 0, fmt.Errorf("the readinessProbe port has unexpected type %q", portIOS.Type)
	}
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
	echo, err := ctl.coreclient.Pods(requestingPod.Namespace).Update(ctx, requestingPod, metav1.UpdateOptions{FieldManager: ctl.ControllerName})
	if err == nil {
		logger.V(2).Info("Set status", "serverRequestingPod", requestingPod.Name, "status", status, "newResourceVersion", echo.ResourceVersion)
	} else {
		logger.V(3).Info("Failed to set status", "serverRequestingPod", requestingPod.Name, "status", status, "resourceVersion", requestingPod.ResourceVersion)
	}
	return err, false
}

func postToReadiness(url string) error {
	client := &http.Client{
		Timeout: 5 * time.Second,
	}

	resp, err := client.Post(url, "application/json", nil)
	if err != nil {
		return fmt.Errorf("http post %q: %w", url, err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

func isPodReady(pod *corev1.Pod) bool {
	for _, cond := range pod.Status.Conditions {
		if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
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

func IterFilter[Elt any](it iter.Seq[Elt], include func(Elt) bool) iter.Seq[Elt] {
	return func(yield func(Elt) bool) {
		for elt := range it {
			if include(elt) {
				if !yield(elt) {
					return
				}
			}
		}
	}
}
