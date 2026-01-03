package launcherpool

import (
	"testing"

	fmav1alpha1 "github.com/llm-d-incubation/llm-d-fast-model-actuation/api/fma/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func TestMatchesResourceConditions(t *testing.T) {
	tests := []struct {
		name        string
		allocatable corev1.ResourceList
		ranges      fmav1alpha1.ResourceRanges
		expected    bool
	}{
		{
			name: "empty ranges should match all nodes",
			allocatable: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("2"),
				corev1.ResourceMemory: resource.MustParse("4Gi"),
			},
			ranges:   fmav1alpha1.ResourceRanges{},
			expected: true,
		},
		{
			name: "node missing required resource should not match",
			allocatable: corev1.ResourceList{
				corev1.ResourceCPU: resource.MustParse("2"),
			},
			ranges: fmav1alpha1.ResourceRanges{
				corev1.ResourceMemory: fmav1alpha1.ResourceRange{
					Min: resourcePtr(resource.MustParse("2Gi")),
				},
			},
			expected: false,
		},
		{
			name: "resource below minimum should not match",
			allocatable: corev1.ResourceList{
				corev1.ResourceCPU: resource.MustParse("1"),
			},
			ranges: fmav1alpha1.ResourceRanges{
				corev1.ResourceCPU: fmav1alpha1.ResourceRange{
					Min: resourcePtr(resource.MustParse("2")),
				},
			},
			expected: false,
		},
		{
			name: "resource above maximum should not match",
			allocatable: corev1.ResourceList{
				corev1.ResourceCPU: resource.MustParse("3"),
			},
			ranges: fmav1alpha1.ResourceRanges{
				corev1.ResourceCPU: fmav1alpha1.ResourceRange{
					Max: resourcePtr(resource.MustParse("2")),
				},
			},
			expected: false,
		},
		{
			name: "resource within range should match",
			allocatable: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("2"),
				corev1.ResourceMemory: resource.MustParse("4Gi"),
			},
			ranges: fmav1alpha1.ResourceRanges{
				corev1.ResourceCPU: fmav1alpha1.ResourceRange{
					Min: resourcePtr(resource.MustParse("1")),
					Max: resourcePtr(resource.MustParse("4")),
				},
				corev1.ResourceMemory: fmav1alpha1.ResourceRange{
					Min: resourcePtr(resource.MustParse("2Gi")),
					Max: resourcePtr(resource.MustParse("8Gi")),
				},
			},
			expected: true,
		},
		{
			name: "resource equal to minimum should match",
			allocatable: corev1.ResourceList{
				corev1.ResourceCPU: resource.MustParse("2"),
			},
			ranges: fmav1alpha1.ResourceRanges{
				corev1.ResourceCPU: fmav1alpha1.ResourceRange{
					Min: resourcePtr(resource.MustParse("2")),
				},
			},
			expected: true,
		},
		{
			name: "resource equal to maximum should match",
			allocatable: corev1.ResourceList{
				corev1.ResourceCPU: resource.MustParse("2"),
			},
			ranges: fmav1alpha1.ResourceRanges{
				corev1.ResourceCPU: fmav1alpha1.ResourceRange{
					Max: resourcePtr(resource.MustParse("2")),
				},
			},
			expected: true,
		},
		{
			name: "multiple resources with one out of range should not match",
			allocatable: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("1"), // below min
				corev1.ResourceMemory: resource.MustParse("4Gi"),
			},
			ranges: fmav1alpha1.ResourceRanges{
				corev1.ResourceCPU: fmav1alpha1.ResourceRange{
					Min: resourcePtr(resource.MustParse("2")),
				},
				corev1.ResourceMemory: fmav1alpha1.ResourceRange{
					Min: resourcePtr(resource.MustParse("2Gi")),
				},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := matchesResourceConditions(tt.allocatable, tt.ranges)
			if result != tt.expected {
				t.Errorf("matchesResourceConditions() = %v, expected %v", result, tt.expected)
			}
		})
	}
}

// Helper function to create resource.Quantity pointers
func resourcePtr(r resource.Quantity) *resource.Quantity {
	return &r
}
