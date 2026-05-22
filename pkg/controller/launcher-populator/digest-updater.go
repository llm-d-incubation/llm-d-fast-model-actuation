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

	fmav1alpha1 "github.com/llm-d-incubation/llm-d-fast-model-actuation/api/fma/v1alpha1"
	"github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/controller/utils"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"
)

// updateDigestForLC is the SOLE place that:
//   - validates the LC PodTemplate (utils.ValidateLauncherPodTemplate);
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

	prev := ctl.policy.lcs[name]
	prevExists := prev != nil && prev.object != nil

	lc, err := ctl.lcLister.LauncherConfigs(ctl.namespace).Get(name)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("failed to get LC %s: %w", name, err)
		}
		// LC deleted.
		logger.Info("LC deleted, marking referencing keys as handsOff", "config", name)
		delete(ctl.policy.lcs, name)
		for _, key := range ctl.policy.keysForLC(name) {
			if entry := ctl.policy.getEntry(key.NodeName, key.LauncherConfigName); entry != nil {
				entry.handsOff = true
				entry.spec = nil
				entry.count = 0
				ctl.keyQueue.Queue.Add(keyItem{key})
			}
		}
		// Existence flip: re-enqueue LPPs that reference this LC so they can
		// recompute missingLCs.
		if prevExists {
			for _, lppName := range ctl.policy.lppNamesRefByLC(name) {
				ctl.digestQueue.Queue.Add(funcItem{kind: kindLPP, name: lppName})
			}
		}
		return nil
	}

	// LC exists: validate and produce the lcDigest.
	var templateErr string
	if vErr := utils.ValidateLauncherPodTemplate(lc.Spec.PodTemplate); vErr != nil {
		templateErr = vErr.Error()
		logger.Error(vErr, "Invalid PodTemplate in LC, reporting in Status", "config", name)
	}
	var templateHash string
	if templateErr == "" {
		h, hashErr := utils.ComputeLauncherTemplateHash(lc.Spec.PodTemplate)
		if hashErr != nil {
			return fmt.Errorf("failed to compute template hash for LC %s: %w", name, hashErr)
		}
		templateHash = h
	}
	if statusErr := ctl.setLCStatusErrors(ctx, lc, nonNilSlice(templateErr)); statusErr != nil {
		return fmt.Errorf("failed to set Status for LC %s: %w", name, statusErr)
	}

	prevTemplateErr := ""
	if prev != nil {
		prevTemplateErr = prev.templateErr
	}

	ctl.policy.lcs[name] = &lcDigest{
		object:       lc,
		templateErr:  templateErr,
		templateHash: templateHash,
		ownerRef:     makeLCOwnerRef(lc),
	}

	// If the LC just became existent, or its template-validity flipped, the
	// LPPs that reference it must be re-evaluated.
	if !prevExists || prevTemplateErr != templateErr {
		for _, lppName := range ctl.policy.lppNamesRefByLC(name) {
			ctl.digestQueue.Queue.Add(funcItem{kind: kindLPP, name: lppName})
		}
	}

	// Refresh per-key entries that reference this LC.
	for _, key := range ctl.policy.keysForLC(name) {
		entry := ctl.policy.getEntry(key.NodeName, key.LauncherConfigName)
		if entry == nil {
			continue
		}
		if templateErr != "" {
			entry.handsOff = true
			entry.spec = nil
		} else {
			entry.handsOff = false
			entry.spec = &lc.Spec
			entry.ownerRef = makeLCOwnerRef(lc)
		}
		ctl.keyQueue.Queue.Add(keyItem{key})
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
// Other code paths must NOT call setLPPStatusErrors or ValidateLauncherPodTemplate.
func (ctl *controller) updateDigestForLPP(ctx context.Context, name string) error {
	logger := klog.FromContext(ctx)

	lpp, err := ctl.lppLister.LauncherPopulationPolicies(ctl.namespace).Get(name)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("failed to get LPP %s: %w", name, err)
		}
		// LPP deleted: detach from all referencing entries.
		logger.Info("LPP deleted, updating digest incrementally", "policy", name)
		for nodeName, nodeMap := range ctl.policy.digest {
			for lcName, entry := range nodeMap {
				if _, hasLPP := entry.lpps[name]; !hasLPP {
					continue
				}
				ctl.detachLPPFromEntry(nodeMap, name, nodeName, lcName, entry)
			}
		}
		delete(ctl.policy.lpps, name)
		return nil
	}

	// Resolve matched nodes and selector errors.
	currentMatchedNodes, selectorErrs, matchErr := ctl.getMatchingNodes(ctx, lpp.Spec.EnhancedNodeSelector)
	if matchErr != nil {
		return fmt.Errorf("failed to get matching nodes for policy %s: %w", lpp.Name, matchErr)
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
	allErrs := append([]string(nil), selectorErrs...)
	allErrs = append(allErrs, missingLCs...)
	if statusErr := ctl.setLPPStatusErrors(ctx, lpp, allErrs); statusErr != nil {
		return fmt.Errorf("failed to set Status for policy %s: %w", lpp.Name, statusErr)
	}

	// Snapshot the LPP digest.
	ctl.policy.lpps[name] = &lppDigest{
		object:       lpp,
		selectorErrs: selectorErrs,
		missingLCs:   missingLCs,
	}

	// Determine old matched nodes from existing digest entries.
	oldNodes := sets.New[string]()
	for nodeName, nodeMap := range ctl.policy.digest {
		for _, entry := range nodeMap {
			if _, hasLPP := entry.lpps[name]; hasLPP {
				oldNodes.Insert(nodeName)
				break
			}
		}
	}
	currentMatched := sets.New[string]()
	for _, node := range currentMatchedNodes {
		currentMatched.Insert(node.Name)
	}
	removedNodes := oldNodes.Difference(currentMatched).UnsortedList()

	// Detach the LPP from entries on nodes it no longer matches.
	for _, nodeName := range removedNodes {
		nodeMap := ctl.policy.digest[nodeName]
		for lcName, entry := range nodeMap {
			if _, hasLPP := entry.lpps[name]; !hasLPP {
				continue
			}
			ctl.detachLPPFromEntry(nodeMap, name, nodeName, lcName, entry)
		}
	}

	// Apply LPP to each matched node and enqueue affected keys.
	for _, node := range currentMatchedNodes {
		ctl.applyLPPToDigestForNode(lpp, node.Name)
		for _, cr := range lpp.Spec.CountForLauncher {
			ctl.keyQueue.Queue.Add(keyItem{NodeLauncherKey{NodeName: node.Name, LauncherConfigName: cr.LauncherConfigName}})
		}
	}

	return nil
}

// updateDigestForNode handles a Node add/update/delete event.
// It recomputes digest entries for the affected node by replaying every cached
// LPP. It does not validate templates or write Status; those are produced by
// updateDigestForLC / updateDigestForLPP exclusively.
func (ctl *controller) updateDigestForNode(ctx context.Context, nodeName string) error {
	logger := klog.FromContext(ctx)

	_, err := ctl.nodeLister.Get(nodeName)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("Node deleted, removing from digest", "node", nodeName)
			removedKeys := ctl.policy.removeNode(nodeName)
			for _, key := range removedKeys {
				ctl.keyQueue.Queue.Add(keyItem{key})
			}
			return nil
		}
		return fmt.Errorf("failed to get node %s: %w", nodeName, err)
	}
	return ctl.recomputeDigestForNode(nodeName)
}

// recomputeDigestForNode rebuilds digest entries for a single node by replaying
// every cached LPP. Pure read of ctl.policy.lpps + ctl.policy.lcs.
func (ctl *controller) recomputeDigestForNode(nodeName string) error {
	ctl.policy.digest[nodeName] = make(map[string]*digestEntry)

	for _, lppd := range ctl.policy.lpps {
		if lppd == nil || lppd.object == nil {
			continue
		}
		ctl.applyLPPToDigestForNode(lppd.object, nodeName)
	}

	nodeMap := ctl.policy.digest[nodeName]
	for lcName := range nodeMap {
		ctl.keyQueue.Queue.Add(keyItem{NodeLauncherKey{NodeName: nodeName, LauncherConfigName: lcName}})
	}
	if len(nodeMap) == 0 {
		delete(ctl.policy.digest, nodeName)
	}
	return nil
}

// applyLPPToDigestForNode evaluates one LPP for one node and updates the digest
// using ONLY ctl.policy.lcs as the source of truth for LC status. It never
// validates templates, writes Status, or fetches from listers.
func (ctl *controller) applyLPPToDigestForNode(lpp *fmav1alpha1.LauncherPopulationPolicy, nodeName string) {
	node, err := ctl.nodeLister.Get(nodeName)
	if err != nil {
		// Node missing or transient lister error: nothing to apply.
		return
	}
	labelSelector, selectorErr := metav1.LabelSelectorAsSelector(&lpp.Spec.EnhancedNodeSelector.LabelSelector)
	if selectorErr != nil {
		return
	}
	if !labelSelector.Matches(labels.Set(node.Labels)) {
		return
	}
	if !matchesResourceConditions(node.Status.Allocatable, lpp.Spec.EnhancedNodeSelector.AllocatableResources) {
		return
	}

	for _, cr := range lpp.Spec.CountForLauncher {
		lcName := cr.LauncherConfigName

		entry := ctl.policy.getEntry(nodeName, lcName)
		if entry == nil {
			entry = &digestEntry{lpps: make(map[string]*fmav1alpha1.LauncherPopulationPolicy)}
			ctl.policy.setEntry(nodeName, lcName, entry)
		}

		lcd := ctl.policy.lcDigestFor(lcName)
		switch {
		case lcd == nil || lcd.object == nil:
			// LC not yet processed or known to be absent: handsOff. The LC's
			// own update path (updateDigestForLC) will re-enqueue this LPP
			// when the LC becomes existent.
			entry.handsOff = true
			entry.spec = nil
		case lcd.templateErr != "":
			// Template invalid; error is already in LC.Status.
			entry.handsOff = true
			entry.spec = nil
		default:
			entry.handsOff = false
			entry.spec = &lcd.object.Spec
			entry.ownerRef = lcd.ownerRef
		}

		if cr.LauncherCount > entry.count {
			entry.count = cr.LauncherCount
		}
		if entry.lpps == nil {
			entry.lpps = make(map[string]*fmav1alpha1.LauncherPopulationPolicy)
		}
		entry.lpps[lpp.Name] = lpp
	}
}

// removeLPPFromEntry removes an LPP reference from a digest entry.
// If the entry has no remaining LPPs, it is reset to a zero state.
func (ctl *controller) removeLPPFromEntry(entry *digestEntry, lppName string) {
	delete(entry.lpps, lppName)
	if len(entry.lpps) == 0 {
		entry.count = 0
		entry.spec = nil
		entry.handsOff = false
	}
}

func (ctl *controller) detachLPPFromEntry(nodeMap map[string]*digestEntry, lppName, nodeName, lcName string, entry *digestEntry) {
	ctl.removeLPPFromEntry(entry, lppName)
	if len(entry.lpps) == 0 {
		delete(nodeMap, lcName)
		if len(nodeMap) == 0 {
			delete(ctl.policy.digest, nodeName)
		}
	} else {
		ctl.recomputeEntryFromLPPs(entry, lcName)
	}
	ctl.keyQueue.Queue.Add(keyItem{NodeLauncherKey{NodeName: nodeName, LauncherConfigName: lcName}})
}

// recomputeEntryFromLPPs recomputes the (handsOff, count, spec, ownerRef) of a
// digestEntry from its remaining LPP references and the cached lcDigest.
// Pure read; no Status writes or template validation.
func (ctl *controller) recomputeEntryFromLPPs(entry *digestEntry, lcName string) {
	if len(entry.lpps) == 0 {
		entry.count = 0
		entry.spec = nil
		entry.handsOff = false
		return
	}

	var maxCount int32
	for _, lpp := range entry.lpps {
		for _, cr := range lpp.Spec.CountForLauncher {
			if cr.LauncherConfigName == lcName && cr.LauncherCount > maxCount {
				maxCount = cr.LauncherCount
			}
		}
	}

	lcd := ctl.policy.lcDigestFor(lcName)
	switch {
	case lcd == nil || lcd.object == nil:
		entry.handsOff = true
		entry.count = maxCount
		entry.spec = nil
	case lcd.templateErr != "":
		entry.handsOff = true
		entry.count = maxCount
		entry.spec = nil
	default:
		entry.handsOff = false
		entry.count = maxCount
		entry.spec = &lcd.object.Spec
		entry.ownerRef = lcd.ownerRef
	}
}


