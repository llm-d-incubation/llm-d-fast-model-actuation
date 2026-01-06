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
	"fmt"
)

// NodeLauncherKey defines the unique identifier for a (Node, LauncherConfig) pair
type NodeLauncherKey struct {
	LauncherConfigName string
	NodeName           string
}

func (k NodeLauncherKey) String() string {
	return fmt.Sprintf("%s/%s", k.LauncherConfigName, k.NodeName)
}

// MapToLoggable converts a map of NodeLauncherKey to int32 values into a string representation.
// This function formats the map as a string with the format "{namespace/name/node:count, ...}"
// for debugging and logging purposes.
func MapToLoggable[Key interface {
	comparable
	fmt.Stringer
}, Val any](m map[Key]Val) map[string]any {
	result := make(map[string]any, len(m))
	for k, v := range m {
		result[k.String()] = v
	}
	return result
}
