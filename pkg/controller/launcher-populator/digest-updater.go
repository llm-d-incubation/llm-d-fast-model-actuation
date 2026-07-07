/*
Copyright 2025 The llm-d Authors.

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
	"fmt"

	"github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/controller/utils"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"
)

// updateDigestForLC is the SOLE place that:
//   - validates the LC PodTemplate;
//   - computes the node-independent template hash;
//   - writes LauncherConfig.Status (via setLCStatusErrors);
//   - records the per-LC derived data into ctl.policy.lcs[name].
//
// Other code paths must read ctl.policy.lcs[name] instead of recomputing.
// On LC existence transitions or templateErr changes, all LPPs that reference
// this LC are enqueued so they can refresh their lppDigest (missingLCs) and
// re-apply to the digest.
func (ctl *controller) updateDigestForLC(ctx context.Context, name string) error {
	logger := klog.FromContext(ctx)
	var templateErr, templateHash, prevTemplateHash string
	var nodeIndep *corev1.Pod
	var good bool

	prevDigest := ctl.policy.lcs[name]
	prevExists := prevDigest != nil && prevDigest.object != nil
	if prevDigest != nil {
		prevTemplateHash = prevDigest.templateHash
	}
	prevGood := prevExists && prevDigest.templateErr == ""

	if lc, err := ctl.lcLister.LauncherConfigs(ctl.namespace).Get(name); err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("failed to get LC %s: %w", name, err)
		}
		if !prevExists {
			return nil
		}
		logger.Info("LC deleted, requeuing referencing LPPs", "config", name)
		delete(ctl.policy.lcs, name)
		// Drop this LC's fma_launcher_pod_count series once no launcher keeps it
		// alive (see metricsState).
		ctl.metrics.setLCExists(name, false)
	} else { // LC exists: digest it
		nodeIndep, templateHash, err = utils.BuildNodeIndependentLauncherTemplate(lc)
		if err != nil {
			templateErr = err.Error()
			logger.Info("Invalid PodTemplate in LC, reporting in Status", "config", name, "err", templateErr)
		} else {
			good = true
		}
		if statusErr := ctl.setLCStatusErrors(ctx, lc, nonNilSlice(templateErr)); statusErr != nil {
			return fmt.Errorf("failed to set Status for LC %s: %w", name, statusErr)
		}
		ctl.policy.lcs[name] = &lcDigest{
			object:          lc,
			templateErr:     templateErr,
			templateHash:    templateHash,
			nodeIndependent: nodeIndep,
		}
		// The LauncherConfig object exists, so its fma_launcher_pod_count series
		// must exist (as explicit zeros until launchers arrive).
		ctl.metrics.setLCExists(name, true)
	}

	if prevGood != good || good && (prevTemplateHash != templateHash) {
		for _, lppName := range ctl.policy.lppNamesRefByLC(name) {
			ctl.digestQueue.Queue.Add(funcItem{Kind: kindLPP, Name: lppName})
		}
	}
	return nil
}

// updateDigestForLPP is the SOLE place that:
//   - runs getMatchingNodes (collecting selector errors);
//   - determines which referenced LCs are missing (by reading ctl.policy.lcs);
//   - writes LauncherPopulationPolicy.Status (via setLPPStatusErrors);
//   - records the per-LPP derived data into ctl.policy.lpps[name];
//   - applies the LPP to digest entries on every matched node.
//
// Other code paths must NOT call setLPPStatusErrors or getMatchingNodes.
func (ctl *controller) updateDigestForLPP(ctx context.Context, lppName string) error {
	logger := klog.FromContext(ctx).WithValues("lppName", lppName)
	ctx = klog.NewContext(ctx, logger)

	// This method needs to visit two outer products in the (Node,LauncherConfig) space:
	// 1. Those for which the LPP is currently relevant in ctl.policy, and
	// 2. Those that a fresh evaluation decides are relevant.
	// These two outer products can overlap in any way.

	// First compute the fresh evaluation, yielding
	// `currentMatchedNodes` and `digested`.
	// Next: enumerate the nodes that are currently recorded
	// in ctl.policy as being relevant to the LPP,
	// and for those that are no longer relevant update as such.
	// Finally, enumerate the nodes that are now relevant
	// and update each, passing both the old and new LCName→count maps.

	prevDigest := ctl.policy.lpps[lppName]
	var currentMatchedNodeNames sets.Set[string]
	var digested map[string]int // lcName to count, for this LPP
	if lpp, err := ctl.lppLister.LauncherPopulationPolicies(ctl.namespace).Get(lppName); err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("failed to get LPP %s: %w", lppName, err)
		}
		delete(ctl.policy.lpps, lppName)
	} else {
		var selectorErrs []string
		labelSelector, selectorErr := metav1.LabelSelectorAsSelector(&lpp.Spec.EnhancedNodeSelector.LabelSelector)
		if selectorErr != nil {
			selectorErrs = []string{selectorErr.Error()}
		} else {
			if prevDigest != nil && prevDigest.object.Generation == lpp.Generation {
				digested = prevDigest.digested
			} else {
				digested = make(map[string]int, len(lpp.Spec.CountForLauncher))
				for _, cfl := range lpp.Spec.CountForLauncher {
					digested[cfl.LauncherConfigName] = max(digested[cfl.LauncherConfigName], int(cfl.LauncherCount))
				}
			}

			// Resolve matched nodes and selector errors.
			var matchErr error
			currentMatchedNodeNames, matchErr = ctl.getMatchingNodeNames(ctx, labelSelector, lpp.Spec.EnhancedNodeSelector.AllocatableResources)
			if matchErr != nil {
				return fmt.Errorf("failed to get matching nodes for policy %s: %w", lpp.Name, matchErr)
			}
		}
		// Compute missing-LC errors by consulting ctl.policy.lcs only.
		var missingLCs []string
		for _, cr := range lpp.Spec.CountForLauncher {
			lcd := ctl.policy.lcDigestFor(cr.LauncherConfigName)
			if lcd == nil || lcd.object == nil {
				missingLCs = append(missingLCs, fmt.Sprintf(
					"LauncherConfig %q referenced in CountForLauncher does not exist", cr.LauncherConfigName))
			}
		}

		// Combine all user-facing errors and write LPP.Status (the SOLE writer).
		allErrs := append(selectorErrs, missingLCs...)
		if statusErr := ctl.setLPPStatusErrors(ctx, lpp, allErrs); statusErr != nil {
			return fmt.Errorf("failed to set Status for policy %s: %w", lpp.Name, statusErr)
		}

		// Snapshot the LPP digest.
		ctl.policy.lpps[lppName] = &lppDigest{
			object:        lpp,
			labelSelector: labelSelector,
			selectorErr:   selectorErr,
			digested:      digested,
		}
	}

	for nodeName := range ctl.policy.nodeNamesRefingLPP(lppName) {
		if currentMatchedNodeNames.Has(nodeName) {
			continue
		}
		ctl.applyLPPToDigestForNode(lppName, nil, nodeName)
	}

	// Apply LPP to each matched node and enqueue affected keys.
	for nodeName := range currentMatchedNodeNames {
		ctl.applyLPPToDigestForNode(lppName, digested, nodeName)
	}

	return nil
}

// updateDigestForNode handles a Node add/update/delete event.
// It recomputes digest entries for the affected node by replaying every cached
// LPP. It does not validate templates or write Status; those are produced by
// updateDigestForLC / updateDigestForLPP exclusively.
func (ctl *controller) updateDigestForNode(ctx context.Context, nodeName string) error {
	logger := klog.FromContext(ctx)

	node, err := ctl.nodeLister.Get(nodeName)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("Node deleted, removing from digest", "node", nodeName)
			delete(ctl.policy.digest, nodeName)
			return nil
		}
		return fmt.Errorf("failed to get node %s: %w", nodeName, err)
	}
	ctl.recomputeDigestForNode(node)
	return nil
}

// recomputeDigestForNode rebuilds digest entries for a single node by replaying
// every cached LPP. Pure read of ctl.policy.lpps + ctl.policy.lcs.
func (ctl *controller) recomputeDigestForNode(node *corev1.Node) {
	for lppName, lppd := range ctl.policy.lpps {
		newDigested := lppd.digested
		matches := lppd.selectorErr == nil && lppd.labelSelector.Matches(labels.Set(node.Labels)) && matchesResourceConditions(node.Status.Allocatable, lppd.object.Spec.EnhancedNodeSelector.AllocatableResources)
		if !matches {
			newDigested = nil
		}
		ctl.applyLPPToDigestForNode(lppName, newDigested, node.Name)
	}

	if len(ctl.policy.digest[node.Name]) == 0 {
		delete(ctl.policy.digest, node.Name)
	}
}

// applyLPPToDigestForNode updates the digested policy map regarding
// a particular node and LPP, given the new result of digesting
// that LPP.
// It never validates templates, writes Status, or fetches from listers.
// Enqueues keys for which this made a difference.
func (ctl *controller) applyLPPToDigestForNode(lppName string, lcToCount map[string]int, nodeName string) {
	for lcName := range lcToCount {
		entry := ctl.policy.getEntry(nodeName, lcName, true)
		entry.lpps[lppName] = lcToCount
		changed := ctl.recomputeEntryFromLPPs(entry, lcName)
		if changed {
			ctl.keyQueue.Queue.Add(keyItem{NodeLauncherKey{NodeName: nodeName, LauncherConfigName: lcName}})
		}
	}
	nodeMap := ctl.policy.digest[nodeName]
	for lcName, entry := range nodeMap {
		if _, lcIsWanted := lcToCount[lcName]; lcIsWanted {
			continue
		}
		if _, lppWasWanted := entry.lpps[lppName]; !lppWasWanted {
			continue
		}
		delete(entry.lpps, lppName)
		if len(entry.lpps) == 0 {
			ctl.policy.deleteEntry(nodeName, lcName)
		}
		changed := ctl.recomputeEntryFromLPPs(entry, lcName)
		if changed {
			ctl.keyQueue.Queue.Add(keyItem{NodeLauncherKey{NodeName: nodeName, LauncherConfigName: lcName}})
		}
	}
}

// recomputeEntryFromLPPs recomputes the desiredPopulation of a
// digestEntry from its remaining LPP references and the cached lcDigest.
// Pure read of other objects; no Status writes or template validation.
// Return value indicates whether there was a change.
func (ctl *controller) recomputeEntryFromLPPs(entry *digestEntry, lcName string) bool {
	oldDesired := entry.desiredCount
	var newDesired int
	var lcd *lcDigest
	if len(entry.lpps) > 0 {
		lcd = ctl.policy.lcDigestFor(lcName)
		if lcd == nil || lcd.object == nil || lcd.templateErr != "" {
			newDesired = HandsOff
		} else {
			for _, lcToCount := range entry.lpps {
				if thisCount, have := lcToCount[lcName]; have {
					newDesired = max(newDesired, thisCount)
				}
			}
		}
	}
	if oldDesired == newDesired {
		return false
	}
	entry.desiredCount = newDesired
	return true
}
