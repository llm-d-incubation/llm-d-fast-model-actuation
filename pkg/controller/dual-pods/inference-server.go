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

	"github.com/prometheus/client_golang/prometheus"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	k8sserializer "k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	k8svalidation "k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/yaml"

	fmav1alpha1 "github.com/llm-d-incubation/llm-d-fast-model-actuation/api/fma/v1alpha1"
	"github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/api"
	ctlrcommon "github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/controller/common"
	"github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/controller/utils"
	stubapi "github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/spi"
)

var reservedKeyPrefixes = []string{"dual-pods.llm-d.ai/", "kubernetes.io/", "k8s.io/"}

func hasReservedPrefix(key string) bool {
	for _, prefix := range reservedKeyPrefixes {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

type nodeItem struct {
	NodeName string
}

type launcherSyncResult struct {
	instances          *AllInstancesState
	stoppedInstanceIDs sets.Set[string] // bound instances found stopped (not deleted by sync)
}

// vllmInstanceState holds a snapshot of the ISC-derived state for a
// launcher-based vLLM instance. Once a *vllmInstanceState has been
// returned from computeDesiredInstanceState, neither its fields nor
// the values they reference (pointed-to VllmConfig, map entries) are
// mutated.
type vllmInstanceState struct {
	cfg            *VllmConfig
	instanceID     string
	serverPort     int32
	iscLabels      map[string]string
	iscAnnotations map[string]string
}

func (ni nodeItem) process(ctx context.Context, ctl *controller) (error, bool) {
	logger := klog.FromContext(ctx).WithValues("node", ni.NodeName)
	ctx = klog.NewContext(ctx, logger)
	nodeDat := ctl.getNodeData(ni.NodeName)
	items := nodeDat.yankItems()
	var retries int
	logger.V(4).Info("Processing items for node", "count", len(items))
	for localItem, addTime := range items {
		logger.V(4).Info("Processing node-local item", "item", localItem, "enqueuedAt", addTime)
		processStart := time.Now()
		queueDurationHists.WithLabelValues(ni.NodeName).Observe(processStart.Sub(addTime).Seconds())
		err, retry := localItem.process(ctx, ctl, nodeDat)
		processFin := time.Now()
		workDurationHists.WithLabelValues(ni.NodeName).Observe(processFin.Sub(processStart).Seconds())
		if err != nil {
			if retry {
				logger.Info("Processing node local item suffered transient error, will retry", "item", localItem, "err", err)
			} else {
				logger.Error(err, "Processing node local item failed", "item", localItem)
			}
		} else {
			logger.V(4).Info("Finished processing node-local item", "item", localItem, "willRetry", retry)
		}
		if retry {
			nodeDat.add(localItem)
			retriesCounters.WithLabelValues(ni.NodeName).Inc()
			retries++
		}
	}
	logger.V(4).Info("Done processing items for node", "numToRetry", retries)
	return nil, retries > 0
}

func (item unboundLauncherPodItem) process(ctx context.Context, ctl *controller, nodeDat *nodeData) (error, bool) {
	logger := klog.FromContext(ctx).WithValues("launcherPod", item.LauncherPodName, "node", item.NodeName)
	ctx = klog.NewContext(ctx, logger)

	launcherPod, err := ctl.podLister.Pods(ctl.namespace).Get(item.LauncherPodName)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.V(2).Info("Launcher pod deleted, cleaning up launcher data")
			ctl.clearLauncherData(nodeDat, item.LauncherPodName)
			ctl.enqueueUnboundInfSvrItemsOnNode(ctx, item.NodeName, fmt.Sprintf("launcher pod %s deleted", item.LauncherPodName))
			return nil, false
		}
		return err, true
	}

	// Sync launcher instances to keep internal state fresh and clean up stopped instances.
	_, syncErr, syncRetry := ctl.syncLauncherInstances(ctx, nodeDat, nil, launcherPod)

	ctl.enqueueUnboundInfSvrItemsOnNode(ctx, item.NodeName, fmt.Sprintf("launcher pod %s changed", item.LauncherPodName))

	if syncErr != nil {
		return fmt.Errorf("failed to sync launcher instances: %w", syncErr), syncRetry
	}
	return nil, syncRetry
}

func (item infSvrItem) process(urCtx context.Context, ctl *controller, nodeDat *nodeData) (error, bool) {
	// The `requesterName` value is relied upon by the log parser in benchmarking
	logger := klog.FromContext(urCtx).WithValues("serverUID", item.UID, "requesterName", item.RequesterName)
	serverDat := ctl.getServerData(nodeDat, item.RequesterName, item.UID)
	if serverDat.InstanceID != "" {
		logger = logger.WithValues("instanceID", serverDat.InstanceID)
	}
	ctx := klog.NewContext(urCtx, logger)
	requesterRV := "(non existent)"
	providerRV := "(non existent)"
	var requesterDeletionTimestamp, providerDeletionTimestamp *string
	var requesterRCS, providerRCS *reducedContainerState
	var iscName string

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
		requesterDeletionTimestamp = TimePtrToStringPtr(requestingPod.DeletionTimestamp)
		requesterRCS = getReducedInferenceContainerState(requestingPod)
		iscName = requestingPod.Annotations[api.InferenceServerConfigAnnotationName]
	}

	var providingPod *corev1.Pod
	providingPodAnys, err := ctl.podInformer.GetIndexer().ByIndex(requesterIndexName, string(item.UID))
	if err != nil { //impossible
		return err, false
	}
	switch len(providingPodAnys) {
	case 0:
	case 1:
		providingPod = providingPodAnys[0].(*corev1.Pod)
		providerRV = providingPod.ResourceVersion
		providerDeletionTimestamp = TimePtrToStringPtr(providingPod.DeletionTimestamp)
		providerRCS = getReducedInferenceContainerState(providingPod)
		logger = logger.WithValues("providerName", providingPod.Name)
		ctx = klog.NewContext(urCtx, logger)
		serverDat.ProvidingPodName = providingPod.Name
	default:
		providerNames, _ := utils.SliceMap(providingPodAnys, func(podAny any) (string, error) {
			pod := podAny.(*corev1.Pod)
			return pod.Name, nil
		})
		return fmt.Errorf("found multiple bound server-running Pods: %v", providerNames), false
	}

	logger.V(5).Info("Processing inference server",
		"requesterResourceVersion", requesterRV, "requesterDeletionTimestamp", requesterDeletionTimestamp,
		"requesterRCS", requesterRCS,
		"providerResourceVersion", providerRV, "providerDeletionTimestamp", providerDeletionTimestamp,
		"providerRCS", providerRCS,
		"GPUIDs", serverDat.GPUIDs)

	podOps := ctl.coreclient.Pods(ctl.namespace)

	// Delete the in-memory data after both Pods are gone.
	// Also un-assert the binding metric.
	if requestingPod == nil && providingPod == nil {
		ctl.clearServerData(nodeDat, item.UID)
		logger.V(2).Info("End of life of inference server")
		if serverDat.InstanceID != "" {
			ctl.ensureDualityMetric(ctx, serverDat, nodeDat.NodeName, false)
		} else {
			logger.V(2).Info("Not setting duality=0, because InstanceID is unknown")
		}
		return nil, false
	}

	// Decide what to do about the finalizer on the server-requesting Pod,
	// and do it if that is a removal.
	var shouldAddRequesterFinalizer bool
	if requestingPod != nil {
		removed, shouldAdd, err, retry := ctl.maybeRemoveRequesterFinalizer(ctx, requestingPod, providingPod)
		if removed || err != nil {
			return err, retry
		}
		shouldAddRequesterFinalizer = shouldAdd
	}

	// Handle the deletion of a server-providing Pod
	if providingPod != nil && providingPod.DeletionTimestamp != nil {
		if requestingPod != nil && requestingPod.DeletionTimestamp == nil {
			// Reflect providingPod deletion to requestingPod deletion.
			gonerRV := requesterRV
			if shouldAddRequesterFinalizer { // don't let delete complete too quickly
				gonerRV, err = ctl.addRequesterFinalizer(ctx, requestingPod, providingPod.Name, serverDat.InstanceID)
				if err != nil {
					return err, true
				}
			}
			delStart := time.Now()
			err := podOps.Delete(ctx, requestingPod.Name, metav1.DeleteOptions{
				PropagationPolicy: ptr.To(metav1.DeletePropagationBackground),
				Preconditions:     &metav1.Preconditions{UID: &item.UID, ResourceVersion: &gonerRV}})
			delStartStr := delStart.Format(time.RFC3339Nano)
			if err == nil {
				logger.V(2).Info("Requested deletion of server-requesting Pod because of deletion of server-providing Pod", "k8sCallStartTime", delStartStr)
			} else if apierrors.IsGone(err) || apierrors.IsNotFound(err) {
				logger.V(2).Info("The server-requesting Pod is already gone", "k8sCallStartTime", delStartStr)
			} else {
				return fmt.Errorf("failed to delete server-requesting Pod (started %s): %w", delStartStr, err), true
			}
			serverDat.RequesterDeleteRequested = true
		}
		// Ensure finalizer is absent from server-providing Pod so that its deletion can complete
		changed, err := ctl.removeProviderFinalizer(ctx, providingPod)
		if err != nil {
			return err, true
		}
		if !changed {
			logger.V(5).Info("Finalizer is absent from server-providing Pod, waiting for deletions to finish")
		}
		return nil, false
	}
	// Assert: providingPod == nil || providingPod.DeletionTimestamp == nil

	// If the server-requesting Pod is absent or being deleted,
	// ensure that the server-providing Pod is not bound.
	if (requestingPod == nil || requestingPod.DeletionTimestamp != nil) && providingPod != nil {
		// Time to unbind.
		// As a special favor, delete providingPod if it is in trouble.
		if utils.PodIsInTrouble(providingPod) {
			delStart := time.Now()
			err := podOps.Delete(ctx, providingPod.Name, metav1.DeleteOptions{
				Preconditions:     &metav1.Preconditions{UID: &providingPod.UID, ResourceVersion: &providingPod.ResourceVersion},
				PropagationPolicy: ptr.To(metav1.DeletePropagationBackground),
			})
			delStartStr := delStart.Format(time.RFC3339Nano)
			if err == nil {
				stJSON, marshalErr := json.Marshal(providingPod.Status)
				logger.V(2).Info("Deleted server-providing Pod because it is in trouble", "providerName", providingPod.Name, "status", string(stJSON), "marshalErr", marshalErr, "k8sCallStartTime", delStartStr)
				return nil, false
			} else if apierrors.IsNotFound(err) || apierrors.IsGone(err) {
				logger.V(2).Info("Troubled server-providing Pod was concurrently deleted", "providerName", providingPod.Name, "k8sCallStartTime", delStartStr)
			} else {
				logger.V(2).Info("Failed to delete troubled server-providing Pod", "providerName", providingPod.Name, "k8sCallStartTime", delStartStr, "err", err.Error())
			}
		}
		// since now requestingPod could be nil, use the providingPod's launcherConfigNameLabelKey
		// to help determine whether providingPod is launcher-based
		providingPodLauncherBased := false
		if providingPod.Labels != nil {
			_, providingPodLauncherBased = providingPod.Labels[ctlrcommon.LauncherConfigNameLabelKey]
		}
		err := ctl.ensureUnbound(ctx, serverDat, iscName, nodeDat, providingPod, providingPodLauncherBased)
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
		// Node is gone or going away, do nothing to maintain server-providing Pod.
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

	var isc *fmav1alpha1.InferenceServerConfig
	_, launcherBased := requestingPod.Annotations[api.InferenceServerConfigAnnotationName]
	if launcherBased {
		logger.V(5).Info("Server requesting Pod is asking for launcher-based server providing Pod")
	}

	// Fetch the assigned GPUs if that has not already been done.
	if serverDat.GPUIDsStr == nil {
		logger.V(5).Info("Querying accelerators", "ip", requesterIP, "port", adminPort)
		url := fmt.Sprintf("http://%s:%s%s", requesterIP, adminPort, stubapi.AcceleratorQueryPath)
		gpuUUIDs, err := getGPUUUIDs(ctx, ctl.httpLatencySecsHistograms.MustCurryWith(prometheus.Labels{"isc_name": iscName}), url)
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

		gpuIDsStr := strings.Join(gpuUUIDs, ",")
		serverDat.GPUIDs = gpuUUIDs
		serverDat.GPUIDsStr = &gpuIDsStr
	}

	if !launcherBased && serverDat.GPUIndicesStr == nil {
		gpuIndices, err := ctl.mapToGPUIndices(requestingPod.Spec.NodeName, serverDat.GPUIDs)
		if err != nil {
			return ctl.ensureReqStatus(ctx, requestingPod, serverDat, err.Error())
		}
		gpuIndicesStr := strings.Join(gpuIndices, ",")
		serverDat.GPUIndices = gpuIndices
		serverDat.GPUIndicesStr = &gpuIndicesStr
	}

	var desiredInstanceState *vllmInstanceState
	if launcherBased && providingPod == nil {
		// from the requestingPod's annotations, get the InferenceServerConfig object
		if iscName == "" {
			return ctl.ensureReqStatus(ctx, requestingPod, serverDat,
				fmt.Sprintf("empty value for annotation %q", api.InferenceServerConfigAnnotationName),
			)
		}
		isc, err = ctl.iscLister.InferenceServerConfigs(ctl.namespace).Get(iscName)
		if err != nil {
			return ctl.ensureReqStatus(ctx, requestingPod, serverDat,
				fmt.Sprintf("failed to get InferenceServerConfig %q: %v", iscName, err),
			)
		}
		desiredInstanceState, err = ctl.computeDesiredInstanceState(isc, serverDat.GPUIDs)
		if err != nil {
			return ctl.ensureReqStatus(ctx, requestingPod, serverDat,
				fmt.Sprintf("failed to configure inference server: %v", err))
		}
	}

	// If there is already a bound server-providing Pod then ensure that it is awake,
	// ensure status reported, and relay readiness if needed.
	if providingPod != nil {
		var serverPort int32
		if launcherBased {
			if err := recoverInstanceStateFromLauncherPod(serverDat, providingPod); err != nil {
				return ctl.ensureReqStatus(ctx, requestingPod, serverDat, err.Error())
			}
			serverPort = serverDat.ServerPort
			ctl.ensureDualityMetric(ctx, serverDat, nodeDat.NodeName, true)
		} else {
			_, serverPort, err = utils.GetInferenceServerContainerIndexAndPort(providingPod)
			if err != nil { // Impossible, because such a providingPod would never be created by this controller
				return fmt.Errorf("unable to wake up server because port not known: %w", err), true
			}
		}
		if launcherBased {
			if providingPod.Status.PodIP == "" || !utils.IsPodReady(providingPod) {
				logger.V(5).Info("Bound launcher pod not yet reachable, waiting", "podIP", providingPod.Status.PodIP, "ready", utils.IsPodReady(providingPod))
				return nil, false
			}

			syncResult, err, retry := ctl.syncLauncherInstances(ctx, nodeDat, serverDat.InstancesDeleted, providingPod)
			if err != nil || retry {
				if err != nil {
					return fmt.Errorf("failed to sync launcher instances for bound launcher Pod: %w", err), retry
				}
				logger.V(5).Info("Launcher instance sync requested retry")
				return nil, true
			}

			_, instancePresent := findInstanceState(syncResult.instances.Instances, serverDat.InstanceID)
			_, instanceStopped := syncResult.stoppedInstanceIDs[serverDat.InstanceID]

			if instanceStopped || !instancePresent {
				if instanceStopped || serverDat.InstanceKnownToExist {
					// instanceStopped is an objective signal that the instance existed
					// and died — no dependency on in-memory InstanceKnownToExist state.
					// When !instancePresent && InstanceKnownToExist==true the instance vanished
					// (e.g. launcher restart) — same treatment.
					// Delete the requesting Pod first so the intent is durable in the
					// Kubernetes API; the stopped vLLM instance is cleaned up by the
					// next sync after the server data is removed.
					if instanceStopped {
						logger.V(2).Info("Bound instance found stopped on launcher")
					} else {
						logger.V(2).Info("Bound instance not found in launcher, treating as dead")
					}
					// Mark as sleeping so that ensureUnbound (called during requester deletion)
					// does not attempt to POST /sleep on the dead instance.
					serverDat.Sleeping = ptr.To(true)
					delStart := time.Now()
					err = podOps.Delete(ctx, requestingPod.Name, metav1.DeleteOptions{
						PropagationPolicy: ptr.To(metav1.DeletePropagationBackground),
						Preconditions:     &metav1.Preconditions{UID: &item.UID, ResourceVersion: &requestingPod.ResourceVersion}})
					delStartStr := delStart.Format(time.RFC3339Nano)
					if err == nil {
						logger.V(2).Info("Requested deletion of server-requesting Pod because bound instance stopped", "k8sCallStartTime", delStartStr)
					} else if apierrors.IsGone(err) || apierrors.IsNotFound(err) {
						logger.V(2).Info("The server-requesting Pod is already gone", "k8sCallStartTime", delStartStr)
					} else {
						return fmt.Errorf("failed to delete server-requesting Pod for stopped instance (started %s): %w", delStartStr, err), true
					}
					serverDat.RequesterDeleteRequested = true
					return nil, false
				}
				// InstanceKnownToExist is false and instance is absent (not stopped) —
				// not yet created (bind-first path) or controller restarted and lost tracking.
				// We just synced, so we know the instance is not on the launcher — create directly.
				serverDat.NeededNewInstance = true
				launcherBaseURL := fmt.Sprintf("http://%s:%d", providingPod.Status.PodIP, ctlrcommon.LauncherServicePort)
				lClient, err := NewLauncherClient(launcherBaseURL, ctl.httpLatencySecsHistograms.MustCurryWith(prometheus.Labels{"isc_name": iscName}))
				if err != nil {
					return err, true
				}
				result, err := lClient.CreateNamedInstance(ctx, serverDat.InstanceID, *serverDat.InstanceConfig)
				if err != nil {
					return fmt.Errorf("failed to create vLLM instance %q: %w", serverDat.InstanceID, err), true
				}
				serverDat.InstanceKnownToExist = true
				launcherDat := ctl.getLauncherData(nodeDat, providingPod.Name)
				launcherDat.Instances[serverDat.InstanceID] = time.Now()
				logger.V(2).Info("Created vLLM instance", "instance_id", result.InstanceID, "status", result.Status)
			}
			serverDat.InstanceKnownToExist = true
		}
		if serverDat.Sleeping == nil {
			sleeping, err := ctl.querySleeping(ctx, iscName, providingPod, serverPort)
			if err != nil {
				return err, true
			}
			logger.V(2).Info("Determined whether provider is sleeping", "isSleeping", sleeping)
			serverDat.Sleeping = &sleeping
		}
		if *(serverDat.Sleeping) {
			err = ctl.wakeSleeper(ctx, serverDat, requestingPod, providingPod, serverPort, "discovered-bound")
			if err != nil {
				return err, true
			}
		}
		// Apply the ISC-provided routing labels/annotations only now, once the
		// bound vLLM instance is confirmed serving (either querySleeping
		// returned successfully or wakeSleeper completed). Before this point
		// the launcher Pod has no InferencePool-matching labels, so EPP does
		// not treat it as an endpoint. See issue #629.
		if launcherBased && !*serverDat.Sleeping && !serverDat.LabelsApplied {
			stop, err := ctl.applyDeferredISCRoutingMetadata(ctx, requestingPod, serverDat, providingPod, iscName)
			if stop {
				return err, err != nil
			}
		}
		if err := ctl.ensureSleepingLabel(ctx, providingPod, *(serverDat.Sleeping)); err != nil {
			return err, true
		}
		err, _ = ctl.ensureReqState(ctx, requestingPod, serverDat, shouldAddRequesterFinalizer, false)
		if err != nil {
			return err, true
		}
		// Relay readiness if not already done.
		// For launcher-based providers, readiness follows the bound instance's
		// sleeping state rather than the launcher's Pod readiness.
		ready := utils.IsPodReady(providingPod)
		if launcherBased {
			ready = !*serverDat.Sleeping
		}
		var alreadyReady bool
		if reqCS := getContainerStatus(requestingPod, api.InferenceServerContainerName); reqCS != nil {
			alreadyReady = reqCS.Ready
		}
		if serverDat.ReadinessRelayed == nil || ready != *serverDat.ReadinessRelayed {
			if ready == alreadyReady {
				serverDat.ReadinessRelayed = &ready
				if ready {
					serverDat.FirstReadyRelayed = true
				}
			} else {
				url, readiness := fmt.Sprintf("http://%s:%s", requestingPod.Status.PodIP, adminPort), ""
				if ready {
					logger.V(5).Info("Server-providing pod is ready", "name", providingPod.Name)
					url += stubapi.BecomeReadyPath
					readiness = "ready"
				} else {
					logger.V(5).Info("Server-providing pod is not ready", "name", providingPod.Name)
					url += stubapi.BecomeUnreadyPath
					readiness = "unready"
				}
				_, err = doHTTP(ctx, "relay_"+readiness, "POST", url, ctl.httpLatencySecsHistograms.MustCurryWith(prometheus.Labels{"isc_name": iscName}), nil, nil)
				if err != nil {
					logger.Error(err, "Failed to relay the readiness", "name", providingPod.Name, "readiness", readiness, "url", url)
					return err, true
				}
				serverDat.ReadinessRelayed = &ready
				if ready && !serverDat.FirstReadyRelayed {
					reqCS := getContainerStatus(requestingPod, api.InferenceServerContainerName)
					if reqCS != nil && reqCS.State.Running != nil {
						path := "hot"
						if serverDat.NeededNewLauncher {
							path = "cold"
						} else if serverDat.NeededNewInstance {
							path = "warm"
						}
						serverDat.FirstReadyRelayed = true
						now := time.Now()
						actuationSecs := now.Sub(reqCS.State.Running.StartedAt.Time).Seconds()
						actuationSecsHistograms.WithLabelValues(ctl.namespace, path,
							strconv.FormatInt(int64(len(serverDat.InstancesDeleted)), 10),
							iscName,
						).Observe(actuationSecs)
						logger.V(5).Info("Observed actuation latency", "path", path, "actuationSecs", actuationSecs)
					} else {
						logger.V(5).Info("Unable to observe actuation latency due to requester container being either missing or not running")
					}
				}
				logger.V(2).Info("Successfully relayed the readiness", "readiness", readiness, "url", url, "requesterCreateTime", requestingPod.CreationTimestamp.Format(time.RFC3339Nano))
			}
		}
		// TODO: sync desired and actual providingPod wrt labels (spec is mostly immutable, possible mutations are allowed)
		logger.V(5).Info("Nothing more to do")
		return nil, false
	}
	// Assert: providingPod == nil && !shouldAddRequesterFinalizer

	if node.Spec.Unschedulable {
		// Reflect the inability to serve back to the client/user
		logger.V(2).Info("Deleting server-requesting Pod because it is bound to an unschedulable Node and has no server-providing Pod")
		delStart := time.Now()
		err := podOps.Delete(ctx, requestingPod.Name, metav1.DeleteOptions{PropagationPolicy: ptr.To(metav1.DeletePropagationBackground)})
		delStartStr := delStart.Format(time.RFC3339Nano)
		if err != nil {
			return fmt.Errorf("failed to delete server-requesting Pod on unschedulable Node (started %s): %w", delStartStr, err), false
		}
		logger.V(2).Info("Deleted server-requesting Pod on unschedulable Node", "k8sCallStartTime", delStartStr)
		return nil, false
	}
	// What remains to be done is to wake or create a server-providing Pod

	if !launcherBased {
		serverPatch := requestingPod.Annotations[api.ServerPatchAnnotationName]
		if serverPatch == "" { // this is bad, somebody has hacked important data
			return ctl.ensureReqStatus(ctx, requestingPod, serverDat, "the "+api.ServerPatchAnnotationName+" annotation is missing")
		}
		// use the server patch to build the server-providing pod, if not already done.
		desiredProvidingPod, nominalHash, err := serverDat.getNominalServerProvidingPod(ctx, requestingPod, serverPatch, api.ProviderData{
			NodeName: requestingPod.Spec.NodeName,
		})
		if err != nil {
			return ctl.ensureReqStatus(ctx, requestingPod, serverDat, fmt.Sprintf("failed to construct the nominal server-providing Pod: %s", err.Error()))
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
			providingPod = sleepingAnys[0].(*corev1.Pod)
			return ctl.bind(ctx, serverDat, requestingPod, providingPod, nil, false)
		}
		// What remains is to make a new server-providing Pod --- if the sleeper budget allows.

		err, retry := ctl.enforceSleeperBudget(ctx, serverDat, requestingPod, ctl.sleeperLimit)
		if err != nil || retry {
			return err, retry
		}
		// Sleeper budget is met. Make the new Pod.

		logger.V(3).Info("Creating server-providing pod", "node", requestingPod.Spec.NodeName, "gpus", serverDat.GPUIndicesStr, "labels", desiredProvidingPod.Labels)
		createStart := time.Now()
		echo, err := podOps.Create(ctx, desiredProvidingPod, metav1.CreateOptions{})
		createStartStr := createStart.Format(time.RFC3339Nano)
		if err != nil {
			errMsg := err.Error()
			if invalidPodRE.MatchString(errMsg) {
				logger.V(2).Info("Failed to create server-providing Pod", "node", requestingPod.Spec.NodeName, "gpus", serverDat.GPUIndicesStr, "k8sCallStartTime", createStartStr, "err", errMsg)
				return ctl.ensureReqStatus(ctx, requestingPod, serverDat, "the nominal server-providing "+errMsg)
			}
			wrappedErr := fmt.Errorf("failed to create server-providing Pod (started %s): %w", createStartStr, err)
			innerErr, _ := ctl.ensureReqStatus(ctx, requestingPod, serverDat, fmt.Sprintf("failed to create server-providing Pod: %s", errMsg))
			if innerErr != nil {
				return errors.Join(wrappedErr, innerErr), true
			}
			return wrappedErr, true
		}
		serverDat.Sleeping = ptr.To(false)
		logger.V(2).Info("Created server-providing pod", "name", echo.Name, "gpus", serverDat.GPUIndicesStr, "annotations", echo.Annotations, "labels", echo.Labels, "resourceVersion", echo.ResourceVersion, "k8sCallStartTime", createStartStr)

		return ctl.ensureReqStatus(ctx, requestingPod, serverDat)
	}
	// What remains to be done is to wake or create a launcher-based server-providing Pod

	// from the InferenceServerConfig object, get the launcherConfig object
	lcName := isc.Spec.LauncherConfigName
	lc, err := ctl.lcLister.LauncherConfigs(ctl.namespace).Get(lcName)
	if err != nil {
		return ctl.ensureReqStatus(ctx, requestingPod, serverDat,
			fmt.Sprintf("failed to get LauncherConfig %q: %v", lcName, err),
		)
	}

	nodeIndependentLauncherTemplate, _, err := utils.BuildNodeIndependentLauncherTemplate(lc)
	if err != nil {
		return fmt.Errorf("failed to build launcher Pod from LauncherConfig %q: %w", lcName, err), true
	}
	desiredLauncherPod := utils.SpecializeLauncherTemplateToNode(nodeIndependentLauncherTemplate, requestingPod.Spec.NodeName)
	lcHash := desiredLauncherPod.Annotations[ctlrcommon.LauncherConfigHashAnnotationKey]
	logger.V(5).Info("LauncherConfig's hash", "hash", lcHash)
	launcherPodAnys, err := ctl.podInformer.GetIndexer().ByIndex(launcherConfigHashIndexName, lcHash)
	if err != nil {
		return err, false
	}

	desiredPort := desiredInstanceState.serverPort
	logger.V(5).Info("Nominal hash of InferenceServerConfig", "hash", desiredInstanceState.instanceID)

	if len(launcherPodAnys) > 0 {
		// Multiple launcher Pods could exist for one LauncherConfig object on one node.
		// Select the best launcher Pod: prioritize those with sleeping instances (fast wake-up),
		// then those with capacity for new instances.
		// Note that multiple vLLM instances could exist in one launcher Pod, but at most one instance could be awake at a time.

		launcherPod, hasSleepingInstance, retry, err := ctl.selectOrReclaimLauncherPod(ctx, launcherPodAnys, desiredInstanceState.instanceID, desiredPort, int(lc.Spec.MaxInstances)-1, nodeDat, serverDat.InstancesDeleted)
		if err != nil {
			return err, true
		}
		if retry {
			logger.V(4).Info("Launcher Pod selection or reclaim requested retry")
			return nil, true
		}
		if launcherPod != nil {
			// Bind first, then rely on informer notification to trigger re-reconciliation.
			// The "bound provider" path will handle instance creation/waking.
			// This ensures the invariant: vllm awake implies provider Pod is bound.
			logger.V(5).Info("Selected launcher Pod, binding first", "name", launcherPod.Name, "hasSleepingInstance", hasSleepingInstance)
			return ctl.bind(ctx, serverDat, requestingPod, launcherPod, desiredInstanceState, true)
		}
		// Fall through to create new launcher Pod.
	}
	// Remains: Zero matching launcher Pods, or the matching launcher Pod cannot host more instances to fulfill the request.
	// Make a new launcher Pod.
	serverDat.NeededNewLauncher = true

	// Bind at creation time so the launcher-populator cannot delete this pod
	// while the vLLM instance is being set up.
	desiredLauncherPod.Annotations = utils.MapSet(desiredLauncherPod.Annotations, requesterAnnotationKey, string(requestingPod.UID)+" "+requestingPod.Name)
	desiredLauncherPod.Labels = utils.MapSet(desiredLauncherPod.Labels, api.DualLabelName, requestingPod.Name)
	// Write only controller-managed annotations here; the ISC-provided labels
	// and annotations are applied later, after the vLLM instance is confirmed
	// serving (issue #629). Validation runs now so misconfiguration surfaces
	// before we create the launcher Pod.
	problems := applyControllerMetadataToLauncherPod(desiredLauncherPod, desiredInstanceState)
	if len(problems) > 0 {
		return ctl.ensureReqStatus(ctx, requestingPod, serverDat, problems...)
	}
	if !slices.Contains(desiredLauncherPod.Finalizers, providerFinalizer) {
		desiredLauncherPod.Finalizers = append(desiredLauncherPod.Finalizers, providerFinalizer)
	}

	createStart := time.Now()
	echo, err := podOps.Create(ctx, desiredLauncherPod, metav1.CreateOptions{})
	createEnd := time.Now()
	launcherCreateSecsHistograms.WithLabelValues(ctl.namespace,
		lcName,
		ite(err == nil, "true", "false")).
		Observe(createEnd.Sub(createStart).Seconds())
	createStartStr := createStart.Format(time.RFC3339Nano)
	if err != nil {
		errMsg := err.Error()
		if invalidPodRE.MatchString(errMsg) {
			logger.V(2).Info("Failed to create launcher-based server-providing Pod", "node", requestingPod.Spec.NodeName, "k8sCallStartTime", createStartStr, "err", errMsg)
			return ctl.ensureReqStatus(ctx, requestingPod, serverDat, "the desired launcher-based server-providing "+errMsg)
		}
		wrappedErr := fmt.Errorf("failed to create launcher-based server-providing Pod (started %s): %w", createStartStr, err)
		innerErr, _ := ctl.ensureReqStatus(ctx, requestingPod, serverDat, fmt.Sprintf("failed to create launcher-based server-providing Pod: %s", errMsg))
		if innerErr != nil {
			return errors.Join(wrappedErr, innerErr), true
		}
		return wrappedErr, true
	}
	serverDat.Sleeping = nil
	commitInstanceState(serverDat, desiredInstanceState)
	serverDat.LabelsApplied = false
	serverDat.ProvidingPodName = echo.Name
	ctl.ensureDualityMetric(ctx, serverDat, nodeDat.NodeName, true)
	logger.V(2).Info("Created launcher-based server-providing pod", "name", echo.Name, "gpus", serverDat.GPUIDsStr, "annotations", echo.Annotations, "labels", echo.Labels, "resourceVersion", echo.ResourceVersion, "k8sCallStartTime", createStartStr)

	return ctl.ensureReqStatus(ctx, requestingPod, serverDat)
}

func (ctl *controller) ensureDualityMetric(ctx context.Context, serverDat *serverData, nodeName string, on bool) {
	if serverDat.DualityMetricAsserted == on {
		return
	}
	var val float64
	if on {
		val = 1
	}
	klog.FromContext(ctx).V(2).Info("Setting duality metric", "value", val, "gpuUUIDs", serverDat.GPUIDs)
	for _, gpuUUID := range serverDat.GPUIDs {
		dualityGauges.WithLabelValues(ctl.namespace, serverDat.RequestingPodName, serverDat.ProvidingPodName,
			api.InferenceServerContainerName, serverDat.InstanceID,
			gpuUUID, nodeName).
			Set(val)
	}
	serverDat.DualityMetricAsserted = on
}

type launcherReclaimPlan struct {
	launcherPod *corev1.Pod
	launcherDat *launcherData
	victims     []string
	lruID       string
	lruTime     time.Time
}

// selectOrReclaimLauncherPod evaluates matching launcher Pods and selects one
// for fulfilling a request. Priority 1 is a launcher with a sleeping vLLM
// instance matching targetInstanceID. Priority 2 is a launcher with capacity
// for a new vLLM instance. Priority 3 is reclaiming capacity from the launcher
// that needs the most vLLM instance deletions, using LRU as a tie-breaker.
// If it finds an unbound launcher's malformed instance state, it repairs that
// state by deleting the malformed instance and asks the caller to retry with a
// fresh launcher snapshot.
// Returns (selectedPod, hasSleepingInstance, retry, error).
// hasSleepingInstance is true when selectedPod already hosts the target vLLM
// instance and only needs binding/waking. retry tells the caller to requeue
// and try again later. Returns (nil, false, false, nil) if no suitable
// launcher is found and all launcher Pods are ready or failed.
func (ctl *controller) selectOrReclaimLauncherPod(
	ctx context.Context,
	launcherPodAnys []interface{},
	targetInstanceID string,
	desiredPort int32,
	maxOthers int,
	nodeDat *nodeData,
	instancesDeleted sets.Set[string],
) (*corev1.Pod, bool, bool, error) {
	logger := klog.FromContext(ctx)

	var candidateWithCapacity *corev1.Pod
	var somePodsNotReady bool
	var bestReclaimPlan *launcherReclaimPlan

	for _, podAny := range launcherPodAnys {
		launcherPod := podAny.(*corev1.Pod)

		if launcherPod.Status.Phase == corev1.PodFailed || launcherPod.DeletionTimestamp != nil {
			continue
		}
		if requesterValue := launcherPod.Annotations[requesterAnnotationKey]; requesterValue != "" {
			logger.V(5).Info("Launcher Pod already bound to another requester, skipping", "name", launcherPod.Name, "boundRequester", requesterValue)
			continue
		}

		// Track pods that are not ready yet - we should give them time instead of
		// failing and creating new launcher Pods immediately.
		if launcherPod.Status.PodIP == "" || !utils.IsPodReady(launcherPod) {
			logger.V(5).Info("Launcher Pod not ready yet", "name", launcherPod.Name, "hasIP", launcherPod.Status.PodIP != "")
			somePodsNotReady = true
			continue
		}

		syncResult, err, retry := ctl.syncLauncherInstances(ctx, nodeDat, instancesDeleted, launcherPod)
		if err != nil || retry {
			somePodsNotReady = true
			continue
		}
		insts := syncResult.instances

		launcherDat := ctl.getLauncherData(nodeDat, launcherPod.Name)
		hasSleepingInstance := false
		portConflictVictims := make([]string, 0, 1)
		otherVictims := make([]string, 0, len(insts.Instances))
		for _, inst := range insts.Instances {
			instPort, err := getVLLMInstancePort(inst)
			if err != nil {
				logger.V(5).Info("Deleting vLLM instance because it reports no usable port",
					"name", launcherPod.Name,
					"instanceID", inst.InstanceID,
					"annotations", inst.Annotations,
					"options", inst.Options,
					"err", err)
				launcherBaseURL := fmt.Sprintf("http://%s:%d", launcherPod.Status.PodIP, ctlrcommon.LauncherServicePort)
				iscName := inst.Annotations[VllmConfigISCNameAnnotationKey]
				lClient, clientErr := NewLauncherClient(launcherBaseURL, ctl.httpLatencySecsHistograms.MustCurryWith(prometheus.Labels{"isc_name": iscName}))
				if clientErr != nil {
					return nil, false, true, fmt.Errorf("failed to create launcher client for deleting instance %q from launcher Pod %q: %w", inst.InstanceID, launcherPod.Name, clientErr)
				}
				_, delErr := lClient.DeleteInstance(ctx, inst.InstanceID)
				if delErr != nil && !IsInstanceNotFoundError(delErr) {
					return nil, false, true, fmt.Errorf("failed to delete instance %q with no usable inference port from launcher Pod %q: %w", inst.InstanceID, launcherPod.Name, delErr)
				}
				instancesDeleted.Insert(inst.InstanceID)
				delete(launcherDat.Instances, inst.InstanceID)
				logger.V(2).Info("Ensured vLLM instance absent because it reports no usable port",
					"launcherPod", launcherPod.Name, "instanceID", inst.InstanceID)
				return nil, false, true, nil
			}
			if inst.InstanceID == targetInstanceID {
				if inst.Status != InstanceStatusStopped {
					hasSleepingInstance = true
				}
				continue
			}
			if instPort == desiredPort {
				portConflictVictims = append(portConflictVictims, inst.InstanceID)
			} else {
				otherVictims = append(otherVictims, inst.InstanceID)
			}
		}
		if hasSleepingInstance {
			// Priority 1: Found a sleeping instance
			logger.V(5).Info("Found launcher with sleeping instance (fastest path)",
				"name", launcherPod.Name,
				"instanceID", targetInstanceID,
				"totalInstances", insts.TotalInstances,
				"runningInstances", insts.RunningInstances)
			return launcherPod, true, false, nil
		}

		// Check if this launcher has capacity for a new instance
		if len(portConflictVictims) == 0 && insts.TotalInstances <= maxOthers {
			if candidateWithCapacity == nil {
				// Priority 2: Has capacity for new instance
				logger.V(5).Info("Found launcher with capacity for new instance",
					"name", launcherPod.Name,
					"totalInstances", insts.TotalInstances)
				candidateWithCapacity = launcherPod
			}
			// Don't return yet - keep looking for sleeping instances (higher priority).
			continue
		}

		toDelete := max(insts.TotalInstances-maxOthers, 1)
		// Any conflicting vLLM instance must be deleted; deleting only other
		// vLLM instances would leave the desired port unavailable.
		victims := append(portConflictVictims, pickInstanceVictims(otherVictims, launcherDat.Instances, toDelete-len(portConflictVictims))...)
		lruID, lruTime := reclaimPlanLRU(victims, launcherDat.Instances)
		plan := &launcherReclaimPlan{
			launcherPod: launcherPod,
			launcherDat: launcherDat,
			victims:     victims,
			lruID:       lruID,
			lruTime:     lruTime,
		}
		if bestReclaimPlan == nil || compareReclaimPlans(plan, bestReclaimPlan) < 0 {
			bestReclaimPlan = plan
		}
	}

	// No sleeper but we found a launcher with capacity, use it.
	if candidateWithCapacity != nil {
		logger.V(4).Info("Selected launcher with capacity (slower path)", "name", candidateWithCapacity.Name)
		return candidateWithCapacity, false, false, nil
	}

	if bestReclaimPlan != nil {
		launcherBaseURL := fmt.Sprintf("http://%s:%d", bestReclaimPlan.launcherPod.Status.PodIP, ctlrcommon.LauncherServicePort)
		lClient, err := NewLauncherClient(launcherBaseURL, ctl.httpLatencySecsHistograms.MustCurryWith(prometheus.Labels{"isc_name": ""}))
		if err != nil {
			return nil, false, true, err
		}
		for _, victim := range bestReclaimPlan.victims {
			instancesDeleted.Insert(victim)
			_, err := lClient.DeleteInstance(ctx, victim)
			if err != nil && !IsInstanceNotFoundError(err) {
				return nil, false, true, fmt.Errorf("failed to delete instance %q from launcher Pod %q: %w", victim, bestReclaimPlan.launcherPod.Name, err)
			}
			delete(bestReclaimPlan.launcherDat.Instances, victim)
			logger.V(2).Info("Ensured vLLM instance absent to reclaim launcher capacity",
				"launcherPod", bestReclaimPlan.launcherPod.Name, "instanceID", victim, "maxOthers", maxOthers)
		}
		return bestReclaimPlan.launcherPod, false, false, nil
	}

	// Found no sleeper or capable launcher, but there are launcher Pods not
	// ready yet. Signal caller to retry later.
	if somePodsNotReady {
		logger.V(4).Info("Found launcher Pods not ready yet, will retry later")
		return nil, false, true, nil
	}

	// No suitable launcher Pod found.
	logger.V(4).Info("No suitable launcher Pod found with sleeping instance, capacity, or reclaimable capacity")
	return nil, false, false, nil
}

// pickInstanceVictims chooses up to limit instance IDs to delete.
func pickInstanceVictims(
	candidates []string,
	knownLastUsed map[string]time.Time,
	limit int,
) []string {
	if limit <= 0 {
		return nil
	}
	candidates = slices.Clone(candidates)
	slices.SortFunc(candidates, func(a, b string) int {
		return compareInstanceLastUsed(a, b, knownLastUsed)
	})
	return candidates[:min(limit, len(candidates))]
}

func reclaimPlanLRU(victims []string, knownLastUsed map[string]time.Time) (string, time.Time) {
	lruID := victims[0]
	lruTime := knownLastUsed[lruID]
	for _, victim := range victims[1:] {
		victimTime := knownLastUsed[victim]
		if compareLastUsed(victim, victimTime, lruID, lruTime) < 0 {
			lruID = victim
			lruTime = victimTime
		}
	}
	return lruID, lruTime
}

func compareReclaimPlans(a, b *launcherReclaimPlan) int {
	if len(a.victims) != len(b.victims) {
		return len(b.victims) - len(a.victims)
	}
	return compareLastUsed(a.lruID, a.lruTime, b.lruID, b.lruTime)
}

func compareInstanceLastUsed(a, b string, knownLastUsed map[string]time.Time) int {
	return compareLastUsed(a, knownLastUsed[a], b, knownLastUsed[b])
}

func compareLastUsed(a string, aTime time.Time, b string, bTime time.Time) int {
	if aTime.Before(bTime) {
		return -1
	}
	if bTime.Before(aTime) {
		return 1
	}
	return strings.Compare(a, b)
}

// configInferenceServer computes the VllmConfig.
// `isc` and `gpuUUIDs` are deeply immutable.
// The result is deeply immutable.
func (ctl *controller) configInferenceServer(isc *fmav1alpha1.InferenceServerConfig, gpuUUIDs []string) (*VllmConfig, string, error) {
	portS := strconv.Itoa(int(isc.Spec.ModelServerConfig.Port))
	options := isc.Spec.ModelServerConfig.Options + " --port " + portS
	vllmCfg := VllmConfig{
		Options:  options,
		GpuUUIDs: gpuUUIDs,
		EnvVars:  isc.Spec.ModelServerConfig.EnvVars,
		Annotations: map[string]string{
			VllmConfigISCNameAnnotationKey:       isc.Name,
			VllmConfigInferencePortAnnotationKey: portS,
		},
	}
	iscBytes, err := yaml.Marshal(isc.Spec.ModelServerConfig)
	if err != nil {
		return nil, "", fmt.Errorf("failed to marshal InferenceServerConfig %q: %w", isc.Name, err)
	}
	hasher := sha256.New()
	hasher.Write(iscBytes)
	hasher.Write([]byte(";gpus="))
	hasher.Write([]byte(strings.Join(gpuUUIDs, ",")))
	var hash [sha256.Size]byte
	hashSl := hasher.Sum(hash[:0])
	// Using Raw_URL_Encoding because this hash will be used in URLs to the launcher.
	// Wrapping with "I" prefix and "i" suffix to ensure the value is a valid Kubernetes
	// label value (which must start and end with an alphanumeric character).
	nominalHash := "I" + base64.RawURLEncoding.EncodeToString(hashSl) + "i"

	return &vllmCfg, nominalHash, nil
}

func (ctl *controller) computeDesiredInstanceState(isc *fmav1alpha1.InferenceServerConfig, gpuUUIDs []string) (*vllmInstanceState, error) {
	cfg, instanceID, err := ctl.configInferenceServer(isc, gpuUUIDs)
	if err != nil {
		return nil, err
	}
	return &vllmInstanceState{
		cfg:            cfg,
		instanceID:     instanceID,
		serverPort:     isc.Spec.ModelServerConfig.Port,
		iscLabels:      isc.Spec.ModelServerConfig.Labels,
		iscAnnotations: isc.Spec.ModelServerConfig.Annotations,
	}, nil
}

// validateInstanceStateForLauncherPod checks the ISC-provided labels and
// annotations in state against qualified-name rules, reserved prefixes, and
// collisions with what is already on providingPod. It returns a non-empty
// slice of problem strings iff validation failed.
// Called both by applyControllerMetadataToLauncherPod (at bind/create time,
// so misconfiguration surfaces immediately) and by
// applyISCRoutingMetadataToLauncherPod (before actually writing the labels).
func validateInstanceStateForLauncherPod(providingPod *corev1.Pod, state *vllmInstanceState) []string {
	var problems []string
	for k, v := range state.iscLabels {
		if errs := k8svalidation.IsQualifiedName(k); len(errs) > 0 {
			problems = append(problems, fmt.Sprintf("ISC label key %q is not a valid qualified name: %s", k, strings.Join(errs, "; ")))
		} else if hasReservedPrefix(k) {
			problems = append(problems, fmt.Sprintf("ISC label key %q uses a reserved prefix", k))
		} else if _, exists := providingPod.Labels[k]; exists {
			problems = append(problems, fmt.Sprintf("ISC label key %q collides with existing pod label", k))
		}
		if errs := k8svalidation.IsValidLabelValue(v); len(errs) > 0 {
			problems = append(problems, fmt.Sprintf("ISC label value %q for key %q is not valid: %s", v, k, strings.Join(errs, "; ")))
		}
	}
	for k := range state.iscAnnotations {
		if errs := k8svalidation.IsQualifiedName(k); len(errs) > 0 {
			problems = append(problems, fmt.Sprintf("ISC annotation key %q is not a valid qualified name: %s", k, strings.Join(errs, "; ")))
		} else if hasReservedPrefix(k) {
			problems = append(problems, fmt.Sprintf("ISC annotation key %q uses a reserved prefix", k))
		} else if _, exists := providingPod.Annotations[k]; exists {
			problems = append(problems, fmt.Sprintf("ISC annotation key %q collides with existing pod annotation", k))
		}
	}
	return problems
}

// applyControllerMetadataToLauncherPod writes only controller-managed
// annotations (instance ID, server port, vLLM config) on the launcher Pod.
// The ISC-provided labels and annotations are intentionally NOT written
// here — they are deferred until the vLLM instance is confirmed serving,
// so the Pod does not become an InferencePool-matching routing target
// before its vLLM instance can accept traffic on the inference port
// (see applyISCRoutingMetadataToLauncherPod and issue #629).
// Validation of the ISC labels/annotations still runs here so
// misconfiguration is surfaced immediately at bind/create time.
func applyControllerMetadataToLauncherPod(providingPod *corev1.Pod, state *vllmInstanceState) []string {
	if problems := validateInstanceStateForLauncherPod(providingPod, state); len(problems) > 0 {
		return problems
	}
	cfgJSON, err := json.Marshal(state.cfg)
	if err != nil {
		return []string{fmt.Sprintf("failed to marshal launcher instance config: %s", err)}
	}
	providingPod.Annotations = utils.MapSet(providingPod.Annotations, launcherInstanceIDAnnotationKey, state.instanceID)
	providingPod.Annotations[launcherServerPortAnnotationKey] = strconv.Itoa(int(state.serverPort))
	providingPod.Annotations[launcherVllmConfigAnnotationKey] = string(cfgJSON)
	return nil
}

// applyISCRoutingMetadataToLauncherPod writes the ISC-provided labels and
// annotations (routing metadata, e.g. the InferencePool selector labels)
// on the launcher Pod, along with the tracking annotations that let
// ensureUnbound remove them later. Callers must invoke this only after
// the vLLM instance is confirmed serving.
func applyISCRoutingMetadataToLauncherPod(providingPod *corev1.Pod, state *vllmInstanceState) []string {
	if problems := validateInstanceStateForLauncherPod(providingPod, state); len(problems) > 0 {
		return problems
	}
	labelKeys := make([]string, 0, len(state.iscLabels))
	for k, v := range state.iscLabels {
		providingPod.Labels = utils.MapSet(providingPod.Labels, k, v)
		labelKeys = append(labelKeys, k)
	}
	slices.Sort(labelKeys)

	annotationKeys := make([]string, 0, len(state.iscAnnotations))
	for k, v := range state.iscAnnotations {
		providingPod.Annotations = utils.MapSet(providingPod.Annotations, k, v)
		annotationKeys = append(annotationKeys, k)
	}
	slices.Sort(annotationKeys)

	providingPod.Annotations = utils.MapSet(providingPod.Annotations, iscLabelKeysAnnotationKey, strings.Join(labelKeys, " "))
	providingPod.Annotations[iscAnnotationKeysAnnotationKey] = strings.Join(annotationKeys, " ")
	return nil
}

func commitInstanceState(serverDat *serverData, state *vllmInstanceState) {
	serverDat.InstanceID = state.instanceID
	serverDat.InstanceConfig = state.cfg
	serverDat.ServerPort = state.serverPort
}

// applyDeferredISCRoutingMetadata is called from the bound-provider reconcile
// path once the bound vLLM instance is confirmed serving, to write the
// ISC-provided labels and annotations onto the launcher Pod. Deferring these
// until the instance is serving keeps the Pod from becoming an
// InferencePool-matching routing target while its data-plane port (:8000) is
// still unavailable (issue #629). On successful application
// serverDat.LabelsApplied is set to true.
//
// It returns (stop, err). When stop is true the caller must return from the
// reconcile without relaying readiness: either an error occurred (err != nil,
// so the item is retried) or a tear-down was requested (err == nil, waiting for
// the requester deletion to be observed).
//
// If the InferenceServerConfig was edited between bind and readiness so that
// its current hash no longer matches the bound instance, that instance is
// obsolete and can never carry the current routing labels. Leaving it in place
// would strand a Ready-but-unrouted instance: the instance-GC path only reaps
// obsolete instances that are sleeping, and this one is awake. Instead the
// requester Pod is deleted so a fresh one rebinds against the current ISC; the
// standard unbind flow (maybeDeleteObsoleteInstance) then removes the obsolete
// instance from the launcher.
func (ctl *controller) applyDeferredISCRoutingMetadata(
	ctx context.Context,
	requestingPod *corev1.Pod,
	serverDat *serverData,
	providingPod *corev1.Pod,
	iscName string,
) (bool, error) {
	logger := klog.FromContext(ctx)
	podOps := ctl.coreclient.Pods(ctl.namespace)
	isc, err := ctl.iscLister.InferenceServerConfigs(ctl.namespace).Get(iscName)
	if err != nil {
		return true, fmt.Errorf("failed to load InferenceServerConfig %q for deferred label application: %w", iscName, err)
	}
	desired, err := ctl.computeDesiredInstanceState(isc, serverDat.GPUIDs)
	if err != nil {
		return true, fmt.Errorf("failed to compute instance state for deferred label application: %w", err)
	}
	if desired.instanceID != serverDat.InstanceID {
		// The ISC changed after bind but before the instance became ready, so the
		// bound instance is obsolete. Delete the requester to force a rebind
		// against the current ISC. Leave serverDat.Sleeping as-is (the instance is
		// awake) so the unbind flow's maybeDeleteObsoleteInstance deletes the
		// obsolete instance from the launcher rather than leaving it orphaned.
		logger.V(2).Info("Bound instance is obsolete (ISC changed during startup); deleting requester to rebind",
			"instanceID", serverDat.InstanceID, "currentISCHash", desired.instanceID)
		delStart := time.Now()
		err = podOps.Delete(ctx, requestingPod.Name, metav1.DeleteOptions{
			PropagationPolicy: ptr.To(metav1.DeletePropagationBackground),
			Preconditions:     &metav1.Preconditions{UID: &requestingPod.UID, ResourceVersion: &requestingPod.ResourceVersion}})
		delStartStr := delStart.Format(time.RFC3339Nano)
		if err == nil {
			logger.V(2).Info("Requested deletion of server-requesting Pod because bound instance is obsolete", "k8sCallStartTime", delStartStr)
		} else if apierrors.IsGone(err) || apierrors.IsNotFound(err) {
			logger.V(2).Info("The server-requesting Pod is already gone", "k8sCallStartTime", delStartStr)
		} else {
			return true, fmt.Errorf("failed to delete server-requesting Pod for obsolete instance (started %s): %w", delStartStr, err)
		}
		serverDat.RequesterDeleteRequested = true
		return true, nil
	}

	updated := providingPod.DeepCopy()
	if problems := applyISCRoutingMetadataToLauncherPod(updated, desired); len(problems) > 0 {
		// Same failure mode as at bind/create time — report to the requester
		// and stop; the next ISC edit or reconcile will retry.
		statusErr, _ := ctl.ensureReqStatus(ctx, requestingPod, serverDat, problems...)
		if statusErr != nil {
			return true, statusErr
		}
		return true, fmt.Errorf("ISC labels/annotations invalid for deferred application: %s", strings.Join(problems, "; "))
	}
	updStart := time.Now()
	echo, err := podOps.Update(ctx, updated, metav1.UpdateOptions{FieldManager: ControllerName})
	updStartStr := updStart.Format(time.RFC3339Nano)
	if err != nil {
		return true, fmt.Errorf("failed to apply deferred ISC labels to launcher Pod %s (started %s): %w", updated.Name, updStartStr, err)
	}
	serverDat.LabelsApplied = true
	logger.V(2).Info("Applied deferred ISC labels/annotations to bound launcher Pod",
		"name", updated.Name, "instanceID", serverDat.InstanceID,
		"newResourceVersion", echo.ResourceVersion, "k8sCallStartTime", updStartStr,
	)
	return false, nil
}

// clearInstanceStateFromLauncherPod removes the five controller-managed
// launcher-instance annotations from the providing Pod. It is the inverse
// of the annotation writes done by applyControllerMetadataToLauncherPod
// and applyISCRoutingMetadataToLauncherPod combined.
// Returns true iff any annotation was removed.
func clearInstanceStateFromLauncherPod(providingPod *corev1.Pod) bool {
	var changed bool
	for _, k := range []string{
		launcherInstanceIDAnnotationKey,
		launcherServerPortAnnotationKey,
		launcherVllmConfigAnnotationKey,
		iscLabelKeysAnnotationKey,
		iscAnnotationKeysAnnotationKey,
	} {
		if _, have := providingPod.Annotations[k]; have {
			delete(providingPod.Annotations, k)
			changed = true
		}
	}
	return changed
}

// recoverInstanceStateFromLauncherPod populates the serverData snapshot from
// the controller-written annotations on a bound launcher Pod. The three snapshot
// fields are written atomically by commitInstanceState and applyControllerMetadataToLauncherPod,
// so if any one is set in serverData the others are too and recovery is a no-op;
// otherwise all three annotations must be present on the Pod.
// It also derives serverDat.LabelsApplied from the presence of the
// iscLabelKeysAnnotationKey tracking annotation, which is written by
// applyISCRoutingMetadataToLauncherPod only after the ISC-provided labels
// have actually been placed on the Pod.
func recoverInstanceStateFromLauncherPod(serverDat *serverData, providingPod *corev1.Pod) error {
	if serverDat.InstanceID != "" {
		return nil
	}
	instanceID, ok := providingPod.Annotations[launcherInstanceIDAnnotationKey]
	if !ok || instanceID == "" {
		return fmt.Errorf("bound launcher Pod %q is missing annotation %q", providingPod.Name, launcherInstanceIDAnnotationKey)
	}
	portS, ok := providingPod.Annotations[launcherServerPortAnnotationKey]
	if !ok {
		return fmt.Errorf("bound launcher Pod %q is missing annotation %q", providingPod.Name, launcherServerPortAnnotationKey)
	}
	port, err := strconv.ParseInt(portS, 10, 32)
	if err != nil {
		return fmt.Errorf("bound launcher Pod %q has invalid annotation %q value %q: %w", providingPod.Name, launcherServerPortAnnotationKey, portS, err)
	}
	cfgJSON, ok := providingPod.Annotations[launcherVllmConfigAnnotationKey]
	if !ok {
		return fmt.Errorf("bound launcher Pod %q is missing annotation %q", providingPod.Name, launcherVllmConfigAnnotationKey)
	}
	cfg := &VllmConfig{}
	if err := json.Unmarshal([]byte(cfgJSON), cfg); err != nil {
		return fmt.Errorf("bound launcher Pod %q has invalid annotation %q: %w", providingPod.Name, launcherVllmConfigAnnotationKey, err)
	}

	serverDat.InstanceID = instanceID
	serverDat.InstanceConfig = cfg
	serverDat.ServerPort = int32(port)
	// The tracking annotation is written only after the ISC-provided labels
	// and annotations have been placed on the Pod. Its presence therefore
	// tells a restarted controller that label application has already
	// completed, so applyDeferredISCRoutingMetadata does not need to run.
	if _, have := providingPod.Annotations[iscLabelKeysAnnotationKey]; have {
		serverDat.LabelsApplied = true
	}
	return nil
}

func parseSpaceSeparatedAnnotation(value string) []string {
	if value == "" {
		return []string{}
	}
	return strings.Split(value, " ")
}

func getVLLMInstancePort(inst InstanceState) (int32, error) {
	if value, ok := inst.Annotations[VllmConfigInferencePortAnnotationKey]; ok {
		port, err := strconv.ParseInt(value, 10, 32)
		if err != nil {
			return 0, fmt.Errorf("parse annotations[%s] value %q: %w", VllmConfigInferencePortAnnotationKey, value, err)
		}
		return int32(port), nil
	}
	return 0, fmt.Errorf("missing annotations[%s]", VllmConfigInferencePortAnnotationKey)
}

func (ctl *controller) ensureSleepingLabel(ctx context.Context, providingPod *corev1.Pod, desired bool) error {
	logger := klog.FromContext(ctx)
	desiredStr := strconv.FormatBool(desired)
	if providingPod.Labels[api.SleepingLabelName] != desiredStr {
		providingPod = providingPod.DeepCopy()
		providingPod.Labels = utils.MapSet(providingPod.Labels, api.SleepingLabelName, desiredStr)
		updStart := time.Now()
		echo, err := ctl.coreclient.Pods(ctl.namespace).Update(ctx, providingPod, metav1.UpdateOptions{
			FieldManager: ControllerName})
		updStartStr := updStart.Format(time.RFC3339Nano)
		if err != nil {
			return fmt.Errorf("failed to revise sleeping label on server-providing Pod to %s (started %s): %w", desiredStr, updStartStr, err)
		}
		logger.V(2).Info("Updated sleeping label on server-providing Pod", "sleeping", desiredStr, "newResourceVersion", echo.ResourceVersion, "k8sCallStartTime", updStartStr)
	}
	return nil
}

var invalidPodRE = regexp.MustCompile(`^Pod "[a-z0-9.-]*" is invalid`)

func (ctl *controller) enforceSleeperBudget(ctx context.Context, serverDat *serverData, requestingPod *corev1.Pod, sleeperLimit int) (error, bool) {
	logger := klog.FromContext(ctx)
	podOps := ctl.coreclient.Pods(ctl.namespace)
	gonerNames := sets.New[string]() // names of deleted server-providing Pods
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
		sleepingPods, _ := utils.SliceMap(sleepingAnys, func(sleepingAny any) (*corev1.Pod, error) {
			pod := sleepingAny.(*corev1.Pod)
			if gonerNames.Has(pod.Name) {
				return nil, io.EOF
			}
			return pod, nil
		})
		// Every existing server-providing Pod on this GPU must have a sleeping inference server,
		// otherwise the scheduler and kubelet would not have assigned this GPU to the server-requesting Pod.
		toGo := len(sleepingPods) - sleeperLimit
		if toGo <= 0 {
			continue
		}
		slices.SortFunc(sleepingPods, comparePods)
		for idx, goner := range sleepingPods[:toGo] {
			gonerNames.Insert(goner.Name)
			delStart := time.Now()
			err := podOps.Delete(ctx, goner.Name, metav1.DeleteOptions{
				Preconditions:     &metav1.Preconditions{UID: &goner.UID, ResourceVersion: &goner.ResourceVersion},
				PropagationPolicy: ptr.To(metav1.DeletePropagationBackground),
			})
			delStartStr := delStart.Format(time.RFC3339Nano)
			if err == nil {
				logger.V(2).Info("Deleted server-providing Pod with sleeping server, to respect sleeper-limit", "idx", idx, "total", len(sleepingPods), "limit", sleeperLimit, "name", goner.Name, "resourceVersion", goner.ResourceVersion, "k8sCallStartTime", delStartStr)
			} else if apierrors.IsNotFound(err) || apierrors.IsGone(err) {
				logger.V(2).Info("Server-providing Pod was concurrently deleted", "name", goner.Name, "k8sCallStartTime", delStartStr)
			} else {
				return fmt.Errorf("unable to delete server-providing Pod %s (RV=%s) (started %s): %w", goner.Name, goner.ResourceVersion, delStartStr, err), true
			}
		}
	}
	return nil, len(gonerNames) > 0
}

// launcherState is non-nil iff launcher-based.
func (ctl *controller) bind(ctx context.Context, serverDat *serverData, requestingPod, providingPod *corev1.Pod, launcherState *vllmInstanceState, skipWake bool) (error, bool) {
	logger := klog.FromContext(ctx)
	providingPod = providingPod.DeepCopy()
	providingPod.Annotations = utils.MapSet(providingPod.Annotations, requesterAnnotationKey, string(requestingPod.UID)+" "+requestingPod.Name)
	if !slices.Contains(providingPod.Finalizers, providerFinalizer) {
		providingPod.Finalizers = append(providingPod.Finalizers, providerFinalizer)
	}
	providingPod.Labels = utils.MapSet(providingPod.Labels, api.DualLabelName, requestingPod.Name)
	launcherBased := launcherState != nil
	if launcherBased {
		// Write only controller-managed annotations here; the ISC-provided
		// labels and annotations are applied later, after the vLLM instance
		// is confirmed serving (issue #629). Validation runs now so
		// misconfiguration surfaces before we mutate the launcher Pod.
		problems := applyControllerMetadataToLauncherPod(providingPod, launcherState)
		if len(problems) > 0 {
			return ctl.ensureReqStatus(ctx, requestingPod, serverDat, problems...)
		}
	}
	serverDat.Sleeping = nil
	updStart := time.Now()
	echo, err := ctl.coreclient.Pods(ctl.namespace).Update(ctx, providingPod, metav1.UpdateOptions{FieldManager: ControllerName})
	updStartStr := updStart.Format(time.RFC3339Nano)
	if err != nil {
		return fmt.Errorf("failed to bind server-providing Pod %s (started %s): %w", providingPod.Name, updStartStr, err), true
	}
	serverDat.ProvidingPodName = providingPod.Name
	if launcherBased {
		commitInstanceState(serverDat, launcherState)
		serverDat.LabelsApplied = false
		ctl.ensureDualityMetric(ctx, serverDat, requestingPod.Spec.NodeName, true)
	}
	logger.V(2).Info("Bound server-providing Pod", "name", providingPod.Name, "node", requestingPod.Spec.NodeName, "gpus", serverDat.GPUIDsStr, "newResourceVersion", echo.ResourceVersion, "instanceID", serverDat.InstanceID, "k8sCallStartTime", updStartStr)
	var serverPort int32
	// For launcher-based server-providing Pods, ServerPort is written when binding.
	// For direct server-providing Pods, ServerPort is written (earlier) when
	// constructing the server-providing Pod's spec in getNominalServerProvidingPod.
	if launcherBased {
		serverPort = launcherState.serverPort
	} else {
		_, serverPort, err = utils.GetInferenceServerContainerIndexAndPort(providingPod)
		if err != nil { // Impossible, because such a providingPod would never be created by this controller
			return fmt.Errorf("unable to wake up server because port not known: %w", err), true
		}
	}
	if !skipWake {
		err = ctl.wakeSleeper(ctx, serverDat, requestingPod, providingPod, serverPort, "freshly-bound")
		if err != nil {
			return err, true
		}
	}
	return ctl.ensureReqState(ctx, requestingPod, serverDat, !slices.Contains(requestingPod.Finalizers, requesterFinalizer), false)
}

func (ctl *controller) wakeSleeper(ctx context.Context, serverDat *serverData, requestingPod, providingPod *corev1.Pod, serverPort int32, description string) error {
	if ctl.debugAccelMemory {
		if err := ctl.accelMemoryIsLowEnough(ctx, requestingPod, serverDat); err != nil {
			return err
		}
	}
	endpoint := fmt.Sprintf("%s:%d", providingPod.Status.PodIP, serverPort)
	wakeURL := "http://" + endpoint + "/wake_up"
	logger := klog.FromContext(ctx)
	_, err := doHTTP(ctx, "wake", "POST", wakeURL, ctl.httpLatencySecsHistograms.MustCurryWith(prometheus.Labels{"isc_name": requestingPod.Annotations[api.InferenceServerConfigAnnotationName]}), nil, nil)
	if err != nil {
		return fmt.Errorf("failed to wake inference server at %s: %w", endpoint, err)
	}
	logger.V(2).Info("Woke inference server", "endpoint", endpoint, "description", description)
	if err := ctl.ensureSleepingLabel(ctx, providingPod, false); err != nil {
		return err
	}
	serverDat.Sleeping = ptr.To(false)
	return nil
}

// maybeRemoveRequesterFinalizer removes the requesterFinalizer if necessary,
// and determines whether the finalizer needs to be added.
// requestingPod != nil; providingPod might be nil.
// Returns (removed, shouldAdd bool, err error, retry bool).
func (ctl *controller) maybeRemoveRequesterFinalizer(ctx context.Context, requestingPod, providingPod *corev1.Pod) (bool, bool, error, bool) {
	// First, determine whether finalizer should be present
	var wantFinalizer bool
	if providingPod != nil {
		if cs := getContainerStatus(providingPod, api.InferenceServerContainerName); cs != nil {
			wantFinalizer = cs.State.Running != nil
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
	logger := klog.FromContext(ctx)
	updStart := time.Now()
	echo, err := podOps.Update(ctx, requestingPod, metav1.UpdateOptions{FieldManager: ControllerName})
	updStartStr := updStart.Format(time.RFC3339Nano)
	if err != nil {
		return false, false, fmt.Errorf("failed to remove finalizer from server-requesting Pod (started %s): %w", updStartStr, err), true
	}
	logger.V(2).Info("Removed requester finalizer", "newResourceVersion", echo.ResourceVersion, "k8sCallStartTime", updStartStr)
	return true, false, nil, false
}

// addRequesterFinalizer does the API call to add the controller's finalizer to the server-requesting Pod.
// Returns (newResourceVersion string, err error)
func (ctl *controller) addRequesterFinalizer(ctx context.Context, requestingPod *corev1.Pod, providingPodName, instanceID string) (string, error) {
	podOps := ctl.coreclient.Pods(ctl.namespace)
	requestingPod = requestingPod.DeepCopy()
	if requestingPod.Labels[api.DualLabelName] != providingPodName {
		requestingPod.Labels = utils.MapSet(requestingPod.Labels, api.DualLabelName, providingPodName)
	}
	if instanceID != "" {
		requestingPod.Labels = utils.MapSet(requestingPod.Labels, api.InstanceLabelName, instanceID)
	}
	requestingPod.Finalizers = append(requestingPod.Finalizers, requesterFinalizer)
	logger := klog.FromContext(ctx)
	updStart := time.Now()
	echo, err := podOps.Update(ctx, requestingPod, metav1.UpdateOptions{FieldManager: ControllerName})
	updStartStr := updStart.Format(time.RFC3339Nano)
	if err != nil {
		return "", fmt.Errorf("failed to add finalizer from server-requesting Pod (started %s): %w", updStartStr, err)
	}
	logger.V(2).Info("Added requester finalizer", "newResourceVersion", echo.ResourceVersion, "k8sCallStartTime", updStartStr)
	return echo.ResourceVersion, nil
}

// removeProviderFinalizer does the API call to remove the controller's finalizer from the server-providing Pod.
// Returns (changed bool, err error)
func (ctl *controller) removeProviderFinalizer(ctx context.Context, providingPod *corev1.Pod) (bool, error) {
	logger := klog.FromContext(ctx)
	podOps := ctl.coreclient.Pods(ctl.namespace)
	// Ensure finalizer is absent from server-providing Pod so that its deletion can complete
	if newFinalizers, changed := utils.SliceRemoveOnce(providingPod.Finalizers, providerFinalizer); changed {
		providingPod = providingPod.DeepCopy()
		providingPod.Finalizers = newFinalizers
		updStart := time.Now()
		echo, err := podOps.Update(ctx, providingPod, metav1.UpdateOptions{FieldManager: ctl.ControllerName})
		updStartStr := updStart.Format(time.RFC3339Nano)
		if err != nil {
			return false, fmt.Errorf("failed to remove finalizer from server-providing Pod %s (RV %s) (started %s): %w", providingPod.Name, providingPod.ResourceVersion, updStartStr, err)
		}
		logger.V(2).Info("Removed finalizer from server-providing Pod", "provider", providingPod.Name, "newResourceVersion", echo.ResourceVersion, "k8sCallStartTime", updStartStr)
		return true, nil // update and/or delete event will trigger more processing
	}
	return false, nil // no change
}

func (item instanceGCItem) process(ctx context.Context, ctl *controller, nodeDat *nodeData) (error, bool) {
	logger := klog.FromContext(ctx).WithValues("iscName", item.ISCName)

	isc, err := ctl.iscLister.InferenceServerConfigs(ctl.namespace).Get(item.ISCName)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, false
		}
		return err, true
	}

	for launcherPodName, launcherDat := range nodeDat.Launchers {
		launcherPod, err := ctl.podLister.Pods(ctl.namespace).Get(launcherPodName)
		if err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			logger.Error(err, "Failed to get launcher pod during instance GC", "launcherPod", launcherPodName)
			continue
		}
		if launcherPod.DeletionTimestamp != nil || launcherPod.Status.PodIP == "" {
			continue
		}
		launcherBaseURL := fmt.Sprintf("http://%s:%d", launcherPod.Status.PodIP, ctlrcommon.LauncherServicePort)
		lClient, err := NewLauncherClient(launcherBaseURL, ctl.httpLatencySecsHistograms.MustCurryWith(prometheus.Labels{"isc_name": ""}))
		if err != nil {
			logger.Error(err, "Failed to create launcher client during instance GC", "launcherPod", launcherPodName)
			continue
		}
		allInsts, err := lClient.ListInstances(ctx)
		if err != nil {
			logger.Error(err, "Failed to list instances during instance GC", "launcherPod", launcherPodName)
			continue
		}
		logger.V(4).Info("Listed launcher instances during GC", "launcherPod", launcherPodName, "totalInstances", allInsts.TotalInstances)
		for _, inst := range allInsts.Instances {
			if inst.Annotations[VllmConfigISCNameAnnotationKey] != isc.Name {
				continue
			}
			if len(inst.GpuUUIDs) == 0 {
				logger.V(4).Info("Skipping instance GC: no GPU UUIDs", "launcherPod", launcherPodName, "instanceID", inst.InstanceID)
				continue
			}
			_, currentHash, err := ctl.configInferenceServer(isc, inst.GpuUUIDs)
			if err != nil {
				logger.Error(err, "Failed to compute current hash during instance GC", "launcherPod", launcherPodName, "instanceID", inst.InstanceID)
				continue
			}
			if inst.InstanceID == currentHash {
				continue // not obsolete
			}
			instPort, err := getVLLMInstancePort(inst)
			if err != nil {
				logger.Error(err, "Failed to determine instance port during GC", "launcherPod", launcherPodName, "instanceID", inst.InstanceID)
				continue
			}
			sleeping, err := ctl.querySleeping(ctx, inst.Annotations[VllmConfigISCNameAnnotationKey], launcherPod, instPort)
			if err != nil {
				logger.Error(err, "Failed to query sleeping state during instance GC", "launcherPod", launcherPodName, "instanceID", inst.InstanceID)
				continue
			}
			if !sleeping {
				logger.V(4).Info("Skipping instance GC: instance not explicitly sleeping", "launcherPod", launcherPodName, "instanceID", inst.InstanceID)
				continue
			}
			_, err = lClient.DeleteInstance(ctx, inst.InstanceID)
			if err != nil {
				if !IsInstanceNotFoundError(err) {
					logger.Error(err, "Failed to delete obsolete sleeping instance during GC", "launcherPod", launcherPodName, "instanceID", inst.InstanceID)
				}
				continue
			}
			delete(launcherDat.Instances, inst.InstanceID)
			logger.V(2).Info("Deleted obsolete sleeping instance", "launcherPod", launcherPodName, "instanceID", inst.InstanceID, "currentHash", currentHash)
		}
	}
	return nil, false
}

// Unbinds the given server-providing Pod.
func (ctl *controller) ensureUnbound(ctx context.Context, serverDat *serverData, iscName string, nodeDat *nodeData, providingPod *corev1.Pod, launcherBased bool) error {
	logger := klog.FromContext(ctx)
	if launcherBased {
		if err := recoverInstanceStateFromLauncherPod(serverDat, providingPod); err != nil {
			return err
		}
		ctl.ensureDualityMetric(ctx, serverDat, nodeDat.NodeName, true)
	}
	// A providingPod with no IP is not scheduled, so we know that it is not awake.
	// If providingPod is stale then the update will fail.
	if (serverDat.Sleeping == nil || !*(serverDat.Sleeping)) && providingPod.Status.PodIP != "" { // need to put to sleep
		// For launcher-based instances, check if the instance is already obsolete
		// (i.e. its InferenceServerConfig was updated since the instance was created).
		// If so, delete it from the launcher rather than putting it to sleep.
		var iscNameRead string
		var deletedFromLauncher bool
		if launcherBased {
			iscNameRead, deletedFromLauncher = ctl.maybeDeleteObsoleteInstance(ctx, serverDat, nodeDat, providingPod)
			if iscName == "" && iscNameRead != "" {
				iscName = iscNameRead
			}
		}
		if deletedFromLauncher {
			serverDat.Sleeping = ptr.To(true)
		} else {
			serverPort := serverDat.ServerPort
			if !launcherBased {
				if serverDat.NominalProvidingPod == nil {
					var err error
					_, serverPort, err = utils.GetInferenceServerContainerIndexAndPort(providingPod)
					if err != nil { // Impossible, because such a providingPod would never be created by this controller
						return fmt.Errorf("unable to put server to sleep because port not known: %w", err)
					}
				}
			}
			endpoint := fmt.Sprintf("%s:%d", providingPod.Status.PodIP, serverPort)
			sleepURL := "http://" + endpoint + "/sleep"
			_, err := doHTTP(ctx, "sleep", "POST", sleepURL, ctl.httpLatencySecsHistograms.MustCurryWith(prometheus.Labels{"isc_name": iscName}), nil, nil)
			if err != nil {
				return fmt.Errorf("failed to put provider %q to sleep, POST %s: %w", serverDat.ProvidingPodName, sleepURL, err)
			}
			serverDat.Sleeping = ptr.To(true)
			logger.V(2).Info("Put inference server to sleep", "endpoint", endpoint)
		}
	}
	providingPod = providingPod.DeepCopy()
	var aChange, fChange bool
	// Ensure the sleeping label is correct
	sleepLabelValue := providingPod.Labels[api.SleepingLabelName]
	lChange := sleepLabelValue != "true"
	if lChange {
		providingPod.Labels = utils.MapSet(providingPod.Labels, api.SleepingLabelName, "true")
	}
	// Ensure requester annotation is absent
	if _, have := providingPod.Annotations[requesterAnnotationKey]; have {
		delete(providingPod.Annotations, requesterAnnotationKey)
		aChange = true
	}
	// Ensure finalizer is absent
	providingPod.Finalizers, fChange = utils.SliceRemoveOnce(providingPod.Finalizers, providerFinalizer)
	// Remove ISC labels
	for _, k := range parseSpaceSeparatedAnnotation(providingPod.Annotations[iscLabelKeysAnnotationKey]) {
		if _, have := providingPod.Labels[k]; have {
			delete(providingPod.Labels, k)
			lChange = true
		}
	}
	// Remove ISC annotations
	for _, k := range parseSpaceSeparatedAnnotation(providingPod.Annotations[iscAnnotationKeysAnnotationKey]) {
		if _, have := providingPod.Annotations[k]; have {
			delete(providingPod.Annotations, k)
			aChange = true
		}
	}
	// Remove tracking annotations
	if clearInstanceStateFromLauncherPod(providingPod) {
		aChange = true
	}
	if aChange || fChange || lChange {
		if providingPod.Labels != nil {
			delete(providingPod.Labels, api.DualLabelName)
		}
		podOps := ctl.coreclient.Pods(ctl.namespace)
		updStart := time.Now()
		echo, err := podOps.Update(ctx, providingPod, metav1.UpdateOptions{FieldManager: ControllerName})
		updStartStr := updStart.Format(time.RFC3339Nano)
		if err != nil {
			return fmt.Errorf("failed to unbind server-providing Pod %s (started %s): %w", providingPod.Name, updStartStr, err)
		}
		logger.V(2).Info("Unbound server-providing Pod", "name", providingPod.Name, "node", providingPod.Spec.NodeName, "gpus", serverDat.GPUIDsStr, "newResourceVersion", echo.ResourceVersion, "k8sCallStartTime", updStartStr)
	} else {
		logger.V(3).Info("Server-providing Pod remains unbound", "name", providingPod.Name, "resourceVersion", providingPod.ResourceVersion)
	}
	return nil
}

// maybeDeleteObsoleteInstance checks whether the launcher-based instance is obsolete
// (its InferenceServerConfig was updated since the instance was created) and if so,
// deletes it from the launcher. Returns:
// `iscName string` if discovered,
// `deleted bool` indicating whether the instance was deleted (by this method or something else).
func (ctl *controller) maybeDeleteObsoleteInstance(ctx context.Context, serverDat *serverData, nodeDat *nodeData, providingPod *corev1.Pod) (string, bool) {
	logger := klog.FromContext(ctx)
	if serverDat.InstanceID == "" {
		return "", false
	}
	launcherBaseURL := fmt.Sprintf("http://%s:%d", providingPod.Status.PodIP, ctlrcommon.LauncherServicePort)
	lClient, err := newLauncherClient(launcherBaseURL, ctl.httpLatencySecsHistograms, nil)
	if err != nil {
		logger.V(4).Info("Cannot check instance obsolescence: failed to create launcher client", "err", err)
		return "", false
	}
	instState, err := lClient.GetInstanceState(ctx, serverDat.InstanceID)
	if err != nil {
		if IsInstanceNotFoundError(err) {
			logger.V(2).Info("Now vLLM instance does not exist", "instanceID", serverDat.InstanceID)
			return "", true
		}
		logger.V(4).Info("Cannot check instance obsolescence: failed to get instance state", "instanceID", serverDat.InstanceID, "err", err)
		return "", false
	}
	iscName := instState.Annotations[VllmConfigISCNameAnnotationKey]
	if iscName == "" {
		logger.V(4).Info("Cannot check instance obsolescence: no ISC name annotation on instance", "instanceID", serverDat.InstanceID)
		return iscName, false
	}
	logger.V(4).Info("Got vLLM instance state", "instanceID", serverDat.InstanceID)
	currentISC, err := ctl.iscLister.InferenceServerConfigs(ctl.namespace).Get(iscName)
	if err != nil {
		logger.V(4).Info("Cannot check instance obsolescence: ISC not found", "iscName", iscName, "err", err)
		return iscName, false
	}
	if len(instState.GpuUUIDs) == 0 {
		logger.V(4).Info("Cannot check instance obsolescence: no GPU UUIDs on instance", "instanceID", serverDat.InstanceID)
		return iscName, false
	}
	_, currentHash, err := ctl.configInferenceServer(currentISC, instState.GpuUUIDs)
	if err != nil {
		logger.V(4).Info("Cannot check instance obsolescence: failed to compute current hash", "iscName", iscName, "err", err)
		return iscName, false
	}
	if currentHash == serverDat.InstanceID {
		return iscName, false // not obsolete
	}
	// Instance is obsolete — delete from launcher instead of sleeping.
	serverDat.InstancesDeleted.Insert(serverDat.InstanceID)
	_, delErr := lClient.DeleteInstance(ctx, serverDat.InstanceID)
	if delErr != nil {
		if !IsInstanceNotFoundError(delErr) {
			logger.Error(delErr, "Failed to delete obsolete instance during unbinding",
				"instanceID", serverDat.InstanceID)
			return iscName, false
		}
	}
	if launcherDat := nodeDat.Launchers[providingPod.Name]; launcherDat != nil {
		delete(launcherDat.Instances, serverDat.InstanceID)
	}
	logger.V(2).Info("Deleted obsolete instance during unbinding",
		"instanceID", serverDat.InstanceID, "currentHash", currentHash, "iscName", iscName)
	return iscName, true
}

// getNominalServerProvidingPod returns the nominal server-providing Pod,
// which is cached in the serverData, computing the Pod if necessary.
// This also ensures that the serverData fields NominalProvidingPod and NominalProvidingPodHash
// have the right values.
// Returns (NominalProvidingPod, NominalProvidingPodHash, error)
func (serverDat *serverData) getNominalServerProvidingPod(ctx context.Context, reqPod *corev1.Pod, rawTmpl string, data api.ProviderData) (*corev1.Pod, string, error) {
	logger := klog.FromContext(ctx)
	if serverDat.NominalProvidingPod == nil {
		logger.V(5).Info("Building server-providing pod from patch", "patch", rawTmpl)
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
			Spec: *utils.DeIndividualize(reqPod.Spec.DeepCopy()),
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

		cIdx, serverPort, err := utils.GetInferenceServerContainerIndexAndPort(pod)
		if err != nil {
			return nil, "", err
		}
		serverDat.ServerPort = serverPort
		isCtr := &pod.Spec.Containers[cIdx]

		// ensure the value of CUDA_VISIBLE_DEVICES envar for the inference server container
		if ev := utils.SliceGetByFeature(isCtr.Env, EnvVarName, "CUDA_VISIBLE_DEVICES"); ev == nil {
			isCtr.Env = append(isCtr.Env, corev1.EnvVar{
				Name:  "CUDA_VISIBLE_DEVICES",
				Value: *serverDat.GPUIndicesStr,
			})
		} else {
			ev.Value = *serverDat.GPUIndicesStr
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
		pod.Finalizers = append(pod.Finalizers, providerFinalizer)
		pod.Annotations = utils.MapSet(pod.Annotations, nominalHashAnnotationKey, nominalHash)
		pod.Annotations[requesterAnnotationKey] = string(reqPod.UID) + " " + reqPod.Name
		pod.Annotations[api.AcceleratorsAnnotationName] = *serverDat.GPUIDsStr
		pod.Labels = utils.MapSet(pod.Labels, api.DualLabelName, reqPod.Name)
		pod.Labels[api.SleepingLabelName] = "false"
		serverDat.NominalProvidingPod = pod
		serverDat.NominalProvidingPodHash = nominalHash
	}
	return serverDat.NominalProvidingPod, serverDat.NominalProvidingPodHash, nil
}

// reducedContainerState is the subset of `corev1.ContainerState` that we want to log
type reducedContainerState struct {
	State                corev1.ContainerState
	LastTerminationState corev1.ContainerState
	Ready                bool
	RestartCount         int32
	Started              *bool
}

func (rcs *reducedContainerState) set(from corev1.ContainerStatus) *reducedContainerState {
	*rcs = reducedContainerState{
		State:                from.State,
		LastTerminationState: from.LastTerminationState,
		Ready:                from.Ready,
		RestartCount:         from.RestartCount,
		Started:              from.Started,
	}
	return rcs
}

func getContainerStatus(from *corev1.Pod, containerName string) *corev1.ContainerStatus {
	return utils.SliceGetByFeature(from.Status.ContainerStatuses,
		func(cs *corev1.ContainerStatus) string { return cs.Name },
		containerName)
}

func getReducedInferenceContainerState(from *corev1.Pod) *reducedContainerState {
	if cs := getContainerStatus(from, api.InferenceServerContainerName); cs != nil {
		var ans reducedContainerState
		ans.set(*cs)
		return &ans
	}
	return nil
}

func (ctl *controller) querySleeping(ctx context.Context, iscName string, providingPod *corev1.Pod, serverPort int32) (bool, error) {
	queryURL := fmt.Sprintf("http://%s:%d/is_sleeping", providingPod.Status.PodIP, serverPort)
	var sleepState api.SleepState
	_, err := doHTTP(ctx, "query_sleeping", "GET", queryURL, ctl.httpLatencySecsHistograms.MustCurryWith(prometheus.Labels{"isc_name": iscName}), nil, &sleepState)
	return sleepState.IsSleeping, err
}

func (ctl *controller) accelMemoryIsLowEnough(ctx context.Context, requestingPod *corev1.Pod, serverDat *serverData) error {
	adminPort := requestingPod.Annotations[api.AdminPortAnnotationName]
	if adminPort == "" {
		adminPort = api.AdminPortDefaultValue
	}
	url := fmt.Sprintf("http://%s:%s%s", requestingPod.Status.PodIP, adminPort, stubapi.AcceleratorMemoryQueryPath)
	usageMap := map[string]int64{}
	_, err := doHTTP(ctx, "get_accel_memory_usage", "GET", url, ctl.httpLatencySecsHistograms.MustCurryWith(prometheus.Labels{"isc_name": requestingPod.Annotations[api.InferenceServerConfigAnnotationName]}), nil, &usageMap)
	if err != nil {
		return err
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
// on the server-providing Pod shows the given user errors.
// The returned (err error, retry bool) is a convenient match for the signature of
// a sync function; always `retry == (err != nil)`.
func (ctl *controller) ensureReqStatus(ctx context.Context, requestingPod *corev1.Pod, serverDat *serverData, errors ...string) (error, bool) {
	return ctl.ensureReqState(ctx, requestingPod, serverDat, false, false, errors...)
}

// ensureReqState makes the API call if necessary to:
// 1. set the controller's reported state to consist of the given errors;
// 2. add or remove the controller's finalizer if stipulated.
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
	oldStatusStr := requestingPod.Annotations[api.StatusAnnotationName]
	newFinalizers := requestingPod.Finalizers
	if removeFinalizer {
		newFinalizers, _ = utils.SliceRemoveOnce(newFinalizers, requesterFinalizer)
	} else if addFinalizer {
		newFinalizers = append(newFinalizers, requesterFinalizer)
	}
	desiredAccelerators := ptr.Deref(serverDat.GPUIDsStr, "")
	currentAccelerators := requestingPod.Annotations[api.AcceleratorsAnnotationName]
	desiredInstanceID := ""
	if serverDat.ProvidingPodName != "" {
		desiredInstanceID = serverDat.InstanceID
	}
	if oldStatusStr == newStatusStr && desiredAccelerators == currentAccelerators && len(newFinalizers) == len(requestingPod.Finalizers) && serverDat.ProvidingPodName == requestingPod.Labels[api.DualLabelName] && desiredInstanceID == requestingPod.Labels[api.InstanceLabelName] {
		logger.V(5).Info("No need to update status, accelerators, boundName, instanceID, or finalizers", "serverRequestingPod", requestingPod.Name, "status", status, "accelerators", desiredAccelerators, "boundName", serverDat.ProvidingPodName, "instanceID", desiredInstanceID, "finalizers", requestingPod.Finalizers)
		return nil, false
	}
	requestingPod = requestingPod.DeepCopy()
	requestingPod.Annotations = utils.MapSet(requestingPod.Annotations, api.StatusAnnotationName, newStatusStr)
	requestingPod.Annotations[api.AcceleratorsAnnotationName] = desiredAccelerators
	requestingPod.Finalizers = newFinalizers
	if serverDat.ProvidingPodName != "" {
		requestingPod.Labels = utils.MapSet(requestingPod.Labels, api.DualLabelName, serverDat.ProvidingPodName)
		if serverDat.InstanceID != "" {
			requestingPod.Labels = utils.MapSet(requestingPod.Labels, api.InstanceLabelName, serverDat.InstanceID)
		}
	} else if requestingPod.Labels != nil {
		delete(requestingPod.Labels, api.DualLabelName)
		delete(requestingPod.Labels, api.InstanceLabelName)
	}
	updStart := time.Now()
	echo, err := ctl.coreclient.Pods(requestingPod.Namespace).Update(ctx, requestingPod, metav1.UpdateOptions{FieldManager: ctl.ControllerName})
	updStartStr := updStart.Format(time.RFC3339Nano)
	if err == nil {
		logger.V(2).Info("Set status/finalizers", "serverRequestingPod", requestingPod.Name, "status", status, "accelerators", desiredAccelerators, "boundName", serverDat.ProvidingPodName, "instanceID", desiredInstanceID, "finalizers", requestingPod.Finalizers, "newResourceVersion", echo.ResourceVersion, "k8sCallStartTime", updStartStr)
	} else {
		logger.V(2).Info("Failed to set status/finalizers", "serverRequestingPod", requestingPod.Name, "status", status, "accelerators", desiredAccelerators, "boundName", serverDat.ProvidingPodName, "instanceID", desiredInstanceID, "finalizers", requestingPod.Finalizers, "resourceVersion", requestingPod.ResourceVersion, "k8sCallStartTime", updStartStr, "err", err.Error())
	}
	return err, err != nil
}

var coreScheme *k8sruntime.Scheme
var codecFactory k8sserializer.CodecFactory
var podDecoder k8sruntime.Decoder

func findInstanceState(insts []InstanceState, instanceID string) (*InstanceState, bool) {
	for idx := range insts {
		if insts[idx].InstanceID == instanceID {
			return &insts[idx], true
		}
	}
	return nil, false
}

// syncLauncherInstances queries the launcher pod for its current instances,
// updates the controller's internal launcherData state, and returns the fresh
// launcher response used for the update.
// instancesDeleted may be nil, in which case the deleted instance IDs are not reported.
func (ctl *controller) syncLauncherInstances(ctx context.Context, nodeDat *nodeData, instancesDeleted sets.Set[string], launcherPod *corev1.Pod) (*launcherSyncResult, error, bool) {
	logger := klog.FromContext(ctx)

	if launcherPod.Status.PodIP == "" || !utils.IsPodReady(launcherPod) {
		logger.V(5).Info("Launcher pod not ready yet, waiting for another Pod event", "name", launcherPod.Name)
		return nil, nil, false
	}

	launcherBaseURL := fmt.Sprintf("http://%s:%d", launcherPod.Status.PodIP, ctlrcommon.LauncherServicePort)
	lClient, err := NewLauncherClient(launcherBaseURL, ctl.httpLatencySecsHistograms.MustCurryWith(prometheus.Labels{"isc_name": ""}))
	if err != nil {
		logger.Error(err, "Failed to create launcher client")
		return nil, err, true
	}

	insts, err := lClient.ListInstances(ctx)
	if err != nil {
		logger.Error(err, "Failed to list instances from launcher")
		return nil, err, true
	}

	launcherDat := ctl.getLauncherData(nodeDat, launcherPod.Name)

	boundInstanceIDs := sets.New[string]()
	for _, sd := range nodeDat.InferenceServers {
		if sd.ProvidingPodName == launcherPod.Name && sd.InstanceID != "" {
			boundInstanceIDs.Insert(sd.InstanceID)
		}
	}

	newInstances := make(map[string]time.Time)
	remainingInstances := make([]InstanceState, 0, len(insts.Instances))
	stoppedInstanceIDs := sets.New[string]()
	runningCount := 0
	for _, inst := range insts.Instances {
		if inst.Status == InstanceStatusStopped {
			if boundInstanceIDs.Has(inst.InstanceID) {
				// Bound stopped instance — defer deletion so the caller can
				// delete the requesting Pod first (resolves create/delete ambiguity).
				stoppedInstanceIDs.Insert(inst.InstanceID)
				logger.V(2).Info("Found stopped bound instance, deferring cleanup",
					"instanceID", inst.InstanceID)
			} else {
				if instancesDeleted != nil {
					instancesDeleted.Insert(inst.InstanceID)
				}
				_, delErr := lClient.DeleteInstance(ctx, inst.InstanceID)
				if delErr != nil && !IsInstanceNotFoundError(delErr) {
					logger.V(2).Info("Failed to delete stopped instance from launcher during sync",
						"instanceID", inst.InstanceID, "err", delErr)
				} else {
					logger.V(2).Info("Deleted stopped instance from launcher during sync",
						"instanceID", inst.InstanceID)
				}
			}
			continue
		}
		remainingInstances = append(remainingInstances, inst)
		if inst.Status == "running" {
			runningCount++
		}
		if lastUsed, exists := launcherDat.Instances[inst.InstanceID]; exists {
			newInstances[inst.InstanceID] = lastUsed
		} else {
			newInstances[inst.InstanceID] = time.Now()
		}
	}

	// Replace the returned instance list and counts with the filtered view
	// so that callers (e.g. selectOrReclaimLauncherPod) see accurate capacity.
	insts.Instances = remainingInstances
	insts.TotalInstances = len(remainingInstances)
	insts.RunningInstances = runningCount

	launcherDat.Instances = newInstances
	launcherDat.Accurate = true

	logger.V(2).Info("Synced launcher instances",
		"launcherPod", launcherPod.Name,
		"totalInstances", insts.TotalInstances,
		"runningInstances", insts.RunningInstances,
		"instanceCount", len(newInstances),
	)

	return &launcherSyncResult{
		instances:          insts,
		stoppedInstanceIDs: stoppedInstanceIDs,
	}, nil, false
}

func init() {
	coreScheme = k8sruntime.NewScheme()
	err := corev1.AddToScheme(coreScheme)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Failed to corev1.AddToScheme: "+err.Error())
	}
	codecFactory = k8sserializer.NewCodecFactory(coreScheme, k8sserializer.EnableStrict)
	podDecoder = codecFactory.UniversalDecoder(corev1.SchemeGroupVersion)
}

var myHTTPClient = &http.Client{
	Timeout: 30 * time.Second,
}

// ObserverCube is the needed fragment of prometheus.ObserverVec,
// semantically (but not in the type system) specialized to
// needing values for three labels: purpose, method, status_code.
type ObserverCube interface {
	// WithLabelValues takes purpose, method, and status_code
	WithLabelValues(values ...string) prometheus.Observer
}

// Do an HTTP call.
// latencyHistogramVec needs values for labels purpose, method, status_code
func doHTTP(ctx context.Context, purpose, method, url string, latencyHistogramVec ObserverCube, requestData, resultData any) (int, error) {
	var reqBody io.Reader
	if requestData != nil {
		b, err := json.Marshal(requestData)
		if err != nil {
			return 0, fmt.Errorf("failed to marshal request body for %s %q (requestData=%#v): %w", method, url, requestData, err)
		}
		reqBody = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reqBody)
	if err != nil {
		return 0, fmt.Errorf("failed to construct HTTP request to %s %q: %w", method, url, err)
	}
	if requestData != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if resultData != nil {
		req.Header.Set("Accept", "application/json")
	}
	httpCallStartTime := time.Now()
	resp, err := myHTTPClient.Do(req)
	latencySecs := time.Since(httpCallStartTime).Seconds()
	var statusCode int
	if resp != nil {
		statusCode = resp.StatusCode
	}
	if err != nil {
		err = fmt.Errorf("failed to do HTTP %s %q: %w", method, url, err)
	} else {
		defer resp.Body.Close() //nolint:errcheck
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			body, bodyReadErr := io.ReadAll(resp.Body)
			err = fmt.Errorf("HTTP %s %q returned unexpected status %d; bodyReadErr=%v; responseBody=%s", method, url, resp.StatusCode, bodyReadErr, string(body))
		} else if resultData != nil {
			decoder := json.NewDecoder(resp.Body)
			bodyErr := decoder.Decode(resultData)
			if bodyErr != nil {
				err = fmt.Errorf("failed to decode response to %s %q: %w", method, url, bodyErr)
			}
		}
	}
	logger := klog.FromContext(ctx)
	logger.V(5).Info("HTTP call done", "purpose", purpose, "method", method, "url", url, "httpCallStartTime", httpCallStartTime.Format(time.RFC3339Nano), "latencySecs", latencySecs, "statusCode", statusCode, "err", err)
	latencyHistogramVec.WithLabelValues(purpose, method, strconv.FormatInt(int64(statusCode), 10)).Observe(latencySecs)
	return statusCode, err
}

// getGPUUUIDs does the HTTP GET on the given URL to fetch the assigned GPU UUIDs.
func getGPUUUIDs(ctx context.Context, httpLatencySecsHistograms ObserverCube, url string) ([]string, error) {
	var uuids []string
	_, err := doHTTP(ctx, "get_gpu_uuids", "GET", url, httpLatencySecsHistograms, nil, &uuids)
	return uuids, err
}

// findGPUIndices maps GPU UUIDs to GPU indices.
// This func will be moved into the launcher in milestone 3
func (ctl *controller) mapToGPUIndices(nodeName string, gpuUUIDs []string) ([]string, error) {
	gpuMapPtr := ctl.gpuMap.Load()
	if gpuMapPtr == nil {
		return nil, fmt.Errorf("GPU map ConfigMap %s is not available", GPUMapName)
	}
	indices, errs := utils.SliceMap(gpuUUIDs, func(uuid string) (string, error) {
		loc, have := (*gpuMapPtr)[uuid]
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

func TimePtrToStringPtr(tp *metav1.Time) *string {
	if tp == nil {
		return nil
	}
	str := tp.String()
	return &str
}

func ite[T any](cond bool, valTrue, valFalse T) T {
	if cond {
		return valTrue
	}
	return valFalse
}
