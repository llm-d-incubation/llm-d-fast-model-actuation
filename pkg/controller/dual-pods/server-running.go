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
	"context"
	"fmt"
	"io"
	"net/http"
	"slices"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"

	"github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/api"
	stubapi "github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/stub/api"
)

func IsOwnedByRequest(runningPod *corev1.Pod) (string, bool) {
	runningPodName := runningPod.Name
	if suffStart := len(runningPodName) - len(api.ServerRunningPodNameSuffix); suffStart <= 0 || runningPodName[suffStart:] != api.ServerRunningPodNameSuffix {
		return "", false
	} else {
		requestingPodName := runningPodName[:suffStart]
		// get the server-requesting pod from the owner reference
		has := slices.ContainsFunc(runningPod.OwnerReferences, func(r metav1.OwnerReference) bool {
			return r.Kind == "Pod" && r.Name == requestingPodName
		})
		return requestingPodName, has
	}
}

func (ctl *controller) processServerRunningPod(ctx context.Context, runningPod *corev1.Pod) (error, bool) {
	logger := klog.FromContext(ctx)
	logger.V(5).Info("Processing server-running pod", "name", runningPod.Name)

	requestingPodName, has := IsOwnedByRequest(runningPod)
	if !has {
		logger.V(5).Info("Pod is not a server-running Pod", "ref", cache.MetaObjectToName(runningPod))
		return nil, false
	}
	requestingPod, err := ctl.podLister.Pods(runningPod.Namespace).Get(requestingPodName)
	if err != nil {
		if errors.IsNotFound(err) {
			requestingPod = nil
		} else {
			logger.Error(err, "Failed to get server-requesting pod", "name", runningPod.Name, "ownerName", requestingPodName)
			return err, true
		}
	}

	if requestingPod == nil || requestingPod.DeletionTimestamp != nil || runningPod.DeletionTimestamp != nil {
		podOps := ctl.coreclient.Pods(runningPod.Namespace)
		// Deletion requested, so remove finalizer and delete server-requesting Pod
		if index := slices.Index(runningPod.Finalizers, runnerFinalizer); index >= 0 {
			newFinalizers := slices.Delete(runningPod.Finalizers, index, index+1)
			runningPod = runningPod.DeepCopy()
			runningPod.Finalizers = newFinalizers
			echo, err := podOps.Update(ctx, runningPod, metav1.UpdateOptions{FieldManager: ctl.ControllerName})
			if err != nil {
				return fmt.Errorf("failed to remove finalizer from server-running Pod %s (RV %s): %w", runningPod.Name, runningPod.ResourceVersion, err), false
			}
			logger.V(2).Info("Removed finalizer from server-running Pod", "runner", runningPod.Name, "newResourceVersion", echo.ResourceVersion)
		}
		if requestingPod != nil && requestingPod.DeletionTimestamp == nil {
			logger.V(2).Info("Deleting server-requesting Pod because of deletion of server-runningPod", "requester", requestingPod.Name, "runner", runningPod.Name)
			err = podOps.Delete(ctx, requestingPodName, metav1.DeleteOptions{PropagationPolicy: ptr.To(metav1.DeletePropagationBackground)})
		} else {
			err = nil
		}
		return err, false
	}

	// relay the readiness
	port := requestingPod.Annotations[api.AdminPortAnnotationName]
	if port == "" {
		port = api.AdminPortDefaultValue
	}
	url, readiness := fmt.Sprintf("http://%s:%s", requestingPod.Status.PodIP, port), ""
	if isPodReady(runningPod) {
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
	logger.V(5).Info("Successfully relayed the readiness", "name", runningPod.Name, "readiness", readiness)

	logger.V(5).Info("Processed server-running pod", "name", runningPod.Name)
	return nil, false
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
