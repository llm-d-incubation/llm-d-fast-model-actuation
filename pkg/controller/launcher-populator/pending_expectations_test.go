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
)

func TestPendingExpectations_BasicCreation(t *testing.T) {
	pe := newPendingExpectations(DefaultExpectationTimeout)
	key := NodeLauncherKey{NodeName: "node-1", LauncherConfigName: "lc-1"}

	// Initially satisfied
	if status := pe.check(key); status != ExpectationsSatisfied {
		t.Errorf("expected ExpectationsSatisfied, got %v", status)
	}

	// After expecting creations, should be Waiting
	pe.expectCreation(key, types.UID("uid-1"))
	pe.expectCreation(key, types.UID("uid-2"))
	pe.expectCreation(key, types.UID("uid-3"))
	if status := pe.check(key); status != ExpectationsWaiting {
		t.Errorf("expected ExpectationsWaiting, got %v", status)
	}

	// Observe all 3 creations
	pe.observeCreation(key, types.UID("uid-1"))
	pe.observeCreation(key, types.UID("uid-2"))
	pe.observeCreation(key, types.UID("uid-3"))

	// Should be satisfied now
	if status := pe.check(key); status != ExpectationsSatisfied {
		t.Errorf("expected ExpectationsSatisfied after all observations, got %v", status)
	}
}

func TestPendingExpectations_BasicDeletion(t *testing.T) {
	pe := newPendingExpectations(DefaultExpectationTimeout)
	key := NodeLauncherKey{NodeName: "node-1", LauncherConfigName: "lc-1"}

	pe.expectDeletion(key, types.UID("uid-a"))
	pe.expectDeletion(key, types.UID("uid-b"))
	if status := pe.check(key); status != ExpectationsWaiting {
		t.Errorf("expected ExpectationsWaiting, got %v", status)
	}

	pe.observeDeletion(key, types.UID("uid-a"))
	// Still one pending
	if status := pe.check(key); status != ExpectationsWaiting {
		t.Errorf("expected ExpectationsWaiting with 1 pending, got %v", status)
	}

	pe.observeDeletion(key, types.UID("uid-b"))
	if status := pe.check(key); status != ExpectationsSatisfied {
		t.Errorf("expected ExpectationsSatisfied after all observations, got %v", status)
	}
}

func TestPendingExpectations_MixedCreationDeletion(t *testing.T) {
	pe := newPendingExpectations(DefaultExpectationTimeout)
	key := NodeLauncherKey{NodeName: "node-1", LauncherConfigName: "lc-1"}

	pe.expectCreation(key, types.UID("uid-c1"))
	pe.expectCreation(key, types.UID("uid-c2"))
	pe.expectDeletion(key, types.UID("uid-d1"))

	// Observe the creations but not deletion
	pe.observeCreation(key, types.UID("uid-c1"))
	pe.observeCreation(key, types.UID("uid-c2"))
	// creations satisfied, deletion still pending
	if status := pe.check(key); status != ExpectationsWaiting {
		t.Errorf("expected ExpectationsWaiting (dels pending), got %v", status)
	}

	pe.observeDeletion(key, types.UID("uid-d1"))
	if status := pe.check(key); status != ExpectationsSatisfied {
		t.Errorf("expected ExpectationsSatisfied, got %v", status)
	}
}

func TestPendingExpectations_Timeout(t *testing.T) {
	pe := newPendingExpectations(DefaultExpectationTimeout)
	key := NodeLauncherKey{NodeName: "node-1", LauncherConfigName: "lc-1"}

	pe.expectCreation(key, types.UID("uid-timeout"))

	// Manually set the deadline to the past to simulate timeout
	pe.mu.Lock()
	pe.entries[key].deadline = time.Now().Add(-1 * time.Second)
	pe.mu.Unlock()

	if status := pe.check(key); status != ExpectationsTimedOut {
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

	if status := pe.check(key); status != ExpectationsSatisfied {
		t.Errorf("expected ExpectationsSatisfied after reset, got %v", status)
	}
}

func TestPendingExpectations_ObserveWithoutExpectation(t *testing.T) {
	pe := newPendingExpectations(DefaultExpectationTimeout)
	key := NodeLauncherKey{NodeName: "node-1", LauncherConfigName: "lc-1"}

	// Should not panic when observing without any expectation
	pe.observeCreation(key, types.UID("uid-unknown"))
	pe.observeDeletion(key, types.UID("uid-unknown"))

	if status := pe.check(key); status != ExpectationsSatisfied {
		t.Errorf("expected ExpectationsSatisfied, got %v", status)
	}
}

func TestPendingExpectations_MultipleKeys(t *testing.T) {
	pe := newPendingExpectations(DefaultExpectationTimeout)
	key1 := NodeLauncherKey{NodeName: "node-1", LauncherConfigName: "lc-1"}
	key2 := NodeLauncherKey{NodeName: "node-2", LauncherConfigName: "lc-1"}

	pe.expectCreation(key1, types.UID("uid-k1a"))
	pe.expectCreation(key1, types.UID("uid-k1b"))
	pe.expectDeletion(key2, types.UID("uid-k2a"))

	// key1 waiting, key2 waiting
	if status := pe.check(key1); status != ExpectationsWaiting {
		t.Errorf("key1: expected ExpectationsWaiting, got %v", status)
	}
	if status := pe.check(key2); status != ExpectationsWaiting {
		t.Errorf("key2: expected ExpectationsWaiting, got %v", status)
	}

	// Satisfy key2
	pe.observeDeletion(key2, types.UID("uid-k2a"))
	if status := pe.check(key2); status != ExpectationsSatisfied {
		t.Errorf("key2: expected ExpectationsSatisfied, got %v", status)
	}
	// key1 still waiting
	if status := pe.check(key1); status != ExpectationsWaiting {
		t.Errorf("key1: expected ExpectationsWaiting, got %v", status)
	}
}

func TestPendingExpectations_OverObserve(t *testing.T) {
	pe := newPendingExpectations(DefaultExpectationTimeout)
	key := NodeLauncherKey{NodeName: "node-1", LauncherConfigName: "lc-1"}

	pe.expectCreation(key, types.UID("uid-oo"))
	pe.observeCreation(key, types.UID("uid-oo"))
	// Over-observe: observing a UID not in the set is a no-op
	pe.observeCreation(key, types.UID("uid-oo"))

	if status := pe.check(key); status != ExpectationsSatisfied {
		t.Errorf("expected ExpectationsSatisfied after over-observe, got %v", status)
	}
}

func TestPendingExpectations_OtherActorDoesNotSatisfy(t *testing.T) {
	pe := newPendingExpectations(DefaultExpectationTimeout)
	key := NodeLauncherKey{NodeName: "node-1", LauncherConfigName: "lc-1"}

	// We expect our specific Pod UID
	pe.expectCreation(key, types.UID("uid-ours"))

	// Another actor creates a different pod for the same key
	pe.observeCreation(key, types.UID("uid-theirs"))

	// Our expectation should NOT be satisfied (different UID)
	if status := pe.check(key); status != ExpectationsWaiting {
		t.Errorf("expected ExpectationsWaiting (other actor's pod should not satisfy), got %v", status)
	}

	// Only observing our specific UID satisfies the expectation
	pe.observeCreation(key, types.UID("uid-ours"))
	if status := pe.check(key); status != ExpectationsSatisfied {
		t.Errorf("expected ExpectationsSatisfied, got %v", status)
	}
}
