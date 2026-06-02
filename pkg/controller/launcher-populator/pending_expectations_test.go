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
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
)

// uidSet is a small helper to build the presentUIDs argument tersely.
func uidSet(uids ...types.UID) sets.Set[types.UID] {
	return sets.New(uids...)
}

func TestPendingExpectations_BasicCreation(t *testing.T) {
	pe := newPendingExpectations(DefaultExpectationTimeout)
	key := NodeLauncherKey{NodeName: "node-1", LauncherConfigName: "lc-1"}

	// Initially satisfied: no expectations, no cache entries.
	if status := pe.check(key, uidSet()); status != ExpectationsSatisfied {
		t.Errorf("expected ExpectationsSatisfied, got %v", status)
	}

	// After expecting creations, with cache still empty, should be Waiting.
	pe.expectCreation(key, types.UID("uid-1"))
	pe.expectCreation(key, types.UID("uid-2"))
	pe.expectCreation(key, types.UID("uid-3"))
	if status := pe.check(key, uidSet()); status != ExpectationsWaiting {
		t.Errorf("expected ExpectationsWaiting, got %v", status)
	}

	// Once all expected UIDs appear in the informer cache, satisfied.
	present := uidSet("uid-1", "uid-2", "uid-3")
	if status := pe.check(key, present); status != ExpectationsSatisfied {
		t.Errorf("expected ExpectationsSatisfied after cache convergence, got %v", status)
	}
}

func TestPendingExpectations_BasicDeletion(t *testing.T) {
	pe := newPendingExpectations(DefaultExpectationTimeout)
	key := NodeLauncherKey{NodeName: "node-1", LauncherConfigName: "lc-1"}

	pe.expectDeletion(key, types.UID("uid-a"))
	pe.expectDeletion(key, types.UID("uid-b"))
	// Both UIDs still in cache → still waiting.
	if status := pe.check(key, uidSet("uid-a", "uid-b")); status != ExpectationsWaiting {
		t.Errorf("expected ExpectationsWaiting, got %v", status)
	}

	// uid-a has been evicted from cache; uid-b still present.
	if status := pe.check(key, uidSet("uid-b")); status != ExpectationsWaiting {
		t.Errorf("expected ExpectationsWaiting with 1 pending, got %v", status)
	}

	// Both gone from cache → satisfied.
	if status := pe.check(key, uidSet()); status != ExpectationsSatisfied {
		t.Errorf("expected ExpectationsSatisfied after both gone, got %v", status)
	}
}

func TestPendingExpectations_MixedCreationDeletion(t *testing.T) {
	pe := newPendingExpectations(DefaultExpectationTimeout)
	key := NodeLauncherKey{NodeName: "node-1", LauncherConfigName: "lc-1"}

	pe.expectCreation(key, types.UID("uid-c1"))
	pe.expectCreation(key, types.UID("uid-c2"))
	pe.expectDeletion(key, types.UID("uid-d1"))

	// Creations now present, but uid-d1 still in cache → still waiting.
	if status := pe.check(key, uidSet("uid-c1", "uid-c2", "uid-d1")); status != ExpectationsWaiting {
		t.Errorf("expected ExpectationsWaiting (deletion pending), got %v", status)
	}

	// uid-d1 finally evicted; all expectations met.
	if status := pe.check(key, uidSet("uid-c1", "uid-c2")); status != ExpectationsSatisfied {
		t.Errorf("expected ExpectationsSatisfied, got %v", status)
	}
}

func TestPendingExpectations_Timeout(t *testing.T) {
	pe := newPendingExpectations(DefaultExpectationTimeout)
	key := NodeLauncherKey{NodeName: "node-1", LauncherConfigName: "lc-1"}

	pe.expectCreation(key, types.UID("uid-timeout"))

	// Manually set the deadline to the past to simulate timeout.
	pe.mu.Lock()
	pe.entries[key].deadline = time.Now().Add(-1 * time.Second)
	pe.mu.Unlock()

	// Cache has not caught up: pending UID never appears.
	if status := pe.check(key, uidSet()); status != ExpectationsTimedOut {
		t.Errorf("expected ExpectationsTimedOut, got %v", status)
	}
}

func TestPendingExpectations_Reset(t *testing.T) {
	pe := newPendingExpectations(DefaultExpectationTimeout)
	key := NodeLauncherKey{NodeName: "node-1", LauncherConfigName: "lc-1"}

	pe.expectCreation(key, types.UID("uid-r1"))
	pe.expectCreation(key, types.UID("uid-r2"))
	pe.expectCreation(key, types.UID("uid-r3"))
	pe.expectCreation(key, types.UID("uid-r4"))
	pe.expectCreation(key, types.UID("uid-r5"))
	pe.reset(key)

	if status := pe.check(key, uidSet()); status != ExpectationsSatisfied {
		t.Errorf("expected ExpectationsSatisfied after reset, got %v", status)
	}
}

// TestPendingExpectations_ExpectAfterCacheAlreadyCaughtUp covers the race
// where the informer cache reflects the new Pod before the controller calls
// expectCreation (the watch fired while the Create response was still in
// flight). The next check immediately reconciles the expectation against
// the cache and reports Satisfied, with no extra bookkeeping needed.
func TestPendingExpectations_ExpectAfterCacheAlreadyCaughtUp(t *testing.T) {
	pe := newPendingExpectations(DefaultExpectationTimeout)
	key := NodeLauncherKey{NodeName: "node-1", LauncherConfigName: "lc-1"}
	uid := types.UID("uid-race-c")

	// Controller records the expectation after the cache already shows it.
	pe.expectCreation(key, uid)

	if status := pe.check(key, uidSet(uid)); status != ExpectationsSatisfied {
		t.Errorf("expected ExpectationsSatisfied when cache already reflects the UID, got %v", status)
	}
	pe.mu.Lock()
	_, exists := pe.entries[key]
	pe.mu.Unlock()
	if exists {
		t.Errorf("entry should have been garbage-collected after expectation satisfied")
	}
}

// TestPendingExpectations_ExpectAfterCacheAlreadyEvicted mirrors the
// expect-after-cache race for deletions.
func TestPendingExpectations_ExpectAfterCacheAlreadyEvicted(t *testing.T) {
	pe := newPendingExpectations(DefaultExpectationTimeout)
	key := NodeLauncherKey{NodeName: "node-1", LauncherConfigName: "lc-1"}
	uid := types.UID("uid-race-d")

	pe.expectDeletion(key, uid)

	// Cache no longer holds the UID; expectation is immediately satisfied.
	if status := pe.check(key, uidSet()); status != ExpectationsSatisfied {
		t.Errorf("expected ExpectationsSatisfied when cache already evicted the UID, got %v", status)
	}
	pe.mu.Lock()
	_, exists := pe.entries[key]
	pe.mu.Unlock()
	if exists {
		t.Errorf("entry should have been garbage-collected after expectation satisfied")
	}
}

func TestPendingExpectations_MultipleKeys(t *testing.T) {
	pe := newPendingExpectations(DefaultExpectationTimeout)
	key1 := NodeLauncherKey{NodeName: "node-1", LauncherConfigName: "lc-1"}
	key2 := NodeLauncherKey{NodeName: "node-2", LauncherConfigName: "lc-1"}

	pe.expectCreation(key1, types.UID("uid-k1a"))
	pe.expectCreation(key1, types.UID("uid-k1b"))
	pe.expectDeletion(key2, types.UID("uid-k2a"))

	// Both keys waiting: key1 needs UIDs to appear, key2 needs uid-k2a gone.
	if status := pe.check(key1, uidSet()); status != ExpectationsWaiting {
		t.Errorf("key1: expected ExpectationsWaiting, got %v", status)
	}
	if status := pe.check(key2, uidSet("uid-k2a")); status != ExpectationsWaiting {
		t.Errorf("key2: expected ExpectationsWaiting, got %v", status)
	}

	// Satisfy key2 by evicting its UID from the cache.
	if status := pe.check(key2, uidSet()); status != ExpectationsSatisfied {
		t.Errorf("key2: expected ExpectationsSatisfied, got %v", status)
	}
	// key1 still waiting.
	if status := pe.check(key1, uidSet()); status != ExpectationsWaiting {
		t.Errorf("key1: expected ExpectationsWaiting, got %v", status)
	}
}

// TestPendingExpectations_OtherActorDoesNotSatisfy verifies that pods created
// by a different actor for the same key do not accidentally satisfy our
// UID-specific expectation. Only the appearance of our exact UID counts.
func TestPendingExpectations_OtherActorDoesNotSatisfy(t *testing.T) {
	pe := newPendingExpectations(DefaultExpectationTimeout)
	key := NodeLauncherKey{NodeName: "node-1", LauncherConfigName: "lc-1"}

	// We expect our specific Pod UID.
	pe.expectCreation(key, types.UID("uid-ours"))

	// Cache contains only another actor's pod for the same key.
	if status := pe.check(key, uidSet("uid-theirs")); status != ExpectationsWaiting {
		t.Errorf("expected ExpectationsWaiting (other actor's pod should not satisfy), got %v", status)
	}

	// When our UID finally appears in the cache, the expectation is satisfied.
	if status := pe.check(key, uidSet("uid-theirs", "uid-ours")); status != ExpectationsSatisfied {
		t.Errorf("expected ExpectationsSatisfied, got %v", status)
	}
}

// TestPendingExpectations_CheckWithoutExpectations confirms check() is a
// no-op when nothing is pending, regardless of what the cache contains.
func TestPendingExpectations_CheckWithoutExpectations(t *testing.T) {
	pe := newPendingExpectations(DefaultExpectationTimeout)
	key := NodeLauncherKey{NodeName: "node-1", LauncherConfigName: "lc-1"}

	if status := pe.check(key, uidSet("uid-foreign")); status != ExpectationsSatisfied {
		t.Errorf("expected ExpectationsSatisfied with no expectations, got %v", status)
	}
	pe.mu.Lock()
	_, exists := pe.entries[key]
	pe.mu.Unlock()
	if exists {
		t.Errorf("no entry should have been created by check() alone")
	}
}
