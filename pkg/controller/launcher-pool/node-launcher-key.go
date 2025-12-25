package launcherpool

import (
	"fmt"
	"strings"
)

// NodeLauncherKey defines the unique identifier for a (Node, LauncherConfig) pair
type NodeLauncherKey struct {
	LauncherConfigName      string
	LauncherConfigNamespace string
	NodeName                string
}

// mapToString converts a map of NodeLauncherKey to int32 values into a string representation.
// This function formats the map as a string with the format "{namespace/name/node:count, ...}"
// for debugging and logging purposes.
func mapToString(m map[NodeLauncherKey]int32) string {
	if len(m) == 0 {
		return "{}"
	}
	var result []string
	for k, v := range m {
		result = append(result, fmt.Sprintf("%s/%s/%s:%d", k.LauncherConfigNamespace, k.LauncherConfigName, k.NodeName, v))
	}
	return fmt.Sprintf("{%s}", strings.Join(result, ", "))
}
