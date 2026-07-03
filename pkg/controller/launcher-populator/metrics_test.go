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
	"k8s.io/client-go/util/workqueue"
	"k8s.io/component-base/metrics/legacyregistry"

	"github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/controller/common"
	genctlr "github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/controller/generic"
)

const testTemplateHash = "hash-current"

// launcherPodBuilder builds a launcher Pod for classification tests. All
// timestamps are supplied by tests so classification is deterministic.
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

// hash overrides the template-hash annotation (to model a superseded template).
func (b *launcherPodBuilder) hash(h string) *launcherPodBuilder {
	b.pod.Annotations[common.LauncherTemplateHashAnnotationKey] = h
	return b
}

// noHash removes the template-hash annotation entirely.
func (b *launcherPodBuilder) noHash() *launcherPodBuilder {
	delete(b.pod.Annotations, common.LauncherTemplateHashAnnotationKey)
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

// deleting marks the Pod as terminating (DeletionTimestamp set).
func (b *launcherPodBuilder) deleting() *launcherPodBuilder {
	dt := metav1.NewTime(time.Date(2026, 6, 21, 11, 0, 0, 0, time.UTC))
	b.pod.DeletionTimestamp = &dt
	return b
}

func (b *launcherPodBuilder) build() *corev1.Pod { return &b.pod }

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
	allHashKinds  = []hashKind{hashMatches, hashDiffers, hashAbsent}
	allAgeKinds   = []ageKind{ageYoung, ageOldUnscheduled, ageOldScheduledLongAgo, ageOldScheduledRecently}
)

// buildClassifyPod constructs a launcher Pod for the given input dimensions.
// It only wires inputs; it does not encode the expected classification.
func buildClassifyPod(now time.Time, bound bool, hk hashKind, ready bool, age ageKind) *corev1.Pod {
	old := now.Add(-10 * time.Minute)   // older than the 7.5m threshold
	recent := now.Add(-1 * time.Minute) // younger than the threshold
	b := newLauncherPod()               // defaults to the current template hash
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
	return b.build()
}

// TestLauncherPhaseOf exhaustively covers the cross product of the
// classification inputs: bound/unbound x hash(matches/differs/absent) x
// ready/not x four age buckets (2*3*2*4 = 48). Inputs are generated
// programmatically; expected phases are hand-authored per block (constants for
// the short-circuit blocks, an explicit map for the age-dependent block) so the
// oracle never re-implements launcherPhaseOf.
func TestLauncherPhaseOf(t *testing.T) {
	ctl := &controller{}
	const threshold = 7*time.Minute + 30*time.Second
	now := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)

	check := func(t *testing.T, bound bool, hk hashKind, ready bool, age ageKind, want launcherPhase) {
		t.Helper()
		pod := buildClassifyPod(now, bound, hk, ready, age)
		if got := ctl.launcherPhaseOf(pod, testTemplateHash, threshold, now); got != want {
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

	// Block 4 (4): unbound + matching hash + not Ready -> depends on age.
	wantByAge := map[ageKind]launcherPhase{
		ageYoung:                phaseUnbound,
		ageOldUnscheduled:       phaseStuck,
		ageOldScheduledLongAgo:  phaseStuck,
		ageOldScheduledRecently: phaseUnbound,
	}
	for _, age := range allAgeKinds {
		run(t, false, hashMatches, false, age, wantByAge[age])
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

// gatheredPhases returns the fma_launcher_pod_count series currently present
// for one LauncherConfig, as phase->value. It reads the registry directly (not
// WithLabelValues, which would lazily create a missing series at 0), so a
// deleted series is observably absent and an explicit zero is observably
// present.
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

// seriesCountForLcfg returns how many fma_launcher_pod_count series exist for
// the given LauncherConfig.
func seriesCountForLcfg(t *testing.T, lcfg string) int {
	t.Helper()
	return len(gatheredPhases(t, lcfg))
}

// assertLcfgPhases asserts the exact set of phase series (values and presence)
// for one lcfg.
func assertLcfgPhases(t *testing.T, lcfg string, bound, unbound, stuck, stale int) {
	t.Helper()
	got := gatheredPhases(t, lcfg)
	want := map[string]float64{
		string(phaseBound):   float64(bound),
		string(phaseUnbound): float64(unbound),
		string(phaseStuck):   float64(stuck),
		string(phaseStale):   float64(stale),
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("lcfg %q phases = %v, want %v", lcfg, got, want)
	}
}

// TestMetricsStatePublish verifies cross-node aggregation, per-phase Set
// (including explicit zeros — proven by the series count, not by a value read),
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
		// All four phases must be present (count == 4 proves bound/stale exist as
		// explicit zeros, not just that a value read returns 0).
		ms.publish(keyA, phaseCounts{phaseUnbound: 2})
		ms.publish(keyB, phaseCounts{phaseUnbound: 1, phaseStuck: 1})
		if n := seriesCountForLcfg(t, lcfg); n != 4 {
			t.Errorf("series count = %d, want 4 (all phases present incl. explicit zeros)", n)
		}
		assertLcfgPhases(t, lcfg, 0, 3, 1, 0)
	})

	t.Run("overwrite key re-aggregates", func(t *testing.T) {
		// Overwriting an existing, still-nonzero key must recompute (not
		// accumulate) the aggregate from the replaced counts.
		ms.publish(keyA, phaseCounts{phaseUnbound: 5, phaseBound: 1})
		if n := seriesCountForLcfg(t, lcfg); n != 4 {
			t.Errorf("series count = %d, want 4 after overwrite", n)
		}
		assertLcfgPhases(t, lcfg, 1, 6, 1, 0)
	})

	t.Run("drop one node keeps lcfg", func(t *testing.T) {
		ms.publish(keyA, phaseCounts{})
		if _, ok := ms.perLcfg[lcfg][keyA]; ok {
			t.Error("keyA should be removed from perLcfg after dropping to zero")
		}
		if n := seriesCountForLcfg(t, lcfg); n != 4 {
			t.Errorf("series count = %d, want 4 while nodeB still has launchers", n)
		}
		assertLcfgPhases(t, lcfg, 0, 1, 1, 0)
	})

	t.Run("delete last node removes series", func(t *testing.T) {
		// The series must be truly gone from the registry (not left as stale
		// zeros) — the whole point of metricsState — so assert absence via the
		// count, not a value read.
		ms.publish(keyB, phaseCounts{})
		if _, ok := ms.perLcfg[lcfg]; ok {
			t.Error("lcfg should be removed from perLcfg once no node has launchers")
		}
		if n := seriesCountForLcfg(t, lcfg); n != 0 {
			t.Errorf("series count = %d, want 0 (series must be deleted, not left as zeros)", n)
		}
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
	if len(ms.perLcfg) != 0 {
		t.Errorf("perLcfg should stay empty, got %v", ms.perLcfg)
	}
	if n := seriesCountForLcfg(t, "never"); n != 0 {
		t.Errorf("series count = %d, want 0", n)
	}
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
		if n := seriesCountForLcfg(t, lcX); n != 4 {
			t.Errorf("lcX series count = %d, want 4", n)
		}
		if n := seriesCountForLcfg(t, lcY); n != 4 {
			t.Errorf("lcY series count = %d, want 4", n)
		}
		assertLcfgPhases(t, lcX, 0, 2, 0, 0)
		assertLcfgPhases(t, lcY, 0, 0, 1, 0)
	})

	t.Run("deleting one keeps the other", func(t *testing.T) {
		ms.publish(NodeLauncherKey{NodeName: "n1", LauncherConfigName: lcX}, phaseCounts{})
		if _, ok := ms.perLcfg[lcY]; !ok {
			t.Error("lcY must survive deletion of lcX")
		}
		if n := seriesCountForLcfg(t, lcX); n != 0 {
			t.Errorf("lcX series count = %d, want 0 after deletion", n)
		}
		if n := seriesCountForLcfg(t, lcY); n != 4 {
			t.Errorf("lcY series count = %d, want 4 (untouched)", n)
		}
		assertLcfgPhases(t, lcY, 0, 0, 1, 0)
	})
}

// TestMetricsStatePublishConcurrent exercises the mutex in publish. Run with
// -race to catch unsynchronized access to perLcfg / the gauge.
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

	if n := seriesCountForLcfg(t, lcfg); n != 4 {
		t.Errorf("series count = %d, want 4", n)
	}
	assertLcfgPhases(t, lcfg, 0, nKeys, 0, 0) // every key contributed unbound:1
}

// TestComputeKeyPhases covers the two behaviors the maintainer asked for that
// live only in the tallying path: terminating Pods are still counted (#3), and
// the earliest future "stuck" instant is computed for AddAfter scheduling (#1).
func TestComputeKeyPhases(t *testing.T) {
	ctl := &controller{stuckThreshold: 7*time.Minute + 30*time.Second}
	now := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)
	recent := now.Add(-1 * time.Minute)
	older := now.Add(-2 * time.Minute)
	old := now.Add(-10 * time.Minute)

	t.Run("terminating pod is counted but never scheduled", func(t *testing.T) {
		// Unbound, not-Ready, young — would schedule AddAfter if not terminating.
		pods := []*corev1.Pod{newLauncherPod().deleting().scheduledAt(recent).build()}
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
			newLauncherPod().scheduledAt(recent).build(),         // stuck at recent+threshold
			newLauncherPod().scheduledAt(older).build(),          // stuck at older+threshold (earlier)
			newLauncherPod().ready().scheduledAt(recent).build(), // Ready: never becomes stuck
		}
		counts, earliest := ctl.computeKeyPhases(pods, testTemplateHash, now)
		if counts[phaseUnbound] != 3 {
			t.Errorf("unbound=%d want 3", counts[phaseUnbound])
		}
		want := older.Add(ctl.stuckThreshold)
		if !earliest.Equal(want) {
			t.Errorf("earliestStuck=%v want %v", earliest, want)
		}
	})

	t.Run("already-stuck pod does not schedule", func(t *testing.T) {
		pods := []*corev1.Pod{newLauncherPod().scheduledAt(old).build()}
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
		// age since scheduling == threshold exactly. launcherPhaseOf uses strict
		// '>', so it is still unbound; becomesStuckAt == now, and the strict
		// After(now) guard must not schedule (guards against an AddAfter(0) loop).
		atThreshold := now.Add(-ctl.stuckThreshold)
		pods := []*corev1.Pod{newLauncherPod().scheduledAt(atThreshold).build()}
		counts, earliest := ctl.computeKeyPhases(pods, testTemplateHash, now)
		if counts[phaseUnbound] != 1 {
			t.Errorf("boundary pod unbound=%d want 1", counts[phaseUnbound])
		}
		if !earliest.IsZero() {
			t.Errorf("boundary pod (becomesStuckAt==now) must not schedule, got %v", earliest)
		}
	})

	t.Run("earliestStuck excludes Ready pods even when they are earliest", func(t *testing.T) {
		// The Ready pod is scheduled earliest, so if the !Ready guard were
		// dropped it would (wrongly) win the minimum. The true answer is the
		// not-Ready pod's later instant.
		earliestSched := now.Add(-3 * time.Minute)
		laterSched := now.Add(-1 * time.Minute)
		pods := []*corev1.Pod{
			newLauncherPod().ready().scheduledAt(earliestSched).build(), // Ready: must be skipped
			newLauncherPod().scheduledAt(laterSched).build(),            // not Ready: the real winner
		}
		_, earliest := ctl.computeKeyPhases(pods, testTemplateHash, now)
		want := laterSched.Add(ctl.stuckThreshold)
		if !earliest.Equal(want) {
			t.Errorf("earliestStuck=%v want %v (Ready pod must be excluded)", earliest, want)
		}
	})

	t.Run("earliestStuck from unscheduled pod uses creation time", func(t *testing.T) {
		// No PodScheduled condition -> reference is creation time.
		pods := []*corev1.Pod{newLauncherPod().createdAt(recent).build()}
		counts, earliest := ctl.computeKeyPhases(pods, testTemplateHash, now)
		if counts[phaseUnbound] != 1 {
			t.Errorf("unbound=%d want 1", counts[phaseUnbound])
		}
		want := recent.Add(ctl.stuckThreshold)
		if !earliest.Equal(want) {
			t.Errorf("earliestStuck=%v want %v (from creation time)", earliest, want)
		}
	})

	t.Run("mixed phases: counts every pod, schedules only qualifying unbound", func(t *testing.T) {
		pods := []*corev1.Pod{
			newLauncherPod().bound().build(),                     // bound
			newLauncherPod().hash("superseded").build(),          // stale
			newLauncherPod().scheduledAt(old).build(),            // stuck (not ready, past threshold)
			newLauncherPod().scheduledAt(recent).build(),         // unbound, counting down
			newLauncherPod().ready().scheduledAt(recent).build(), // unbound, Ready
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
	const threshold = 7*time.Minute + 30*time.Second

	setup := func() (*controller, *recordingQueue) {
		launcherPodCountGauge.Reset()
		q := &recordingQueue{}
		ctl := &controller{
			stuckThreshold: threshold,
			metrics:        newMetricsState(),
			keyQueue:       &genctlr.QueueAndWorkers[keyItem]{Queue: q},
		}
		return ctl, q
	}

	t.Run("publishes counts and schedules the future stuck transition", func(t *testing.T) {
		ctl, q := setup()
		key := NodeLauncherKey{NodeName: "n1", LauncherConfigName: "lc-rec"}
		// Unbound, not Ready, just scheduled -> counting down.
		pods := []*corev1.Pod{newLauncherPod().scheduledAt(time.Now()).build()}

		ctl.recordLauncherPhases(key, pods, testTemplateHash)

		assertLcfgPhases(t, "lc-rec", 0, 1, 0, 0) // publish happened
		if len(q.added) != 1 {
			t.Fatalf("AddAfter calls = %d, want 1", len(q.added))
		}
		if q.added[0].item != (keyItem{NodeLauncherKey: key}) {
			t.Errorf("scheduled item = %+v, want key %+v", q.added[0].item, key)
		}
		if d := q.added[0].delay; d <= 0 || d > threshold {
			t.Errorf("scheduled delay = %v, want in (0, %v]", d, threshold)
		}
	})

	t.Run("does not schedule when nothing is counting down", func(t *testing.T) {
		ctl, q := setup()
		key := NodeLauncherKey{NodeName: "n1", LauncherConfigName: "lc-rec2"}
		// Ready -> unbound but never becomes stuck.
		pods := []*corev1.Pod{newLauncherPod().ready().scheduledAt(time.Now()).build()}

		ctl.recordLauncherPhases(key, pods, testTemplateHash)

		assertLcfgPhases(t, "lc-rec2", 0, 1, 0, 0)
		if len(q.added) != 0 {
			t.Errorf("AddAfter calls = %d, want 0", len(q.added))
		}
	})
}
