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
	"fmt"
	"slices"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/cache"

	fmav1alpha1 "github.com/llm-d-incubation/llm-d-fast-model-actuation/api/fma/v1alpha1"
	"github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/controller/common"
	"github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/controller/utils"
)

func TestReconcileNodeAvailability(t *testing.T) {
	for _, nodeState := range []string{"missing", "deleting"} {
		for _, templateState := range []string{"current", "stale", "no-pods"} {
			for _, desiredCount := range []int32{0, 2} {
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
					podObjs := utils.SliceMapErrless(pods, podToRO)
					_, _, node, setupObjs := orphanSetupObjects(desiredCount)
					ctl, cs, fmaCS, _ := newStuckTestController(t, stuckTestNow, digestOnly, append(setupObjs, podObjs...)...)
					if !ctl.runUntilQuiet(t, time.Second*3, time.Second*20) {
						return
					}
					ctx := t.Context()
					cs.ClearActions()
					fmaCS.ClearActions()

					allowedActions := []actionAbstract{}
					if nodeState == "deleting" {
						node = node.DeepCopy()
						deleting := metav1.NewTime(stuckTestNow)
						node.DeletionTimestamp = &deleting
						_, err := cs.CoreV1().Nodes().Update(ctx, node, metav1.UpdateOptions{})
						if err != nil {
							t.Fatalf("Failed to update test node: %v", err)
						}
						allowedActions = append(allowedActions, actionAbstract{"update", "nodes"})
						waitForNodeDeletingInCache(t, ctl, node.Name)
					} else {
						err := cs.CoreV1().Nodes().Delete(ctx, node.Name, *metav1.NewDeleteOptions(0))
						if err != nil {
							t.Fatalf("Failed to deleete test node: %v", err)
						}
						allowedActions = append(allowedActions, actionAbstract{"delete", "nodes"})
						waitForNodeGoneFromCache(t, ctl, node.Name)
					}
					if !ctl.runUntilQuiet(t, time.Second*3, time.Second*20) {
						return
					}

					err, requeue := ctl.processKey(ctx, stuckTestKey())
					if err != nil {
						t.Fatalf("reconcileKey: %v", err)
					}
					if templateState == "no-pods" {
						checkActions(t, cs.Actions(), allowedActions)
						if requeue {
							t.Fatalf("unavailable Node with no launchers must not requeue")
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
					allowedActions = append(allowedActions,
						actionAbstract{"get", "pods"},
						actionAbstract{"delete", "pods"})
					checkActions(t, cs.Actions(), allowedActions)
					checkActions(t, fmaCS.Actions(), []actionAbstract{})
				})
			}
		}
	}
}

func TestReconcileLiveNodeCreatesLauncher(t *testing.T) {
	_, _, node, setupObjs := orphanSetupObjects(1)
	ctl, cs, fmaCS, _ := newStuckTestController(t, stuckTestNow, digestOnly, setupObjs...)

	if !ctl.runUntilQuiet(t, time.Second*3, time.Second*20) {
		return
	}
	cs.ClearActions()
	fmaCS.ClearActions()

	if err, _ := ctl.processKey(t.Context(), stuckTestKey()); err != nil {
		t.Fatalf("reconcileKey: %v", err)
	}
	checkActions(t, cs.Actions(), []actionAbstract{{"create", "pods"}})
	checkActions(t, fmaCS.Actions(), []actionAbstract{})
	pods := listLaunchers(t, cs)
	if len(pods) != 1 || pods[0].Spec.NodeSelector["kubernetes.io/hostname"] != node.Name {
		t.Fatalf("live Node must get one launcher targeting it, got %v", pods)
	}
}

func TestPopulationPolicyDownscaleCleansLaunchers(t *testing.T) {
	lpp, _, objects := orphanLifecycleObjects(t, true)
	ctl, cs, fmaCS, _ := newStuckTestController(t, stuckTestNow, digestAndKeys, objects...)

	if !ctl.runUntilQuiet(t, time.Second*3, time.Second*20) {
		return
	}
	key := stuckTestKey()
	snap := ctl.policy.snapshotForKey(key)
	if !snap.exists {
		t.Fatal("Digested policy lacks expected entry")
		return
	}
	if snap.desiredCount != 2 {
		t.Fatalf("Expected desiredCount=%v, got desiredCount=%v", 2, snap.desiredCount)
		return
	}

	cs.ClearActions()
	fmaCS.ClearActions()

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
	checkActions(t, cs.Actions(), []actionAbstract{
		{"list", "pods"}, {"delete", "pods"}})
	checkActions(t, fmaCS.Actions(), []actionAbstract{
		{"get", "launcherpopulationpolicies"},
		{"update", "launcherpopulationpolicies"},
	})
}

func TestStartupCleansLaunchersWithoutNodeOrPolicy(t *testing.T) {
	orphan := orphanLifecyclePod("orphan", testTemplateHash, false)
	bound := orphanLifecyclePod("bound", testTemplateHash, true)
	_, cs, fmaCS, _ := newStuckTestController(t, stuckTestNow, digestAndKeys, orphan, bound)

	waitForOrphanPodNames(t, cs, "bound")
	checkActions(t, fmaCS.Actions(), []actionAbstract{
		{"list", "launcherconfigs"}, {"watch", "launcherconfigs"},
		{"list", "launcherpopulationpolicies"}, {"watch", "launcherpopulationpolicies"},
	})
}

func orphanSetupObjects(desiredCount int32) (*fmav1alpha1.LauncherConfig, *fmav1alpha1.LauncherPopulationPolicy, *corev1.Node, []runtime.Object) {
	lc := &fmav1alpha1.LauncherConfig{
		ObjectMeta: metav1.ObjectMeta{Name: stuckTestLauncherConfig, Namespace: stuckTestNamespace, Generation: 1},
		Spec: fmav1alpha1.LauncherConfigSpec{
			MaxInstances: 1,
			PodTemplate:  fmav1alpha1.EmbeddedPodTemplateSpec{Spec: testNodeTemplate("launcher-template").Spec},
		},
	}
	lpp := &fmav1alpha1.LauncherPopulationPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "policy", Namespace: stuckTestNamespace, Generation: 1},
		Spec: fmav1alpha1.LauncherPopulationPolicySpec{
			CountForLauncher: []fmav1alpha1.CountForLauncher{{LauncherConfigName: lc.Name, LauncherCount: desiredCount}},
		},
	}
	node := testNode()
	return lc, lpp, node, []runtime.Object{lc, lpp, node}
}

func orphanLifecycleObjects(t *testing.T, podsRunning bool) (*fmav1alpha1.LauncherPopulationPolicy, *corev1.Node, []runtime.Object) {
	t.Helper()
	lc, lpp, node, setupObjs := orphanSetupObjects(2)
	_, templateHash, err := utils.BuildNodeIndependentLauncherTemplate(lc)
	if err != nil {
		t.Fatal(err)
	}
	orphan := orphanLifecyclePod("orphan", templateHash, podsRunning)
	bound := orphanLifecyclePod("bound", templateHash, podsRunning)
	return lpp, node, append(setupObjs, orphan, bound)
}

func orphanLifecyclePod(name, templateHash string, running bool) *corev1.Pod {
	pod := stuckLauncherPod(name)
	pod.Labels[common.NodeNameLabelKey] = stuckTestNode
	pod.Labels[common.ComponentLabelKey] = common.LauncherComponentLabelValue
	pod.Annotations[common.LauncherTemplateHashAnnotationKey] = templateHash
	pod.Spec.NodeSelector = map[string]string{"kubernetes.io/hostname": stuckTestNode}
	if running {
		ready(pod)
		pod.Status.Phase = corev1.PodRunning
	}
	if name == "bound" {
		pod.Annotations[common.RequesterAnnotationKey] = "requester-uid requester"
	}
	return pod
}

var errNotQuiet = errors.New("controller did not quiesce in time")

func (ctl *controller) runUntilQuiet(t *testing.T, idleTime, timeout time.Duration) bool {
	ctxTimed, cancel := context.WithTimeoutCause(t.Context(), timeout, errNotQuiet)
	defer cancel()
	if !cache.WaitForNamedCacheSync("test", ctxTimed.Done(), ctl.digestQueue.HasProcessedSync) {
		t.Fatalf("digestQueue.HasProcessedSync not reached in %v", timeout)
		return false
	}
	lastDigesterStatus := ctl.digestQueue.GetStatus()
	lastKeyerStatus := ctl.keyQueue.GetStatus()
	err := wait.PollUntilContextCancel(ctxTimed, idleTime/3, false, func(ctx context.Context) (done bool, err error) {
		nextDigesterStatus := ctl.digestQueue.GetStatus()
		nextKeyerStatus := ctl.keyQueue.GetStatus()
		if lastDigesterStatus == nextDigesterStatus && lastKeyerStatus == nextKeyerStatus {
			digestDT := time.Since(lastDigesterStatus.ApproxLastChangeTime)
			keyDT := time.Since(lastKeyerStatus.ApproxLastChangeTime)
			if digestDT >= idleTime && keyDT >= idleTime {
				return true, nil
			}
		}
		lastDigesterStatus = nextDigesterStatus
		lastKeyerStatus = nextKeyerStatus
		return false, nil
	})
	if err != nil {
		t.Fatalf("controller did not quiesce in %v", timeout)
		return false
	}
	return true
}

func waitForOrphanPodNames(t *testing.T, cs *k8sfake.Clientset, names ...string) {
	t.Helper()
	slices.Sort(names)
	if err := wait.PollUntilContextTimeout(t.Context(), 250*time.Millisecond, 5*time.Second, true, func(ctx context.Context) (bool, error) {
		pods, err := cs.CoreV1().Pods(stuckTestNamespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			return false, err
		}
		got := make([]string, 0, len(pods.Items))
		for _, pod := range pods.Items {
			got = append(got, pod.Name)
		}
		slices.Sort(got)
		t.Logf("Current pods are %v", got)
		return slices.Equal(got, names), nil
	}); err != nil {
		t.Fatalf("waiting for launcher Pods %v: %v", names, err)
	}
}

func waitForNodeGoneFromCache(t *testing.T, ctl *controller, nodeName string) {
	if err := wait.PollUntilContextTimeout(t.Context(), 250*time.Millisecond, 5*time.Second, true, func(ctx context.Context) (bool, error) {
		node, err := ctl.nodeLister.Get(nodeName)
		if err != nil {
			if apierrors.IsNotFound(err) {
				return true, nil
			}
			t.Logf("failed to get Node %s from cache: %v", nodeName, err)
			return false, nil
		}
		if node == nil {
			t.Logf("failed to get Node %s from cache: got nil instead", nodeName)
			return true, nil
		}
		t.Logf("Got Node %s from the cache", nodeName)
		return false, nil
	}); err != nil {
		t.Fatalf("waiting for Node %v: %v", nodeName, err)
	}
}

func waitForNodeDeletingInCache(t *testing.T, ctl *controller, nodeName string) {
	if err := wait.PollUntilContextTimeout(t.Context(), 250*time.Millisecond, 5*time.Second, true, func(ctx context.Context) (bool, error) {
		node, err := ctl.nodeLister.Get(nodeName)
		if err != nil {
			t.Logf("failed to get Node %s from cache: %v", nodeName, err)
			return false, err
		}
		if node == nil {
			t.Logf("failed to get Node %s from cache: got nil instead", nodeName)
			return false, errors.New("Node " + nodeName + " not found")
		}
		if node.DeletionTimestamp == nil {
			t.Logf("Node %v not is not yet being deleted", nodeName)
			return false, nil
		}
		t.Logf("Node %s in the cache shows deletion in progress", nodeName)
		return true, nil
	}); err != nil {
		t.Fatalf("waiting for Node %v: %v", nodeName, err)
	}
}

type actionAbstract struct{ Method, Resource string }

func checkActions(t *testing.T, actions []k8stesting.Action, allowed []actionAbstract) {
	t.Helper()
	for _, action := range actions {
		if slices.Index(allowed, actionAbstract{action.GetVerb(), action.GetResource().Resource}) < 0 {
			t.Errorf("Unexpected action: %v", action)
		}
	}
}
