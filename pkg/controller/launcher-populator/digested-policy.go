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
	"iter"
	"sync"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"

	fmav1alpha1 "github.com/llm-d-incubation/llm-d-fast-model-actuation/api/fma/v1alpha1"
)

// A special value for desired count that means "hands off" ---
// that is, make no changes. This is used when there is a user
// error wrt the relevant LauncherConfig.
const HandsOff = -1

// digestEntry is the per-NodeLauncherKey entry in the digestedPolicy.
// It holds the digested desired count and supporting details needed for
// creating launcher Pods.
type digestEntry struct {
	// The result of digesting lpps and the current lcDigest values.
	// A value of -1 codes for "hands off".
	desiredCount int

	// Maps LPP name to LC name to desired count.
	// From the digested LPPs.
	lpps map[string]map[string]int
}

// lcDigest holds all node-independent derived data for a LauncherConfig.
// Written exclusively by updateDigestForLC; read by all other paths.
type lcDigest struct {
	object          *fmav1alpha1.LauncherConfig // nil iff the LC API object does not exist
	templateErr     string                      // empty when no error
	nodeIndependent *corev1.Pod
	templateHash    string // populated only when templateErr == ""
}

// lppDigest holds all derived data for a LauncherPopulationPolicy.
// Written exclusively by updateDigestForLPP; read by all other paths.
type lppDigest struct {
	object        *fmav1alpha1.LauncherPopulationPolicy
	labelSelector labels.Selector
	selectorErr   error
	digested      map[string]int // maps LauncherConfig name to desired count
}

// digestedPolicy holds the controller's digested view of all policies.
// It is the single source of truth for per-key reconciliation:
//   - digest maps each (node, LC) pair to its desired state. INVARIANT: an outer
//     nodeName key exists if and only if its inner map is non-empty; mutators
//     must remove the outer key when the last inner entry is deleted.
//   - lcs caches the per-LC digest, including template validation result and hash.
//   - lpps caches the per-LPP digest, including selector errors and missing-LC list.
//
// Concurrency: mu serializes write transactions performed by the (single)
// digest worker against snapshot reads performed by the keyQueue workers. The
// digest worker holds Lock() for the duration of one updateDigestForX call;
// readers hold RLock() while taking a small value-typed snapshot.
//
// In short, changes to this data structure are serialized.
// Changes to LauncherPopulationPolicy objects enter through only one method, updateDigestForLPP.
// Changes to LauncherConfig objects enter through only one method, updateDigestForLC.
// Changes to Node objects enter through either of two methods,
// updateDigestForLPP and updateDigestForNode.
// There is some internal redundancy in this data structure;
// each of those three methods consistently updates the whole
// data structure for the change(s) it ingests.
type digestedPolicy struct {
	mu     sync.RWMutex
	digest map[string]map[string]*digestEntry // node name → LC name → entry
	lcs    map[string]*lcDigest               // LC name → lcDigest
	lpps   map[string]*lppDigest              // LPP name → lppDigest
}

// keySnapshot is a value-typed view of one digestEntry plus the LC's template
// hash and node-independent Pod template, captured under digestedPolicy.mu.RLock().
// Nothing modifies any part of the Pod template.
type keySnapshot struct {
	exists                          bool
	desiredCount                    int // HandsOff or non-negative
	templateHash                    string
	nodeIndependentLauncherTemplate *corev1.Pod
}

// newDigestedPolicy creates an empty digestedPolicy.
func newDigestedPolicy() *digestedPolicy {
	return &digestedPolicy{
		digest: make(map[string]map[string]*digestEntry),
		lcs:    make(map[string]*lcDigest),
		lpps:   make(map[string]*lppDigest),
	}
}

// lcDigestFor returns the lcDigest for lcName, or nil when the LC has not been
// processed yet.
func (dp *digestedPolicy) lcDigestFor(lcName string) *lcDigest {
	return dp.lcs[lcName]
}

// nodeNamesRefingLPP returns an enumerator of node names for which dp
// records that the given LPP is relevant.
func (dp *digestedPolicy) nodeNamesRefingLPP(lppName string) iter.Seq[string] {
	return func(yield func(nodeName string) bool) {
		for nodeName, nodeMap := range dp.digest {
			for _, de := range nodeMap {
				if _, has := de.lpps[lppName]; has {
					if !yield(nodeName) {
						return
					} else {
						break
					}
				}
			}
		}
	}
}

// lppNamesRefByLC returns names of all cached LPPs that reference lcName in
// their CountForLauncher list. Used to enqueue dependents on LC existence
// transitions or template-validity changes.
func (dp *digestedPolicy) lppNamesRefByLC(lcName string) []string {
	var names []string
	for name, lppd := range dp.lpps {
		if lppd == nil || lppd.object == nil {
			continue
		}
		for _, cr := range lppd.object.Spec.CountForLauncher {
			if cr.LauncherConfigName == lcName {
				names = append(names, name)
				break
			}
		}
	}
	return names
}

// getEntry returns the digestEntry for the given key, or nil if absent.
func (dp *digestedPolicy) getEntry(nodeName, lcName string, addIfMissing bool) *digestEntry {
	nodeMap, ok := dp.digest[nodeName]
	if !ok {
		if !addIfMissing {
			return nil
		}
		nodeMap = make(map[string]*digestEntry)
		dp.digest[nodeName] = nodeMap
	}
	entry, ok := nodeMap[lcName]
	if !ok {
		if !addIfMissing {
			return nil
		}
		entry = &digestEntry{lpps: make(map[string]map[string]int)}
		nodeMap[lcName] = entry
	}
	return entry
}

// deleteEntry removes the digestEntry for the given key
func (dp *digestedPolicy) deleteEntry(nodeName, lcName string) {
	nodeMap, ok := dp.digest[nodeName]
	if !ok {
		return
	}
	delete(nodeMap, lcName)
	if len(nodeMap) == 0 {
		delete(dp.digest, nodeName)
	}
}

// snapshotForKey returns a value-typed snapshot of the digestEntry and its
// LC's templateHash, taken under RLock. Concurrency-safe; callers must drop
// the snapshot before treating any pointer fields (spec) as still belonging
// to the digest.
func (dp *digestedPolicy) snapshotForKey(key NodeLauncherKey) keySnapshot {
	dp.mu.RLock()
	defer dp.mu.RUnlock()
	var snap keySnapshot
	if entry := dp.getEntry(key.NodeName, key.LauncherConfigName, false); entry != nil {
		snap.exists = true
		snap.desiredCount = entry.desiredCount
	}
	if lcd := dp.lcs[key.LauncherConfigName]; lcd != nil {
		snap.templateHash = lcd.templateHash
		snap.nodeIndependentLauncherTemplate = lcd.nodeIndependent
	}
	return snap
}
