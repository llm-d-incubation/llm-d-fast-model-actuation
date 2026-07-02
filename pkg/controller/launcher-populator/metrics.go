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
	"context"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	kubemetrics "k8s.io/component-base/metrics"
	"k8s.io/component-base/metrics/legacyregistry"
	"k8s.io/klog/v2"

	"github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/controller/common"
	"github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/controller/utils"
)

// DefaultStuckThreshold is the default minimum age (measured from the time a
// launcher Pod was scheduled) after which a not-yet-Ready launcher is reported
// as "stuck". 7.5 minutes leaves room for slow image pulls while remaining
// operationally relevant.
const DefaultStuckThreshold = 7*time.Minute + 30*time.Second

// DefaultMetricsResyncPeriod is how often the launcher-count gauge is recomputed
// from the Pod informer cache. The "stuck" phase is a function of elapsed time,
// so it must be recomputed on a timer rather than only on Pod events.
const DefaultMetricsResyncPeriod = 30 * time.Second

// launcherPhase is the value of the "phase" label on fma_launcher_count.
type launcherPhase string

const (
	// phaseBound: the launcher is assigned to a server-requesting Pod.
	phaseBound launcherPhase = "bound"
	// phaseUnbound: the launcher uses the current template, is unbound, and is
	// either Ready or still within the stuck threshold.
	phaseUnbound launcherPhase = "unbound"
	// phaseStuck: the launcher uses the current template, is unbound, has been
	// scheduled longer than the stuck threshold, and is not Ready.
	phaseStuck launcherPhase = "stuck"
	// phaseStale: the launcher was built from a superseded template.
	phaseStale launcherPhase = "stale"
)

// allLauncherPhases lists every phase value so the gauge can emit an explicit
// zero for phases with no members. This keeps series from disappearing (which
// would otherwise make count-based alerts flap between "0" and "no data").
var allLauncherPhases = []launcherPhase{phaseBound, phaseUnbound, phaseStuck, phaseStale}

const lcfgNameLabel = "lcfg_name"
const phaseLabel = "phase"

var (
	metricsOnce sync.Once

	// launcherCountGauge is a GaugeVec of the number of launcher Pods, labeled
	// by LauncherConfig name and phase. It deliberately carries no Node label:
	// a Node label would make cardinality grow with cluster size.
	launcherCountGauge *kubemetrics.GaugeVec
)

// registerMetrics registers the launcher-populator metrics with the k8s legacy
// registry exactly once. Safe to call from multiple NewController invocations.
func registerMetrics() {
	metricsOnce.Do(func() {
		launcherCountGauge = kubemetrics.NewGaugeVec(&kubemetrics.GaugeOpts{
			Namespace:      "fma",
			Name:           "launcher_pod_count",
			Help:           "Number of launcher Pods by LauncherConfig and phase (bound, unbound, stuck, stale)",
			StabilityLevel: kubemetrics.ALPHA,
		}, []string{lcfgNameLabel, phaseLabel})
		legacyregistry.MustRegister(launcherCountGauge)
	})
}

// launcherPhaseOf classifies a single (non-terminating) launcher Pod into a
// phase. It is a pure function of the arguments so it can be unit-tested in
// isolation. currentTemplateHash is the template hash the digest currently
// wants for this Pod's (node, LC) key; an empty value (LC gone / not yet
// digested) makes any templated Pod classify as stale.
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
// for time spent waiting in the scheduler.
func stuckReferenceTime(pod *corev1.Pod) time.Time {
	if cond := utils.GetPodCondition(pod, corev1.PodScheduled); cond != nil && cond.Status == corev1.ConditionTrue {
		return cond.LastTransitionTime.Time
	}
	return pod.CreationTimestamp.Time
}

// runMetricsLoop recomputes the launcher-count gauge every metricsResyncPeriod
// until ctx is cancelled. It runs one computation immediately so metrics are
// populated without waiting a full period.
func (ctl *controller) runMetricsLoop(ctx context.Context) {
	logger := klog.FromContext(ctx).WithName("metrics")
	ticker := time.NewTicker(ctl.metricsResyncPeriod)
	defer ticker.Stop()
	for {
		ctl.updateLauncherCounts(logger)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// updateLauncherCounts sweeps the launcher Pods in the informer cache, tallies
// them per (LauncherConfig, phase), and republishes launcherCountGauge.
func (ctl *controller) updateLauncherCounts(logger klog.Logger) {
	selector := labels.SelectorFromSet(labels.Set{
		common.ComponentLabelKey: common.LauncherComponentLabelValue,
	})
	pods, err := ctl.podLister.List(selector)
	if err != nil {
		logger.Error(err, "Failed to list launcher Pods for metrics")
		return
	}

	now := time.Now()
	// counts[lcName][phase] = number of launcher Pods.
	counts := map[string]map[launcherPhase]int{}
	for _, pod := range pods {
		if pod.DeletionTimestamp != nil {
			continue
		}
		lcName := pod.Labels[common.LauncherConfigNameLabelKey]
		if lcName == "" {
			continue
		}
		key := NodeLauncherKey{
			NodeName:           pod.Labels[common.NodeNameLabelKey],
			LauncherConfigName: lcName,
		}
		currentTemplateHash := ctl.policy.snapshotForKey(key).templateHash
		phase := ctl.launcherPhaseOf(pod, currentTemplateHash, ctl.stuckThreshold, now)
		if counts[lcName] == nil {
			counts[lcName] = map[launcherPhase]int{}
		}
		counts[lcName][phase]++
	}

	// Reset drops series for LauncherConfigs that no longer have any launcher
	// Pods. Re-emit every phase (including zero) for each LC still present so
	// count-based alerts see an explicit 0 rather than missing data.
	launcherCountGauge.Reset()
	for lcName, phases := range counts {
		for _, phase := range allLauncherPhases {
			launcherCountGauge.WithLabelValues(lcName, string(phase)).Set(float64(phases[phase]))
		}
	}
}
