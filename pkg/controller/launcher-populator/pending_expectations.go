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
// has performed but whose effects have not yet been observed via informer
// notifications. This prevents the controller from making incorrect decisions
// based on stale informer cache state.
//
// Expectations are tracked by individual Pod UID rather than by count. This
// is critical for correctness in two scenarios:
//   - Other actors (including another replica of this controller) may also
//     create or delete pods for the same key. Count-based tracking could be
//     satisfied by unrelated mutations while our own pods remain invisible.
//   - Watch notifications can arrive before the API write response returns.
//     With UID-based tracking, a "lost" observe is harmless: the expectation
//     will simply time out and fall back to an authoritative apiserver query.
//
// Concurrency model. The controller goroutine and the informer goroutine race:
// the informer's OnAdd/OnDelete handler can fire before the corresponding
// Create/Delete API call returns to the controller goroutine. To make
// expect/observe order-independent, each entry keeps two extra UID sets,
// earlyCreations and earlyDeletions, which buffer observations that arrived
// before the matching expectXxx call. expectXxx and observeXxx cancel matching
// UIDs across these sets, so the final state is the same regardless of which
// goroutine wins the race.
//
// The typical lifecycle:
//  1. Controller creates/deletes a Pod and the informer is notified, in some
//     order. expectCreation/expectDeletion and observeCreation/observeDeletion
//     are each called once with the Pod UID; matching pairs cancel out.
//  2. Next reconcile calls check() to determine if the cache is safe to use.
//
// If expected notifications never arrive (e.g., a Pod was created and
// immediately deleted externally), the timeout ensures the controller
// eventually falls back to an authoritative apiserver query.
type pendingExpectations struct {
	mu      sync.Mutex
	entries map[NodeLauncherKey]*expectationEntry
	// timeout is how long to wait for the informer cache to reflect pending
	// mutations before falling back to a direct apiserver query.
	timeout time.Duration
}

type expectationEntry struct {
	// pendingCreations tracks UIDs of Pods whose creation has been confirmed
	// by the apiserver but not yet observed via informer notification.
	pendingCreations sets.Set[types.UID]
	// pendingDeletions tracks UIDs of Pods whose deletion has been confirmed
	// by the apiserver but not yet observed via informer notification.
	pendingDeletions sets.Set[types.UID]
	// earlyCreations buffers UIDs whose informer add notification arrived
	// before the controller had a chance to call expectCreation (the watch
	// fired while the Create response was still in flight). The next
	// expectCreation with the same UID cancels out instead of adding to
	// pendingCreations, so expect/observe order is irrelevant.
	earlyCreations sets.Set[types.UID]
	// earlyDeletions buffers UIDs whose informer delete notification arrived
	// before expectDeletion. Mirrors earlyCreations.
	earlyDeletions sets.Set[types.UID]
	// deadline is the wall-clock time after which we consider the expectations
	// stale and fall back to querying the apiserver directly. The same
	// deadline also bounds how long early-arrival buffers may sit unused
	// before being garbage-collected.
	deadline time.Time
}

func newPendingExpectations(timeout time.Duration) *pendingExpectations {
	return &pendingExpectations{
		entries: make(map[NodeLauncherKey]*expectationEntry),
		timeout: timeout,
	}
}

// expectCreation records that a Pod creation (identified by UID) is pending
// for the given key. Call this immediately after a successful Create. Safe
// to call before, during, or after the matching observeCreation: if the
// informer's add notification has already been buffered as an early
// arrival, it is consumed here instead of recording a new pending entry.
func (pe *pendingExpectations) expectCreation(key NodeLauncherKey, uid types.UID) {
	pe.mu.Lock()
	defer pe.mu.Unlock()
	e := pe.getOrCreate(key)
	if e.earlyCreations.Has(uid) {
		// Observation already arrived before this expectation was recorded;
		// the two cancel out without ever adding to pendingCreations.
		e.earlyCreations.Delete(uid)
	} else {
		e.pendingCreations.Insert(uid)
	}
	e.deadline = time.Now().Add(pe.timeout)
	pe.cleanupIfSatisfied(key, e)
}

// expectDeletion records that a Pod deletion (identified by UID) is pending
// for the given key. Call this immediately after a successful Delete. Safe
// to call before, during, or after the matching observeDeletion: an early
// delete notification is consumed here without recording a new pending entry.
func (pe *pendingExpectations) expectDeletion(key NodeLauncherKey, uid types.UID) {
	pe.mu.Lock()
	defer pe.mu.Unlock()
	e := pe.getOrCreate(key)
	if e.earlyDeletions.Has(uid) {
		e.earlyDeletions.Delete(uid)
	} else {
		e.pendingDeletions.Insert(uid)
	}
	e.deadline = time.Now().Add(pe.timeout)
	pe.cleanupIfSatisfied(key, e)
}

// observeCreation is called when the informer notifies of a launcher Pod
// creation for the given key. If a matching expectation is already pending
// it is fulfilled; otherwise the UID is buffered as an early arrival so the
// upcoming expectCreation call cancels out. This makes expect/observe
// order-independent across the controller and informer goroutines.
func (pe *pendingExpectations) observeCreation(key NodeLauncherKey, uid types.UID) {
	pe.mu.Lock()
	defer pe.mu.Unlock()
	e := pe.getOrCreate(key)
	if e.pendingCreations.Has(uid) {
		e.pendingCreations.Delete(uid)
	} else {
		// Observation arrived before the matching expectCreation. Stash it;
		// the deadline bounds how long it may sit here in case no matching
		// expectation ever arrives (e.g. another actor created the Pod).
		e.earlyCreations.Insert(uid)
		e.deadline = time.Now().Add(pe.timeout)
	}
	pe.cleanupIfSatisfied(key, e)
}

// observeDeletion is called when the informer notifies of a launcher Pod
// deletion for the given key. Mirrors observeCreation's order-independent
// reconciliation against pendingDeletions and earlyDeletions.
func (pe *pendingExpectations) observeDeletion(key NodeLauncherKey, uid types.UID) {
	pe.mu.Lock()
	defer pe.mu.Unlock()
	e := pe.getOrCreate(key)
	if e.pendingDeletions.Has(uid) {
		e.pendingDeletions.Delete(uid)
	} else {
		e.earlyDeletions.Insert(uid)
		e.deadline = time.Now().Add(pe.timeout)
	}
	pe.cleanupIfSatisfied(key, e)
}

// check returns the current status of expectations for the given key.
func (pe *pendingExpectations) check(key NodeLauncherKey) ExpectationStatus {
	pe.mu.Lock()
	defer pe.mu.Unlock()
	e, ok := pe.entries[key]
	if !ok {
		return ExpectationsSatisfied
	}
	if e.pendingCreations.Len() == 0 && e.pendingDeletions.Len() == 0 {
		// No mutations the controller is waiting on. Drop the entry if the
		// early-arrival buffers are empty too, or if their deadline has
		// passed (an early arrival without a matching expectXxx is treated
		// as foreign activity and discarded once stale).
		if (e.earlyCreations.Len() == 0 && e.earlyDeletions.Len() == 0) || time.Now().After(e.deadline) {
			delete(pe.entries, key)
		}
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
		earlyCreations:   sets.New[types.UID](),
		earlyDeletions:   sets.New[types.UID](),
	}
	pe.entries[key] = e
	return e
}

func (pe *pendingExpectations) cleanupIfSatisfied(key NodeLauncherKey, e *expectationEntry) {
	if e.pendingCreations.Len() == 0 && e.pendingDeletions.Len() == 0 &&
		e.earlyCreations.Len() == 0 && e.earlyDeletions.Len() == 0 {
		delete(pe.entries, key)
	}
}
