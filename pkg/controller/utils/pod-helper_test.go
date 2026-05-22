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
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	v1alpha1 "github.com/llm-d-incubation/llm-d-fast-model-actuation/api/fma/v1alpha1"
	"github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/api"
	"github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/controller/common"
)

// makeTemplate returns a representative EmbeddedPodTemplateSpec used as the shared fixture for the determinism / order-independence / node-independence tests.
func makeTemplate() v1alpha1.EmbeddedPodTemplateSpec {
	return v1alpha1.EmbeddedPodTemplateSpec{
		Metadata: v1alpha1.EmbeddedObjectMeta{
			Labels: map[string]string{
				"app":     "launcher",
				"version": "v1",
				"team":    "fma",
			},
			Annotations: map[string]string{
				"note":  "alpha",
				"owner": "tester",
			},
		},
		Spec: corev1.PodSpec{
			NodeSelector: map[string]string{
				"role":     "worker",
				"zone":     "us-west-1",
				"hardware": "gpu",
			},
			Tolerations: []corev1.Toleration{
				{Key: "nvidia.com/gpu", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule},
				{Key: "node.kubernetes.io/not-ready", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoExecute},
			},
			ImagePullSecrets: []corev1.LocalObjectReference{
				{Name: "registry-a"},
				{Name: "registry-b"},
			},
			Volumes: []corev1.Volume{
				{Name: "config", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
				{Name: "cache", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
				{Name: "data", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
			},
			Containers: []corev1.Container{
				{
					Name:  api.InferenceServerContainerName,
					Image: "fake/launcher:latest",
					Env: []corev1.EnvVar{
						{Name: "FOO", Value: "1"},
						{Name: "BAR", Value: "2"},
						{Name: "BAZ", Value: "3"},
					},
					VolumeMounts: []corev1.VolumeMount{
						{Name: "config", MountPath: "/etc/config"},
						{Name: "cache", MountPath: "/var/cache"},
						{Name: "data", MountPath: "/var/data"},
					},
					Ports: []corev1.ContainerPort{
						{Name: "http", ContainerPort: 8080, Protocol: corev1.ProtocolTCP},
						{Name: "metrics", ContainerPort: 9090, Protocol: corev1.ProtocolTCP},
					},
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("1"),
							corev1.ResourceMemory: resource.MustParse("1Gi"),
						},
						Limits: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("2"),
							corev1.ResourceMemory: resource.MustParse("2Gi"),
						},
					},
					ReadinessProbe: &corev1.Probe{},
				},
			},
		},
	}
}

// TestComputeLauncherTemplateHash_Deterministic asserts that the same template hashes identically across repeated calls.
func TestComputeLauncherTemplateHash_Deterministic(t *testing.T) {
	tpl := makeTemplate()

	first, err := ComputeLauncherTemplateHash(tpl)
	if err != nil {
		t.Fatalf("first ComputeLauncherTemplateHash returned error: %v", err)
	}
	if first == "" {
		t.Fatalf("expected non-empty hash, got empty string")
	}

	for i := 0; i < 50; i++ {
		got, err := ComputeLauncherTemplateHash(tpl)
		if err != nil {
			t.Fatalf("iter %d: ComputeLauncherTemplateHash returned error: %v", i, err)
		}
		if got != first {
			t.Fatalf("iter %d: hash drift: got %q, want %q", i, got, first)
		}
	}
}

// TestComputeLauncherTemplateHash_OrderIndependent asserts that reordering order-independent slices (Env / VolumeMounts / Ports / Volumes / Tolerations / ImagePullSecrets) does not change the hash.
func TestComputeLauncherTemplateHash_OrderIndependent(t *testing.T) {
	base := makeTemplate()
	baseHash, err := ComputeLauncherTemplateHash(base)
	if err != nil {
		t.Fatalf("base hash error: %v", err)
	}

	shuffled := makeTemplate()
	c := &shuffled.Spec.Containers[0]
	c.Env = []corev1.EnvVar{
		{Name: "BAZ", Value: "3"},
		{Name: "BAR", Value: "2"},
		{Name: "FOO", Value: "1"},
	}
	c.VolumeMounts = []corev1.VolumeMount{
		{Name: "data", MountPath: "/var/data"},
		{Name: "cache", MountPath: "/var/cache"},
		{Name: "config", MountPath: "/etc/config"},
	}
	c.Ports = []corev1.ContainerPort{
		{Name: "metrics", ContainerPort: 9090, Protocol: corev1.ProtocolTCP},
		{Name: "http", ContainerPort: 8080, Protocol: corev1.ProtocolTCP},
	}
	shuffled.Spec.Volumes = []corev1.Volume{
		{Name: "data", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		{Name: "cache", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		{Name: "config", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
	}
	shuffled.Spec.Tolerations = []corev1.Toleration{
		{Key: "node.kubernetes.io/not-ready", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoExecute},
		{Key: "nvidia.com/gpu", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule},
	}
	shuffled.Spec.ImagePullSecrets = []corev1.LocalObjectReference{
		{Name: "registry-b"},
		{Name: "registry-a"},
	}

	shuffledHash, err := ComputeLauncherTemplateHash(shuffled)
	if err != nil {
		t.Fatalf("shuffled hash error: %v", err)
	}
	if shuffledHash != baseHash {
		t.Fatalf("hash changed under permutation of order-independent slices:\n base = %q\n shuf = %q", baseHash, shuffledHash)
	}
}

// TestComputeLauncherTemplateHash_DifferentContentDiffers asserts that genuine content changes (e.g. image) yield distinct hashes.
func TestComputeLauncherTemplateHash_DifferentContentDiffers(t *testing.T) {
	a := makeTemplate()
	b := makeTemplate()
	b.Spec.Containers[0].Image = "fake/launcher:other"

	ha, err := ComputeLauncherTemplateHash(a)
	if err != nil {
		t.Fatalf("hash a error: %v", err)
	}
	hb, err := ComputeLauncherTemplateHash(b)
	if err != nil {
		t.Fatalf("hash b error: %v", err)
	}
	if ha == hb {
		t.Fatalf("expected distinct hashes for distinct image, both = %q", ha)
	}
}

// TestBuildLauncherPodFromTemplate_NodeIndependentTemplateHash asserts that LauncherTemplateHashAnnotationKey is node-independent while the legacy LauncherConfigHashAnnotationKey differs between nodes.
func TestBuildLauncherPodFromTemplate_NodeIndependentTemplateHash(t *testing.T) {
	tpl := makeTemplate()

	tplHash, err := ComputeLauncherTemplateHash(tpl)
	if err != nil {
		t.Fatalf("ComputeLauncherTemplateHash error: %v", err)
	}

	podA, err := BuildLauncherPodFromTemplate(tpl, "ns", "node-a", "lc1", tplHash)
	if err != nil {
		t.Fatalf("build pod A error: %v", err)
	}
	podB, err := BuildLauncherPodFromTemplate(tpl, "ns", "node-b", "lc1", tplHash)
	if err != nil {
		t.Fatalf("build pod B error: %v", err)
	}

	gotA := podA.Annotations[string(common.LauncherTemplateHashAnnotationKey)]
	gotB := podB.Annotations[string(common.LauncherTemplateHashAnnotationKey)]
	if gotA != tplHash {
		t.Fatalf("podA template-hash annotation = %q, want %q", gotA, tplHash)
	}
	if gotB != tplHash {
		t.Fatalf("podB template-hash annotation = %q, want %q", gotB, tplHash)
	}
	if gotA != gotB {
		t.Fatalf("template-hash should be node-independent, got A=%q B=%q", gotA, gotB)
	}

	legacyA := podA.Annotations[string(common.LauncherConfigHashAnnotationKey)]
	legacyB := podB.Annotations[string(common.LauncherConfigHashAnnotationKey)]
	if legacyA == "" || legacyB == "" {
		t.Fatalf("legacy launcher-config-hash must be set on both pods, got A=%q B=%q", legacyA, legacyB)
	}
	if legacyA == legacyB {
		t.Fatalf("legacy launcher-config-hash must differ between nodes, both = %q", legacyA)
	}
}
