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
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/component-base/metrics/legacyregistry"
	testingclock "k8s.io/utils/clock/testing"

	"github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/controller/common"
	genctlr "github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/controller/generic"
)

const testTemplateHash = "hash-current"

// podBuilder wraps a launcher Pod for classification tests, configured via
// fluent setters. All timestamps are supplied by tests so classification is
// deterministic.
type podBuilder struct {
	p corev1.Pod
}

func newBuilder() *podBuilder {
	return &podBuilder{p: corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "launcher-test",
			Annotations: map[string]string{common.LauncherTemplateHashAnnotationKey: testTemplateHash},
		},
	}}
}

// hash overrides the template-hash annotation (to model a superseded template).
func (b *podBuilder) hash(h string) *podBuilder {
	b.p.Annotations[common.LauncherTemplateHashAnnotationKey] = h
	return b
}

// noHash removes the template-hash annotation entirely.
func (b *podBuilder) noHash() *podBuilder {
	delete(b.p.Annotations, common.LauncherTemplateHashAnnotationKey)
	return b
}

func (b *podBuilder) bound() *podBuilder {
	b.p.Annotations[common.RequesterAnnotationKey] = "some-uid requester-name"
	return b
}

func (b *podBuilder) ready() *podBuilder {
	b.p.Status.Conditions = append(b.p.Status.Conditions, corev1.PodCondition{
		Type:   corev1.PodReady,
		Status: corev1.ConditionTrue,
	})
	return b
}

func (b *podBuilder) scheduledAt(t time.Time) *podBuilder {
	b.p.Status.Conditions = append(b.p.Status.Conditions, corev1.PodCondition{
		Type:               corev1.PodScheduled,
		Status:             corev1.ConditionTrue,
		LastTransitionTime: metav1.NewTime(t),
	})
	return b
}

func (b *podBuilder) createdAt(t time.Time) *podBuilder {
	b.p.CreationTimestamp = metav1.NewTime(t)
	return b
}

// deleting marks the Pod as terminating (DeletionTimestamp set).
func (b *podBuilder) deleting() *podBuilder {
	dt := metav1.NewTime(time.Date(2026, 6, 21, 11, 0, 0, 0, time.UTC))
	b.p.DeletionTimestamp = &dt
	return b
}

// pod returns the wrapped Pod.
func (b *podBuilder) pod() *corev1.Pod { return &b.p }

// Classification input dimensions, enumerated exhaustively by
// TestLauncherPhaseOf.
type hashKind int

const (
	hashMatches hashKind = iota // annotation equals the current template hash
	hashDiffers                 // annotation present but superseded
	hashAbsent                  // annotation missing
)

type ageKind int

const (
	ageYoung                ageKind = iota // younger than the stuck threshold
	ageOldUnscheduled                      // older than threshold, never scheduled
	ageOldScheduledLongAgo                 // older than threshold, scheduled long ago
	ageOldScheduledRecently                // created long ago but scheduled recently
)

var (
	hashKindNames = map[hashKind]string{hashMatches: "hashMatches", hashDiffers: "hashDiffers", hashAbsent: "hashAbsent"}
	ageKindNames  = map[ageKind]string{ageYoung: "young", ageOldUnscheduled: "oldUnscheduled", ageOldScheduledLongAgo: "oldSchedLongAgo", ageOldScheduledRecently: "oldSchedRecently"}
	// Derived from the name maps (sorted, i.e. iota order) to avoid restating them.
	allHashKinds = sets.List(sets.KeySet(hashKindNames))
	allAgeKinds  = sets.List(sets.KeySet(ageKindNames))
)

// buildClassifyPod constructs a launcher Pod for the given input dimensions.
// It only wires inputs; it does not encode the expected classification.
func buildClassifyPod(now time.Time, bound bool, hk hashKind, ready bool, age ageKind) *corev1.Pod {
	old := now.Add(-10 * time.Minute)   // older than both thresholds
	recent := now.Add(-1 * time.Minute) // younger than both thresholds
	b := newBuilder()                   // defaults to the current template hash
	switch hk {
	case hashDiffers:
		b.hash("superseded")
	case hashAbsent:
		b.noHash()
	}
	if bound {
		b.bound()
	}
	if ready {
		b.ready()
	}
	switch age {
	case ageYoung:
		b.createdAt(recent).scheduledAt(recent)
	case ageOldUnscheduled:
		b.createdAt(old) // no PodScheduled condition
	case ageOldScheduledLongAgo:
		b.createdAt(old).scheduledAt(old)
	case ageOldScheduledRecently:
		b.createdAt(old).scheduledAt(recent)
	}
	return b.pod()
}

// TestLauncherPhaseOf exhaustively covers the cross product of the
// classification inputs: bound/unbound x hash(matches/differs/absent) x
// ready/not x four age buckets (2*3*2*4 = 48). Inputs are generated
// programmatically; expected phases are hand-authored per block (constants for
// the short-circuit blocks, one explicit case per age bucket for the
// age-dependent block) so the oracle never re-implements launcherPhaseOf.
func TestLauncherPhaseOf(t *testing.T) {
	ctl := &controller{}
	const pendingThreshold = 2 * time.Minute
	const stuckThreshold = 7*time.Minute + 30*time.Second
	now := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)

	check := func(t *testing.T, bound bool, hk hashKind, ready bool, age ageKind, want launcherPhase) {
		t.Helper()
		pod := buildClassifyPod(now, bound, hk, ready, age)
		if got, _ := ctl.launcherPhaseOf(pod, testTemplateHash, pendingThreshold, stuckThreshold, now); got != want {
			t.Errorf("bound=%v hash=%s ready=%v age=%s: got %q, want %q",
				bound, hashKindNames[hk], ready, ageKindNames[age], got, want)
		}
	}
	run := func(t *testing.T, bound bool, hk hashKind, ready bool, age ageKind, want launcherPhase) {
		name := fmt.Sprintf("bound=%v/%s/ready=%v/%s", bound, hashKindNames[hk], ready, ageKindNames[age])
		t.Run(name, func(t *testing.T) { check(t, bound, hk, ready, age, want) })
	}

	// Block 1 (24): bound short-circuits every other attribute -> always bound.
	for _, hk := range allHashKinds {
		for _, ready := range []bool{true, false} {
			for _, age := range allAgeKinds {
				run(t, true, hk, ready, age, phaseBound)
			}
		}
	}

	// Block 2 (16): unbound + wrong/absent hash -> always stale.
	for _, hk := range []hashKind{hashDiffers, hashAbsent} {
		for _, ready := range []bool{true, false} {
			for _, age := range allAgeKinds {
				run(t, false, hk, ready, age, phaseStale)
			}
		}
	}

	// Block 3 (4): unbound + matching hash + Ready -> always unbound.
	for _, age := range allAgeKinds {
		run(t, false, hashMatches, true, age, phaseUnbound)
	}

	// Block 4 (4): unbound + matching hash + not Ready -> depends on whether it
	// has been scheduled and for how long. Unscheduled-and-old is pending;
	// scheduled-and-old is stuck; recently scheduled (or young) is still unbound.
	run(t, false, hashMatches, false, ageYoung, phaseUnbound)
	run(t, false, hashMatches, false, ageOldUnscheduled, phasePending)
	run(t, false, hashMatches, false, ageOldScheduledLongAgo, phaseStuck)
	run(t, false, hashMatches, false, ageOldScheduledRecently, phaseUnbound)
}

// TestLauncherPhaseOfEmptyCurrentHash verifies that when the digest has no
// template hash for a Pod's key (LC gone / not yet digested), a templated Pod
// classifies as stale.
func TestLauncherPhaseOfEmptyCurrentHash(t *testing.T) {
	ctl := &controller{}
	now := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)
	pod := newBuilder().scheduledAt(now).pod()
	if got, _ := ctl.launcherPhaseOf(pod, "", DefaultPendingThreshold, DefaultStuckThreshold, now); got != phaseStale {
		t.Errorf("launcherPhaseOf() with empty current hash = %q, want %q", got, phaseStale)
	}
}

// gatheredPhases returns a map from phase to the value currently reported by the
// fma_launcher_pod_count metric for the given LauncherConfig name. It reads the
// registry directly (not WithLabelValues, which would lazily create a missing
// series at 0), so a deleted series is observably absent and an explicit zero is
// observably present.
func gatheredPhases(t *testing.T, lcfg string) map[string]float64 {
	t.Helper()
	mfs, err := legacyregistry.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gathering metrics: %v", err)
	}
	out := map[string]float64{}
	for _, mf := range mfs {
		if mf.GetName() != "fma_launcher_pod_count" {
			continue
		}
		for _, m := range mf.GetMetric() {
			var name, phase string
			for _, lp := range m.GetLabel() {
				switch lp.GetName() {
				case lcfgNameLabel:
					name = lp.GetValue()
				case phaseLabel:
					phase = lp.GetValue()
				}
			}
			if name == lcfg {
				out[phase] = m.GetGauge().GetValue()
			}
		}
	}
	return out
}

// assertLcfgAbsent asserts that no fma_launcher_pod_count series exist for the
// given LauncherConfig (as opposed to existing with zero values).
func assertLcfgAbsent(t *testing.T, lcfg string) {
	t.Helper()
	if got := gatheredPhases(t, lcfg); len(got) != 0 {
		t.Errorf("lcfg %q: want no series, got %v", lcfg, got)
	}
}

// assertLcfgPhases asserts the exact value reported for every phase of one
// LauncherConfig, and that all of those phases (and only them) are present.
func assertLcfgPhases(t *testing.T, lcfg string, bound, unbound, pending, stuck, stale int) {
	t.Helper()
	got := gatheredPhases(t, lcfg)
	want := map[string]float64{
		string(phaseBound):   float64(bound),
		string(phaseUnbound): float64(unbound),
		string(phasePending): float64(pending),
		string(phaseStuck):   float64(stuck),
		string(phaseStale):   float64(stale),
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("lcfg %q phases = %v, want %v", lcfg, got, want)
	}
}

// TestMetricsStatePublish verifies cross-node aggregation, per-phase Set
// (including explicit zeros — assertLcfgPhases proves every phase is present),
// overwrite/re-aggregation, and true deletion of a LauncherConfig's series once
// it has no launcher Pods on any node.
func TestMetricsStatePublish(t *testing.T) {
	registerMetrics()
	launcherPodCountGauge.Reset() // isolate this test's series from others
	ms := newMetricsState()
	const lcfg = "lc-publish-test"
	keyA := NodeLauncherKey{NodeName: "nodeA", LauncherConfigName: lcfg}
	keyB := NodeLauncherKey{NodeName: "nodeB", LauncherConfigName: lcfg}

	// The steps below share ms/gauge state and must run in order.
	t.Run("aggregate across nodes", func(t *testing.T) {
		ms.publish(keyA, phaseCounts{phaseUnbound: 2})
		ms.publish(keyB, phaseCounts{phaseUnbound: 1, phaseStuck: 1})
		// assertLcfgPhases requires every phase present, so bound/pending/stale
		// existing as explicit zeros is part of what this asserts.
		assertLcfgPhases(t, lcfg, 0, 3, 0, 1, 0)
	})

	t.Run("overwrite key re-aggregates", func(t *testing.T) {
		// Overwriting an existing, still-nonzero key must recompute (not
		// accumulate) the aggregate from the replaced counts.
		ms.publish(keyA, phaseCounts{phaseUnbound: 5, phaseBound: 1})
		assertLcfgPhases(t, lcfg, 1, 6, 0, 1, 0)
	})

	t.Run("drop one node keeps lcfg", func(t *testing.T) {
		ms.publish(keyA, phaseCounts{})
		if _, ok := ms.perNode[lcfg][keyA.NodeName]; ok {
			t.Error("nodeA should be removed from perNode after dropping to zero")
		}
		assertLcfgPhases(t, lcfg, 0, 1, 0, 1, 0)
	})

	t.Run("delete last node removes series", func(t *testing.T) {
		// No LauncherConfig object exists here, so once the last node drops the
		// series must be truly gone from the registry (not left as stale zeros).
		ms.publish(keyB, phaseCounts{})
		if _, ok := ms.perNode[lcfg]; ok {
			t.Error("lcfg should be removed from perNode once no node has launchers")
		}
		assertLcfgAbsent(t, lcfg)
	})
}

// TestMetricsStatePublishZeroForAbsentKey covers publishing a zero count for a
// key that was never present (nil inner map): it must not panic and must leave
// no series.
func TestMetricsStatePublishZeroForAbsentKey(t *testing.T) {
	registerMetrics()
	launcherPodCountGauge.Reset()
	ms := newMetricsState()
	ms.publish(NodeLauncherKey{NodeName: "n", LauncherConfigName: "never"}, phaseCounts{})
	if len(ms.perNode) != 0 {
		t.Errorf("perNode should stay empty, got %v", ms.perNode)
	}
	assertLcfgAbsent(t, "never")
}

// TestMetricsStatePublishIndependentLcfgs verifies that two LauncherConfigs do
// not interfere: aggregation is per-lcfg and deleting one leaves the other.
func TestMetricsStatePublishIndependentLcfgs(t *testing.T) {
	registerMetrics()
	launcherPodCountGauge.Reset()
	ms := newMetricsState()
	const lcX, lcY = "lc-x", "lc-y"

	t.Run("both present independently", func(t *testing.T) {
		ms.publish(NodeLauncherKey{NodeName: "n1", LauncherConfigName: lcX}, phaseCounts{phaseUnbound: 2})
		ms.publish(NodeLauncherKey{NodeName: "n1", LauncherConfigName: lcY}, phaseCounts{phaseStuck: 1})
		assertLcfgPhases(t, lcX, 0, 2, 0, 0, 0)
		assertLcfgPhases(t, lcY, 0, 0, 0, 1, 0)
	})

	t.Run("deleting one keeps the other", func(t *testing.T) {
		ms.publish(NodeLauncherKey{NodeName: "n1", LauncherConfigName: lcX}, phaseCounts{})
		if _, ok := ms.perNode[lcY]; !ok {
			t.Error("lcY must survive deletion of lcX")
		}
		assertLcfgAbsent(t, lcX)
		assertLcfgPhases(t, lcY, 0, 0, 0, 1, 0)
	})
}

// TestMetricsStatePublishConcurrent exercises the mutex in publish. Run with
// -race to catch unsynchronized access to perNode / agg / the gauge.
func TestMetricsStatePublishConcurrent(t *testing.T) {
	registerMetrics()
	launcherPodCountGauge.Reset()
	ms := newMetricsState()
	const lcfg = "lc-concurrent"
	const nKeys = 32

	var wg sync.WaitGroup
	for i := 0; i < nKeys; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := NodeLauncherKey{NodeName: fmt.Sprintf("node-%d", i), LauncherConfigName: lcfg}
			ms.publish(key, phaseCounts{phaseUnbound: 1})
		}(i)
	}
	wg.Wait()

	assertLcfgPhases(t, lcfg, 0, nKeys, 0, 0, 0) // every key contributed unbound:1
}

// TestMetricsStateSetLCExists verifies the "series exist while the
// LauncherConfig object exists OR a launcher references it" semantics: the
// series appear as soon as either does and disappear only once neither holds.
func TestMetricsStateSetLCExists(t *testing.T) {
	registerMetrics()
	launcherPodCountGauge.Reset()
	ms := newMetricsState()
	const lcfg = "lc-exists"
	key := NodeLauncherKey{NodeName: "n1", LauncherConfigName: lcfg}

	// Steps share ms/gauge state and must run in order.
	t.Run("LC object with no launchers reports explicit zeros", func(t *testing.T) {
		ms.setLCExists(lcfg, true)
		assertLcfgPhases(t, lcfg, 0, 0, 0, 0, 0)
	})

	t.Run("launchers add on top of the existing zeros", func(t *testing.T) {
		ms.publish(key, phaseCounts{phaseUnbound: 2, phaseStuck: 1})
		assertLcfgPhases(t, lcfg, 0, 2, 0, 1, 0)
	})

	t.Run("deleting the LC object keeps series while a launcher remains", func(t *testing.T) {
		ms.setLCExists(lcfg, false)
		assertLcfgPhases(t, lcfg, 0, 2, 0, 1, 0)
	})

	t.Run("removing the last launcher then drops the series", func(t *testing.T) {
		ms.publish(key, phaseCounts{})
		assertLcfgAbsent(t, lcfg)
	})
}

// TestMetricsStateLCObjectOutlivesLaunchers covers the other order: a launcher's
// series must survive the launcher's own removal as long as the LauncherConfig
// object still exists (reported as explicit zeros), and vanish once it too goes.
func TestMetricsStateLCObjectOutlivesLaunchers(t *testing.T) {
	registerMetrics()
	launcherPodCountGauge.Reset()
	ms := newMetricsState()
	const lcfg = "lc-outlive"
	key := NodeLauncherKey{NodeName: "n1", LauncherConfigName: lcfg}

	ms.setLCExists(lcfg, true)
	ms.publish(key, phaseCounts{phaseUnbound: 1})
	assertLcfgPhases(t, lcfg, 0, 1, 0, 0, 0)

	// Launcher goes away but the LC object still exists -> zeros remain.
	ms.publish(key, phaseCounts{})
	assertLcfgPhases(t, lcfg, 0, 0, 0, 0, 0)

	// LC object deleted too -> series gone.
	ms.setLCExists(lcfg, false)
	assertLcfgAbsent(t, lcfg)
}

// TestComputeKeyPhases covers the two behaviors the maintainer asked for that
// live only in the tallying path: terminating Pods are still counted (#3), and
// the earliest future "stuck" instant is computed for AddAfter scheduling (#1).
func TestComputeKeyPhases(t *testing.T) {
	ctl := &controller{pendingThreshold: 2 * time.Minute, stuckThreshold: 7*time.Minute + 30*time.Second}
	now := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)
	recent := now.Add(-30 * time.Second)
	earlier := now.Add(-1 * time.Minute)
	old := now.Add(-10 * time.Minute)

	t.Run("terminating pod is counted but never scheduled", func(t *testing.T) {
		// Unbound, not-Ready, young — would schedule AddAfter if not terminating.
		pods := []*corev1.Pod{newBuilder().deleting().scheduledAt(recent).pod()}
		counts, earliest := ctl.computeKeyPhases(pods, testTemplateHash, now)
		if counts[phaseUnbound] != 1 {
			t.Errorf("terminating pod not counted: unbound=%d want 1", counts[phaseUnbound])
		}
		if !earliest.IsZero() {
			t.Errorf("terminating pod must not schedule a future transition, got %v", earliest)
		}
	})

	t.Run("earliest future-stuck instant across pods", func(t *testing.T) {
		pods := []*corev1.Pod{
			newBuilder().scheduledAt(recent).pod(),         // stuck at recent+threshold
			newBuilder().scheduledAt(earlier).pod(),        // stuck at earlier+threshold (the winner)
			newBuilder().ready().scheduledAt(recent).pod(), // Ready: never becomes stuck
		}
		counts, earliest := ctl.computeKeyPhases(pods, testTemplateHash, now)
		if counts[phaseUnbound] != 3 {
			t.Errorf("unbound=%d want 3", counts[phaseUnbound])
		}
		want := earlier.Add(ctl.stuckThreshold)
		if !earliest.Equal(want) {
			t.Errorf("earliestStuck=%v want %v", earliest, want)
		}
	})

	t.Run("already-stuck pod does not schedule", func(t *testing.T) {
		pods := []*corev1.Pod{newBuilder().scheduledAt(old).pod()}
		counts, earliest := ctl.computeKeyPhases(pods, testTemplateHash, now)
		if counts[phaseStuck] != 1 {
			t.Errorf("stuck=%d want 1", counts[phaseStuck])
		}
		if !earliest.IsZero() {
			t.Errorf("already-stuck pod must not schedule, got %v", earliest)
		}
	})

	t.Run("empty pod slice", func(t *testing.T) {
		counts, earliest := ctl.computeKeyPhases(nil, testTemplateHash, now)
		if counts.total() != 0 {
			t.Errorf("empty slice total=%d want 0", counts.total())
		}
		if !earliest.IsZero() {
			t.Errorf("empty slice must not schedule, got %v", earliest)
		}
	})

	t.Run("exact-threshold boundary is not yet stuck and not scheduled", func(t *testing.T) {
		// age since scheduling == stuckThreshold exactly. launcherPhaseOf uses
		// strict '>', so it is still unbound; the transition instant == now, and
		// the strict After(now) guard must not schedule (guards against an
		// AddAfter(0) loop).
		atThreshold := now.Add(-ctl.stuckThreshold)
		pods := []*corev1.Pod{newBuilder().scheduledAt(atThreshold).pod()}
		counts, earliest := ctl.computeKeyPhases(pods, testTemplateHash, now)
		if counts[phaseUnbound] != 1 {
			t.Errorf("boundary pod unbound=%d want 1", counts[phaseUnbound])
		}
		if !earliest.IsZero() {
			t.Errorf("boundary pod (transition instant == now) must not schedule, got %v", earliest)
		}
	})

	t.Run("exact-pending-threshold boundary is not yet pending and not scheduled", func(t *testing.T) {
		// Symmetric with the stuck boundary, on the pending track: age since
		// creation == pendingThreshold exactly (never scheduled). Strict '>' keeps
		// it unbound, and the transition instant == now must not schedule.
		atThreshold := now.Add(-ctl.pendingThreshold)
		pods := []*corev1.Pod{newBuilder().createdAt(atThreshold).pod()}
		counts, earliest := ctl.computeKeyPhases(pods, testTemplateHash, now)
		if counts[phaseUnbound] != 1 {
			t.Errorf("boundary pod unbound=%d want 1", counts[phaseUnbound])
		}
		if !earliest.IsZero() {
			t.Errorf("boundary pod (transition instant == now) must not schedule, got %v", earliest)
		}
	})

	t.Run("earliestStuck excludes Ready pods even when they are earliest", func(t *testing.T) {
		// The Ready pod is scheduled earliest, so if the !Ready guard were
		// dropped it would (wrongly) win the minimum. The true answer is the
		// not-Ready pod's later instant.
		earliestSched := now.Add(-3 * time.Minute)
		laterSched := now.Add(-1 * time.Minute)
		pods := []*corev1.Pod{
			newBuilder().ready().scheduledAt(earliestSched).pod(), // Ready: must be skipped
			newBuilder().scheduledAt(laterSched).pod(),            // not Ready: the real winner
		}
		_, earliest := ctl.computeKeyPhases(pods, testTemplateHash, now)
		want := laterSched.Add(ctl.stuckThreshold)
		if !earliest.Equal(want) {
			t.Errorf("earliestStuck=%v want %v (Ready pod must be excluded)", earliest, want)
		}
	})

	t.Run("earliestStuck from unscheduled pod uses creation time and pending threshold", func(t *testing.T) {
		// No PodScheduled condition -> pending track, measured from creation.
		pods := []*corev1.Pod{newBuilder().createdAt(recent).pod()}
		counts, earliest := ctl.computeKeyPhases(pods, testTemplateHash, now)
		if counts[phaseUnbound] != 1 {
			t.Errorf("unbound=%d want 1", counts[phaseUnbound])
		}
		want := recent.Add(ctl.pendingThreshold)
		if !earliest.Equal(want) {
			t.Errorf("earliestStuck=%v want %v (creation time + pending threshold)", earliest, want)
		}
	})

	t.Run("unscheduled past pending threshold counts pending and does not schedule", func(t *testing.T) {
		// Never scheduled and older than the pending threshold -> pending, and
		// already past its transition so it schedules nothing.
		pods := []*corev1.Pod{newBuilder().createdAt(old).pod()}
		counts, earliest := ctl.computeKeyPhases(pods, testTemplateHash, now)
		if counts[phasePending] != 1 {
			t.Errorf("pending=%d want 1", counts[phasePending])
		}
		if !earliest.IsZero() {
			t.Errorf("already-pending pod must not schedule, got %v", earliest)
		}
	})

	t.Run("mixed phases: counts every pod, schedules only qualifying unbound", func(t *testing.T) {
		pods := []*corev1.Pod{
			newBuilder().bound().pod(),                     // bound
			newBuilder().hash("superseded").pod(),          // stale
			newBuilder().scheduledAt(old).pod(),            // stuck (not ready, past threshold)
			newBuilder().scheduledAt(recent).pod(),         // unbound, counting down
			newBuilder().ready().scheduledAt(recent).pod(), // unbound, Ready
		}
		counts, earliest := ctl.computeKeyPhases(pods, testTemplateHash, now)
		if counts.total() != len(pods) {
			t.Errorf("total=%d want %d (every pod must be counted)", counts.total(), len(pods))
		}
		for phase, want := range map[launcherPhase]int{phaseBound: 1, phaseStale: 1, phaseStuck: 1, phaseUnbound: 2} {
			if counts[phase] != want {
				t.Errorf("counts[%s]=%d want %d", phase, counts[phase], want)
			}
		}
		// Only the not-Ready, counting-down unbound pod drives earliestStuck.
		want := recent.Add(ctl.stuckThreshold)
		if !earliest.Equal(want) {
			t.Errorf("earliestStuck=%v want %v", earliest, want)
		}
	})
}

// recordingQueue is a keyQueue stand-in that captures AddAfter calls. Embedding
// the interface leaves all other methods nil; recordLauncherPhases only calls
// AddAfter, so nothing else is exercised.
type recordingQueue struct {
	workqueue.TypedRateLimitingInterface[keyItem]
	added []scheduledItem
}

type scheduledItem struct {
	item  keyItem
	delay time.Duration
}

func (q *recordingQueue) AddAfter(item keyItem, d time.Duration) {
	q.added = append(q.added, scheduledItem{item: item, delay: d})
}

// TestRecordLauncherPhases covers the glue that publishes the tally and, only
// when a launcher is counting down toward stuck, schedules a re-reconcile via
// AddAfter.
func TestRecordLauncherPhases(t *testing.T) {
	registerMetrics()
	const pendingThreshold = 2 * time.Minute
	const stuckThreshold = 7*time.Minute + 30*time.Second
	// A fixed fake clock lets us assert the AddAfter delay exactly.
	base := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)

	setup := func() (*controller, *recordingQueue) {
		launcherPodCountGauge.Reset()
		q := &recordingQueue{}
		ctl := &controller{
			pendingThreshold: pendingThreshold,
			stuckThreshold:   stuckThreshold,
			clock:            testingclock.NewFakeClock(base),
			metrics:          newMetricsState(),
			keyQueue:         &genctlr.QueueAndWorkers[keyItem]{Queue: q},
		}
		return ctl, q
	}

	t.Run("publishes counts and schedules the future stuck transition", func(t *testing.T) {
		ctl, q := setup()
		key := NodeLauncherKey{NodeName: "n1", LauncherConfigName: "lc-rec"}
		// Unbound, not Ready, scheduled exactly at (fake) now -> counts down and
		// becomes stuck exactly stuckThreshold later.
		pods := []*corev1.Pod{newBuilder().scheduledAt(base).pod()}

		ctl.recordLauncherPhases(key, pods, testTemplateHash)

		assertLcfgPhases(t, "lc-rec", 0, 1, 0, 0, 0) // publish happened
		if len(q.added) != 1 {
			t.Fatalf("AddAfter calls = %d, want 1", len(q.added))
		}
		if q.added[0].item != (keyItem{NodeLauncherKey: key}) {
			t.Errorf("scheduled item = %+v, want key %+v", q.added[0].item, key)
		}
		if d := q.added[0].delay; d != stuckThreshold {
			t.Errorf("scheduled delay = %v, want %v", d, stuckThreshold)
		}
	})

	t.Run("does not schedule when nothing is counting down", func(t *testing.T) {
		ctl, q := setup()
		key := NodeLauncherKey{NodeName: "n1", LauncherConfigName: "lc-rec2"}
		// Ready -> unbound but never becomes stuck.
		pods := []*corev1.Pod{newBuilder().ready().scheduledAt(base).pod()}

		ctl.recordLauncherPhases(key, pods, testTemplateHash)

		assertLcfgPhases(t, "lc-rec2", 0, 1, 0, 0, 0)
		if len(q.added) != 0 {
			t.Errorf("AddAfter calls = %d, want 0", len(q.added))
		}
	})
}
