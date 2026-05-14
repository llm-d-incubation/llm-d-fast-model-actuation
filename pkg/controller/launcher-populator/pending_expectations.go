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
)

const (
	// expectationTimeout is how long to wait for the informer cache to reflect
	// pending mutations before falling back to a direct apiserver query.
	// This covers the normal watch propagation delay while bounding how long
	// the controller will defer reconciliation.
	expectationTimeout = 5 * time.Second
)

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
// The typical lifecycle:
//  1. Controller creates/deletes Pods → calls expectCreations/expectDeletions.
//  2. Informer fires OnAdd/OnDelete → calls observeCreation/observeDeletion.
//  3. Next reconcile calls check() to determine if the cache is safe to use.
//
// If expected notifications never arrive (e.g., a Pod was created and
// immediately deleted externally), the timeout ensures the controller
// eventually falls back to an authoritative apiserver query.
type pendingExpectations struct {
	mu      sync.Mutex
	entries map[NodeLauncherKey]*expectationEntry
}

type expectationEntry struct {
	// adds is the number of Pod creations not yet reflected in the cache.
	adds int
	// dels is the number of Pod deletions not yet reflected in the cache.
	dels int
	// deadline is the wall-clock time after which we consider the expectations
	// stale and fall back to querying the apiserver directly.
	deadline time.Time
}

func newPendingExpectations() *pendingExpectations {
	return &pendingExpectations{
		entries: make(map[NodeLauncherKey]*expectationEntry),
	}
}

// expectCreations records that `count` Pod creations are pending for the key.
func (pe *pendingExpectations) expectCreations(key NodeLauncherKey, count int) {
	pe.mu.Lock()
	defer pe.mu.Unlock()
	e := pe.getOrCreate(key)
	e.adds += count
	e.deadline = time.Now().Add(expectationTimeout)
}

// expectDeletions records that `count` Pod deletions are pending for the key.
func (pe *pendingExpectations) expectDeletions(key NodeLauncherKey, count int) {
	pe.mu.Lock()
	defer pe.mu.Unlock()
	e := pe.getOrCreate(key)
	e.dels += count
	e.deadline = time.Now().Add(expectationTimeout)
}

// observeCreation is called when the informer notifies of a launcher Pod
// creation for the given key, reducing the pending creation count.
func (pe *pendingExpectations) observeCreation(key NodeLauncherKey) {
	pe.mu.Lock()
	defer pe.mu.Unlock()
	e, ok := pe.entries[key]
	if !ok {
		return
	}
	e.adds--
	pe.cleanupIfSatisfied(key, e)
}

// observeDeletion is called when the informer notifies of a launcher Pod
// deletion for the given key, reducing the pending deletion count.
func (pe *pendingExpectations) observeDeletion(key NodeLauncherKey) {
	pe.mu.Lock()
	defer pe.mu.Unlock()
	e, ok := pe.entries[key]
	if !ok {
		return
	}
	e.dels--
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
	if e.adds <= 0 && e.dels <= 0 {
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
	e := &expectationEntry{}
	pe.entries[key] = e
	return e
}

func (pe *pendingExpectations) cleanupIfSatisfied(key NodeLauncherKey, e *expectationEntry) {
	if e.adds <= 0 && e.dels <= 0 {
		delete(pe.entries, key)
	}
}
