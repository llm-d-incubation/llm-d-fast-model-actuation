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

	LauncherConfigNameLabelKey = "dual-pods.llm-d.ai/launcher-config-name"

	NodeNameLabelKey = "dual-pods.llm-d.ai/node-name"

	// LauncherConfigHashAnnotationKey is the key of an annotation on a
	// launcher-based server-providing Pod. The value of the annotation is the hash of information
	// that is relevant to identify the launcher-based server-providing Pod, mainly the
	// corresponding LauncherConfig object's PodTemplate that the server-providing Pod uses.
	LauncherConfigHashAnnotationKey = "dual-pods.llm-d.ai/launcher-config-hash"

	// LauncherTemplateHashAnnotationKey is the node-independent template hash on a launcher Pod, used for spec-drift detection.
	LauncherTemplateHashAnnotationKey = "dual-pods.llm-d.ai/launcher-populator-template-hash"

	// LauncherStuckLabelKey is set by the launcher-populator on a launcher Pod
	// that is currently stuck (existing past a configured threshold without
	// becoming Ready, or without ever being scheduled) and has exhausted its
	// retry. Its value is "true". It is findable state that outlives the
	// corresponding Kubernetes Event, so an operator can locate stuck launchers
	// with a label selector:
	//   kubectl get pods -l dual-pods.llm-d.ai/launcher-stuck=true
	// The label denotes the *current* condition: the populator removes it again
	// if the launcher later recovers, so it is never a false positive on a
	// launcher that has since become Ready.
	LauncherStuckLabelKey = "dual-pods.llm-d.ai/launcher-stuck"

	// LauncherStuckLabelValue is the value the launcher-populator sets for
	// LauncherStuckLabelKey.
	LauncherStuckLabelValue = "true"

	// LauncherRetryCountAnnotationKey records how many times the
	// launcher-populator has recreated a launcher for a slot in response to the
	// previous launcher being stuck. It lives in an annotation rather than a
	// label because label volume is a more precious resource, and it survives
	// controller restarts so the single-retry cap holds across them.
	LauncherRetryCountAnnotationKey = "dual-pods.llm-d.ai/launcher-retry-count"

	// LauncherServicePort is the port number on which the launcher exposes its HTTP service
	// for the management of vLLM instances.
	// This is a contract between the controllers and the launcher implementation.
	LauncherServicePort = 8001
)
