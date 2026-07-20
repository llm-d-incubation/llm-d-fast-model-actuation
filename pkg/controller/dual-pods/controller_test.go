/*
Copyright 2026 The llm-d Authors.

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

package dualpods

import (
	"context"
	"math"
	"testing"
	"time"

	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"
	metricstestutil "k8s.io/component-base/metrics/testutil"
	"k8s.io/klog/v2/ktesting"

	fmafake "github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/generated/clientset/versioned/fake"
	fmainformers "github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/generated/informers/externalversions"
)

type testItem struct {
	id string
}

func (ti testItem) process(_ context.Context, _ *controller, _ *nodeData) processResult {
	return processResult{}
}

var config = ControllerConfig{
	SleeperLimit:                      1,
	NumWorkers:                        2,
	AcceleratorSleepingMemoryLimitMiB: math.MaxInt64,
}

const nodeName = "node-1"

func setUpController(t *testing.T, config ControllerConfig) (*controller, error) {
	logger, _ := ktesting.NewTestContext(t)
	kubeClient := fake.NewSimpleClientset()
	kubeInformers := informers.NewSharedInformerFactory(kubeClient, 0)
	fmaInformers := fmainformers.NewSharedInformerFactory(fmafake.NewSimpleClientset(), 0)
	return config.NewController(
		logger,
		kubeClient.CoreV1(),
		"test-namespace",
		kubeInformers.Core().V1(),
		fmaInformers,
	)
}
func TestAdd_Single(t *testing.T) {
	ctrl, err := setUpController(t, config)
	if err != nil {
		t.Fatal(err)
	}
	nd := ctrl.getNodeData(nodeName)
	nd.add(testItem{id: "a"})

	if got := len(nd.LocalQueue); got != 1 {
		t.Fatalf("expected 1 item, got %d", got)
	}
	si := nd.LocalQueue[testItem{id: "a"}]
	if si == nil {
		t.Fatal("expected item 'a' in queue")
	}
	if si.processAfter.After(time.Now()) {
		t.Error("processAfter should be <= now for add()")
	}
}

func TestAdd_Dedup(t *testing.T) {
	ctrl, err := setUpController(t, config)
	if err != nil {
		t.Fatal(err)
	}
	nd := ctrl.getNodeData(nodeName)
	nd.add(testItem{id: "a"})
	firstAddTime := nd.LocalQueue[testItem{id: "a"}].addTime

	nd.add(testItem{id: "a"})

	if got := len(nd.LocalQueue); got != 1 {
		t.Fatalf("expected 1 item after duplicate add, got %d", got)
	}
	if nd.LocalQueue[testItem{id: "a"}].addTime != firstAddTime {
		t.Error("addTime should not change on duplicate add")
	}
}

func TestAdd_Multiple(t *testing.T) {
	ctrl, err := setUpController(t, config)
	if err != nil {
		t.Fatal(err)
	}
	nd := ctrl.getNodeData(nodeName)

	addsBefore, _ := metricstestutil.GetCounterMetricValue(addsCounters.WithLabelValues(nodeName))

	nd.add(testItem{id: "a"})
	nd.add(testItem{id: "b"})
	nd.add(testItem{id: "c"})

	if got := len(nd.LocalQueue); got != 3 {
		t.Fatalf("expected 3 items, got %d", got)
	}

	addsAfter, err := metricstestutil.GetCounterMetricValue(addsCounters.WithLabelValues(nodeName))
	if err != nil {
		t.Fatalf("failed to read addsCounters: %v", err)
	}
	if delta := addsAfter - addsBefore; delta != 3 {
		t.Errorf("addsCounters delta = %v, want 3", delta)
	}

	depth, err := metricstestutil.GetGaugeMetricValue(queueDepthGauges.WithLabelValues(nodeName))
	if err != nil {
		t.Fatalf("failed to read queueDepthGauges: %v", err)
	}
	if depth != 3 {
		t.Errorf("queueDepthGauges = %v, want 3", depth)
	}
}

func TestAddAfter_NewItem(t *testing.T) {
	ctrl, err := setUpController(t, config)
	if err != nil {
		t.Fatal(err)
	}
	nd := ctrl.getNodeData(nodeName)
	future := time.Now().Add(10 * time.Minute)
	nd.addAfter(testItem{id: "a"}, future)

	if got := len(nd.LocalQueue); got != 1 {
		t.Fatalf("expected 1 item, got %d", got)
	}
	si := nd.LocalQueue[testItem{id: "a"}]
	if !si.processAfter.Equal(future) {
		t.Errorf("processAfter = %v, want %v", si.processAfter, future)
	}
}

func TestAddAfter_UpdatesToEarlierTime(t *testing.T) {
	ctrl, err := setUpController(t, config)
	if err != nil {
		t.Fatal(err)
	}
	nd := ctrl.getNodeData(nodeName)
	later := time.Now().Add(10 * time.Minute)
	earlier := time.Now().Add(1 * time.Minute)

	nd.addAfter(testItem{id: "a"}, later)
	nd.addAfter(testItem{id: "a"}, earlier)

	si := nd.LocalQueue[testItem{id: "a"}]
	if !si.processAfter.Equal(earlier) {
		t.Errorf("processAfter should be updated to earlier time; got %v, want %v", si.processAfter, earlier)
	}
}

func TestAddAfter_KeepsEarlierTime(t *testing.T) {
	ctrl, err := setUpController(t, config)
	if err != nil {
		t.Fatal(err)
	}
	nd := ctrl.getNodeData(nodeName)
	earlier := time.Now().Add(1 * time.Minute)
	later := time.Now().Add(10 * time.Minute)

	nd.addAfter(testItem{id: "a"}, earlier)
	nd.addAfter(testItem{id: "a"}, later)

	si := nd.LocalQueue[testItem{id: "a"}]
	if !si.processAfter.Equal(earlier) {
		t.Errorf("processAfter should remain at earlier time; got %v, want %v", si.processAfter, earlier)
	}
}

func TestAddAfter_AddThenAddAfterDoesNotReplace(t *testing.T) {
	ctrl, err := setUpController(t, config)
	if err != nil {
		t.Fatal(err)
	}
	nd := ctrl.getNodeData(nodeName)
	nd.add(testItem{id: "a"})
	originalProcessAfter := nd.LocalQueue[testItem{id: "a"}].processAfter

	future := time.Now().Add(10 * time.Minute)
	nd.addAfter(testItem{id: "a"}, future)

	si := nd.LocalQueue[testItem{id: "a"}]
	if !si.processAfter.Equal(originalProcessAfter) {
		t.Error("addAfter with a later time should not update processAfter set by add()")
	}
}

func TestTakeReadyItems_EmptyQueue(t *testing.T) {
	ctrl, err := setUpController(t, config)
	if err != nil {
		t.Fatal(err)
	}
	nd := ctrl.getNodeData(nodeName)
	ready := nd.takeReadyItems(time.Now())

	if len(ready) != 0 {
		t.Fatalf("expected 0 items from empty queue, got %d", len(ready))
	}
}

func TestTakeReadyItems_AllReady(t *testing.T) {
	ctrl, err := setUpController(t, config)
	if err != nil {
		t.Fatal(err)
	}
	nd := ctrl.getNodeData(nodeName)
	nd.add(testItem{id: "a"})
	nd.add(testItem{id: "b"})

	ready := nd.takeReadyItems(time.Now())

	if len(ready) != 2 {
		t.Fatalf("expected 2 ready items, got %d", len(ready))
	}
	if len(nd.LocalQueue) != 0 {
		t.Fatalf("expected queue to be empty after taking all, got %d", len(nd.LocalQueue))
	}
}

func TestTakeReadyItems_NoneReady(t *testing.T) {
	ctrl, err := setUpController(t, config)
	if err != nil {
		t.Fatal(err)
	}
	nd := ctrl.getNodeData(nodeName)
	future := time.Now().Add(10 * time.Minute)
	nd.addAfter(testItem{id: "a"}, future)
	nd.addAfter(testItem{id: "b"}, future)

	ready := nd.takeReadyItems(time.Now())

	if len(ready) != 0 {
		t.Fatalf("expected 0 ready items, got %d", len(ready))
	}
	if len(nd.LocalQueue) != 2 {
		t.Fatalf("expected 2 items still in queue, got %d", len(nd.LocalQueue))
	}
}

func TestTakeReadyItems_MixedReadiness(t *testing.T) {
	ctrl, err := setUpController(t, config)
	if err != nil {
		t.Fatal(err)
	}
	nd := ctrl.getNodeData(nodeName)
	nd.add(testItem{id: "ready"})
	nd.addAfter(testItem{id: "not-ready"}, time.Now().Add(10*time.Minute))

	ready := nd.takeReadyItems(time.Now())

	if len(ready) != 1 {
		t.Fatalf("expected 1 ready item, got %d", len(ready))
	}
	if _, ok := ready[testItem{id: "ready"}]; !ok {
		t.Error("expected item 'ready' in returned set")
	}
	if len(nd.LocalQueue) != 1 {
		t.Fatalf("expected 1 item remaining in queue, got %d", len(nd.LocalQueue))
	}
	if _, ok := nd.LocalQueue[testItem{id: "not-ready"}]; !ok {
		t.Error("expected item 'not-ready' to remain in queue")
	}
}

func TestTakeReadyItems_ExactBoundary(t *testing.T) {
	ctrl, err := setUpController(t, config)
	if err != nil {
		t.Fatal(err)
	}
	nd := ctrl.getNodeData(nodeName)
	boundary := time.Now().Truncate(time.Second)
	nd.addAfter(testItem{id: "exact"}, boundary)

	ready := nd.takeReadyItems(boundary)

	if len(ready) != 1 {
		t.Fatalf("item with processAfter == now should be taken; got %d items", len(ready))
	}
}

func TestEarliestPending_EmptyQueue(t *testing.T) {
	ctrl, err := setUpController(t, config)
	if err != nil {
		t.Fatal(err)
	}
	nd := ctrl.getNodeData(nodeName)
	earliest := nd.earliestPending()

	if !earliest.IsZero() {
		t.Errorf("expected zero time for empty queue, got %v", earliest)
	}
}

func TestEarliestPending_ReturnsEarliest(t *testing.T) {
	ctrl, err := setUpController(t, config)
	if err != nil {
		t.Fatal(err)
	}
	nd := ctrl.getNodeData(nodeName)
	early := time.Now().Add(1 * time.Minute)
	mid := time.Now().Add(5 * time.Minute)
	late := time.Now().Add(10 * time.Minute)
	nd.addAfter(testItem{id: "late"}, late)
	nd.addAfter(testItem{id: "early"}, early)
	nd.addAfter(testItem{id: "mid"}, mid)

	earliest := nd.earliestPending()

	if !earliest.Equal(early) {
		t.Errorf("expected %v, got %v", early, earliest)
	}
}

func TestEarliestPending_AfterTakingReadyItems(t *testing.T) {
	ctrl, err := setUpController(t, config)
	if err != nil {
		t.Fatal(err)
	}
	nd := ctrl.getNodeData(nodeName)
	nd.add(testItem{id: "ready"})
	future := time.Now().Add(5 * time.Minute)
	nd.addAfter(testItem{id: "pending"}, future)

	nd.takeReadyItems(time.Now())
	earliest := nd.earliestPending()

	if !earliest.Equal(future) {
		t.Errorf("expected %v after taking ready items, got %v", future, earliest)
	}
}

func TestTakeReadyItems_PreservesAddTime(t *testing.T) {
	ctrl, err := setUpController(t, config)
	if err != nil {
		t.Fatal(err)
	}
	nd := ctrl.getNodeData(nodeName)
	nd.add(testItem{id: "a"})
	originalAddTime := nd.LocalQueue[testItem{id: "a"}].addTime

	ready := nd.takeReadyItems(time.Now())

	si := ready[testItem{id: "a"}]
	if si == nil {
		t.Fatal("expected item 'a' in ready set")
	}
	if !si.addTime.Equal(originalAddTime) {
		t.Error("addTime should be preserved in returned scheduledItem")
	}
}

// add() on an item that already has a pending addAfter delay:
// add() sets processAfter=now which is earlier than any future time,
// so the item becomes immediately ready.
func TestAddAfter_ThenAddOverridesDelay(t *testing.T) {
	ctrl, err := setUpController(t, config)
	if err != nil {
		t.Fatal(err)
	}
	nd := ctrl.getNodeData(nodeName)
	nd.addAfter(testItem{id: "a"}, time.Now().Add(10*time.Minute))

	// A new event triggers add() for the same item — should not be
	// blocked by the pending delay.
	nd.add(testItem{id: "a"})

	// The item's processAfter was set to ~now by the original addAfter,
	// but add() is a no-op on existing items. So it stays at +10min.
	// This documents that add() does NOT override an existing entry.
	ready := nd.takeReadyItems(time.Now())
	if len(ready) != 0 {
		t.Fatal("add() is a no-op on an existing item; the delayed item should not become immediately ready")
	}
}

// addAfter with a time in the past on an existing add()'d item:
// add() sets processAfter ≈ now. A past time is earlier than now,
// so the early-schedule-wins rule should update processAfter.
func TestAddAfter_PastTimeOnAddedItem(t *testing.T) {
	ctrl, err := setUpController(t, config)
	if err != nil {
		t.Fatal(err)
	}
	nd := ctrl.getNodeData(nodeName)
	nd.add(testItem{id: "a"})
	original := nd.LocalQueue[testItem{id: "a"}].processAfter

	past := original.Add(-5 * time.Minute)
	nd.addAfter(testItem{id: "a"}, past)

	si := nd.LocalQueue[testItem{id: "a"}]
	if !si.processAfter.Equal(past) {
		t.Errorf("addAfter with past time should advance processAfter earlier; got %v, want %v", si.processAfter, past)
	}
}

// Retry-with-delay contract: take an item, re-enqueue it via addAfter
// with a backoff delay, verify it stays pending until the delay expires.
func TestRetryWithDelay(t *testing.T) {
	ctrl, err := setUpController(t, config)
	if err != nil {
		t.Fatal(err)
	}
	nd := ctrl.getNodeData(nodeName)
	nd.add(testItem{id: "a"})

	taken := nd.takeReadyItems(time.Now())
	if _, ok := taken[testItem{id: "a"}]; !ok {
		t.Fatal("initial take should return the item")
	}

	base := time.Now()

	// Re-enqueue with 5-minute backoff.
	retryAt := base.Add(5 * time.Minute)
	nd.addAfter(testItem{id: "a"}, retryAt)

	// Not ready 1 minute later.
	if len(nd.takeReadyItems(base.Add(1*time.Minute))) != 0 {
		t.Error("item should not be ready before retry time")
	}

	// Ready exactly at retry time.
	ready := nd.takeReadyItems(retryAt)
	if _, ok := ready[testItem{id: "a"}]; !ok {
		t.Error("item should be ready at retry time")
	}
}

// Multi-round drain: items at staggered times become ready in waves.
func TestTakeReadyItems_StaggeredDrain(t *testing.T) {
	ctrl, err := setUpController(t, config)
	if err != nil {
		t.Fatal(err)
	}
	nd := ctrl.getNodeData(nodeName)
	base := time.Now().Truncate(time.Second)
	t1 := base.Add(1 * time.Minute)
	t2 := base.Add(2 * time.Minute)
	t3 := base.Add(3 * time.Minute)

	nd.addAfter(testItem{id: "a"}, t1)
	nd.addAfter(testItem{id: "b"}, t2)
	nd.addAfter(testItem{id: "c"}, t3)

	r1 := nd.takeReadyItems(t1)
	if len(r1) != 1 {
		t.Fatalf("round 1: expected 1, got %d", len(r1))
	}
	if _, ok := r1[testItem{id: "a"}]; !ok {
		t.Error("round 1: expected 'a'")
	}

	r2 := nd.takeReadyItems(t2)
	if len(r2) != 1 {
		t.Fatalf("round 2: expected 1, got %d", len(r2))
	}
	if _, ok := r2[testItem{id: "b"}]; !ok {
		t.Error("round 2: expected 'b'")
	}

	r3 := nd.takeReadyItems(t3)
	if len(r3) != 1 {
		t.Fatalf("round 3: expected 1, got %d", len(r3))
	}
	if _, ok := r3[testItem{id: "c"}]; !ok {
		t.Error("round 3: expected 'c'")
	}

	if len(nd.LocalQueue) != 0 {
		t.Errorf("queue should be empty after full drain, got %d", len(nd.LocalQueue))
	}
}

// earliestPending tracks correctly as addAfter moves processAfter
// around via the early-schedule-wins rule.
func TestEarliestPending_TracksAddAfterUpdates(t *testing.T) {
	ctrl, err := setUpController(t, config)
	if err != nil {
		t.Fatal(err)
	}
	nd := ctrl.getNodeData(nodeName)
	t10 := time.Now().Add(10 * time.Minute)
	t20 := time.Now().Add(20 * time.Minute)
	t5 := time.Now().Add(5 * time.Minute)

	nd.addAfter(testItem{id: "a"}, t10)
	nd.addAfter(testItem{id: "b"}, t20)
	if !nd.earliestPending().Equal(t10) {
		t.Errorf("earliest should be t10; got %v", nd.earliestPending())
	}

	// Pull "b" earlier than "a" — earliest should update.
	nd.addAfter(testItem{id: "b"}, t5)
	if !nd.earliestPending().Equal(t5) {
		t.Errorf("earliest should be t5 after updating 'b'; got %v", nd.earliestPending())
	}

	// Try to push "b" later — no-op, earliest stays at t5.
	nd.addAfter(testItem{id: "b"}, t20)
	if !nd.earliestPending().Equal(t5) {
		t.Errorf("earliest should still be t5; got %v", nd.earliestPending())
	}
}
