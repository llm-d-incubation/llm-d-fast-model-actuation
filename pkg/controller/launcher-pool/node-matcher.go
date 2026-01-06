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

package launcherpool

import (
	fmav1alpha1 "github.com/llm-d-incubation/llm-d-fast-model-actuation/api/fma/v1alpha1"
	corev1 "k8s.io/api/core/v1"
)

// matchesResourceConditions checks if a node's allocatable resources match the specified ranges
// If no resource ranges are specified, match all nodes
func matchesResourceConditions(allocatable corev1.ResourceList, ranges fmav1alpha1.ResourceRanges) bool {
	for resourceName, rr := range ranges {
		// Get the resource quantity from node's allocatable resources
		qty, exists := allocatable[resourceName]
		if !exists {
			// If the node doesn't have this resource, it doesn't match
			return false
		}
		// Check minimum requirement
		if rr.Min != nil && qty.Cmp(*rr.Min) < 0 {
			return false
		}
		// Check maximum requirement
		if rr.Max != nil && qty.Cmp(*rr.Max) > 0 {
			return false
		}
	}
	return true
}
