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
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/intstr"

	v1alpha1 "github.com/llm-d-incubation/llm-d-fast-model-actuation/api/fma/v1alpha1"
	"github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/api"
	"github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/controller/common"
)

var apiAccessRE = regexp.MustCompile(`^kube-api-access-[a-z0-9]+$`)

// PodIsInTrouble is both (a) some container restarts and (b) Pod not ready
func PodIsInTrouble(pod *corev1.Pod) bool {
	var sumRestarts int32
	for _, ctrStat := range pod.Status.ContainerStatuses {
		sumRestarts += ctrStat.RestartCount
	}
	if sumRestarts == 0 {
		return false
	}
	condIdx := slices.IndexFunc(pod.Status.Conditions, func(cond corev1.PodCondition) bool {
		return cond.Type == "Ready"
	})
	if condIdx >= 0 {
		if pod.Status.Conditions[condIdx].Status == corev1.ConditionTrue {
			return false
		}
	}
	return true
}

// DeIndividualize removes the parts of a PodSpec that are specific to an individual.
// This func side-effects the given `*PodSpec` and returns it.
func DeIndividualize(podSpec *corev1.PodSpec) *corev1.PodSpec {
	podSpec.EphemeralContainers = nil // these may not be given in Create
	// The api-access Volume is individualized
	volIdx := slices.IndexFunc(podSpec.Volumes, func(vol corev1.Volume) bool {
		return apiAccessRE.MatchString(vol.Name)
	})
	if volIdx >= 0 {
		volName := podSpec.Volumes[volIdx].Name
		podSpec.Volumes = slices.Delete(podSpec.Volumes, volIdx, volIdx+1)
		for ctrIdx := range podSpec.Containers {
			removeVolumeMount(&podSpec.Containers[ctrIdx], volName)
		}
		for ctrIdx := range podSpec.InitContainers {
			removeVolumeMount(&podSpec.InitContainers[ctrIdx], volName)
		}
	}
	return podSpec
}

func removeVolumeMount(ctr *corev1.Container, volumeName string) {
	mntIdx := slices.IndexFunc(ctr.VolumeMounts, func(mnt corev1.VolumeMount) bool {
		return mnt.Name == volumeName
	})
	if mntIdx >= 0 {
		ctr.VolumeMounts = slices.Delete(ctr.VolumeMounts, mntIdx, mntIdx+1)
	}
}

// GetInferenceServerContainerIndexAndPort returns the index of the inference server container
// and the port for a server-providing Pod.
// This function is for direct (non-launcher-based) server-providing Pods.
// The port is identified from the readinessProbe.
// Returns (containerIndex int, inferenceServerPort int16, err error).
func GetInferenceServerContainerIndexAndPort(pod *corev1.Pod) (int, int16, error) {
	cIdx, err := GetInferenceServerContainerIndex(pod)
	if err != nil {
		return 0, 0, err
	}

	isCtr := &pod.Spec.Containers[cIdx]
	if isCtr.ReadinessProbe == nil {
		return 0, 0, errors.New("the inference server container has no readinessProbe")
	} else if isCtr.ReadinessProbe.HTTPGet == nil {
		return 0, 0, fmt.Errorf("the readinessProbe is not an HTTPGet")
	}
	portIOS := isCtr.ReadinessProbe.HTTPGet.Port
	switch portIOS.Type {
	case intstr.Int:
		return cIdx, int16(portIOS.IntVal), nil
	case intstr.String:
		if portIOS.StrVal == "http" || portIOS.StrVal == "HTTP" {
			return cIdx, 80, nil
		} else {
			return 0, 0, fmt.Errorf("unsupported readinessProbe port %q", portIOS.StrVal)
		}
	default:
		return 0, 0, fmt.Errorf("the readinessProbe port has unexpected type %q", portIOS.Type)
	}
}

// GetInferenceServerContainerIndex returns the index of the inference server container
// in the given Pod's container list.
// This function is for both direct (non-launcher-based) and launcher-based server-providing Pods.
func GetInferenceServerContainerIndex(pod *corev1.Pod) (int, error) {
	cIdx := slices.IndexFunc(pod.Spec.Containers, func(c corev1.Container) bool {
		return c.Name == api.InferenceServerContainerName
	})
	if cIdx == -1 {
		return 0, fmt.Errorf("container %q not found", api.InferenceServerContainerName)
	}
	return cIdx, nil
}

// ValidateLauncherPodTemplate checks whether the given EmbeddedPodTemplateSpec is valid
// for use as a launcher pod template. It returns an error if the template is missing the
// required inference server container. This check does not depend on any node-specific
// information and is safe to call once per LauncherConfig rather than once per node.
func ValidateLauncherPodTemplate(template v1alpha1.EmbeddedPodTemplateSpec) error {
	cIdx := slices.IndexFunc(template.Spec.Containers, func(c corev1.Container) bool {
		return c.Name == api.InferenceServerContainerName
	})
	if cIdx == -1 {
		return fmt.Errorf("container %q not found", api.InferenceServerContainerName)
	}
	return nil
}

func IsPodReady(pod *corev1.Pod) bool {
	for _, cond := range pod.Status.Conditions {
		if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

// BuildLauncherPodFromTemplate creates a launcher pod from a LauncherConfig object's
// Spec.PodTemplate and assigns the built launcher pod to a node
func BuildLauncherPodFromTemplate(template v1alpha1.EmbeddedPodTemplateSpec, ns, nodeName, launcherConfigName string) (*corev1.Pod, error) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Labels:      maps.Clone(template.Metadata.Labels),
			Annotations: maps.Clone(template.Metadata.Annotations),
		},
		Spec: *DeIndividualize(template.Spec.DeepCopy()),
	}
	pod.Namespace = ns
	pod.GenerateName = fmt.Sprintf("launcher-%s-", launcherConfigName)
	// Ensure labels are set
	if pod.Labels == nil {
		pod.Labels = make(map[string]string)
	}
	pod.Labels[common.ComponentLabelKey] = common.LauncherComponentLabelValue
	pod.Labels[common.LauncherConfigNameLabelKey] = launcherConfigName
	pod.Labels[common.NodeNameLabelKey] = nodeName
	pod.Labels[api.SleepingLabelName] = "false"

	hasher := sha256.New()
	modifiedJSON, _ := json.Marshal(pod)
	hasher.Write(modifiedJSON)
	hasher.Write([]byte(";gpus="))
	hasher.Write([]byte("all")) //@TODO will be refined
	hasher.Write([]byte(";node="))
	hasher.Write([]byte(nodeName))
	var modifiedHash [sha256.Size]byte
	modifiedHashSl := hasher.Sum(modifiedHash[:0])
	nominalHash := base64.RawStdEncoding.EncodeToString(modifiedHashSl)

	if pod.Annotations == nil {
		pod.Annotations = make(map[string]string)
	}
	pod.Annotations = MapSet(pod.Annotations, string(common.LauncherConfigHashAnnotationKey), nominalHash)

	cIdx, err := GetInferenceServerContainerIndex(pod)
	if err != nil {
		return nil, err
	}
	container := &pod.Spec.Containers[cIdx]

	// Configure required environment variables
	configureRequiredEnvVars(container)

	// Set fixed liveness probe
	container.LivenessProbe = &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{
				Path:   "/health",
				Port:   intstr.FromInt(common.LauncherServicePort),
				Scheme: corev1.URISchemeHTTP,
			},
		},
		InitialDelaySeconds: 10,
		PeriodSeconds:       20,
		TimeoutSeconds:      1,
		SuccessThreshold:    1,
		FailureThreshold:    3,
	}

	// Set readiness probe to check if launcher can list instances.
	// This is necessary because otherwise the dual-pods controller will be confused when
	// the launcher Pod is said to be ready but got refused when listing its vLLM instances.
	container.ReadinessProbe = &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{
				Path:   "/v2/vllm/instances",
				Port:   intstr.FromInt(common.LauncherServicePort),
				Scheme: corev1.URISchemeHTTP,
			},
		},
		InitialDelaySeconds: 2,
		PeriodSeconds:       5,
		TimeoutSeconds:      2,
		SuccessThreshold:    1,
		FailureThreshold:    3,
	}

	// Remove nvidia.com/gpu from resource limits
	removeGPUResourceLimits(container)

	// Remove nvidia.com/gpu from Pod-level resource overhead
	if pod.Spec.Overhead != nil {
		delete(pod.Spec.Overhead, corev1.ResourceName("nvidia.com/gpu"))
	}

	if pod.Spec.NodeSelector == nil {
		pod.Spec.NodeSelector = make(map[string]string)
	}
	pod.Spec.NodeSelector["kubernetes.io/hostname"] = nodeName
	addLauncherNotifierSidecar(pod, container.Image, container.ImagePullPolicy)
	return pod, nil
}

// configureRequiredEnvVars adds or updates required environment variables
func configureRequiredEnvVars(container *corev1.Container) {
	envVars := map[string]string{
		"PYTHONPATH":                 "/app",
		"NVIDIA_VISIBLE_DEVICES":     "all",
		"NVIDIA_DRIVER_CAPABILITIES": "compute,utility",
		"VLLM_SERVER_DEV_MODE":       "1",
	}

	// Create a mapping of existing environment variables for easy lookup
	existingEnv := make(map[string]*corev1.EnvVar)
	for i := range container.Env {
		envVar := &container.Env[i]
		existingEnv[envVar.Name] = envVar
	}

	// Add or update required environment variables
	for envName, envValue := range envVars {
		if envVar, exists := existingEnv[envName]; exists {
			// If it already exists, update its value
			envVar.Value = envValue
		} else {
			// If it doesn't exist, add a new environment variable
			container.Env = append(container.Env, corev1.EnvVar{
				Name:  envName,
				Value: envValue,
			})
		}
	}
}

// removeGPUResourceLimits removes nvidia.com/gpu from container resource limits and requests
func removeGPUResourceLimits(container *corev1.Container) {
	if container.Resources.Limits != nil {
		container.Resources.Limits[corev1.ResourceName("nvidia.com/gpu")] = resource.MustParse("0")
	}
	if container.Resources.Requests != nil {
		container.Resources.Requests[corev1.ResourceName("nvidia.com/gpu")] = resource.MustParse("0")
	}
}

func addLauncherNotifierSidecar(pod *corev1.Pod, launcherImage string, pullPolicy corev1.PullPolicy) {
	const sidecarName = "state-change-reflector"
	idx := slices.IndexFunc(pod.Spec.Containers, func(c corev1.Container) bool {
		return c.Name == sidecarName
	})

	notifier := corev1.Container{
		Name:            sidecarName,
		Image:           launcherImage,
		ImagePullPolicy: pullPolicy,
		Command:         []string{"python3", "/app/launcher_pod_notifier.py"},
		Env: []corev1.EnvVar{
			{
				Name:  "LAUNCHER_BASE_URL",
				Value: fmt.Sprintf("http://127.0.0.1:%d", common.LauncherServicePort),
			},
			{
				Name: "POD_NAME",
				ValueFrom: &corev1.EnvVarSource{
					FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"},
				},
			},
			{
				Name: "NAMESPACE",
				ValueFrom: &corev1.EnvVarSource{
					FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.namespace"},
				},
			},
		},
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("10m"),
				corev1.ResourceMemory: resource.MustParse("64Mi"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("100m"),
				corev1.ResourceMemory: resource.MustParse("128Mi"),
			},
		},
	}

	if idx >= 0 {
		pod.Spec.Containers[idx] = notifier
		return
	}
	pod.Spec.Containers = append(pod.Spec.Containers, notifier)
}
