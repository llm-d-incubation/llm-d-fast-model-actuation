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

import "slices"

// SliceMap applies a given function (that can return an error) to a slice,
// returning two slices: the successful results and the errors.
func SliceMap[Domain, Range any](slice []Domain, mapFn func(Domain) (Range, error)) ([]Range, []error) {
	var mapped []Range
	var errors []error
	for _, dom := range slice {
		rng, err := mapFn(dom)
		if err == nil {
			mapped = append(mapped, rng)
		} else {
			errors = append(errors, err)
		}
	}
	return mapped, errors
}

// SliceRemoveOnce removes the first occurence of the given element from the given slice.
// This returns a new slice rather than side-effecting the given one.
func SliceRemoveOnce[Elt comparable](slice []Elt, goner Elt) ([]Elt, bool) {
	idx := slices.Index(slice, goner)
	if idx < 0 {
		return slice, false
	}
	return slices.Delete(slice, idx, idx+1), true
}

// MapSet modifies the given map by setting one entry.
// The given map may be `nil`.
// This returns the modified map, which `==` the given one (if it was not `nil`).
func MapSet[Dom comparable, Rng any](urMap map[Dom]Rng, dom Dom, rng Rng) map[Dom]Rng {
	if urMap == nil {
		urMap = map[Dom]Rng{}
	}
	urMap[dom] = rng
	return urMap
}
