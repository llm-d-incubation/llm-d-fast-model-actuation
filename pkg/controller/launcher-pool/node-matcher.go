package launcherpool

import (
	fmav1alpha1 "github.com/llm-d-incubation/llm-d-fast-model-actuation/api/fma/v1alpha1"
	corev1 "k8s.io/api/core/v1"
)

// matchesResourceConditions checks if a node's allocatable resources match the specified ranges
func matchesResourceConditions(allocatable corev1.ResourceList, ranges fmav1alpha1.ResourceRanges) bool {
	// If no resource ranges are specified, match all nodes
	if len(ranges) == 0 {
		return true
	}
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
