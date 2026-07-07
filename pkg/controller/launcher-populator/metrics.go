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

// DefaultStuckThreshold is the default minimum age since scheduling after which
// a scheduled-but-not-yet-Ready launcher is reported in the "stuck" phase. 7.5
// minutes leaves room for slow image pulls while remaining operationally
// relevant.
const DefaultStuckThreshold = 7*time.Minute + 30*time.Second

// DefaultPendingThreshold is the default minimum age since creation after which
// an unscheduled launcher is reported in the "pending" phase. Scheduling does
// not involve a big image pull, so this is much shorter than the stuck
// threshold; 2 minutes still leaves room for autoscaling / node warm-up.
const DefaultPendingThreshold = 2 * time.Minute

// launcherPhase is the value of the "phase" label on fma_launcher_pod_count.
type launcherPhase string

const (
	// phaseBound: the launcher is assigned to a server-requesting Pod.
	phaseBound launcherPhase = "bound"
	// phaseUnbound: the launcher uses the current template, is unbound, and is
	// either Ready or has not yet existed past its pending (unscheduled) or stuck
	// (scheduled) threshold.
	phaseUnbound launcherPhase = "unbound"
	// phasePending: the launcher uses the current template, is unbound, is not
	// Ready, has not been scheduled, and has existed (since creation) past the
	// pending threshold. It is stuck getting scheduled.
	phasePending launcherPhase = "pending"
	// phaseStuck: the launcher uses the current template, is unbound, is not
	// Ready, has been scheduled, and has existed (since scheduling) past the
	// stuck threshold. It is stuck starting up (e.g. a slow image pull).
	phaseStuck launcherPhase = "stuck"
	// phaseStale: the launcher was built from a superseded template.
	phaseStale launcherPhase = "stale"
)

// allLauncherPhases lists every phase value so that, for every LauncherConfig
// whose object exists or that has launcher Pods, the gauge always carries an
// explicit value (including zero) for each phase. This keeps count-based alerts
// from having to distinguish "0" from "no data".
var allLauncherPhases = []launcherPhase{phaseBound, phaseUnbound, phasePending, phaseStuck, phaseStale}

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
			Help:           "Number of launcher Pods by LauncherConfig and phase (bound, unbound, pending, stuck, stale)",
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

// metricsState is the source of truth backing launcherPodCountGauge: it holds
// the per-LauncherConfig launcher tallies and republishes the gauge as they
// change.
type metricsState struct {
	mu sync.Mutex
	// perNode is the phase tally of each Node with launchers, per LauncherConfig.
	perNode map[string]map[string]phaseCounts
	// agg is the per-LauncherConfig sum of perNode across Nodes, kept in sync
	// incrementally so publish need not re-sum every Node.
	agg map[string]phaseCounts
	// lcExists is the set of LauncherConfig names whose object currently exists.
	lcExists map[string]bool
}

func newMetricsState() *metricsState {
	return &metricsState{
		perNode:  map[string]map[string]phaseCounts{},
		agg:      map[string]phaseCounts{},
		lcExists: map[string]bool{},
	}
}

// publish records the phase tally for one (node, LC) key and republishes the
// affected LauncherConfig's gauge series.
func (ms *metricsState) publish(key NodeLauncherKey, counts phaseCounts) {
	lcfg, node := key.LauncherConfigName, key.NodeName
	ms.mu.Lock()
	defer ms.mu.Unlock()

	nodes := ms.perNode[lcfg]
	old := nodes[node]

	// Fold the change into the running aggregate by its delta.
	if counts.total() != 0 || old.total() != 0 {
		agg := ms.agg[lcfg]
		if agg == nil {
			agg = phaseCounts{}
			ms.agg[lcfg] = agg
		}
		for _, phase := range allLauncherPhases {
			agg[phase] += counts[phase] - old[phase]
		}
	}

	// Update the per-Node store, dropping empty Nodes (and the empty LC map).
	if counts.total() == 0 {
		delete(nodes, node)
		if len(nodes) == 0 {
			delete(ms.perNode, lcfg)
		}
	} else {
		if nodes == nil {
			nodes = map[string]phaseCounts{}
			ms.perNode[lcfg] = nodes
		}
		nodes[node] = counts
	}

	ms.republishLocked(lcfg)
}

// setLCExists records whether a LauncherConfig object currently exists and
// republishes its series accordingly.
func (ms *metricsState) setLCExists(lcfg string, exists bool) {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	if exists {
		ms.lcExists[lcfg] = true
	} else {
		delete(ms.lcExists, lcfg)
	}
	ms.republishLocked(lcfg)
}

// republishLocked rewrites one LauncherConfig's gauge series from the current
// aggregate, or deletes them once neither its object nor any launcher remains.
// Callers must hold ms.mu.
func (ms *metricsState) republishLocked(lcfg string) {
	if !ms.lcExists[lcfg] && len(ms.perNode[lcfg]) == 0 {
		delete(ms.agg, lcfg)
		for _, phase := range allLauncherPhases {
			launcherPodCountGauge.Delete(map[string]string{lcfgNameLabel: lcfg, phaseLabel: string(phase)})
		}
		return
	}
	agg := ms.agg[lcfg] // nil when the LC exists but has no launchers -> all zeros
	for _, phase := range allLauncherPhases {
		launcherPodCountGauge.WithLabelValues(lcfg, string(phase)).Set(float64(agg[phase]))
	}
}

// launcherPhaseOf classifies a launcher Pod into a phase (see the launcherPhase
// constants) and, for one still counting down toward pending or stuck, also
// returns the instant at which it would cross that threshold (zero Time if none).
//
// currentTemplateHash is the node-independent template hash the digest wants for
// this Pod's LauncherConfig; an empty value (LC gone / not yet digested) makes
// any templated Pod stale.
func (ctl *controller) launcherPhaseOf(pod *corev1.Pod, currentTemplateHash string, pendingThreshold, stuckThreshold time.Duration, now time.Time) (launcherPhase, time.Time) {
	if bound, _ := ctl.isLauncherBoundToServerRequestingPod(pod); bound {
		return phaseBound, time.Time{}
	}
	if pod.Annotations[common.LauncherTemplateHashAnnotationKey] != currentTemplateHash {
		return phaseStale, time.Time{}
	}
	if utils.IsPodReady(pod) {
		return phaseUnbound, time.Time{}
	}
	// Measure the age from scheduling (so time waiting in the scheduler is not
	// blamed), or from creation when never scheduled (so an unschedulable
	// launcher still surfaces, as pending).
	if cond := utils.GetPodCondition(pod, corev1.PodScheduled); cond != nil && cond.Status == corev1.ConditionTrue {
		return phaseByAge(cond.LastTransitionTime.Time, stuckThreshold, phaseStuck, now)
	}
	return phaseByAge(pod.CreationTimestamp.Time, pendingThreshold, phasePending, now)
}

// phaseByAge returns overduePhase when the launcher has been not-yet-Ready past
// threshold (measured from ref); otherwise it returns phaseUnbound plus the
// future instant ref+threshold at which it would become overdue.
func phaseByAge(ref time.Time, threshold time.Duration, overduePhase launcherPhase, now time.Time) (launcherPhase, time.Time) {
	if now.Sub(ref) > threshold {
		return overduePhase, time.Time{}
	}
	return phaseUnbound, ref.Add(threshold)
}

// computeKeyPhases classifies every launcher Pod of one (node, LC) key and
// returns the per-phase tally plus the earliest future instant at which some
// unbound, not-yet-Ready launcher on the current template would cross into
// pending or stuck (zero Time if none). Pods being deleted are counted (the
// metric counts Pod objects that exist) but never schedule a future transition.
func (ctl *controller) computeKeyPhases(pods []*corev1.Pod, templateHash string, now time.Time) (phaseCounts, time.Time) {
	counts := phaseCounts{}
	var earliestTransition time.Time
	for _, pod := range pods {
		phase, becomesOverdueAt := ctl.launcherPhaseOf(pod, templateHash, ctl.pendingThreshold, ctl.stuckThreshold, now)
		counts[phase]++
		if pod.DeletionTimestamp == nil && becomesOverdueAt.After(now) &&
			(earliestTransition.IsZero() || becomesOverdueAt.Before(earliestTransition)) {
			earliestTransition = becomesOverdueAt
		}
	}
	return counts, earliestTransition
}

// recordLauncherPhases publishes the phase tally for one (node, LC) key and,
// when some launcher will become pending or stuck in the future, schedules a
// re-reconcile of the key at that instant via AddAfter, so the gauge flips
// without a periodic sweep of all launchers.
//
// It is called for every key reconcile (from processKey), independent of the
// key's desired state or whether its Node still exists, so a LauncherConfig's
// series are always kept current and are deleted once it has neither launcher
// Pods nor an existing LauncherConfig object.
func (ctl *controller) recordLauncherPhases(key NodeLauncherKey, pods []*corev1.Pod, templateHash string) {
	now := ctl.clock.Now()
	counts, earliestTransition := ctl.computeKeyPhases(pods, templateHash, now)
	ctl.metrics.publish(key, counts)
	if !earliestTransition.IsZero() {
		ctl.keyQueue.Queue.AddAfter(keyItem{NodeLauncherKey: key}, earliestTransition.Sub(now))
	}
}
