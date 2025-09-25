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
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/klog/v2"

	"github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/api"
	stubapi "github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/stub/api"
)

func (ctl *controller) processServerRunningPod(ctx context.Context, runningPod *corev1.Pod) (error, bool) {
	logger := klog.FromContext(ctx)
	logger.V(5).Info("Processing server-running pod", "name", runningPod.Name)

	// get the server-requesting pod from the owner reference
	if len(runningPod.OwnerReferences) == 0 {
		logger.V(5).Info("No owner reference found", "name", runningPod.Name)
		return nil, true
	}
	ownerRef := runningPod.OwnerReferences[0]
	requestingPod, err := ctl.podLister.Pods(runningPod.Namespace).Get(ownerRef.Name)
	if err != nil {
		if errors.IsNotFound(err) {
			logger.V(5).Info("server-requesting pod not found", "name", runningPod.Name, "ownerName", ownerRef.Name)
			return nil, true
		}
		logger.Error(err, "Failed to get server-requesting pod", "name", runningPod.Name, "ownerName", ownerRef.Name)
		return err, true
	}

	// relay the readiness
	port := requestingPod.Annotations[api.AdminPortAnnotationName]
	if port == "" {
		port = api.AdminPortAnnotationDefaultValue
	}
	url := fmt.Sprintf("http://%s:%s", requestingPod.Status.PodIP, port)
	if isPodReady(runningPod) {
		logger.V(5).Info("Server-running pod is ready", "name", runningPod.Name)
		url += stubapi.BecomeReadyPath
	} else {
		logger.V(5).Info("Server-running pod is not ready", "name", runningPod.Name)
		url += stubapi.BecomeUnreadyPath
	}
	err = postToReadiness(url)
	if err != nil {
		logger.Error(err, "Failed to relay the readiness", "url", url)
		return err, true
	}
	logger.V(5).Info("Successfully relayed the readiness", "name", runningPod.Name)

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
