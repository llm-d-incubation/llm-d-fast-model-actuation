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
	"sync"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fmav1alpha1 "github.com/llm-d-incubation/llm-d-fast-model-actuation/api/fma/v1alpha1"
)

// digestEntry is the per-NodeLauncherKey entry in the digestedPolicy.
// It holds the digested desired count and supporting details needed for
// creating launcher Pods.
type digestEntry struct {
	handsOff bool   // true when user error (LC missing or invalid template) precludes action
	count    int32  // max over relevant (existing, well-formed) CountForLauncher
	spec     *fmav1alpha1.LauncherConfigSpec
	ownerRef metav1.OwnerReference
	lpps     map[string]*fmav1alpha1.LauncherPopulationPolicy // LPPs contributing to this entry
}

// lcDigest holds all node-independent derived data for a LauncherConfig.
// Written exclusively by updateDigestForLC; read by all other paths.
type lcDigest struct {
	object       *fmav1alpha1.LauncherConfig // nil iff the LC API object does not exist
	templateErr  string                      // empty when ValidateLauncherPodTemplate passes
	templateHash string                      // populated only when templateErr == ""
	ownerRef     metav1.OwnerReference       // populated only when object != nil
}

// lppDigest holds all derived data for a LauncherPopulationPolicy.
// Written exclusively by updateDigestForLPP; read by all other paths.
type lppDigest struct {
	object       *fmav1alpha1.LauncherPopulationPolicy
	selectorErrs []string // user-facing errors from getMatchingNodes
	missingLCs   []string // CountForLauncher entries referencing a non-existent LC
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
// readers hold RLock() while taking a small value-typed snapshot, then drop
// the lock before issuing K8s API calls.
type digestedPolicy struct {
	mu     sync.RWMutex
	digest map[string]map[string]*digestEntry // node name → LC name → entry
	lcs    map[string]*lcDigest               // LC name → lcDigest
	lpps   map[string]*lppDigest              // LPP name → lppDigest
}

// keySnapshot is a value-typed view of one digestEntry plus the LC's template
// hash, captured under digestedPolicy.mu.RLock(). Once returned, the snapshot
// is safe to read outside the lock; underlying *fmav1alpha1.LauncherConfigSpec
// points into the informer cache, which Kubernetes does not mutate in place.
type keySnapshot struct {
	exists       bool
	handsOff     bool
	count        int32
	spec         *fmav1alpha1.LauncherConfigSpec
	ownerRef     metav1.OwnerReference
	templateHash string
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
func (dp *digestedPolicy) getEntry(nodeName, lcName string) *digestEntry {
	if nodeMap, ok := dp.digest[nodeName]; ok {
		return nodeMap[lcName]
	}
	return nil
}

// setEntry sets or replaces the digestEntry for the given key.
func (dp *digestedPolicy) setEntry(nodeName, lcName string, entry *digestEntry) {
	nodeMap, ok := dp.digest[nodeName]
	if !ok {
		nodeMap = make(map[string]*digestEntry)
		dp.digest[nodeName] = nodeMap
	}
	nodeMap[lcName] = entry
}

// snapshotForKey returns a value-typed snapshot of the digestEntry and its
// LC's templateHash, taken under RLock. Concurrency-safe; callers must drop
// the snapshot before treating any pointer fields (spec) as still belonging
// to the digest.
func (dp *digestedPolicy) snapshotForKey(key NodeLauncherKey) keySnapshot {
	dp.mu.RLock()
	defer dp.mu.RUnlock()
	var snap keySnapshot
	if entry := dp.getEntry(key.NodeName, key.LauncherConfigName); entry != nil {
		snap.exists = true
		snap.handsOff = entry.handsOff
		snap.count = entry.count
		snap.spec = entry.spec
		snap.ownerRef = entry.ownerRef
	}
	if lcd := dp.lcs[key.LauncherConfigName]; lcd != nil {
		snap.templateHash = lcd.templateHash
	}
	return snap
}
