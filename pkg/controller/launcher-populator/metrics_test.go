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

package launcherpopulator

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/controller/common"
)

const testTemplateHash = "hash-current"

// launcherPodBuilder builds a launcher Pod for classification tests.
type launcherPodBuilder struct {
	pod corev1.Pod
}

func newLauncherPod() *launcherPodBuilder {
	return &launcherPodBuilder{pod: corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "launcher-test",
			Annotations: map[string]string{common.LauncherTemplateHashAnnotationKey: testTemplateHash},
		},
	}}
}

func (b *launcherPodBuilder) hash(h string) *launcherPodBuilder {
	b.pod.Annotations[common.LauncherTemplateHashAnnotationKey] = h
	return b
}

func (b *launcherPodBuilder) bound() *launcherPodBuilder {
	b.pod.Annotations[common.RequesterAnnotationKey] = "some-uid requester-name"
	return b
}

func (b *launcherPodBuilder) ready() *launcherPodBuilder {
	b.pod.Status.Conditions = append(b.pod.Status.Conditions, corev1.PodCondition{
		Type:   corev1.PodReady,
		Status: corev1.ConditionTrue,
	})
	return b
}

func (b *launcherPodBuilder) scheduledAt(t time.Time) *launcherPodBuilder {
	b.pod.Status.Conditions = append(b.pod.Status.Conditions, corev1.PodCondition{
		Type:               corev1.PodScheduled,
		Status:             corev1.ConditionTrue,
		LastTransitionTime: metav1.NewTime(t),
	})
	return b
}

func (b *launcherPodBuilder) createdAt(t time.Time) *launcherPodBuilder {
	b.pod.CreationTimestamp = metav1.NewTime(t)
	return b
}

func (b *launcherPodBuilder) build() *corev1.Pod { return &b.pod }

func TestLauncherPhaseOf(t *testing.T) {
	ctl := &controller{}
	const threshold = 7*time.Minute + 30*time.Second
	now := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)
	long := now.Add(-10 * time.Minute) // older than threshold
	short := now.Add(-1 * time.Minute) // younger than threshold

	cases := []struct {
		name string
		pod  *corev1.Pod
		want launcherPhase
	}{
		{
			name: "bound wins over everything",
			pod:  newLauncherPod().bound().hash("stale-hash").scheduledAt(long).build(),
			want: phaseBound,
		},
		{
			name: "stale template hash",
			pod:  newLauncherPod().hash("stale-hash").scheduledAt(short).build(),
			want: phaseStale,
		},
		{
			name: "stuck: current hash, not ready, scheduled long ago",
			pod:  newLauncherPod().scheduledAt(long).build(),
			want: phaseStuck,
		},
		{
			name: "unbound: current hash, ready even if old",
			pod:  newLauncherPod().ready().scheduledAt(long).build(),
			want: phaseUnbound,
		},
		{
			name: "unbound: current hash, not ready but young",
			pod:  newLauncherPod().scheduledAt(short).build(),
			want: phaseUnbound,
		},
		{
			name: "stuck via creation-time fallback when no PodScheduled condition",
			pod:  newLauncherPod().createdAt(long).build(),
			want: phaseStuck,
		},
		{
			name: "unbound: created long ago but scheduled recently (measure from scheduling)",
			pod:  newLauncherPod().createdAt(now.Add(-20 * time.Minute)).scheduledAt(short).build(),
			want: phaseUnbound,
		},
		{
			name: "stale beats stuck: superseded template never reported stuck",
			pod:  newLauncherPod().hash("stale-hash").scheduledAt(long).build(),
			want: phaseStale,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ctl.launcherPhaseOf(tc.pod, testTemplateHash, threshold, now)
			if got != tc.want {
				t.Errorf("launcherPhaseOf() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestLauncherPhaseOfEmptyCurrentHash verifies that when the digest has no
// template hash for a Pod's key (LC gone / not yet digested), a templated Pod
// classifies as stale.
func TestLauncherPhaseOfEmptyCurrentHash(t *testing.T) {
	ctl := &controller{}
	now := time.Now()
	pod := newLauncherPod().scheduledAt(now).build()
	if got := ctl.launcherPhaseOf(pod, "", DefaultStuckThreshold, now); got != phaseStale {
		t.Errorf("launcherPhaseOf() with empty current hash = %q, want %q", got, phaseStale)
	}
}
