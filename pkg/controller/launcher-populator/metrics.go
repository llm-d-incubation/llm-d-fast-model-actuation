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
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	kubemetrics "k8s.io/component-base/metrics"
	"k8s.io/component-base/metrics/legacyregistry"

	"github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/controller/common"
	"github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/controller/utils"
)

// DefaultStuckThreshold is the default minimum age after which a not-yet-Ready
// launcher is reported in the "stuck" phase. Age is measured from the time the
// launcher was scheduled, or from its creation when it has not been scheduled.
// 7.5 minutes leaves room for slow image pulls while remaining operationally
// relevant.
const DefaultStuckThreshold = 7*time.Minute + 30*time.Second

// launcherPhase is the value of the "phase" label on fma_launcher_pod_count.
type launcherPhase string

const (
	// phaseBound: the launcher is assigned to a server-requesting Pod.
	phaseBound launcherPhase = "bound"
	// phaseUnbound: the launcher uses the current template, is unbound, and is
	// either Ready or has not yet existed past the stuck threshold.
	phaseUnbound launcherPhase = "unbound"
	// phaseStuck: the launcher uses the current template, is unbound, is not
	// Ready, and has existed past the stuck threshold (measured from when it was
	// scheduled, or from creation when it has not been scheduled).
	phaseStuck launcherPhase = "stuck"
	// phaseStale: the launcher was built from a superseded template.
	phaseStale launcherPhase = "stale"
)

// allLauncherPhases lists every phase value so that, for every LauncherConfig
// that has launcher Pods, the gauge always carries an explicit value (including
// zero) for each phase. This keeps count-based alerts from having to
// distinguish "0" from "no data".
var allLauncherPhases = []launcherPhase{phaseBound, phaseUnbound, phaseStuck, phaseStale}

const lcfgNameLabel = "lcfg_name"
const phaseLabel = "phase"

var (
	metricsOnce sync.Once

	// launcherPodCountGauge is a GaugeVec of the number of launcher Pods, labeled
	// by LauncherConfig name and phase. It deliberately carries no Node label:
	// a Node label would make cardinality grow with cluster size.
	launcherPodCountGauge *kubemetrics.GaugeVec
)

// registerMetrics registers the launcher-populator metrics with the k8s legacy
// registry exactly once. Safe to call from multiple NewController invocations.
func registerMetrics() {
	metricsOnce.Do(func() {
		launcherPodCountGauge = kubemetrics.NewGaugeVec(&kubemetrics.GaugeOpts{
			Namespace:      "fma",
			Name:           "launcher_pod_count",
			Help:           "Number of launcher Pods by LauncherConfig and phase (bound, unbound, stuck, stale)",
			StabilityLevel: kubemetrics.ALPHA,
		}, []string{lcfgNameLabel, phaseLabel})
		legacyregistry.MustRegister(launcherPodCountGauge)
	})
}

// phaseCounts tallies launcher Pods by phase.
type phaseCounts map[launcherPhase]int

func (pc phaseCounts) total() int {
	sum := 0
	for _, n := range pc {
		sum += n
	}
	return sum
}

// metricsState is the source of truth backing launcherPodCountGauge. It holds,
// per LauncherConfig, the per-(node, LC) key phase tallies. The gauge is
// aggregated across nodes from this map. Keeping our own state (rather than
// GaugeVec.Reset every period) avoids the scrape-timing splinter that Reset
// creates and lets us delete a LauncherConfig's series only when it truly has
// no launcher Pods left.
type metricsState struct {
	mu      sync.Mutex
	perLcfg map[string]map[NodeLauncherKey]phaseCounts
}

func newMetricsState() *metricsState {
	return &metricsState{perLcfg: map[string]map[NodeLauncherKey]phaseCounts{}}
}

// publish records the phase tally for one (node, LC) key and republishes the
// affected LauncherConfig's gauge series. When a LauncherConfig has no launcher
// Pods on any node, its series are deleted so a removed LauncherConfig does not
// leave stale zero series behind.
func (ms *metricsState) publish(key NodeLauncherKey, counts phaseCounts) {
	lcfg := key.LauncherConfigName
	ms.mu.Lock()
	defer ms.mu.Unlock()

	inner := ms.perLcfg[lcfg]
	if counts.total() == 0 {
		delete(inner, key)
	} else {
		if inner == nil {
			inner = map[NodeLauncherKey]phaseCounts{}
			ms.perLcfg[lcfg] = inner
		}
		inner[key] = counts
	}

	if len(ms.perLcfg[lcfg]) == 0 {
		delete(ms.perLcfg, lcfg)
		for _, phase := range allLauncherPhases {
			launcherPodCountGauge.Delete(map[string]string{lcfgNameLabel: lcfg, phaseLabel: string(phase)})
		}
		return
	}

	agg := phaseCounts{}
	for _, kc := range ms.perLcfg[lcfg] {
		for phase, n := range kc {
			agg[phase] += n
		}
	}
	for _, phase := range allLauncherPhases {
		launcherPodCountGauge.WithLabelValues(lcfg, string(phase)).Set(float64(agg[phase]))
	}
}

// launcherPhaseOf classifies a single launcher Pod into a phase.
// currentTemplateHash is the template hash the digest currently wants for this
// Pod's (node, LC) key; an empty value (LC gone / not yet digested) makes any
// templated Pod classify as stale.
func (ctl *controller) launcherPhaseOf(pod *corev1.Pod, currentTemplateHash string, stuckThreshold time.Duration, now time.Time) launcherPhase {
	if bound, _ := ctl.isLauncherBoundToServerRequestingPod(pod); bound {
		return phaseBound
	}
	if pod.Annotations[common.LauncherTemplateHashAnnotationKey] != currentTemplateHash {
		return phaseStale
	}
	if !utils.IsPodReady(pod) && now.Sub(stuckReferenceTime(pod)) > stuckThreshold {
		return phaseStuck
	}
	return phaseUnbound
}

// stuckReferenceTime returns the instant from which a launcher's "stuck" age is
// measured: the time it was scheduled, when known, else its creation time.
// Measuring from scheduling (rather than creation) avoids blaming a launcher
// for time spent waiting in the scheduler; a launcher that has not been
// scheduled at all is still measured from creation, so a launcher that cannot
// be scheduled is eventually reported as stuck.
func stuckReferenceTime(pod *corev1.Pod) time.Time {
	if cond := utils.GetPodCondition(pod, corev1.PodScheduled); cond != nil && cond.Status == corev1.ConditionTrue {
		return cond.LastTransitionTime.Time
	}
	return pod.CreationTimestamp.Time
}

// computeKeyPhases classifies every launcher Pod of one (node, LC) key and
// returns the per-phase tally plus the earliest future instant at which some
// unbound, not-yet-Ready launcher on the current template would cross the stuck
// threshold (zero Time if none). Pods being deleted are counted (the metric
// counts Pod objects that exist) but never schedule a future transition.
func (ctl *controller) computeKeyPhases(pods []*corev1.Pod, templateHash string, now time.Time) (phaseCounts, time.Time) {
	counts := phaseCounts{}
	var earliestStuck time.Time
	for _, pod := range pods {
		phase := ctl.launcherPhaseOf(pod, templateHash, ctl.stuckThreshold, now)
		counts[phase]++
		if pod.DeletionTimestamp == nil && phase == phaseUnbound && !utils.IsPodReady(pod) {
			becomesStuckAt := stuckReferenceTime(pod).Add(ctl.stuckThreshold)
			if becomesStuckAt.After(now) && (earliestStuck.IsZero() || becomesStuckAt.Before(earliestStuck)) {
				earliestStuck = becomesStuckAt
			}
		}
	}
	return counts, earliestStuck
}

// recordLauncherPhases publishes the phase tally for one (node, LC) key and,
// when some launcher will become "stuck" in the future, schedules a
// re-reconcile of the key at that instant via AddAfter, so the gauge flips to
// "stuck" without a periodic sweep of all launchers.
//
// It is called for every key reconcile (from processKey), independent of the
// key's desired state or whether its Node still exists, so a LauncherConfig's
// series are always kept current and are deleted once it has no launcher Pods.
func (ctl *controller) recordLauncherPhases(key NodeLauncherKey, pods []*corev1.Pod, templateHash string) {
	now := time.Now()
	counts, earliestStuck := ctl.computeKeyPhases(pods, templateHash, now)
	ctl.metrics.publish(key, counts)
	if !earliestStuck.IsZero() {
		ctl.keyQueue.Queue.AddAfter(keyItem{NodeLauncherKey: key}, earliestStuck.Sub(now))
	}
}
