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
	"time"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
)

// DefaultExpectationTimeout is the default duration to wait for the informer
// cache to reflect pending mutations before falling back to a direct apiserver
// query. This covers the normal watch propagation delay while bounding how
// long the controller will defer reconciliation.
const DefaultExpectationTimeout = 5 * time.Second

// ExpectationStatus represents the state of expectations for a given key.
type ExpectationStatus int

const (
	// ExpectationsSatisfied means no pending mutations remain; the informer
	// cache is considered up-to-date and safe to read.
	ExpectationsSatisfied ExpectationStatus = iota
	// ExpectationsWaiting means pending mutations exist but the timeout has
	// not yet been reached. The caller should requeue and try again later.
	ExpectationsWaiting
	// ExpectationsTimedOut means pending mutations exist and the timeout has
	// passed. The caller should fall back to querying the apiserver directly.
	ExpectationsTimedOut
)

// pendingExpectations tracks Pod create/delete mutations that the controller
// has performed but whose effects have not yet been observed in the informer's
// local cache. This prevents the controller from making incorrect decisions
// based on stale informer cache state.
type pendingExpectations struct {
	mu      sync.Mutex
	entries map[NodeLauncherKey]*expectationEntry
	// timeout is how long to wait for the informer cache to reflect pending
	// mutations before falling back to a direct apiserver query.
	timeout time.Duration
}

type expectationEntry struct {
	// pendingCreations tracks UIDs of Pods whose creation has been confirmed
	// by the apiserver but is not yet visible in the informer cache.
	pendingCreations sets.Set[types.UID]
	// pendingDeletions tracks UIDs of Pods whose deletion has been confirmed
	// by the apiserver but that are still visible in the informer cache.
	pendingDeletions sets.Set[types.UID]
	// deadline is the wall-clock time after which we consider the expectations
	// stale and fall back to querying the apiserver directly.
	deadline time.Time
}

func newPendingExpectations(timeout time.Duration) *pendingExpectations {
	return &pendingExpectations{
		entries: make(map[NodeLauncherKey]*expectationEntry),
		timeout: timeout,
	}
}

// expectCreation records that a Pod creation (identified by UID) is pending
// for the given key. Call this immediately after a successful Create. The
// expectation is cleared on the next check() call once the UID appears in
// the informer cache.
func (pe *pendingExpectations) expectCreation(key NodeLauncherKey, uid types.UID) {
	pe.mu.Lock()
	defer pe.mu.Unlock()
	e := pe.getOrCreate(key)
	e.pendingCreations.Insert(uid)
	e.deadline = time.Now().Add(pe.timeout)
}

// expectDeletion records that a Pod deletion (identified by UID) is pending
// for the given key. Call this immediately after a successful Delete. The
// expectation is cleared on the next check() call once the UID is no longer
// present in the informer cache.
func (pe *pendingExpectations) expectDeletion(key NodeLauncherKey, uid types.UID) {
	pe.mu.Lock()
	defer pe.mu.Unlock()
	e := pe.getOrCreate(key)
	e.pendingDeletions.Insert(uid)
	e.deadline = time.Now().Add(pe.timeout)
}

// check returns the current status of expectations for the given key. The
// caller passes presentUIDs, the set of launcher Pod UIDs currently visible
// in the informer cache for that key. check prunes pending entries that the
// cache has caught up with: a creation whose UID is now present, and a
// deletion whose UID is no longer present, are both satisfied.
//
// This formulation makes the informer cache the single source of truth for
// reconciling expectations; no event-driven bookkeeping is required.
func (pe *pendingExpectations) check(key NodeLauncherKey, presentUIDs sets.Set[types.UID]) ExpectationStatus {
	pe.mu.Lock()
	defer pe.mu.Unlock()
	e, ok := pe.entries[key]
	if !ok {
		return ExpectationsSatisfied
	}
	for uid := range e.pendingCreations {
		if presentUIDs.Has(uid) {
			e.pendingCreations.Delete(uid)
		}
	}
	for uid := range e.pendingDeletions {
		if !presentUIDs.Has(uid) {
			e.pendingDeletions.Delete(uid)
		}
	}
	if e.pendingCreations.Len() == 0 && e.pendingDeletions.Len() == 0 {
		delete(pe.entries, key)
		return ExpectationsSatisfied
	}
	if time.Now().After(e.deadline) {
		return ExpectationsTimedOut
	}
	return ExpectationsWaiting
}

// reset clears all expectations for the given key. This is called after
// falling back to an apiserver query, since the controller now has
// authoritative state and no longer needs to track pending changes.
func (pe *pendingExpectations) reset(key NodeLauncherKey) {
	pe.mu.Lock()
	defer pe.mu.Unlock()
	delete(pe.entries, key)
}

func (pe *pendingExpectations) getOrCreate(key NodeLauncherKey) *expectationEntry {
	if e, ok := pe.entries[key]; ok {
		return e
	}
	e := &expectationEntry{
		pendingCreations: sets.New[types.UID](),
		pendingDeletions: sets.New[types.UID](),
	}
	pe.entries[key] = e
	return e
}
