package utils

import (
	"errors"
	"fmt"
	"regexp"
	"slices"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/api"
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
