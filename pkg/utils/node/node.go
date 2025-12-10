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
	"fmt"
	"strings"

	fmav1alpha1 "github.com/llm-d-incubation/llm-d-fast-model-actuation/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

func FilterNodes(nodes []*corev1.Node, selector *fmav1alpha1.EnhancedNodeSelector) ([]*corev1.Node, error) {
	var matched []*corev1.Node

	for _, node := range nodes {
		if selector.LabelSelector != nil {
			selector, err := metav1.LabelSelectorAsSelector(selector.LabelSelector)
			if err != nil {
				return nil, fmt.Errorf("invalid label selector: %w", err)
			}
			if !selector.Matches(labels.Set(node.Labels)) {
				continue
			}
		}

		if !matchesResourceRequirements(node, selector.ResourceRequirements) {
			continue
		}

		matched = append(matched, node)
	}

	return matched, nil
}

func matchesResourceRequirements(node *corev1.Node, req *fmav1alpha1.ResourceRequirementSpec) bool {
	if req == nil {
		return true
	}

	// Support allocatable or capacity
	var resourcesToCheck map[corev1.ResourceName]resource.Quantity

	if len(req.Allocatable) > 0 {
		resourcesToCheck = node.Status.Allocatable
	} else if len(req.Capacity) > 0 {
		resourcesToCheck = node.Status.Capacity
	} else {
		return true // no resource conditions
	}

	// Determine which set of rules to use (prefer allocatable, fallback to capacity if not defined)
	rules := req.Allocatable
	if len(rules) == 0 {
		rules = req.Capacity
	}

	for resourceName, expr := range rules {
		condition, err := parseCondition(expr)
		if err != nil {
			// Could log or return error, simplified to not match here
			return false
		}

		actualQty, exists := resourcesToCheck[corev1.ResourceName(resourceName)]
		if !exists {
			// If node doesn't have this resource (e.g. no GPU), treat as 0
			actualQty = resource.MustParse("0")
		}

		if !compareQuantities(actualQty, condition.Val, condition.Op) {
			return false
		}
	}
	return true
}

type Condition struct {
	Op  string // ">", ">=", "==", "<", "<=", "!="
	Val resource.Quantity
}

func parseCondition(expr string) (*Condition, error) {
	expr = strings.TrimSpace(expr)
	ops := []string{">=", "<=", "!=", "==", ">", "<"}

	for _, op := range ops {
		if strings.HasPrefix(expr, op) {
			valStr := strings.TrimSpace(expr[len(op):])
			qty, err := resource.ParseQuantity(valStr)
			if err != nil {
				return nil, fmt.Errorf("invalid quantity in expression %q: %w", expr, err)
			}
			return &Condition{Op: op, Val: qty}, nil
		}
	}
	/// Default to ==
	qty, err := resource.ParseQuantity(expr)
	if err != nil {
		return nil, fmt.Errorf("invalid expression %q: must be like '>=100Mi', '==4', etc", expr)
	}
	return &Condition{Op: "==", Val: qty}, nil
}

func compareQuantities(actual, expected resource.Quantity, op string) bool {
	cmp := actual.Cmp(expected) // returns -1, 0, 1
	switch op {
	case ">":
		return cmp > 0
	case ">=":
		return cmp >= 0
	case "==":
		return cmp == 0
	case "!=":
		return cmp != 0
	case "<":
		return cmp < 0
	case "<=":
		return cmp <= 0
	default:
		return false // should not happen
	}
}
