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
