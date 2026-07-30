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
// current template hash.
func stuckLauncherPod(name string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   stuckTestNamespace,
			UID:         types.UID(name + "-uid"),
			Labels:      map[string]string{common.LauncherConfigNameLabelKey: stuckTestLauncherConfig},
			Annotations: map[string]string{common.LauncherTemplateHashAnnotationKey: testTemplateHash},
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

// createdAt sets the Pod's creation timestamp. With no PodScheduled condition,
// age is measured from creation for stuck_scheduling classification.
func createdAt(p *corev1.Pod, t time.Time) *corev1.Pod {
	p.CreationTimestamp = metav1.NewTime(t)
	return p
}

// listLaunchers returns all launcher Pods currently in the fake clientset,
// used to rebuild a reconcile's cache snapshot between passes.
func listLaunchers(t *testing.T, cs *k8sfake.Clientset) []*corev1.Pod {
	t.Helper()
	list, err := cs.CoreV1().Pods(stuckTestNamespace).List(t.Context(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list pods: %v", err)
	}
	out := make([]*corev1.Pod, 0, len(list.Items))
	for i := range list.Items {
		p := list.Items[i]
		out = append(out, &p)
	}
	return out
}

func nodeTemplate() *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "launcher-template",
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

func podExists(t *testing.T, cs *k8sfake.Clientset, name string) bool {
	t.Helper()
	_, err := cs.CoreV1().Pods(stuckTestNamespace).Get(t.Context(), name, metav1.GetOptions{})
	if err == nil {
		return true
	}
	if apierrors.IsNotFound(err) {
		return false
	}
	t.Fatalf("get pod %s: %v", name, err)
	return false
}

// TestReportStuckLaunchersLabelsAndReports verifies that a stuck launcher is
// kept, labeled, and reported once.
func TestReportStuckLaunchersLabelsAndReports(t *testing.T) {
	pod := scheduledNotReadyAt(stuckLauncherPod("launcher-stuck"), stuckTestNow.Add(-10*time.Minute))
	ctl, cs, rec := newStuckTestController(stuckTestNow, pod)

	if err := ctl.reportStuckLaunchers(t.Context(), []*corev1.Pod{pod}, testTemplateHash); err != nil {
		t.Fatalf("reportStuckLaunchers: %v", err)
	}
	got, err := cs.CoreV1().Pods(stuckTestNamespace).Get(t.Context(), pod.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("stuck pod must be kept: %v", err)
	}
	if got.Labels[common.LauncherStuckLabelKey] != "true" {
		t.Errorf("expected stuck label, labels=%v", got.Labels)
	}
	if all := listLaunchers(t, cs); len(all) != 1 {
		t.Errorf("expected no replacement Pods, got %d Pods", len(all))
	}
	assertWarningLauncherStuck(t, drainEvents(rec), "stuck_starting")
}

// TestReportStuckLaunchersAlreadyLabeledNoEvent verifies idempotence: a stuck,
// already-labeled launcher produces no further Event.
func TestReportStuckLaunchersAlreadyLabeledNoEvent(t *testing.T) {
	pod := scheduledNotReadyAt(stuckLauncherPod("launcher-labeled"), stuckTestNow.Add(-10*time.Minute))
	pod.Labels[common.LauncherStuckLabelKey] = "true"
	ctl, _, rec := newStuckTestController(stuckTestNow, pod)

	if err := ctl.reportStuckLaunchers(t.Context(), []*corev1.Pod{pod}, testTemplateHash); err != nil {
		t.Fatalf("reportStuckLaunchers: %v", err)
	}
	if events := drainEvents(rec); len(events) != 0 {
		t.Errorf("expected no Events for already-labeled pod, got %v", events)
	}
}

// TestReportStuckLaunchersClearsLabelOnRecovery verifies that the label is
// removed when a previously-labeled launcher is no longer stuck, so it never
// stays as a false positive.
func TestReportStuckLaunchersClearsLabelOnRecovery(t *testing.T) {
	pod := ready(stuckLauncherPod("launcher-recovered"))
	pod.Labels[common.LauncherStuckLabelKey] = "true"
	ctl, cs, rec := newStuckTestController(stuckTestNow, pod)

	if err := ctl.reportStuckLaunchers(t.Context(), []*corev1.Pod{pod}, testTemplateHash); err != nil {
		t.Fatalf("reportStuckLaunchers: %v", err)
	}
	got, err := cs.CoreV1().Pods(stuckTestNamespace).Get(t.Context(), pod.Name, metav1.GetOptions{})
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

// --- reconcile-level tests ---

// TestReconcileKeyDownscaleDeletesStuck verifies that normal population
// enforcement still deletes an excess stuck launcher.
func TestReconcileKeyDownscaleDeletesStuck(t *testing.T) {
	pod := scheduledNotReadyAt(stuckLauncherPod("launcher-downscale"), stuckTestNow.Add(-10*time.Minute))
	ctl, cs, rec := newStuckTestController(stuckTestNow, pod)

	err, _ := ctl.reconcileKey(t.Context(), stuckTestKey(), 0, testTemplateHash, nodeTemplate(), []*corev1.Pod{pod})
	if err != nil {
		t.Fatalf("reconcileKey: %v", err)
	}
	if podExists(t, cs, pod.Name) {
		t.Errorf("expected stuck pod deleted on downscale")
	}
	if events := drainEvents(rec); len(events) != 0 {
		t.Errorf("expected no stuck Event for an excess-deleted pod, got %v", events)
	}
}

// TestReconcileKeyDownscaleDeleteConflictDefersStuckReporting verifies that a
// Pod selected for excess deletion is not reported as stuck in the same
// reconcile when its Delete conflicts. If another deletion satisfies the
// desired count, the conflicted Pod is reported on the next reconcile, when it
// is known to be retained.
func TestReconcileKeyDownscaleDeleteConflictDefersStuckReporting(t *testing.T) {
	healthy := ready(stuckLauncherPod("launcher-healthy"))
	stuck := scheduledNotReadyAt(stuckLauncherPod("launcher-stuck"), stuckTestNow.Add(-10*time.Minute))
	ctl, cs, rec := newStuckTestController(stuckTestNow, healthy, stuck)
	cs.PrependReactor("delete", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		deleteAction := action.(k8stesting.DeleteAction)
		if deleteAction.GetName() != stuck.Name {
			return false, nil, nil
		}
		return true, nil, apierrors.NewConflict(
			corev1.Resource("pods"),
			stuck.Name,
			errors.New("resource version changed"),
		)
	})

	err, requeue := ctl.reconcileKey(t.Context(), stuckTestKey(), 1, testTemplateHash, nodeTemplate(), []*corev1.Pod{healthy, stuck})
	if err != nil {
		t.Fatalf("first reconcileKey: %v", err)
	}
	if !requeue {
		t.Errorf("expected requeue after deleting the healthy fallback")
	}
	got, err := cs.CoreV1().Pods(stuckTestNamespace).Get(t.Context(), stuck.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("conflicted Pod must remain: %v", err)
	}
	if got.Labels[common.LauncherStuckLabelKey] == "true" {
		t.Errorf("delete-conflicted Pod must not be labeled stuck in the same reconcile")
	}
	if podExists(t, cs, healthy.Name) {
		t.Errorf("expected healthy fallback Pod to be deleted")
	}
	if events := drainEvents(rec); len(events) != 0 {
		t.Errorf("delete-conflicted Pod must not be reported as stuck in the same reconcile, got %v", events)
	}

	err, requeue = ctl.reconcileKey(t.Context(), stuckTestKey(), 1, testTemplateHash, nodeTemplate(), listLaunchers(t, cs))
	if err != nil {
		t.Fatalf("second reconcileKey: %v", err)
	}
	if requeue {
		t.Errorf("retained stuck reporting alone must not request a requeue")
	}
	got, err = cs.CoreV1().Pods(stuckTestNamespace).Get(t.Context(), stuck.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get retained stuck Pod: %v", err)
	}
	if got.Labels[common.LauncherStuckLabelKey] != "true" {
		t.Errorf("expected retained stuck Pod to be labeled on the next reconcile")
	}
	assertWarningLauncherStuck(t, drainEvents(rec), "stuck_starting")
}

// TestReportStuckLaunchersIgnoresHealthy verifies a not-stuck, unlabeled
// launcher is left completely untouched.
func TestReportStuckLaunchersIgnoresHealthy(t *testing.T) {
	pod := ready(stuckLauncherPod("launcher-healthy"))
	ctl, cs, rec := newStuckTestController(stuckTestNow, pod)

	if err := ctl.reportStuckLaunchers(t.Context(), []*corev1.Pod{pod}, testTemplateHash); err != nil {
		t.Fatalf("reportStuckLaunchers: %v", err)
	}
	if events := drainEvents(rec); len(events) != 0 {
		t.Errorf("expected no Events for healthy launcher, got %v", events)
	}
	got, err := cs.CoreV1().Pods(stuckTestNamespace).Get(t.Context(), pod.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if _, ok := got.Labels[common.LauncherStuckLabelKey]; ok {
		t.Errorf("healthy launcher must not be labeled stuck, labels=%v", got.Labels)
	}
	if all := listLaunchers(t, cs); len(all) != 1 {
		t.Errorf("expected no new Pods, got %d", len(all))
	}
}

// TestReportStuckLaunchersReportsStuckScheduling verifies the unscheduled
// variant is kept, labeled, and reported with the correct phase.
func TestReportStuckLaunchersReportsStuckScheduling(t *testing.T) {
	// Old creation, never scheduled -> stuck_scheduling.
	pod := createdAt(stuckLauncherPod("launcher-unsched"), stuckTestNow.Add(-10*time.Minute))
	ctl, cs, rec := newStuckTestController(stuckTestNow, pod)

	if err := ctl.reportStuckLaunchers(t.Context(), []*corev1.Pod{pod}, testTemplateHash); err != nil {
		t.Fatalf("reportStuckLaunchers: %v", err)
	}
	got := listLaunchers(t, cs)
	if len(got) != 1 || got[0].Name != pod.Name {
		t.Errorf("expected the original launcher only, got %v", got)
	}
	if got[0].Labels[common.LauncherStuckLabelKey] != "true" {
		t.Errorf("expected stuck label, labels=%v", got[0].Labels)
	}
	assertWarningLauncherStuck(t, drainEvents(rec), "stuck_scheduling")
}

// G4: TestSetStuckLabelIgnoresNotFound verifies labeling a Pod that no longer
// exists is a no-op (no error), since the Pod may be deleted concurrently.
func TestSetStuckLabelIgnoresNotFound(t *testing.T) {
	ctl, _, _ := newStuckTestController(stuckTestNow) // empty clientset
	pod := stuckLauncherPod("launcher-gone")

	if err := ctl.setStuckLabel(t.Context(), pod, true); err != nil {
		t.Errorf("setStuckLabel on missing Pod should be ignored, got %v", err)
	}
	if err := ctl.setStuckLabel(t.Context(), pod, false); err != nil {
		t.Errorf("clearing label on missing Pod should be ignored, got %v", err)
	}
}

// TestReconcileKeyReportsStuckWithoutReplacement verifies that stuck
// classification changes observability only: the existing launcher remains the
// sole Pod satisfying desired count, and is labeled and reported.
func TestReconcileKeyReportsStuckWithoutReplacement(t *testing.T) {
	pod := scheduledNotReadyAt(stuckLauncherPod("launcher-stuck"), stuckTestNow.Add(-10*time.Minute))
	ctl, cs, rec := newStuckTestController(stuckTestNow, pod)

	err, requeue := ctl.reconcileKey(t.Context(), stuckTestKey(), 1, testTemplateHash, nodeTemplate(), []*corev1.Pod{pod})
	if err != nil {
		t.Fatalf("reconcileKey: %v", err)
	}
	if requeue {
		t.Errorf("stuck reporting alone must not request a requeue")
	}
	got := listLaunchers(t, cs)
	if len(got) != 1 {
		t.Fatalf("expected the stuck launcher to remain the only launcher, got %d", len(got))
	}
	if got[0].Name != pod.Name {
		t.Errorf("launcher was replaced: got %q, want %q", got[0].Name, pod.Name)
	}
	if got[0].Labels[common.LauncherStuckLabelKey] != "true" {
		t.Errorf("expected stuck label, labels=%v", got[0].Labels)
	}
	assertWarningLauncherStuck(t, drainEvents(rec), "stuck_starting")
}

// TestReportStuckLaunchersPropagatesLabelPatchError verifies that a non-NotFound
// error from the label Patch is surfaced (not swallowed), on both the set (stuck
// launcher) and clear (recovered) label paths.
func TestReportStuckLaunchersPropagatesLabelPatchError(t *testing.T) {
	cases := []struct {
		name string
		pod  *corev1.Pod
	}{
		{
			name: "set on stuck launcher",
			pod:  scheduledNotReadyAt(stuckLauncherPod("launcher-stuck"), stuckTestNow.Add(-10*time.Minute)),
		},
		{
			name: "clear on recovery",
			pod: func() *corev1.Pod {
				p := ready(stuckLauncherPod("launcher-recovered"))
				p.Labels[common.LauncherStuckLabelKey] = "true"
				return p
			}(),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctl, cs, _ := newStuckTestController(stuckTestNow, tc.pod)
			cs.PrependReactor("patch", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
				return true, nil, apierrors.NewInternalError(errors.New("boom"))
			})
			if err := ctl.reportStuckLaunchers(t.Context(), []*corev1.Pod{tc.pod}, testTemplateHash); err == nil {
				t.Errorf("expected the label Patch error to propagate")
			}
		})
	}
}
