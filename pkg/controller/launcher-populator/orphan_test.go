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
	"fmt"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	fmav1alpha1 "github.com/llm-d-incubation/llm-d-fast-model-actuation/api/fma/v1alpha1"
	"github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/controller/common"
	"github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/controller/utils"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	k8sfake "k8s.io/client-go/kubernetes/fake"
)

func TestReconcileNodeAvailability(t *testing.T) {
	for _, nodeState := range []string{"missing", "deleting"} {
		for _, templateState := range []string{"current", "stale", "no-pods"} {
			for _, desiredCount := range []int{0, 2} {
				t.Run(fmt.Sprintf("%s/%s/desired-%d", nodeState, templateState, desiredCount), func(t *testing.T) {
					var pods []*corev1.Pod
					if templateState != "no-pods" {
						orphan := stuckLauncherPod("orphan")
						if templateState == "stale" {
							orphan.Annotations[common.LauncherTemplateHashAnnotationKey] = "stale-template"
						}
						orphan.Spec.NodeSelector = map[string]string{"kubernetes.io/hostname": stuckTestNode}
						orphan.Status.Phase = corev1.PodPending
						bound := stuckLauncherPod("bound")
						bound.Annotations[common.RequesterAnnotationKey] = "requester-uid requester"
						pods = []*corev1.Pod{orphan, bound}
					}
					objects := podsToObjects(pods)
					if nodeState == "deleting" {
						node := testNode()
						deleting := metav1.NewTime(stuckTestNow)
						node.DeletionTimestamp = &deleting
						objects = append(objects, node)
					}

					ctl, cs, _ := newStuckTestControllerWithoutNode(t, stuckTestNow, objects...)

					err, requeue := ctl.reconcileKey(t.Context(), stuckTestKey(), desiredCount, testTemplateHash, nodeTemplate(), pods)
					if err != nil {
						t.Fatalf("reconcileKey: %v", err)
					}
					if templateState == "no-pods" {
						if len(cs.Actions()) != 0 || requeue {
							t.Fatalf("unavailable Node with no launchers must do nothing, actions=%v, requeue=%v", cs.Actions(), requeue)
						}
						return
					}
					if !requeue {
						t.Error("expected requeue to observe launcher deletion")
					}
					if podExists(t, cs, "orphan") {
						t.Error("unbound launcher remains on unavailable Node")
					}
					if !podExists(t, cs, "bound") {
						t.Error("bound launcher must be left for the dual-pods controller")
					}
				})
			}
		}
	}
}

func TestReconcileLiveNodeCreatesLauncher(t *testing.T) {
	ctl, cs, _ := newStuckTestController(t, stuckTestNow)
	if err, _ := ctl.reconcileKey(t.Context(), stuckTestKey(), 1, testTemplateHash, nodeTemplate(), nil); err != nil {
		t.Fatalf("reconcileKey: %v", err)
	}
	pods := listLaunchers(t, cs)
	if len(pods) != 1 || pods[0].Spec.NodeSelector["kubernetes.io/hostname"] != stuckTestNode {
		t.Fatalf("live Node must get one launcher targeting it, got %v", pods)
	}
}

func TestNodeDeletionCleansLaunchersWithoutPolicyChange(t *testing.T) {
	for _, event := range []string{"delete", "update-deleting"} {
		t.Run(event, func(t *testing.T) {
			lpp, node, objects := orphanLifecycleObjects(t)
			ctl, cs, _ := newStuckTestControllerWithoutNode(t, stuckTestNow, objects...)
			progress := observeOrphanReconciles(ctl)
			if err := ctl.Start(t.Context()); err != nil {
				t.Fatal(err)
			}
			waitForOrphanSetup(t, ctl, progress, lpp.Name)
			waitForOrphanPodNames(t, cs, "bound", "orphan")
			policyBefore, err := ctl.fmaclient.LauncherPopulationPolicies(stuckTestNamespace).Get(t.Context(), lpp.Name, metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			before := progress.snapshot()
			if event == "delete" {
				if err := cs.CoreV1().Nodes().Delete(t.Context(), node.Name, metav1.DeleteOptions{}); err != nil {
					t.Fatal(err)
				}
			} else {
				deletingNode := node.DeepCopy()
				deleting := metav1.NewTime(stuckTestNow)
				deletingNode.DeletionTimestamp = &deleting
				if _, err := cs.CoreV1().Nodes().Update(t.Context(), deletingNode, metav1.UpdateOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			if err := wait.PollUntilContextTimeout(t.Context(), 10*time.Millisecond, 5*time.Second, true, func(context.Context) (bool, error) {
				after := progress.snapshot()
				if err := after.err; err != nil {
					return false, err
				}
				for _, item := range after.digests[len(before.digests):] {
					if item.Kind != kindNode {
						return false, fmt.Errorf("setup digest item %v arrived after the Node operation", item)
					}
				}
				return len(after.digests) > len(before.digests) && after.keys > before.keys, nil
			}); err != nil {
				t.Fatalf("waiting for Node reconciliation: %v", err)
			}
			waitForOrphanPodNames(t, cs, "bound")
			// A late status event must fail the test, rather than stand in for the
			// Node event and make a broken Node enqueue path appear to work.
			for _, item := range progress.snapshot().digests[len(before.digests):] {
				if item.Kind != kindNode {
					t.Fatalf("unexpected non-Node digest during cleanup: %v", item)
				}
			}
			assertNoOrphanReplacement(t, cs)
			policyAfter, err := ctl.fmaclient.LauncherPopulationPolicies(stuckTestNamespace).Get(t.Context(), lpp.Name, metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(policyBefore, policyAfter) {
				t.Error("launcher cleanup must not require a population policy change")
			}
		})
	}
}

func TestPopulationPolicyDownscaleCleansLaunchers(t *testing.T) {
	lpp, _, objects := orphanLifecycleObjects(t)
	ctl, cs, _ := newStuckTestControllerWithoutNode(t, stuckTestNow, objects...)
	progress := observeOrphanReconciles(ctl)
	if err := ctl.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	waitForOrphanSetup(t, ctl, progress, lpp.Name)
	waitForOrphanPodNames(t, cs, "bound", "orphan")
	lpp, err := ctl.fmaclient.LauncherPopulationPolicies(stuckTestNamespace).Get(t.Context(), lpp.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	lpp.Spec.CountForLauncher[0].LauncherCount = 0
	lpp.Generation++
	if _, err := ctl.fmaclient.LauncherPopulationPolicies(stuckTestNamespace).Update(t.Context(), lpp, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	waitForOrphanPodNames(t, cs, "bound")
	assertNoOrphanReplacement(t, cs)
}

func TestStartupCleansLaunchersWithoutNodeOrPolicy(t *testing.T) {
	orphan := orphanLifecyclePod("orphan", testTemplateHash)
	orphan.Status.Phase = corev1.PodPending
	orphan.Status.Conditions = nil
	ctl, cs, _ := newStuckTestControllerWithoutNode(t, stuckTestNow,
		orphan, orphanLifecyclePod("bound", testTemplateHash))
	if err := ctl.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	waitForOrphanPodNames(t, cs, "bound")
	assertNoOrphanReplacement(t, cs)
}

func orphanLifecycleObjects(t *testing.T) (*fmav1alpha1.LauncherPopulationPolicy, *corev1.Node, []runtime.Object) {
	t.Helper()
	lc := &fmav1alpha1.LauncherConfig{
		ObjectMeta: metav1.ObjectMeta{Name: stuckTestLauncherConfig, Namespace: stuckTestNamespace, Generation: 1},
		Spec: fmav1alpha1.LauncherConfigSpec{
			MaxInstances: 1,
			PodTemplate:  fmav1alpha1.EmbeddedPodTemplateSpec{Spec: nodeTemplate().Spec},
		},
	}
	lpp := &fmav1alpha1.LauncherPopulationPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "policy", Namespace: stuckTestNamespace, Generation: 1},
		Spec: fmav1alpha1.LauncherPopulationPolicySpec{
			CountForLauncher: []fmav1alpha1.CountForLauncher{{LauncherConfigName: lc.Name, LauncherCount: 2}},
		},
	}
	node := testNode()
	_, templateHash, err := utils.BuildNodeIndependentLauncherTemplate(lc)
	if err != nil {
		t.Fatal(err)
	}
	return lpp, node, []runtime.Object{lc, lpp, node, orphanLifecyclePod("orphan", templateHash), orphanLifecyclePod("bound", templateHash)}
}

func orphanLifecyclePod(name, templateHash string) *corev1.Pod {
	pod := ready(stuckLauncherPod(name))
	pod.Labels[common.NodeNameLabelKey] = stuckTestNode
	pod.Labels[common.ComponentLabelKey] = common.LauncherComponentLabelValue
	pod.Annotations[common.LauncherTemplateHashAnnotationKey] = templateHash
	pod.Spec.NodeSelector = map[string]string{"kubernetes.io/hostname": stuckTestNode}
	pod.Status.Phase = corev1.PodRunning
	if name == "bound" {
		pod.Annotations[common.RequesterAnnotationKey] = "requester-uid requester"
	}
	return pod
}

// Observe completed work without consuming, filtering, or replacing queue items.
type orphanReconcileProgress struct {
	mu      sync.Mutex
	digests []funcItem
	keys    int
	active  int
	err     error
}

type orphanProgressSnapshot struct {
	digests []funcItem
	keys    int
	active  int
	err     error
}

func (p *orphanReconcileProgress) snapshot() orphanProgressSnapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	return orphanProgressSnapshot{digests: slices.Clone(p.digests), keys: p.keys, active: p.active, err: p.err}
}

func observeOrphanReconciles(ctl *controller) *orphanReconcileProgress {
	p := &orphanReconcileProgress{}
	processDigest := ctl.digestQueue.Process
	ctl.digestQueue.Process = func(ctx context.Context, item queueItem) (error, bool) {
		p.mu.Lock()
		p.active++
		p.mu.Unlock()
		err, retry := processDigest(ctx, item)
		p.mu.Lock()
		p.active--
		p.digests = append(p.digests, item.(funcItem))
		if err != nil {
			p.err = err
		}
		p.mu.Unlock()
		return err, retry
	}
	processKey := ctl.keyQueue.Process
	ctl.keyQueue.Process = func(ctx context.Context, item keyItem) (error, bool) {
		p.mu.Lock()
		p.active++
		p.mu.Unlock()
		err, retry := processKey(ctx, item)
		p.mu.Lock()
		p.active--
		p.keys++
		if err != nil {
			p.err = err
		}
		p.mu.Unlock()
		return err, retry
	}
	return p
}

func waitForOrphanSetup(t *testing.T, ctl *controller, progress *orphanReconcileProgress, policyName string) {
	t.Helper()
	var previous orphanProgressSnapshot
	var settledAt time.Time
	if err := wait.PollUntilContextTimeout(t.Context(), 10*time.Millisecond, 5*time.Second, true, func(context.Context) (bool, error) {
		current := progress.snapshot()
		if current.err != nil {
			return false, current.err
		}
		snap := ctl.policy.snapshotForKey(stuckTestKey())
		// Inspect the digested objects, rather than only the informer cache: the
		// final LC/LPP status events must have passed through the digest worker.
		ctl.policy.mu.RLock()
		lc, lpp := ctl.policy.lcs[stuckTestLauncherConfig], ctl.policy.lpps[policyName]
		statusesProcessed := lc != nil && lc.object != nil && lc.object.Status.ObservedGeneration == lc.object.Generation && len(lc.object.Status.Errors) == 0 &&
			lpp != nil && lpp.object.Status.ObservedGeneration == lpp.object.Generation && len(lpp.object.Status.Errors) == 0
		ctl.policy.mu.RUnlock()
		nodeProcessed := slices.ContainsFunc(current.digests, func(item funcItem) bool { return item.Kind == kindNode })
		settled := ctl.digestQueue.HasProcessedSync() && statusesProcessed && nodeProcessed && snap.exists && snap.desiredCount == 2 && current.keys > 0 && current.active == 0 &&
			ctl.digestQueue.Queue.Len() == 0 && ctl.keyQueue.Queue.Len() == 0
		if !settled || current.keys != previous.keys || len(current.digests) != len(previous.digests) {
			settledAt = time.Now()
		}
		previous = current
		return settled && time.Since(settledAt) >= 50*time.Millisecond, nil
	}); err != nil {
		t.Fatalf("waiting for normal setup reconciliation and status events: %v", err)
	}
}

func waitForOrphanPodNames(t *testing.T, cs *k8sfake.Clientset, names ...string) {
	t.Helper()
	slices.Sort(names)
	if err := wait.PollUntilContextTimeout(t.Context(), 10*time.Millisecond, 5*time.Second, true, func(ctx context.Context) (bool, error) {
		pods, err := cs.CoreV1().Pods(stuckTestNamespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			return false, err
		}
		got := make([]string, 0, len(pods.Items))
		for _, pod := range pods.Items {
			got = append(got, pod.Name)
		}
		slices.Sort(got)
		return slices.Equal(got, names), nil
	}); err != nil {
		t.Fatalf("waiting for launcher Pods %v: %v", names, err)
	}
}

func assertNoOrphanReplacement(t *testing.T, cs *k8sfake.Clientset) {
	t.Helper()
	for _, action := range cs.Actions() {
		if action.Matches("create", "pods") {
			t.Error("unexpected replacement launcher creation")
		}
	}
}
