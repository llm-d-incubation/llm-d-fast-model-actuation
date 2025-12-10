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

package node

import (
	"testing"

	fmav1alpha1 "github.com/llm-d-incubation/llm-d-fast-model-actuation/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestFilterNodes(t *testing.T) {
	// 创建测试节点
	nodes := []*corev1.Node{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "node1",
				Labels: map[string]string{
					"node.kubernetes.io/instance-type": "gx3-48x240x2l40s",
					"nvidia.com/gpu.family":            "ada-lovelace",
					"nvidia.com/gpu.product":           "NVIDIA-L40S",
				},
			},
			Status: corev1.NodeStatus{
				Allocatable: corev1.ResourceList{
					"cpu":            resource.MustParse("47"),
					"memory":         resource.MustParse("243293320Ki"),
					"nvidia.com/gpu": resource.MustParse("2"),
				},
				Capacity: corev1.ResourceList{
					"cpu":            resource.MustParse("48"),
					"memory":         resource.MustParse("245760Mi"),
					"nvidia.com/gpu": resource.MustParse("2"),
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "node2",
				Labels: map[string]string{
					"node.kubernetes.io/instance-type": "other-type",
					"nvidia.com/gpu.family":            "other-family",
				},
			},
			Status: corev1.NodeStatus{
				Allocatable: corev1.ResourceList{
					"cpu":            resource.MustParse("16"),
					"memory":         resource.MustParse("65536Mi"),
					"nvidia.com/gpu": resource.MustParse("0"),
				},
			},
		},
	}

	tests := []struct {
		name     string
		selector *fmav1alpha1.EnhancedNodeSelector
		expected []string
	}{
		{
			name: "label selector match",
			selector: &fmav1alpha1.EnhancedNodeSelector{
				LabelSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"nvidia.com/gpu.family": "ada-lovelace",
					},
				},
			},
			expected: []string{"node1"},
		},
		{
			name: "no selector",
			selector: &fmav1alpha1.EnhancedNodeSelector{
				LabelSelector: nil,
			},
			expected: []string{"node1", "node2"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := FilterNodes(nodes, tt.selector)
			if err != nil {
				t.Errorf("FilterNodes() error = %v", err)
				return
			}

			if len(result) != len(tt.expected) {
				t.Errorf("FilterNodes() got %d nodes, want %d", len(result), len(tt.expected))
				return
			}

			for i, node := range result {
				if node.Name != tt.expected[i] {
					t.Errorf("FilterNodes() node[%d] = %v, want %v", i, node.Name, tt.expected[i])
				}
			}
		})
	}
}

func TestMatchesResourceRequirements(t *testing.T) {
	node := &corev1.Node{
		Status: corev1.NodeStatus{
			Allocatable: corev1.ResourceList{
				"cpu":            resource.MustParse("47"),
				"memory":         resource.MustParse("243293320Ki"),
				"nvidia.com/gpu": resource.MustParse("2"),
			},
			Capacity: corev1.ResourceList{
				"cpu":            resource.MustParse("48"),
				"memory":         resource.MustParse("245760Mi"),
				"nvidia.com/gpu": resource.MustParse("2"),
			},
		},
	}

	tests := []struct {
		name     string
		req      *fmav1alpha1.ResourceRequirementSpec
		expected bool
	}{
		{
			name:     "nil requirements",
			req:      nil,
			expected: true,
		},
		{
			name: "match allocatable GPU count",
			req: &fmav1alpha1.ResourceRequirementSpec{
				Allocatable: map[string]string{
					"nvidia.com/gpu": ">=1",
				},
			},
			expected: true,
		},
		{
			name: "no matching allocatable, fallback to capacity",
			req: &fmav1alpha1.ResourceRequirementSpec{
				Allocatable: map[string]string{},
				Capacity: map[string]string{
					"nvidia.com/gpu": "==2",
				},
			},
			expected: true,
		},
		{
			name: "allocatable memory requirement not met",
			req: &fmav1alpha1.ResourceRequirementSpec{
				Allocatable: map[string]string{
					"memory": ">300Gi",
				},
			},
			expected: false,
		},
		{
			name: "capacity CPU requirement met",
			req: &fmav1alpha1.ResourceRequirementSpec{
				Capacity: map[string]string{
					"cpu": "<50",
				},
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := matchesResourceRequirements(node, tt.req)
			if result != tt.expected {
				t.Errorf("matchesResourceRequirements() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestParseCondition(t *testing.T) {
	tests := []struct {
		name        string
		expr        string
		expectedOp  string
		expectedVal string
		expectError bool
	}{
		{
			name:        "greater than or equal",
			expr:        ">=2",
			expectedOp:  ">=",
			expectedVal: "2",
			expectError: false,
		},
		{
			name:        "less than",
			expr:        "<5Gi",
			expectedOp:  "<",
			expectedVal: "5Gi",
			expectError: false,
		},
		{
			name:        "equal with whitespace",
			expr:        " == 4Mi ",
			expectedOp:  "==",
			expectedVal: "4Mi",
			expectError: false,
		},
		{
			name:        "default to equal",
			expr:        "1",
			expectedOp:  "==",
			expectedVal: "1",
			expectError: false,
		},
		{
			name:        "invalid format",
			expr:        "invalid",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := parseCondition(tt.expr)
			if tt.expectError {
				if err == nil {
					t.Errorf("parseCondition() expected error but got none")
				}
				return
			}

			if err != nil {
				t.Errorf("parseCondition() unexpected error: %v", err)
				return
			}

			if result.Op != tt.expectedOp {
				t.Errorf("parseCondition() Op = %v, want %v", result.Op, tt.expectedOp)
			}

			expectedQty := resource.MustParse(tt.expectedVal)
			if result.Val.Cmp(expectedQty) != 0 {
				t.Errorf("parseCondition() Val = %v, want %v", result.Val, expectedQty)
			}
		})
	}
}

func TestCompareQuantities(t *testing.T) {
	tests := []struct {
		name     string
		actual   string
		expected string
		op       string
		result   bool
	}{
		{
			name:     "greater than - true",
			actual:   "5",
			expected: "3",
			op:       ">",
			result:   true,
		},
		{
			name:     "greater than - false",
			actual:   "2",
			expected: "3",
			op:       ">",
			result:   false,
		},
		{
			name:     "equal - true",
			actual:   "3Gi",
			expected: "3072Mi",
			op:       "==",
			result:   true,
		},
		{
			name:     "equal - false",
			actual:   "3Gi",
			expected: "3072",
			op:       "==",
			result:   false,
		},
		{
			name:     "less than or equal - true",
			actual:   "2",
			expected: "3",
			op:       "<=",
			result:   true,
		},
		{
			name:     "less than or equal - also true when equal",
			actual:   "3",
			expected: "3",
			op:       "<=",
			result:   true,
		},
		{
			name:     "not equal - true",
			actual:   "2",
			expected: "3",
			op:       "!=",
			result:   true,
		},
		{
			name:     "not equal - false",
			actual:   "3",
			expected: "3",
			op:       "!=",
			result:   false,
		},
		{
			name:     "invalid operator",
			actual:   "3",
			expected: "3",
			op:       "invalid",
			result:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actualQty := resource.MustParse(tt.actual)
			expectedQty := resource.MustParse(tt.expected)
			result := compareQuantities(actualQty, expectedQty, tt.op)
			if result != tt.result {
				t.Errorf("compareQuantities() = %v, want %v", result, tt.result)
			}
		})
	}
}
