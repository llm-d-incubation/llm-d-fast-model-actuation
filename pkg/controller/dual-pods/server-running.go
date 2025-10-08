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
	"fmt"
	"io"
	"net/http"
	"slices"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/api"
)

func GetOwner(pod *corev1.Pod) (infSvrItem, bool) {
	ownerRef := metav1.GetControllerOfNoCopy(pod)
	if ownerRef != nil && ownerRef.Kind == "Pod" {
		return infSvrItem{ownerRef.UID, ownerRef.Name}, true
	}
	return infSvrItem{}, false
}

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
