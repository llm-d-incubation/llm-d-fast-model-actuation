package launcherpool

import (
	"fmt"
	"strings"
)

// NodeLauncherKey 定义 (Node, LauncherConfig) 对的唯一标识
type NodeLauncherKey struct {
	LauncherConfigName      string
	LauncherConfigNamespace string
	NodeName                string
}

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
