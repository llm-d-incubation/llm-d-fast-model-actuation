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
	"k8s.io/apimachinery/pkg/util/sets"
	kubemetrics "k8s.io/component-base/metrics"
	"k8s.io/component-base/metrics/legacyregistry"

	"github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/controller/common"
	"github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/controller/utils"
)

// DefaultStuckStartingThreshold is the default minimum age since scheduling
// after which a scheduled-but-not-yet-Ready launcher is reported in the
// "stuck_starting" phase. 7.5 minutes leaves room for slow image pulls while
// remaining operationally relevant.
const DefaultStuckStartingThreshold = 7*time.Minute + 30*time.Second

// DefaultStuckSchedulingThreshold is the default minimum age since creation
// after which an unscheduled launcher is reported in the "stuck_scheduling"
// phase. Scheduling does not involve a big image pull, so this is much shorter
// than the stuck-starting threshold; 2 minutes still leaves room for
// autoscaling / node warm-up.
const DefaultStuckSchedulingThreshold = 2 * time.Minute

// launcherPhase is the value of the "phase" label on fma_launcher_pod_count.
type launcherPhase string

const (
	// phaseBound: the launcher is assigned to a server-requesting Pod.
	phaseBound launcherPhase = "bound"
	// phaseUnbound: the launcher uses the current template, is unbound, and is
	// either Ready or has not yet reached its stuck-scheduling (unscheduled) or
	// stuck-starting (scheduled) threshold.
	phaseUnbound launcherPhase = "unbound"
	// phaseStuckScheduling: the launcher uses the current template, is unbound,
	// has not been scheduled, and has reached its stuck-scheduling threshold.
	phaseStuckScheduling launcherPhase = "stuck_scheduling"
	// phaseStuckStarting: the launcher uses the current template, is unbound, is
	// not Ready, has been scheduled, and has reached its stuck-starting threshold
	// (e.g. due to a slow image pull).
	phaseStuckStarting launcherPhase = "stuck_starting"
	// phaseStale: the launcher was built from a superseded template.
	phaseStale launcherPhase = "stale"
)

// isStuck reports whether the phase is one of the two stuck phases the
// populator acts on (labels, Events, and single retry).
func (p launcherPhase) isStuck() bool {
	return p == phaseStuckScheduling || p == phaseStuckStarting
}

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
			Help:           "Number of launcher Pods by LauncherConfig and phase (bound, unbound, stuck_scheduling, stuck_starting, stale)",
			StabilityLevel: kubemetrics.ALPHA,
		}, []string{lcfgNameLabel, phaseLabel})
		legacyregistry.MustRegister(launcherPodCountGauge)
	})
}

// phaseCounts tallies launcher Pods by phase. Keeping each phase as a field
// makes unsupported phases unrepresentable in metricsState.
type phaseCounts struct {
	bound           int
	unbound         int
	stuckScheduling int
	stuckStarting   int
	stale           int
}

// phaseCountsByNode stores phase tallies indexed by Node name.
type phaseCountsByNode map[string]phaseCounts

func (pc phaseCounts) total() int {
	return pc.bound + pc.unbound + pc.stuckScheduling + pc.stuckStarting + pc.stale
}

func (pc *phaseCounts) addDelta(counts, oldCounts phaseCounts) {
	pc.bound += counts.bound - oldCounts.bound
	pc.unbound += counts.unbound - oldCounts.unbound
	pc.stuckScheduling += counts.stuckScheduling - oldCounts.stuckScheduling
	pc.stuckStarting += counts.stuckStarting - oldCounts.stuckStarting
	pc.stale += counts.stale - oldCounts.stale
}

func (pc *phaseCounts) increment(phase launcherPhase) {
	switch phase {
	case phaseBound:
		pc.bound++
	case phaseUnbound:
		pc.unbound++
	case phaseStuckScheduling:
		pc.stuckScheduling++
	case phaseStuckStarting:
		pc.stuckStarting++
	case phaseStale:
		pc.stale++
	default:
		panic("unsupported launcher phase: " + string(phase))
	}
}

// forEach calls yield once for every supported metric phase, including phases
// whose count is zero.
func (pc phaseCounts) forEach(yield func(launcherPhase, int)) {
	yield(phaseBound, pc.bound)
	yield(phaseUnbound, pc.unbound)
	yield(phaseStuckScheduling, pc.stuckScheduling)
	yield(phaseStuckStarting, pc.stuckStarting)
	yield(phaseStale, pc.stale)
}

// metricsState is the source of truth backing launcherPodCountGauge: it holds
// the per-LauncherConfig launcher tallies and republishes the gauge as they
// change.
type metricsState struct {
	mu sync.Mutex
	// perLCFG is indexed first by LauncherConfig name, then by Node name.
	perLCFG map[string]phaseCountsByNode
	// agg is the per-LauncherConfig sum across Nodes, kept in sync incrementally
	// so publish need not re-sum every Node.
	agg map[string]phaseCounts
	// lcExists is the set of LauncherConfig names whose object currently exists.
	lcExists sets.Set[string]
}

func newMetricsState() *metricsState {
	return &metricsState{
		perLCFG:  map[string]phaseCountsByNode{},
		agg:      map[string]phaseCounts{},
		lcExists: sets.New[string](),
	}
}

// publish records the phase tally for one (node, LC) key and republishes the
// affected LauncherConfig's gauge series.
func (ms *metricsState) publish(key NodeLauncherKey, counts phaseCounts) {
	lcfg, node := key.LauncherConfigName, key.NodeName
	ms.mu.Lock()
	defer ms.mu.Unlock()

	countsByNode := ms.perLCFG[lcfg]
	oldCounts := countsByNode[node]

	// Fold the change into the running aggregate by its delta.
	if counts.total() != 0 || oldCounts.total() != 0 {
		agg := ms.agg[lcfg]
		agg.addDelta(counts, oldCounts)
		ms.agg[lcfg] = agg
	}

	// Update the per-Node store, dropping empty Nodes (and the empty LC map).
	if counts.total() == 0 {
		delete(countsByNode, node)
		if len(countsByNode) == 0 {
			delete(ms.perLCFG, lcfg)
		}
	} else {
		if countsByNode == nil {
			countsByNode = phaseCountsByNode{}
			ms.perLCFG[lcfg] = countsByNode
		}
		countsByNode[node] = counts
	}

	ms.republishLocked(lcfg)
}

// setLCExists records whether a LauncherConfig object currently exists and
// republishes its series accordingly.
func (ms *metricsState) setLCExists(lcfg string, exists bool) {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	if exists {
		ms.lcExists.Insert(lcfg)
	} else {
		ms.lcExists.Delete(lcfg)
	}
	ms.republishLocked(lcfg)
}

// republishLocked rewrites one LauncherConfig's gauge series from the current
// aggregate, or deletes them once neither its object nor any launcher remains.
// Callers must hold ms.mu.
func (ms *metricsState) republishLocked(lcfg string) {
	if !ms.lcExists.Has(lcfg) && len(ms.perLCFG[lcfg]) == 0 {
		delete(ms.agg, lcfg)
		phaseCounts{}.forEach(func(phase launcherPhase, _ int) {
			launcherPodCountGauge.DeleteLabelValues(lcfg, string(phase))
		})
		return
	}
	agg := ms.agg[lcfg] // zero value when the LC exists but has no launchers
	agg.forEach(func(phase launcherPhase, count int) {
		launcherPodCountGauge.WithLabelValues(lcfg, string(phase)).Set(float64(count))
	})
}

// launcherPhaseOf classifies a launcher Pod into a phase (see the launcherPhase
// constants) and, for one still counting down toward stuck-scheduling or
// stuck-starting, also returns the instant at which it will reach that threshold
// (zero Time if none).
//
// currentTemplateHash is the node-independent template hash the digest wants for
// this Pod's LauncherConfig; an empty value (LC gone / not yet digested) makes
// any templated Pod stale.
func launcherPhaseOf(pod *corev1.Pod, currentTemplateHash string, stuckSchedulingThreshold, stuckStartingThreshold time.Duration, now time.Time) (launcherPhase, time.Time) {
	if bound, _ := isLauncherBoundToServerRequestingPod(pod); bound {
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
	// launcher still surfaces as stuck-scheduling).
	if cond := utils.GetPodCondition(pod, corev1.PodScheduled); cond != nil && cond.Status == corev1.ConditionTrue {
		return phaseByAge(cond.LastTransitionTime.Time, stuckStartingThreshold, phaseStuckStarting, now)
	}
	return phaseByAge(pod.CreationTimestamp.Time, stuckSchedulingThreshold, phaseStuckScheduling, now)
}

// phaseByAge returns overduePhase when the launcher has been not-yet-Ready for
// at least threshold (measured from ref); otherwise it returns phaseUnbound plus
// the future instant ref+threshold at which it will become overdue.
func phaseByAge(ref time.Time, threshold time.Duration, overduePhase launcherPhase, now time.Time) (launcherPhase, time.Time) {
	overdueAt := ref.Add(threshold)
	if !now.Before(overdueAt) {
		return overduePhase, time.Time{}
	}
	return phaseUnbound, overdueAt
}

// computeKeyPhases classifies every launcher Pod of one (node, LC) key and
// returns the per-phase tally plus the earliest future instant at which some
// unbound, not-yet-Ready launcher on the current template will reach its
// stuck-scheduling or stuck-starting threshold (zero Time if none). Pods being
// deleted are counted (the metric counts Pod objects that exist) but never
// schedule a future transition.
func (ctl *controller) computeKeyPhases(pods []*corev1.Pod, templateHash string, now time.Time) (phaseCounts, time.Time) {
	counts := phaseCounts{}
	var earliestTransition time.Time
	for _, pod := range pods {
		phase, becomesOverdueAt := launcherPhaseOf(pod, templateHash, ctl.stuckSchedulingThreshold, ctl.stuckStartingThreshold, now)
		counts.increment(phase)
		if pod.DeletionTimestamp == nil && becomesOverdueAt.After(now) &&
			(earliestTransition.IsZero() || becomesOverdueAt.Before(earliestTransition)) {
			earliestTransition = becomesOverdueAt
		}
	}
	return counts, earliestTransition
}

// recordLauncherPhases publishes the phase tally for one (node, LC) key and,
// when some launcher will become stuck-scheduling or stuck-starting in the
// future, schedules a re-reconcile of the key at that instant via AddAfter, so
// the gauge flips without a periodic sweep of all launchers.
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
