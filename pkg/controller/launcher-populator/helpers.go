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

package launcherpopulator

import (
	"context"
	"fmt"
	"strings"

	"github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/controller/common"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/klog/v2"

	fmav1alpha1 "github.com/llm-d-incubation/llm-d-fast-model-actuation/api/fma/v1alpha1"
)

// nonNilSlice returns a single-element slice if s is non-empty, or nil otherwise.
func nonNilSlice(s string) []string {
	if s == "" {
		return nil
	}
	return []string{s}
}

// getMatchingNodes returns nodes that match the EnhancedNodeSelector.
// It returns three values: the matched nodes, user-facing selector errors (non-nil when the
// LabelSelector itself is malformed — this is a user configuration error), and an internal
// error (non-nil for unexpected infrastructure failures such as lister errors).
// Callers should handle selectorErrs and err independently.
func (ctl *controller) getMatchingNodes(ctx context.Context, selector fmav1alpha1.EnhancedNodeSelector) ([]corev1.Node, []string, error) {
	// Convert the label selector. A failure here is a user error (malformed LabelSelector).
	labelSelector, selectorErr := metav1.LabelSelectorAsSelector(&selector.LabelSelector)
	if selectorErr != nil {
		return nil, []string{fmt.Sprintf("invalid label selector %#v: %v", selector.LabelSelector, selectorErr)}, nil
	}
	nodes, err := ctl.nodeLister.List(labelSelector)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to list nodes using nodeLister: %w", err)
	}

	var matchedNodes []corev1.Node
	for _, node := range nodes {
		if matchesResourceConditions(node.Status.Allocatable, selector.AllocatableResources) {
			matchedNodes = append(matchedNodes, *node)
		}
	}
	return matchedNodes, nil, nil
}

// isLauncherBoundToServerRequestingPod checks if the launcher pod is bound to any server-requesting pod
func (ctl *controller) isLauncherBoundToServerRequestingPod(launcherPod *corev1.Pod) (bool, string) {
	// Check if the launcher pod has annotations indicating assignment to a server-requesting pod
	requesterAnnotationValue, exists := launcherPod.Annotations[common.RequesterAnnotationKey]
	if !exists {
		return false, ""
	}

	// Verify the format of the annotation value: should be "UID name"
	parts := strings.Split(requesterAnnotationValue, " ")
	if len(parts) != 2 {
		return false, "" // Invalid format
	}

	// Optionally verify that the referenced pod actually exists
	// @TODO if need, we can append the check logic in further PR

	return true, parts[1]
}

// listPodsFromCache reads launcher pods from the informer's local cache (cheap).
func (ctl *controller) listPodsFromCache(key NodeLauncherKey) ([]*corev1.Pod, error) {
	launcherLabels := map[string]string{
		common.ComponentLabelKey:          common.LauncherComponentLabelValue,
		common.LauncherConfigNameLabelKey: key.LauncherConfigName,
		common.NodeNameLabelKey:           key.NodeName,
	}
	pods, err := ctl.podLister.List(labels.SelectorFromSet(launcherLabels))
	if err != nil {
		return nil, fmt.Errorf("failed to list pods from cache: %w", err)
	}
	return pods, nil
}

// listPodsFromApiserver queries the apiserver directly for launcher pods.
// This is more expensive than the cache but provides authoritative state.
// Used as a fallback when expectations time out.
func (ctl *controller) listPodsFromApiserver(ctx context.Context, key NodeLauncherKey) ([]*corev1.Pod, error) {
	launcherLabels := map[string]string{
		common.ComponentLabelKey:          common.LauncherComponentLabelValue,
		common.LauncherConfigNameLabelKey: key.LauncherConfigName,
		common.NodeNameLabelKey:           key.NodeName,
	}
	selector := labels.SelectorFromSet(launcherLabels).String()
	podList, err := ctl.coreclient.Pods(ctl.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: selector,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list pods from apiserver: %w", err)
	}
	result := make([]*corev1.Pod, 0, len(podList.Items))
	for i := range podList.Items {
		result = append(result, &podList.Items[i])
	}
	return result, nil
}

// createLaunchers creates the specified number of launcher pods on a node
// using the given node-specific template Pod template.
func (ctl *controller) createLaunchers(ctx context.Context, node *corev1.Node, key NodeLauncherKey, count int, launcherPodTemplate *corev1.Pod) error {
	logger := klog.FromContext(ctx)

	// Create the specified number of launcher pods
	for i := 0; i < count; i++ {

		createdPod, err := ctl.coreclient.Pods(ctl.namespace).Create(ctx, launcherPodTemplate.DeepCopy(), metav1.CreateOptions{})
		if err != nil {
			return fmt.Errorf("failed to create launcher pod: %w", err)
		}
		// Record expectation for this specific Pod UID immediately after creation.
		ctl.expectations.expectCreation(key, createdPod.UID)
		logger.Info("Created launcher pod",
			"pod", createdPod.Name,
			"uid", createdPod.UID,
			"resourceVersion", createdPod.ResourceVersion,
			"node", node.Name)
	}

	return nil
}
