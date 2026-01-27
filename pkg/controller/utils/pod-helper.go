package utils

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/intstr"

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

// GetInferenceServerPort, given a server-providing Pod,
// returns (containerIndex int, port int16, err error)
func GetInferenceServerPort(pod *corev1.Pod) (int, int16, error) {
	// identify the inference server container
	cIdx := slices.IndexFunc(pod.Spec.Containers, func(c corev1.Container) bool {
		return c.Name == api.InferenceServerContainerName
	})
	if cIdx == -1 {
		return 0, 0, fmt.Errorf("container %q not found", api.InferenceServerContainerName)
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

func IsPodReady(pod *corev1.Pod) bool {
	for _, cond := range pod.Status.Conditions {
		if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

// BuildPodFromTemplate creates a pod from a template and assigns it to a node
func BuildPodFromTemplate(template corev1.PodTemplateSpec, ns, nodeName, launcherConfigName string) (*corev1.Pod, error) {
	pod := &corev1.Pod{
		ObjectMeta: template.ObjectMeta,
		Spec:       *DeIndividualize(template.Spec.DeepCopy()),
	}
	pod.Namespace = ns
	// Ensure labels are set
	if pod.Labels == nil {
		pod.Labels = make(map[string]string)
	}
	pod.Labels[common.ComponentLabelKey] = common.LauncherComponentLabelValue
	pod.Labels[common.LauncherGeneratedByLabelKey] = common.LauncherGeneratedByLabelValue
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

	cIdx, serverPort, err := GetInferenceServerPort(pod)
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
				Port:   intstr.FromInt(int(serverPort)),
				Scheme: corev1.URISchemeHTTP,
			},
		},
		InitialDelaySeconds: 10,
		PeriodSeconds:       20,
		TimeoutSeconds:      1,
		SuccessThreshold:    1,
		FailureThreshold:    3,
	}

	// Remove nvidia.com/gpu from resource limits
	removeGPUResourceLimits(container)

	// Remove nvidia.com/gpu from Pod-level resource overhead
	if pod.Spec.Overhead != nil {
		delete(pod.Spec.Overhead, corev1.ResourceName("nvidia.com/gpu"))
	}

	// Assign to specific node
	pod.Spec.NodeName = nodeName
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
