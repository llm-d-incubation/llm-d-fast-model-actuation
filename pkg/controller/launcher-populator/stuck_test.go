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

package launcherpopulator

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	corev1listers "k8s.io/client-go/listers/core/v1"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	testingclock "k8s.io/utils/clock/testing"

	"github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/controller/common"
)

const (
	stuckTestSchedThreshold = 2 * time.Minute
	stuckTestStartThreshold = 5 * time.Minute
	stuckTestNamespace      = "default"
	stuckTestNode           = "node-a"
	stuckTestLauncherConfig = "lc-a"
)

var stuckTestNow = time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)

func newStuckTestController(now time.Time, objs ...*corev1.Pod) (*controller, *k8sfake.Clientset, *record.FakeRecorder) {
	cs := k8sfake.NewSimpleClientset(podsToObjects(objs)...)
	rec := record.NewFakeRecorder(50)
	indexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	_ = indexer.Add(testNode())
	ctl := &controller{
		coreclient:               cs.CoreV1(),
		namespace:                stuckTestNamespace,
		recorder:                 rec,
		clock:                    testingclock.NewFakeClock(now),
		stuckSchedulingThreshold: stuckTestSchedThreshold,
		stuckStartingThreshold:   stuckTestStartThreshold,
		expectations:             newPendingExpectations(time.Minute),
		nodeLister:               corev1listers.NewNodeLister(indexer),
	}
	return ctl, cs, rec
}

func podsToObjects(pods []*corev1.Pod) []runtime.Object {
	out := make([]runtime.Object, 0, len(pods))
	for _, p := range pods {
		out = append(out, p)
	}
	return out
}

// stuckLauncherPod builds a launcher Pod in the current namespace with the
// current template hash and the given retry-count annotation ("" for none).
func stuckLauncherPod(name string, retryCount string) *corev1.Pod {
	ann := map[string]string{common.LauncherTemplateHashAnnotationKey: testTemplateHash}
	if retryCount != "" {
		ann[common.LauncherRetryCountAnnotationKey] = retryCount
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   stuckTestNamespace,
			UID:         types.UID(name + "-uid"),
			Labels:      map[string]string{common.LauncherConfigNameLabelKey: stuckTestLauncherConfig},
			Annotations: ann,
		},
	}
}

// scheduledNotReadyAt marks the Pod scheduled at t and not Ready, so its age is
// measured from scheduling for stuck_starting classification.
func scheduledNotReadyAt(p *corev1.Pod, t time.Time) *corev1.Pod {
	p.Status.Conditions = append(p.Status.Conditions, corev1.PodCondition{
		Type:               corev1.PodScheduled,
		Status:             corev1.ConditionTrue,
		LastTransitionTime: metav1.NewTime(t),
	})
	return p
}

// ready marks the Pod Ready.
func ready(p *corev1.Pod) *corev1.Pod {
	p.Status.Conditions = append(p.Status.Conditions, corev1.PodCondition{
		Type:   corev1.PodReady,
		Status: corev1.ConditionTrue,
	})
	return p
}

func nodeTemplate() *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "launcher-replacement",
			Namespace:   stuckTestNamespace,
			Labels:      map[string]string{common.LauncherConfigNameLabelKey: stuckTestLauncherConfig},
			Annotations: map[string]string{common.LauncherTemplateHashAnnotationKey: testTemplateHash},
		},
	}
}

func testNode() *corev1.Node {
	return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: stuckTestNode}}
}

func stuckTestKey() NodeLauncherKey {
	return NodeLauncherKey{NodeName: stuckTestNode, LauncherConfigName: stuckTestLauncherConfig}
}

func drainEvents(rec *record.FakeRecorder) []string {
	var events []string
	for {
		select {
		case e := <-rec.Events:
			events = append(events, e)
		default:
			return events
		}
	}
}

// assertWarningLauncherStuck asserts there is exactly one Event and that it is a
// Warning with reason LauncherStuck whose message contains wantSubstr. This
// pins the Event contract (type + reason), not just a message fragment, so a
// regression to Normal or a different reason fails the test.
func assertWarningLauncherStuck(t *testing.T, events []string, wantSubstr string) {
	t.Helper()
	if len(events) != 1 {
		t.Fatalf("expected exactly 1 Event, got %d: %v", len(events), events)
	}
	e := events[0]
	if !strings.HasPrefix(e, "Warning LauncherStuck ") {
		t.Errorf("expected Event %q to have prefix %q", e, "Warning LauncherStuck ")
	}
	if !strings.Contains(e, wantSubstr) {
		t.Errorf("expected Event %q to contain %q", e, wantSubstr)
	}
}

func launcherPodsWithRetryCount(t *testing.T, cs *k8sfake.Clientset, count string) []corev1.Pod {
	t.Helper()
	list, err := cs.CoreV1().Pods(stuckTestNamespace).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list pods: %v", err)
	}
	var out []corev1.Pod
	for i := range list.Items {
		if list.Items[i].Annotations[common.LauncherRetryCountAnnotationKey] == count {
			out = append(out, list.Items[i])
		}
	}
	return out
}

func podExists(t *testing.T, cs *k8sfake.Clientset, name string) bool {
	t.Helper()
	_, err := cs.CoreV1().Pods(stuckTestNamespace).Get(context.Background(), name, metav1.GetOptions{})
	if err == nil {
		return true
	}
	if apierrors.IsNotFound(err) {
		return false
	}
	t.Fatalf("get pod %s: %v", name, err)
	return false
}

// TestHandleStuckLaunchersRetriesByCreatingReplacement verifies the
// restart-safe retry: the replacement carrying retry-count=1 is CREATED, while
// the stuck original is left for later excess deletion (not deleted here). If a
// crash or Create failure interrupts the retry, the counter therefore still
// lives on a Pod.
func TestHandleStuckLaunchersRetriesByCreatingReplacement(t *testing.T) {
	pod := scheduledNotReadyAt(stuckLauncherPod("launcher-stuck-1", ""), stuckTestNow.Add(-10*time.Minute))
	ctl, cs, rec := newStuckTestController(stuckTestNow, pod)

	created, err := ctl.handleStuckLaunchers(context.Background(), stuckTestKey(), testNode(), []*corev1.Pod{pod}, testTemplateHash, nodeTemplate())
	if err != nil {
		t.Fatalf("handleStuckLaunchers: %v", err)
	}
	if !created {
		t.Errorf("expected a replacement to be created")
	}
	if !podExists(t, cs, pod.Name) {
		t.Errorf("stuck original must NOT be deleted by handleStuckLaunchers (create-before-delete)")
	}
	if got := launcherPodsWithRetryCount(t, cs, "1"); len(got) != 1 {
		t.Errorf("expected exactly 1 replacement carrying retry-count=1, got %d", len(got))
	}
	assertWarningLauncherStuck(t, drainEvents(rec), "recreating")
}

// TestHandleStuckLaunchersExhaustedLabelsAndReports verifies that a stuck
// launcher that has used its retry is labeled and reported once, and not
// recreated.
func TestHandleStuckLaunchersExhaustedLabelsAndReports(t *testing.T) {
	pod := scheduledNotReadyAt(stuckLauncherPod("launcher-stuck-2", "1"), stuckTestNow.Add(-10*time.Minute))
	ctl, cs, rec := newStuckTestController(stuckTestNow, pod)

	created, err := ctl.handleStuckLaunchers(context.Background(), stuckTestKey(), testNode(), []*corev1.Pod{pod}, testTemplateHash, nodeTemplate())
	if err != nil {
		t.Fatalf("handleStuckLaunchers: %v", err)
	}
	if created {
		t.Errorf("exhausted launcher must not be recreated")
	}
	got, err := cs.CoreV1().Pods(stuckTestNamespace).Get(context.Background(), pod.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("exhausted pod must be kept: %v", err)
	}
	if got.Labels[common.LauncherStuckLabelKey] != common.LauncherStuckLabelValue {
		t.Errorf("expected stuck label on exhausted pod, labels=%v", got.Labels)
	}
	assertWarningLauncherStuck(t, drainEvents(rec), "exhausted")
}

// TestHandleStuckLaunchersExhaustedAlreadyLabeledNoEvent verifies idempotence:
// a stuck, exhausted, already-labeled launcher produces no further Event.
func TestHandleStuckLaunchersExhaustedAlreadyLabeledNoEvent(t *testing.T) {
	pod := scheduledNotReadyAt(stuckLauncherPod("launcher-stuck-3", "1"), stuckTestNow.Add(-10*time.Minute))
	pod.Labels[common.LauncherStuckLabelKey] = common.LauncherStuckLabelValue
	ctl, _, rec := newStuckTestController(stuckTestNow, pod)

	created, err := ctl.handleStuckLaunchers(context.Background(), stuckTestKey(), testNode(), []*corev1.Pod{pod}, testTemplateHash, nodeTemplate())
	if err != nil {
		t.Fatalf("handleStuckLaunchers: %v", err)
	}
	if created {
		t.Errorf("expected no recreate")
	}
	if events := drainEvents(rec); len(events) != 0 {
		t.Errorf("expected no Events for already-labeled pod, got %v", events)
	}
}

// TestHandleStuckLaunchersClearsLabelOnRecovery verifies that the label is
// removed when a previously-labeled launcher is no longer stuck, so it never
// stays as a false positive.
func TestHandleStuckLaunchersClearsLabelOnRecovery(t *testing.T) {
	pod := ready(stuckLauncherPod("launcher-recovered", "1"))
	pod.Labels[common.LauncherStuckLabelKey] = common.LauncherStuckLabelValue
	ctl, cs, rec := newStuckTestController(stuckTestNow, pod)

	created, err := ctl.handleStuckLaunchers(context.Background(), stuckTestKey(), testNode(), []*corev1.Pod{pod}, testTemplateHash, nodeTemplate())
	if err != nil {
		t.Fatalf("handleStuckLaunchers: %v", err)
	}
	if created {
		t.Errorf("expected no recreate for recovered pod")
	}
	got, err := cs.CoreV1().Pods(stuckTestNamespace).Get(context.Background(), pod.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if _, ok := got.Labels[common.LauncherStuckLabelKey]; ok {
		t.Errorf("expected stuck label removed on recovery, labels=%v", got.Labels)
	}
	if events := drainEvents(rec); len(events) != 0 {
		t.Errorf("expected no Events on recovery, got %v", events)
	}
}

// TestHandleStuckLaunchersSupersededNotRetried verifies that when a newer
// generation already exists (an in-flight replacement), the superseded original
// is not retried again; only the exhausted newest generation is reported.
func TestHandleStuckLaunchersSupersededNotRetried(t *testing.T) {
	orig := scheduledNotReadyAt(stuckLauncherPod("launcher-gen0", ""), stuckTestNow.Add(-10*time.Minute))
	repl := scheduledNotReadyAt(stuckLauncherPod("launcher-gen1", "1"), stuckTestNow.Add(-10*time.Minute))
	ctl, cs, rec := newStuckTestController(stuckTestNow, orig, repl)

	created, err := ctl.handleStuckLaunchers(context.Background(), stuckTestKey(), testNode(), []*corev1.Pod{orig, repl}, testTemplateHash, nodeTemplate())
	if err != nil {
		t.Fatalf("handleStuckLaunchers: %v", err)
	}
	if created {
		t.Errorf("must not create a second replacement while a newer generation exists")
	}
	if got := launcherPodsWithRetryCount(t, cs, "2"); len(got) != 0 {
		t.Errorf("expected no generation-2 replacement, got %d", len(got))
	}
	// The exhausted newest generation is labeled/reported.
	assertWarningLauncherStuck(t, drainEvents(rec), "exhausted")
}

// --- reconcile-level tests ---

// TestReconcileKeyDownscaleDeletesStuckNotRetried verifies P2: when the desired
// count drops (here to 0), a stuck launcher is deleted outright rather than
// retried — no replacement, no "recreating" Event.
func TestReconcileKeyDownscaleDeletesStuckNotRetried(t *testing.T) {
	pod := scheduledNotReadyAt(stuckLauncherPod("launcher-downscale", ""), stuckTestNow.Add(-10*time.Minute))
	ctl, cs, rec := newStuckTestController(stuckTestNow, pod)

	err, _ := ctl.reconcileKey(context.Background(), stuckTestKey(), 0, testTemplateHash, nodeTemplate(), []*corev1.Pod{pod})
	if err != nil {
		t.Fatalf("reconcileKey: %v", err)
	}
	if podExists(t, cs, pod.Name) {
		t.Errorf("expected stuck pod deleted on downscale")
	}
	if got := launcherPodsWithRetryCount(t, cs, "1"); len(got) != 0 {
		t.Errorf("expected no retry replacement on downscale, got %d", len(got))
	}
	for _, e := range drainEvents(rec) {
		if strings.Contains(e, "recreating") {
			t.Errorf("expected no recreating Event on downscale, got %q", e)
		}
	}
}

// TestReconcileKeyRetryCreateFailureKeepsOriginal verifies P1's failure path:
// if creating the replacement fails, the stuck original is NOT deleted, so the
// retry count is not lost and the slot is retried again later rather than being
// silently forgotten.
func TestReconcileKeyRetryCreateFailureKeepsOriginal(t *testing.T) {
	pod := scheduledNotReadyAt(stuckLauncherPod("launcher-createfail", ""), stuckTestNow.Add(-10*time.Minute))
	ctl, cs, _ := newStuckTestController(stuckTestNow, pod)
	cs.PrependReactor("create", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewInternalError(errors.New("boom"))
	})

	err, requeue := ctl.reconcileKey(context.Background(), stuckTestKey(), 1, testTemplateHash, nodeTemplate(), []*corev1.Pod{pod})
	if err == nil {
		t.Errorf("expected error from failed replacement Create")
	}
	if !requeue {
		t.Errorf("expected requeue after failed Create")
	}
	if !podExists(t, cs, pod.Name) {
		t.Errorf("stuck original must be kept when its replacement Create fails")
	}
}
