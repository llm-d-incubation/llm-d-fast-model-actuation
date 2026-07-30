/*
Copyright 2026 The llm-d Authors.

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

package utils

import (
	"slices"
	"testing"
)

func TestSliceFilterKeepsMatchingElementsInOrder(t *testing.T) {
	got := SliceFilter([]int{1, 2, 3, 4}, func(value int) bool {
		return value%2 == 0
	})
	want := []int{2, 4}
	if !slices.Equal(got, want) {
		t.Errorf("SliceFilter result = %v, want %v", got, want)
	}
}

func TestNot1NegatesPredicate(t *testing.T) {
	isOdd := Not1(func(value int) bool {
		return value%2 == 0
	})
	if isOdd(2) {
		t.Error("negated predicate accepted a value accepted by the original")
	}
	if !isOdd(3) {
		t.Error("negated predicate rejected a value rejected by the original")
	}
}
