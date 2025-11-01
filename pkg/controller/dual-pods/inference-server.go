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
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/yaml"

	"github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/api"
	stubapi "github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/stub/api"
)

type nodeItem struct {
	NodeName string
}

func (ni nodeItem) process(ctx context.Context, ctl *controller) (error, bool) {
	logger := klog.FromContext(ctx).WithValues("node", ni.NodeName)
	ctx = klog.NewContext(ctx, logger)
	nodeDat := ctl.getNodeData(ni.NodeName)
	items := nodeDat.yankItems()
	var retries int
	logger.V(4).Info("Processing items for node", "count", len(items))
	for localItem := range items {
		logger.V(4).Info("Processing node-local item", "item", localItem)
		err, retry := localItem.process(ctx, ctl, nodeDat)
		if err != nil {
			logger.Error(err, "Processing node local item failed", "item", localItem, "willRetry", retry)
		} else {
			logger.V(4).Info("Finished processing node-local item", "item", localItem, "willRetry", retry)
		}
		if retry {
			nodeDat.add(localItem)
			retries++
		}
	}
	logger.V(4).Info("Done processing items for node", "numToRetry", retries)
	return nil, retries > 0
}

func (item infSvrItem) process(urCtx context.Context, ctl *controller, nodeDat *nodeData) (error, bool) {
	logger := klog.FromContext(urCtx).WithValues("serverUID", item.UID, "requesterName", item.RequesterName)
	ctx := klog.NewContext(urCtx, logger)
	requesterRV := "(non existent)"
	runnerRV := "(non existent)"
	serverDat := ctl.getServerData(nodeDat, item.RequesterName, item.UID)

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
	switch len(runningPodAnys) {
	case 0:
	case 1:
		runningPod = runningPodAnys[0].(*corev1.Pod)
		runnerRV = runningPod.ResourceVersion
		logger = logger.WithValues("runnerName", runningPod.Name)
		ctx = klog.NewContext(urCtx, logger)
		serverDat.RunningPodName = runningPod.Name
	default:
		runnerNames, _ := SliceMap(runningPodAnys, func(podAny any) (string, error) {
			pod := podAny.(*corev1.Pod)
			return pod.Name, nil
		})
		return fmt.Errorf("found multiple bound server-runninng Pods: %v", runnerNames), false
	}

	logger.V(5).Info("Processing inference server", "requesterResourceVersion", requesterRV, "runnerResourceVersion", runnerRV)

	podOps := ctl.coreclient.Pods(ctl.namespace)

	// Delete the in-memory data after both Pods are gone.
	if requestingPod == nil && runningPod == nil {
		ctl.clearServerData(nodeDat, item.UID)
		logger.V(2).Info("End of life of inference server")
		return nil, false
	}

	// Decide what to do about the finalizer on the server-requesting Pod,
	// and do it if that is a removal.
	var shouldAddRequesterFinalizer bool
	if requestingPod != nil {
		removed, shouldAdd, err, retry := ctl.maybeRemoveRequesterFinalizer(ctx, requestingPod, runningPod)
		if removed || err != nil {
			return err, retry
		}
		shouldAddRequesterFinalizer = shouldAdd
	}

	// Handle the deletion of a server-running Pod
	if runningPod != nil && runningPod.DeletionTimestamp != nil {
		if requestingPod != nil && requestingPod.DeletionTimestamp == nil {
			// Reflect runningPod deletion to requestingPod deletion.
			gonerRV := requesterRV
			if shouldAddRequesterFinalizer { // don't let delete complete too quickly
				gonerRV, err = ctl.addRequesterFinalizer(ctx, requestingPod, runningPod.Name)
				if err != nil {
					return err, true
				}
			}
			err := podOps.Delete(ctx, requestingPod.Name, metav1.DeleteOptions{
				PropagationPolicy: ptr.To(metav1.DeletePropagationBackground),
				Preconditions:     &metav1.Preconditions{UID: &item.UID, ResourceVersion: &gonerRV}})
			if err == nil {
				logger.V(2).Info("Requested deletion of server-requesting Pod because of deletion of server-running Pod")
			} else if apierrors.IsGone(err) || apierrors.IsNotFound(err) {
				logger.V(5).Info("The server-requesting Pod is already gone")
			} else {
				return fmt.Errorf("failed to delete server-requesting Pod: %w", err), true
			}
			serverDat.RequesterDeleteRequested = true
		}
		// Ensure finalizer is absent from server-running Pod so that its deletion can complete
		changed, err := ctl.removeRunnerFinalizer(ctx, runningPod)
		if err != nil {
			return err, true
		}
		if !changed {
			logger.V(5).Info("Finalizer is absent from server-running Pod, waiting for deletions to finish")
		}
		return nil, false
	}
	// Assert: runningPod == nil || runningPod.DeletionTimestamp == nil

	// If the server-requesting Pod is absent or being deleted,
	// ensure that the server-running Pod is not bound.
	if (requestingPod == nil || requestingPod.DeletionTimestamp != nil) && runningPod != nil {
		// Time to unbind.
		// As a special favor, delete runningPod if it is in trouble.
		if podIsInTrouble(runningPod) {
			err := podOps.Delete(ctx, runningPod.Name, metav1.DeleteOptions{
				Preconditions:     &metav1.Preconditions{UID: &runningPod.UID, ResourceVersion: &runningPod.ResourceVersion},
				PropagationPolicy: ptr.To(metav1.DeletePropagationBackground),
			})
			if err == nil {
				stJSON, marshalErr := json.Marshal(runningPod.Status)
				logger.V(2).Info("Deleted server-running Pod because it is in trouble", "runnerName", runningPod.Name, "status", string(stJSON), "marshalErr", marshalErr)
				return nil, false
			} else if apierrors.IsNotFound(err) || apierrors.IsGone(err) {
				logger.V(5).Info("Troubled server-running Pod was concurrently deleted", "runnerName", runningPod.Name)
			} else {
				logger.V(2).Info("Failed to delete troubled server-running Pod", "runnerName", runningPod.Name)
			}
		}
		err := ctl.ensureUnbound(ctx, serverDat, runningPod)
		if err != nil {
			return err, true
		}
		if requestingPod != nil {
			return ctl.ensureReqState(ctx, requestingPod, serverDat, false, true)
		}
		return nil, false
	}
	// Assert: requestingPod != nil

	if requestingPod.Spec.NodeName == "" { // impossible now
		return ctl.ensureReqStatus(ctx, requestingPod, serverDat, "not scheduled yet")
	}

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
		return ctl.ensureReqState(ctx, requestingPod, serverDat, shouldAddRequesterFinalizer, false, "no IP assigned yet")
	}

	adminPort := requestingPod.Annotations[api.AdminPortAnnotationName]
	if adminPort == "" {
		adminPort = api.AdminPortDefaultValue
	}

	// Fetch the assigned GPUs if that has not already been done.
	if serverDat.GPUIndicesStr == nil {
		logger.V(5).Info("Querying accelerators", "ip", requesterIP, "port", adminPort)
		url := fmt.Sprintf("http://%s:%s%s", requesterIP, adminPort, stubapi.AcceleratorQueryPath)
		gpuUUIDs, err := getGPUUUIDs(url)
		if err != nil {
			queryErr := fmt.Errorf("GET %q fails: %s", url, err.Error())
			updateErr, _ := ctl.ensureReqStatus(ctx, requestingPod, serverDat, queryErr.Error())
			if updateErr == nil {
				return queryErr, true
			}
			return errors.Join(queryErr, updateErr), true
		}
		if len(gpuUUIDs) == 0 {
			return ctl.ensureReqStatus(ctx, requestingPod, serverDat, "the assigned set of GPUs is empty")
		}
		logger.V(5).Info("Found GPUs", "gpuUUIDs", gpuUUIDs)
		gpuIndices, err := ctl.mapToGPUIndices(requestingPod.Spec.NodeName, gpuUUIDs)
		if err != nil {
			return ctl.ensureReqStatus(ctx, requestingPod, serverDat, err.Error())
		}
		gpuIDsStr := strings.Join(gpuUUIDs, ",")
		gpuIndicesStr := strings.Join(gpuIndices, ",")
		serverDat.GPUIDs = gpuUUIDs
		serverDat.GPUIDsStr = &gpuIDsStr
		serverDat.GPUIndices = gpuIndices
		serverDat.GPUIndicesStr = &gpuIndicesStr
	}

	// If there is already a server-running Pod then ensure that it is awake,
	// ensure status reported, and relay readiness if needed.
	if runningPod != nil {
		_, serverPort, err := getInferenceServerPort(runningPod)
		if err != nil { // Impossible, because such a runningPod would never be created by this controller
			return fmt.Errorf("unable to wake up server because port not known: %w", err), true
		}
		if serverDat.Sleeping == nil {
			sleeping, err := ctl.querySleeping(ctx, runningPod, serverPort)
			if err != nil {
				return err, true
			}
			logger.V(2).Info("Determined whether provider is sleeping", "isSleeping", sleeping)
			serverDat.Sleeping = &sleeping
		}
		if *(serverDat.Sleeping) {
			err = ctl.wakeSleeper(ctx, serverDat, requestingPod, runningPod, serverPort)
			if err != nil {
				return err, true
			}
			logger.V(2).Info("Woke discovered-bound inference server")
		}
		if err := ctl.ensureSleepingLabel(ctx, runningPod, *(serverDat.Sleeping)); err != nil {
			return err, true
		}
		err, _ = ctl.ensureReqState(ctx, requestingPod, serverDat, shouldAddRequesterFinalizer, false)
		if err != nil {
			return err, true
		}
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
			err = doPost(url)
			if err != nil {
				logger.Error(err, "Failed to relay the readiness", "name", runningPod.Name, "readiness", readiness)
				return err, true
			}
			serverDat.ReadinessRelayed = &ready
			logger.V(5).Info("Successfully relayed the readiness", "name", runningPod.Name, "readiness", readiness)
		}
		// TODO: sync desired and actual runningPod wrt labels (spec is mostly immutable, possible mutations are allowed)
		logger.V(5).Info("Nothing more to do")
		return nil, false
	}
	// Assert: runningPod == nil && !shouldAddRequesterFinalizer

	if node.Spec.Unschedulable {
		// Reflect the inability to serve back to the client/user
		logger.V(2).Info("Deleting server-requesting Pod because it is bound to an unschedulable Node and has no server-running Pod")
		err := podOps.Delete(ctx, requestingPod.Name, metav1.DeleteOptions{PropagationPolicy: ptr.To(metav1.DeletePropagationBackground)})
		return err, false
	}
	// What remains to be done is to wake or create a server-running Pod

	serverPatch := requestingPod.Annotations[api.ServerPatchAnnotationName]
	if serverPatch == "" { // this is bad, somebody has hacked important data
		return ctl.ensureReqStatus(ctx, requestingPod, serverDat, "the "+api.ServerPatchAnnotationName+" annotation is missing")
	}
	// use the server patch to build the server-running pod, if not already done.
	desiredRunningPod, nominalHash, err := serverDat.getNominalServerRunningPod(ctx, requestingPod, serverPatch, api.RunnerData{
		NodeName: requestingPod.Spec.NodeName,
	})
	if err != nil {
		return ctl.ensureReqStatus(ctx, requestingPod, serverDat, fmt.Sprintf("failed to construct the nominal server-running Pod: %s", err.Error()))
	}

	sleepingAnys, err := ctl.podInformer.GetIndexer().ByIndex(nominalHashIndexName, nominalHash)
	if err != nil { // impossible
		return err, false
	}
	if len(sleepingAnys) > 0 {
		// They have to be sleeping, the Kube scheduler and kubelet would not have assigned the same
		// node/gpus to the requester if there was another one awake.
		if len(sleepingAnys) > 1 {
			logger.V(2).Info("Unexpected: multiple sleeping Pods match; using the first", "requesterName", requestingPod.Name)
		}
		runningPod = sleepingAnys[0].(*corev1.Pod)
		return ctl.bind(ctx, serverDat, requestingPod, runningPod)
	}
	// What remains is to make a new server-running Pod --- if the sleeper budget allows.

	err, retry := ctl.enforceSleeperBudget(ctx, serverDat, requestingPod)
	if err != nil || retry {
		return err, retry
	}
	// Sleeper budget is met. Make the new Pod.

	logger.V(3).Info("Creating server-running pod", "node", requestingPod.Spec.NodeName, "gpus", serverDat.GPUIndicesStr, "labels", desiredRunningPod.Labels)
	echo, err := podOps.Create(ctx, desiredRunningPod, metav1.CreateOptions{})
	if err != nil {
		errMsg := err.Error()
		if invalidPodRE.MatchString(errMsg) {
			return ctl.ensureReqStatus(ctx, requestingPod, serverDat, "the nominal server-running "+errMsg)
		}
		innerErr, _ := ctl.ensureReqStatus(ctx, requestingPod, serverDat, fmt.Sprintf("failed to create server-running Pod: %s", errMsg))
		if innerErr != nil {
			return errors.Join(err, innerErr), true
		}
		return err, true
	}
	serverDat.Sleeping = ptr.To(false)
	logger.V(2).Info("Created server-running pod", "name", echo.Name, "gpus", serverDat.GPUIndicesStr, "annotations", echo.Annotations, "labels", echo.Labels, "resourceVersion", echo.ResourceVersion)

	return ctl.ensureReqStatus(ctx, requestingPod, serverDat)
}

func (ctl *controller) ensureSleepingLabel(ctx context.Context, runningPod *corev1.Pod, desired bool) error {
	logger := klog.FromContext(ctx)
	desiredStr := strconv.FormatBool(desired)
	if runningPod.Labels[api.SleepingLabelName] != desiredStr {
		runningPod = runningPod.DeepCopy()
		runningPod.Labels = MapSet(runningPod.Labels, api.SleepingLabelName, desiredStr)
		echo, err := ctl.coreclient.Pods(ctl.namespace).Update(ctx, runningPod, metav1.UpdateOptions{
			FieldManager: ControllerName})
		if err != nil {
			return fmt.Errorf("failed to revise sleeping label on server-running Pod to %s: %w", desiredStr, err)
		}
		logger.V(3).Info("Updated sleeping label on sever-running Pod", "sleeping", desiredStr, "newResourceVersion", echo.ResourceVersion)
	}
	return nil
}

// Trouble is both (a) some container restarts and (b) Pod not ready
func podIsInTrouble(pod *corev1.Pod) bool {
	var sumRestarts int32
	for _, ctrStat := range pod.Status.ContainerStatuses {
		sumRestarts += ctrStat.RestartCount
	}
	if sumRestarts == 0 {
		return false
	}
	condIdx := slices.IndexFunc(pod.Status.Conditions, func(cond corev1.PodCondition) bool {
		return cond.Type == "Ready"
	})
	if condIdx >= 0 {
		if pod.Status.Conditions[condIdx].Status == corev1.ConditionTrue {
			return false
		}
	}
	return true
}

var invalidPodRE = regexp.MustCompile(`^Pod "[a-z0-9.-]*" is invalid`)
var apiAccessRE = regexp.MustCompile(`^kube-api-access-[a-z0-9]+$`)

func (ctl *controller) enforceSleeperBudget(ctx context.Context, serverDat *serverData, requestingPod *corev1.Pod) (error, bool) {
	logger := klog.FromContext(ctx)
	podOps := ctl.coreclient.Pods(ctl.namespace)
	gonerNames := sets.New[string]() // names of deleted server-running Pods
	now := time.Now()
	nameToAge := map[string]time.Duration{}
	getAge := func(pod *corev1.Pod) time.Duration {
		age, have := nameToAge[pod.Name]
		if !have {
			idx := slices.IndexFunc(pod.ManagedFields, func(mf metav1.ManagedFieldsEntry) bool {
				return mf.Manager == ControllerName
			})
			if idx >= 0 {
				age = now.Sub(pod.ManagedFields[idx].Time.Time)
			} else {
				age = now.Sub(pod.CreationTimestamp.Time)
			}
			nameToAge[pod.Name] = age
		}
		return age
	}
	comparePods := func(left, right *corev1.Pod) int {
		leftAge := getAge(left)
		rightAge := getAge(right)
		switch {
		case leftAge > rightAge:
			return -1
		case rightAge > leftAge:
			return 1
		default:
			return strings.Compare(left.Name, right.Name)
		}
	}
	for _, gpuIndex := range serverDat.GPUIndices { // enforce sleeper budget on this GPU
		// This is really simple logic. Just pick some without preference.
		// Recognize deletions done for the sake of other GPUs.
		// TODO: better
		key := requestingPod.Spec.NodeName + " " + gpuIndex
		sleepingAnys, err := ctl.podInformer.GetIndexer().ByIndex(GPUIndexName, key)
		if err != nil { // impossible
			return err, false
		}
		sleepingPods, _ := SliceMap(sleepingAnys, func(sleepingAny any) (*corev1.Pod, error) {
			pod := sleepingAny.(*corev1.Pod)
			if gonerNames.Has(pod.Name) {
				return nil, io.EOF
			}
			return pod, nil
		})
		// Every existing server-running Pod on this GPU must have a sleeping inference server,
		// otherwise the scheduler and kubelet would not have assigned this GPU to the server-requesting Pod.
		toGo := len(sleepingPods) - ctl.sleeperLimit
		if toGo <= 0 {
			continue
		}
		slices.SortFunc(sleepingPods, comparePods)
		for idx, goner := range sleepingPods[:toGo] {
			gonerNames.Insert(goner.Name)
			err := podOps.Delete(ctx, goner.Name, metav1.DeleteOptions{
				Preconditions:     &metav1.Preconditions{UID: &goner.UID, ResourceVersion: &goner.ResourceVersion},
				PropagationPolicy: ptr.To(metav1.DeletePropagationBackground),
			})
			if err == nil {
				logger.V(2).Info("Deleted server-running Pod with sleeping server, to respect sleeper-limit", "idx", idx, "total", len(sleepingPods), "limit", ctl.sleeperLimit, "name", goner.Name, "resourceVersion", goner.ResourceVersion)
			} else if apierrors.IsNotFound(err) || apierrors.IsGone(err) {
				logger.V(5).Info("Server-running Pod was concurrently deleted", "name", goner.Name)
			} else {
				return fmt.Errorf("unable to delete server-running Pod %s (RV=%s): %w", goner.Name, goner.ResourceVersion, err), true
			}
		}
	}
	return nil, len(gonerNames) > 0
}

func (ctl *controller) bind(ctx context.Context, serverDat *serverData, requestingPod, runningPod *corev1.Pod) (error, bool) {
	logger := klog.FromContext(ctx)
	runningPod = runningPod.DeepCopy()
	runningPod.Annotations[requesterAnnotationKey] = string(requestingPod.UID) + " " + requestingPod.Name
	if !slices.Contains(runningPod.Finalizers, runnerFinalizer) {
		runningPod.Finalizers = append(runningPod.Finalizers, runnerFinalizer)
	}
	runningPod.Labels = MapSet(runningPod.Labels, api.DualLabelName, requestingPod.Name)
	serverDat.Sleeping = nil
	echo, err := ctl.coreclient.Pods(ctl.namespace).Update(ctx, runningPod, metav1.UpdateOptions{FieldManager: ControllerName})
	if err != nil {
		return fmt.Errorf("failed to bind server-running Pod %s: %w", runningPod.Name, err), true
	}
	serverDat.RunningPodName = runningPod.Name
	logger.V(2).Info("Bound server-running Pod", "name", runningPod.Name, "node", requestingPod.Spec.NodeName, "gpus", serverDat.GPUIndicesStr, "newResourceVersion", echo.ResourceVersion)
	_, serverPort, err := getInferenceServerPort(runningPod)
	if err != nil { // Impossible, because such a runningPod would never be created by this controller
		return fmt.Errorf("unable to wake up server because port not known: %w", err), true
	}
	err = ctl.wakeSleeper(ctx, serverDat, requestingPod, runningPod, serverPort)
	if err != nil {
		return err, true
	}
	logger.V(2).Info("Woke freshly-bound inference server", "runningPod", runningPod.Name)
	return ctl.ensureReqState(ctx, requestingPod, serverDat, !slices.Contains(requestingPod.Finalizers, requesterFinalizer), false)
}

func (ctl *controller) wakeSleeper(ctx context.Context, serverDat *serverData, requestingPod, runningPod *corev1.Pod, serverPort int16) error {
	if ctl.debugAccelMemory {
		if err := ctl.accelMemoryIsLowEnough(ctx, requestingPod, serverDat); err != nil {
			return err
		}
	}
	wakeURL := fmt.Sprintf("http://%s:%d/wake_up", runningPod.Status.PodIP, serverPort)
	err := doPost(wakeURL)
	if err != nil {
		return err
	}
	if err := ctl.ensureSleepingLabel(ctx, runningPod, false); err != nil {
		return err
	}
	serverDat.Sleeping = ptr.To(false)
	return nil
}

// maybeRemoveRequesterFinalizer removes the requesterFinalizer if necessary,
// and detemines whether the finalizer needs to be added.
// requestingPod != nil; runningPod might be nil.
// Returns (removed, shouldAdd bool, err error, retry bool).
func (ctl *controller) maybeRemoveRequesterFinalizer(ctx context.Context, requestingPod, runningPod *corev1.Pod) (bool, bool, error, bool) {
	// First, determine whether finalizer should be present
	var wantFinalizer bool
	if runningPod != nil {
		isIdx, _, err := getInferenceServerPort(runningPod)
		if err == nil {
			isCtr := &runningPod.Spec.Containers[isIdx]
			statIdx := slices.IndexFunc(runningPod.Status.ContainerStatuses,
				func(status corev1.ContainerStatus) bool {
					return status.Name == isCtr.Name
				})
			if statIdx >= 0 {
				isStatus := &runningPod.Status.ContainerStatuses[statIdx]
				wantFinalizer = isStatus.State.Running != nil
			}
		}
	}
	// Next, determine whether finalizer is present
	finIdx := slices.Index(requestingPod.Finalizers, requesterFinalizer)
	haveFinalizer := finIdx >= 0
	// Finally, deal with it
	if wantFinalizer == haveFinalizer {
		return false, false, nil, false
	}
	if wantFinalizer {
		return false, requestingPod.DeletionTimestamp == nil, nil, false
	}
	podOps := ctl.coreclient.Pods(ctl.namespace)
	requestingPod = requestingPod.DeepCopy()
	requestingPod.Finalizers = slices.Delete(requestingPod.Finalizers, finIdx, finIdx+1)
	echo, err := podOps.Update(ctx, requestingPod, metav1.UpdateOptions{FieldManager: ControllerName})
	if err != nil {
		return false, false, fmt.Errorf("failed to remove finalizer from server-requesting Pod: %w", err), true
	}
	logger := klog.FromContext(ctx)
	logger.V(2).Info("Removed requester finalizer", "newResourceVersion", echo.ResourceVersion)
	return true, false, nil, false
}

// addRequesterFinalizer does the API call to remove the controller's finalizer from the server-requesting Pod.
// Returns (newResourceVersion string, err error)
func (ctl *controller) addRequesterFinalizer(ctx context.Context, requestingPod *corev1.Pod, runningPodName string) (string, error) {
	podOps := ctl.coreclient.Pods(ctl.namespace)
	requestingPod = requestingPod.DeepCopy()
	if requestingPod.Labels[api.DualLabelName] != runningPodName {
		requestingPod.Labels = MapSet(requestingPod.Labels, api.DualLabelName, runningPodName)
	}
	requestingPod.Finalizers = append(requestingPod.Finalizers, requesterFinalizer)
	echo, err := podOps.Update(ctx, requestingPod, metav1.UpdateOptions{FieldManager: ControllerName})
	if err != nil {
		return "", fmt.Errorf("failed to add finalizer from server-requesting Pod: %w", err)
	}
	logger := klog.FromContext(ctx)
	logger.V(2).Info("Added requester finalizer", "newResourceVersion", echo.ResourceVersion)
	return echo.ResourceVersion, nil
}

// removeRunnerFinalizer does the API call to remove the controller's finalizer from the server-running Pod.
// Returns (changed bool, err error)
func (ctl *controller) removeRunnerFinalizer(ctx context.Context, runningPod *corev1.Pod) (bool, error) {
	logger := klog.FromContext(ctx)
	podOps := ctl.coreclient.Pods(ctl.namespace)
	// Ensure finalizer is absent from server-running Pod so that its deletion can complete
	if newFinalizers, changed := SliceRemoveOnce(runningPod.Finalizers, runnerFinalizer); changed {
		runningPod = runningPod.DeepCopy()
		runningPod.Finalizers = newFinalizers
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
func (ctl *controller) ensureUnbound(ctx context.Context, serverDat *serverData, runningPod *corev1.Pod) error {
	logger := klog.FromContext(ctx)
	// A runningPod with no IP is not scheduled, so we know that it is not awake.
	// If runningPod is stale then the update will fail.
	if (serverDat.Sleeping == nil || !*(serverDat.Sleeping)) && runningPod.Status.PodIP != "" { // need to put to sleep
		serverPort := serverDat.ServerPort
		if serverDat.NominalRunningPod == nil {
			var err error
			_, serverPort, err = getInferenceServerPort(runningPod)
			if err != nil { // Impossible, because such a runningPod would never be created by this controller
				return fmt.Errorf("unable to put server to sleep because port not known: %w", err)
			}
		}
		sleepURL := fmt.Sprintf("http://%s:%d/sleep", runningPod.Status.PodIP, serverPort)
		resp, err := http.Post(sleepURL, "", nil)
		if err != nil {
			return fmt.Errorf("failed to put to sleep, POST %s got error: %w", sleepURL, err)
		}
		if sc := resp.StatusCode; sc != http.StatusOK {
			return fmt.Errorf("failed to put to sleep, POST %s returned status %d", sleepURL, sc)
		}
		serverDat.Sleeping = ptr.To(true)
		logger.V(2).Info("Put inference server to sleep")
	}
	runningPod = runningPod.DeepCopy()
	var aChange, fChange bool
	// Ensure the sleeping label is correct
	sleepLabelValue := runningPod.Labels[api.SleepingLabelName]
	lChange := sleepLabelValue != "true"
	if lChange {
		runningPod.Labels = MapSet(runningPod.Labels, api.SleepingLabelName, "true")
	}
	// Ensure requester annotation is absent
	if _, have := runningPod.Annotations[requesterAnnotationKey]; have {
		delete(runningPod.Annotations, requesterAnnotationKey)
		aChange = true
	}
	// Ensure finalizer is absent
	runningPod.Finalizers, fChange = SliceRemoveOnce(runningPod.Finalizers, runnerFinalizer)
	if aChange || fChange || lChange {
		if runningPod.Labels != nil {
			delete(runningPod.Labels, api.DualLabelName)
		}
		podOps := ctl.coreclient.Pods(ctl.namespace)
		echo, err := podOps.Update(ctx, runningPod, metav1.UpdateOptions{FieldManager: ControllerName})
		if err != nil {
			return fmt.Errorf("failed to unbind server-running Pod %s: %w", runningPod.Name, err)
		}
		logger.V(2).Info("Unbound server-running Pod", "name", runningPod.Name, "node", runningPod.Spec.NodeName, "gpus", serverDat.GPUIndicesStr, "newResourceVersion", echo.ResourceVersion)
	} else {
		logger.V(3).Info("Server-running Pod remains unbound", "name", runningPod.Name, "resourceVersion", runningPod.ResourceVersion)
	}
	serverDat.RunningPodName = ""
	return nil
}

// getNominalServerRunningPod returns the nominal server-running Pod,
// which is cached in the serverData, computing the Pod if necessary.
// This also ensures that the serverData fields NominalRunningPod and NominalRunningPodHash
// have the right values.
// Returns (NominalRunningPod, NominalRunningPodHash, error)
func (serverDat *serverData) getNominalServerRunningPod(ctx context.Context, reqPod *corev1.Pod, rawTmpl string, data api.RunnerData) (*corev1.Pod, string, error) {
	logger := klog.FromContext(ctx)
	if serverDat.NominalRunningPod == nil {
		logger.V(5).Info("Building server-running pod from patch", "patch", rawTmpl)
		tmpl, err := template.New("serverPatch").Option("missingkey=error").Parse(rawTmpl)
		if err != nil {
			return nil, "", fmt.Errorf("parse template: %w", err)
		}
		var buf bytes.Buffer
		if err := tmpl.Execute(&buf, data); err != nil {
			return nil, "", fmt.Errorf("failed to execute server patch template: %w", err)
		}
		renderedPatch := buf.Bytes()

		patchJSON, err := yaml.YAMLToJSON(renderedPatch)
		if err != nil {
			return nil, "", fmt.Errorf("failed to convert server patch yaml to json: %w", err)
		}

		basePod := &corev1.Pod{
			TypeMeta: reqPod.TypeMeta,
			ObjectMeta: metav1.ObjectMeta{
				Labels:    reqPod.Labels,
				Namespace: reqPod.Namespace,
			},
			Spec: *deIndividualize(reqPod.Spec.DeepCopy()),
		}
		// marshal into json
		baseJSON, err := json.Marshal(basePod)
		if err != nil {
			return nil, "", fmt.Errorf("failed to marshal server-requesting pod: %w", err)
		}
		logger.V(5).Info("Before StrategicMergePatch", "reqPodName", reqPod.Name, "baseJSON", baseJSON)
		// apply strategic merge patch
		modifiedJSON, err := strategicpatch.StrategicMergePatch(baseJSON, patchJSON, &corev1.Pod{})
		if err != nil {
			return nil, "", fmt.Errorf("failed to apply server patch: %w", err)
		}
		hasher := sha256.New()
		hasher.Write(modifiedJSON)
		hasher.Write([]byte(";gpus="))
		hasher.Write([]byte(*serverDat.GPUIndicesStr))
		hasher.Write([]byte(";node="))
		hasher.Write([]byte(reqPod.Spec.NodeName))
		var modifiedHash [sha256.Size]byte
		modifiedHashSl := hasher.Sum(modifiedHash[:0])
		nominalHash := base64.RawStdEncoding.EncodeToString(modifiedHashSl)

		logger.V(5).Info("Computed nominalHash", "nominalHash", nominalHash, "modifiedJSON", modifiedJSON, "gpus", *serverDat.GPUIndicesStr, "node", reqPod.Spec.NodeName)

		var pod = &corev1.Pod{}
		// Decode back into Pod.
		// Use a real Kubernetes decoder that will complain about spurious fields,
		// to catch common errors here (before sending to apiserver).
		_, _, err = podDecoder.Decode(modifiedJSON, nil, pod)
		if err != nil {
			return nil, "", fmt.Errorf("failed to unmarshal patched pod: %w", err)
		}

		nodeSelector := pod.Spec.NodeSelector
		if nodeSelector == nil {
			nodeSelector = map[string]string{}
			pod.Spec.NodeSelector = nodeSelector
		}
		nodeSelector["kubernetes.io/hostname"] = reqPod.Spec.NodeName

		cIdx, serverPort, err := getInferenceServerPort(pod)
		if err != nil {
			return nil, "", err
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
				Value: *serverDat.GPUIndicesStr,
			})
		} else {
			isCtr.Env[eIdx].Value = *serverDat.GPUIndicesStr
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
		pod.Annotations = MapSet(pod.Annotations, nominalHashAnnotationKey, nominalHash)
		pod.Annotations[requesterAnnotationKey] = string(reqPod.UID) + " " + reqPod.Name
		pod.Annotations[api.AcceleratorsAnnotationName] = *serverDat.GPUIDsStr
		pod.Labels = MapSet(pod.Labels, api.DualLabelName, reqPod.Name)
		pod.Labels[api.SleepingLabelName] = "false"
		serverDat.NominalRunningPod = pod
		serverDat.NominalRunningPodHash = nominalHash
	}
	return serverDat.NominalRunningPod, serverDat.NominalRunningPodHash, nil
}

// deIndividualize removes the parts of a PodSpec that are specific to an individual.
// This func side-effects the given `*PodSpec` and returns it.
func deIndividualize(podSpec *corev1.PodSpec) *corev1.PodSpec {
	podSpec.EphemeralContainers = nil // these may not be given in Create
	// The api-access Volume is individualized
	volIdx := slices.IndexFunc(podSpec.Volumes, func(vol corev1.Volume) bool {
		return apiAccessRE.MatchString(vol.Name)
	})
	if volIdx >= 0 {
		volName := podSpec.Volumes[volIdx].Name
		podSpec.Volumes = slices.Delete(podSpec.Volumes, volIdx, volIdx+1)
		for ctrIdx := range podSpec.Containers {
			removeVolumeMount(&podSpec.Containers[ctrIdx], volName)
		}
		for ctrIdx := range podSpec.InitContainers {
			removeVolumeMount(&podSpec.Containers[ctrIdx], volName)
		}
	}
	return podSpec
}

func removeVolumeMount(ctr *corev1.Container, volumeName string) {
	mntIdx := slices.IndexFunc(ctr.VolumeMounts, func(mnt corev1.VolumeMount) bool {
		return mnt.Name == volumeName
	})
	if mntIdx >= 0 {
		ctr.VolumeMounts = slices.Delete(ctr.VolumeMounts, mntIdx, mntIdx+1)
	}
}

// getInferenceServerPort, given a server-running Pod,
// returns (containerIndex int, port int16, err error)
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

func (ctl *controller) querySleeping(ctx context.Context, runningPod *corev1.Pod, serverPort int16) (bool, error) {
	queryURL := fmt.Sprintf("http://%s:%d/is_sleeping", runningPod.Status.PodIP, serverPort)
	body, err := doGet(queryURL)
	if err != nil {
		return false, err
	}
	sleepState := api.SleepState{}
	err = json.Unmarshal(body, &sleepState)
	if err != nil {
		return false, fmt.Errorf("failed to parse response body to is_sleeping query: %w", err)
	}
	return sleepState.IsSleeping, nil
}

func (ctl *controller) accelMemoryIsLowEnough(ctx context.Context, requestingPod *corev1.Pod, serverDat *serverData) error {
	adminPort := requestingPod.Annotations[api.AdminPortAnnotationName]
	if adminPort == "" {
		adminPort = api.AdminPortDefaultValue
	}
	url := fmt.Sprintf("http://%s:%s%s", requestingPod.Status.PodIP, adminPort, stubapi.AcceleratorMemoryQueryPath)
	body, err := doGet(url)
	if err != nil {
		return err
	}
	usageMap := map[string]int64{}
	err = json.Unmarshal(body, &usageMap)
	if err != nil {
		return fmt.Errorf("failed to parse memory usage map: %w", err)
	}
	logger := klog.FromContext(ctx)
	for _, gpuID := range serverDat.GPUIDs {
		if used, have := usageMap[gpuID]; !have {
			return fmt.Errorf("no GPU usage information for GPU %s", gpuID)
		} else if used > ctl.accelMemoryLimitMiB {
			return fmt.Errorf("accelerator %s is currently using %d MiB of memory, limit for sleeping total is %d MiB", gpuID, used, ctl.accelMemoryLimitMiB)
		} else {
			logger.V(4).Info("OK accelerator memory usage", "node", requestingPod.Spec.NodeName, "accelerator", gpuID, "usageMiB", used, "limitMiB", ctl.accelMemoryLimitMiB)
		}
	}
	logger.V(4).Info("AOK accelerator memory usage", "node", requestingPod.Spec.NodeName, "gpuIDs", serverDat.GPUIDs)
	return nil
}

// ensureReqStatus makes the API call if necessary set the controller's status
// on the server-running Pod shows the given user errors.
// The returned (err error, retry bool) is a convenient match for the signature of
// a sync function; always `retry == (err != nil)`.
func (ctl *controller) ensureReqStatus(ctx context.Context, requestingPod *corev1.Pod, serverDat *serverData, errors ...string) (error, bool) {
	return ctl.ensureReqState(ctx, requestingPod, serverDat, false, false, errors...)
}

// ensureReqState makes the API call if necessary to:
// 1. set the controller's reported state to consist of the given errors;
// 2. add or remove the controll'er finalizer if stipulated.
// The returned (err error, retry bool) is a convenient match for the signature of
// a sync function; always `retry == (err != nil)`.
func (ctl *controller) ensureReqState(ctx context.Context, requestingPod *corev1.Pod, serverDat *serverData, addFinalizer, removeFinalizer bool, errors ...string) (error, bool) {
	status := api.ServerRequestingPodStatus{Errors: errors}
	logger := klog.FromContext(ctx)
	newStatusBytes, err := json.Marshal(status)
	if err != nil { // impossible; handle by infinite retry
		return fmt.Errorf("failed to marshal status (%#v): %w", status, err), true
	}
	newStatusStr := string(newStatusBytes)
	oldStatusStr := requestingPod.Annotations[api.ServerPatchAnnotationErrorsName]
	newFinalizers := requestingPod.Finalizers
	if removeFinalizer {
		newFinalizers, _ = SliceRemoveOnce(newFinalizers, requesterFinalizer)
	} else if addFinalizer {
		newFinalizers = append(newFinalizers, requesterFinalizer)
	}
	desiredAccelerators := ptr.Deref(serverDat.GPUIDsStr, "")
	currentAccelerators := requestingPod.Annotations[api.AcceleratorsAnnotationName]
	if oldStatusStr == newStatusStr && desiredAccelerators == currentAccelerators && len(newFinalizers) == len(requestingPod.Finalizers) && serverDat.RunningPodName == requestingPod.Labels[api.DualLabelName] {
		logger.V(5).Info("No need to update status, accelerators, boundName, or finalizers", "serverRequestingPod", requestingPod.Name, "status", status, "accelerators", desiredAccelerators, "boundName", serverDat.RunningPodName, "finalizers", requestingPod.Finalizers)
		return nil, false
	}
	requestingPod = requestingPod.DeepCopy()
	requestingPod.Annotations = MapSet(requestingPod.Annotations, api.ServerPatchAnnotationErrorsName, newStatusStr)
	requestingPod.Annotations[api.AcceleratorsAnnotationName] = desiredAccelerators
	requestingPod.Finalizers = newFinalizers
	if serverDat.RunningPodName != "" {
		requestingPod.Labels = MapSet(requestingPod.Labels, api.DualLabelName, serverDat.RunningPodName)
	} else if requestingPod.Labels != nil {
		delete(requestingPod.Labels, api.DualLabelName)
	}
	echo, err := ctl.coreclient.Pods(requestingPod.Namespace).Update(ctx, requestingPod, metav1.UpdateOptions{FieldManager: ctl.ControllerName})
	if err == nil {
		logger.V(2).Info("Set status/finalizers", "serverRequestingPod", requestingPod.Name, "status", status, "accelerators", desiredAccelerators, "boundName", serverDat.RunningPodName, "finalizers", requestingPod.Finalizers, "newResourceVersion", echo.ResourceVersion)
	} else {
		logger.V(3).Info("Failed to set status/finalizers", "serverRequestingPod", requestingPod.Name, "status", status, "accelerators", desiredAccelerators, "boundName", serverDat.RunningPodName, "finalizers", requestingPod.Finalizers, "resourceVersion", requestingPod.ResourceVersion)
	}
	return err, err != nil
}

// doPost does the HTTP POST request/response to the given URL.
func doPost(url string) error {
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
		return fmt.Errorf("http POST %q returned unexpected status %d; response body=%s", url, resp.StatusCode, string(body))
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

func doGet(url string) ([]byte, error) {
	client := &http.Client{
		Timeout: 5 * time.Second,
	}

	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("http get %q: %w", url, err)
	}
	defer resp.Body.Close() //nolint:errcheck

	body, bodyReadErr := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("http GET %q returned unexpected status %d; bodyReadErr=%v; responseBody=%s", url, resp.StatusCode, bodyReadErr, string(body))
	}

	if bodyReadErr != nil {
		return nil, fmt.Errorf("failed to read body: %w", bodyReadErr)
	}
	return body, nil
}

// getGPUUUIDs does the HTTP GET on the given URL to fetch the assigned GPU UUIDs.
func getGPUUUIDs(url string) ([]string, error) {
	body, err := doGet(url)
	if err != nil {
		return nil, err
	}
	var uuids []string
	if err := json.Unmarshal(body, &uuids); err != nil {
		return nil, fmt.Errorf("unmarshal uuids: %w", err)
	}

	return uuids, nil
}

// findGPUIndices maps GPU UUIDs to GPU indices.
// This func will be moved into the launcher in milestone 3
func (ctl *controller) mapToGPUIndices(nodeName string, gpuUUIDs []string) ([]string, error) {
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
	return indices, errors.Join(errs...)
}
