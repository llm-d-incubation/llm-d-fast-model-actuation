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
	"testing"
	"time"

	fmav1alpha1 "github.com/llm-d-incubation/llm-d-fast-model-actuation/api/fma/v1alpha1"
	"github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/controller/common"
	"github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/controller/utils"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
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
		for _, desiredCount := range []int32{0, 2} {
			t.Run(fmt.Sprintf("%s/desired-%d", event, desiredCount), func(t *testing.T) {
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
						CountForLauncher: []fmav1alpha1.CountForLauncher{{LauncherConfigName: lc.Name, LauncherCount: desiredCount}},
					},
				}
				node := testNode()
				_, templateHash, err := utils.BuildNodeIndependentLauncherTemplate(lc)
				if err != nil {
					t.Fatal(err)
				}
				objects := []runtime.Object{lc, lpp, node}
				for _, name := range []string{"orphan", "bound"} {
					pod := stuckLauncherPod(name)
					pod.Labels[common.NodeNameLabelKey] = stuckTestNode
					pod.Labels[common.ComponentLabelKey] = common.LauncherComponentLabelValue
					pod.Annotations[common.LauncherTemplateHashAnnotationKey] = templateHash
					pod.Spec.NodeSelector = map[string]string{"kubernetes.io/hostname": stuckTestNode}
					pod.Status.Phase = corev1.PodPending
					if name == "bound" {
						pod.Annotations[common.RequesterAnnotationKey] = "requester-uid requester"
					}
					objects = append(objects, pod)
				}
				ctl, cs, _ := newStuckTestControllerWithoutNode(t, stuckTestNow, objects...)
				processTestDigestQueue(t, ctl)
				// Discard setup requests so only the Node event can trigger cleanup.
				for ctl.keyQueue.Queue.Len() > 0 {
					item, _ := ctl.keyQueue.Queue.Get()
					ctl.keyQueue.Queue.Done(item)
				}
				policyBefore, err := ctl.fmaclient.LauncherPopulationPolicies(stuckTestNamespace).Get(t.Context(), lpp.Name, metav1.GetOptions{})
				if err != nil {
					t.Fatal(err)
				}
				cs.ClearActions()
				if event == "delete" {
					if err := cs.CoreV1().Nodes().Delete(t.Context(), node.Name, metav1.DeleteOptions{}); err != nil {
						t.Fatal(err)
					}
					waitForTestNodeEvent(t, ctl, func() bool {
						_, exists, err := ctl.nodeInformer.GetStore().GetByKey(node.Name)
						return err == nil && !exists
					})
				} else {
					deletingNode := node.DeepCopy()
					deleting := metav1.NewTime(stuckTestNow)
					deletingNode.DeletionTimestamp = &deleting
					_, err = cs.CoreV1().Nodes().Update(t.Context(), deletingNode, metav1.UpdateOptions{})
					if err != nil {
						t.Fatal(err)
					}
					waitForTestNodeEvent(t, ctl, func() bool {
						cached, err := ctl.nodeLister.Get(node.Name)
						return err == nil && cached.DeletionTimestamp != nil
					})
				}
				processTestKeyQueue(t, ctl)
				if podExists(t, cs, "orphan") || !podExists(t, cs, "bound") {
					t.Error("Node event must remove the unbound launcher and preserve the bound launcher")
				}
				for _, action := range cs.Actions() {
					if action.Matches("create", "pods") {
						t.Error("unavailable Node must not get replacement launchers")
					}
				}
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
}

func processTestDigestQueue(t *testing.T, ctl *controller, kinds ...resourceKind) int {
	t.Helper()
	processed := 0
	for ctl.digestQueue.Queue.Len() > 0 {
		item, _ := ctl.digestQueue.Queue.Get()
		if len(kinds) > 0 && !slices.Contains(kinds, item.(funcItem).Kind) {
			ctl.digestQueue.Queue.Done(item)
			continue
		}
		err, _ := ctl.processDigestItem(t.Context(), item)
		ctl.digestQueue.Queue.Done(item)
		if err != nil {
			t.Fatalf("processDigestItem(%v): %v", item, err)
		}
		processed++
	}
	return processed
}

func waitForTestNodeEvent(t *testing.T, ctl *controller, observed func() bool) {
	t.Helper()
	if err := wait.PollUntilContextTimeout(t.Context(), time.Millisecond, 5*time.Second, true, func(context.Context) (bool, error) {
		// Wait for and process the actual Node item. Delayed LC/LPP status updates
		// from setup must neither satisfy the wait nor substitute for Node cleanup.
		return observed() && processTestDigestQueue(t, ctl, kindNode) > 0, nil
	}); err != nil {
		t.Fatalf("waiting for Node informer event: %v", err)
	}
}

func processTestKeyQueue(t *testing.T, ctl *controller) {
	t.Helper()
	for ctl.keyQueue.Queue.Len() > 0 {
		item, _ := ctl.keyQueue.Queue.Get()
		err, _ := ctl.processKeyItem(t.Context(), item)
		ctl.keyQueue.Queue.Done(item)
		if err != nil {
			t.Fatalf("processKeyItem(%v): %v", item, err)
		}
	}
}
