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

package common

const (
	RequesterAnnotationKey = "dual-pods.llm-d.ai/requester"

	ComponentLabelKey           = "app.kubernetes.io/component"
	LauncherComponentLabelValue = "launcher"

	LauncherGeneratedByLabelKey   = "dual-pods.llm-d.ai/generated-by"
	LauncherGeneratedByLabelValue = "launcher-populator"

	LauncherConfigNameLabelKey = "dual-pods.llm-d.ai/launcher-config-name"

	NodeNameLabelKey = "dual-pods.llm-d.ai/node-name"

	// LauncherConfigHashAnnotationKey is the key of an annotation on a
	// launcher-based server-providing Pod. The value of the annotation is the hash of information
	// that is relevant to identify the launcher-based server-providing Pod, mainly the
	// corresponding LauncherConfig object's PodTemplate that the server-providing Pod uses.
	LauncherConfigHashAnnotationKey = "dual-pods.llm-d.ai/launcher-config-hash"
)
