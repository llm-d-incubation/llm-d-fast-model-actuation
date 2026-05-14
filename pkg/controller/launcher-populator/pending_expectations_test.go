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
)

func TestPendingExpectations_BasicCreation(t *testing.T) {
	pe := newPendingExpectations()
	key := NodeLauncherKey{NodeName: "node-1", LauncherConfigName: "lc-1"}

	// Initially satisfied
	if status := pe.check(key); status != ExpectationsSatisfied {
		t.Errorf("expected ExpectationsSatisfied, got %v", status)
	}

	// After expecting creations, should be Waiting
	pe.expectCreations(key, 3)
	if status := pe.check(key); status != ExpectationsWaiting {
		t.Errorf("expected ExpectationsWaiting, got %v", status)
	}

	// Observe all 3 creations
	pe.observeCreation(key)
	pe.observeCreation(key)
	pe.observeCreation(key)

	// Should be satisfied now
	if status := pe.check(key); status != ExpectationsSatisfied {
		t.Errorf("expected ExpectationsSatisfied after all observations, got %v", status)
	}
}

func TestPendingExpectations_BasicDeletion(t *testing.T) {
	pe := newPendingExpectations()
	key := NodeLauncherKey{NodeName: "node-1", LauncherConfigName: "lc-1"}

	pe.expectDeletions(key, 2)
	if status := pe.check(key); status != ExpectationsWaiting {
		t.Errorf("expected ExpectationsWaiting, got %v", status)
	}

	pe.observeDeletion(key)
	// Still one pending
	if status := pe.check(key); status != ExpectationsWaiting {
		t.Errorf("expected ExpectationsWaiting with 1 pending, got %v", status)
	}

	pe.observeDeletion(key)
	if status := pe.check(key); status != ExpectationsSatisfied {
		t.Errorf("expected ExpectationsSatisfied after all observations, got %v", status)
	}
}

func TestPendingExpectations_MixedCreationDeletion(t *testing.T) {
	pe := newPendingExpectations()
	key := NodeLauncherKey{NodeName: "node-1", LauncherConfigName: "lc-1"}

	pe.expectCreations(key, 2)
	pe.expectDeletions(key, 1)

	// Observe the creation but not deletion
	pe.observeCreation(key)
	pe.observeCreation(key)
	// adds satisfied, dels still pending
	if status := pe.check(key); status != ExpectationsWaiting {
		t.Errorf("expected ExpectationsWaiting (dels pending), got %v", status)
	}

	pe.observeDeletion(key)
	if status := pe.check(key); status != ExpectationsSatisfied {
		t.Errorf("expected ExpectationsSatisfied, got %v", status)
	}
}

func TestPendingExpectations_Timeout(t *testing.T) {
	pe := newPendingExpectations()
	key := NodeLauncherKey{NodeName: "node-1", LauncherConfigName: "lc-1"}

	pe.expectCreations(key, 1)

	// Manually set the deadline to the past to simulate timeout
	pe.mu.Lock()
	pe.entries[key].deadline = time.Now().Add(-1 * time.Second)
	pe.mu.Unlock()

	if status := pe.check(key); status != ExpectationsTimedOut {
		t.Errorf("expected ExpectationsTimedOut, got %v", status)
	}
}

func TestPendingExpectations_Reset(t *testing.T) {
	pe := newPendingExpectations()
	key := NodeLauncherKey{NodeName: "node-1", LauncherConfigName: "lc-1"}

	pe.expectCreations(key, 5)
	pe.reset(key)

	if status := pe.check(key); status != ExpectationsSatisfied {
		t.Errorf("expected ExpectationsSatisfied after reset, got %v", status)
	}
}

func TestPendingExpectations_ObserveWithoutExpectation(t *testing.T) {
	pe := newPendingExpectations()
	key := NodeLauncherKey{NodeName: "node-1", LauncherConfigName: "lc-1"}

	// Should not panic when observing without any expectation
	pe.observeCreation(key)
	pe.observeDeletion(key)

	if status := pe.check(key); status != ExpectationsSatisfied {
		t.Errorf("expected ExpectationsSatisfied, got %v", status)
	}
}

func TestPendingExpectations_MultipleKeys(t *testing.T) {
	pe := newPendingExpectations()
	key1 := NodeLauncherKey{NodeName: "node-1", LauncherConfigName: "lc-1"}
	key2 := NodeLauncherKey{NodeName: "node-2", LauncherConfigName: "lc-1"}

	pe.expectCreations(key1, 2)
	pe.expectDeletions(key2, 1)

	// key1 waiting, key2 waiting
	if status := pe.check(key1); status != ExpectationsWaiting {
		t.Errorf("key1: expected ExpectationsWaiting, got %v", status)
	}
	if status := pe.check(key2); status != ExpectationsWaiting {
		t.Errorf("key2: expected ExpectationsWaiting, got %v", status)
	}

	// Satisfy key2
	pe.observeDeletion(key2)
	if status := pe.check(key2); status != ExpectationsSatisfied {
		t.Errorf("key2: expected ExpectationsSatisfied, got %v", status)
	}
	// key1 still waiting
	if status := pe.check(key1); status != ExpectationsWaiting {
		t.Errorf("key1: expected ExpectationsWaiting, got %v", status)
	}
}

func TestPendingExpectations_OverObserve(t *testing.T) {
	pe := newPendingExpectations()
	key := NodeLauncherKey{NodeName: "node-1", LauncherConfigName: "lc-1"}

	pe.expectCreations(key, 1)
	pe.observeCreation(key)
	// Over-observe: should still be satisfied (entry cleaned up)
	pe.observeCreation(key)

	if status := pe.check(key); status != ExpectationsSatisfied {
		t.Errorf("expected ExpectationsSatisfied after over-observe, got %v", status)
	}
}
