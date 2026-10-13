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
	"fmt"
	"reflect"
	"testing"

	fmav1alpha1 "github.com/llm-d-incubation/llm-d-fast-model-actuation/api/fma/v1alpha1"
	"github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/controller/common"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"
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
					ctl, cs, _ := newStuckTestControllerWithoutNode(t, stuckTestNow, pods...)
					if nodeState == "deleting" {
						node := testNode()
						deleting := metav1.NewTime(stuckTestNow)
						node.DeletionTimestamp = &deleting
						if err := ctl.nodeInformer.GetStore().Add(node); err != nil {
							t.Fatal(err)
						}
					}

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

	t.Run("live/no-pods/desired-1", func(t *testing.T) {
		ctl, cs, _ := newStuckTestController(t, stuckTestNow)
		if err, _ := ctl.reconcileKey(t.Context(), stuckTestKey(), 1, testTemplateHash, nodeTemplate(), nil); err != nil {
			t.Fatalf("reconcileKey: %v", err)
		}
		pods := listLaunchers(t, cs)
		if len(pods) != 1 || pods[0].Spec.NodeSelector["kubernetes.io/hostname"] != stuckTestNode {
			t.Fatalf("live Node must get one launcher targeting it, got %v", pods)
		}
	})
}

func TestNodeDeletionCleansLaunchersWithoutPolicyChange(t *testing.T) {
	for _, event := range []string{"delete-tombstone", "update-deleting"} {
		for _, desiredCount := range []int32{0, 2} {
			t.Run(fmt.Sprintf("%s/desired-%d", event, desiredCount), func(t *testing.T) {
				ctl, cs, _ := newStuckTestControllerWithoutNode(t, stuckTestNow)
				lc, err := ctl.fmaclient.LauncherConfigs(stuckTestNamespace).Create(t.Context(), &fmav1alpha1.LauncherConfig{
					ObjectMeta: metav1.ObjectMeta{Name: stuckTestLauncherConfig, Namespace: stuckTestNamespace, Generation: 1},
					Spec: fmav1alpha1.LauncherConfigSpec{
						MaxInstances: 1,
						PodTemplate:  fmav1alpha1.EmbeddedPodTemplateSpec{Spec: nodeTemplate().Spec},
					},
				}, metav1.CreateOptions{})
				if err != nil {
					t.Fatal(err)
				}
				if err := ctl.lcInformer.GetStore().Add(lc); err != nil {
					t.Fatal(err)
				}
				ctl.OnAdd(lc, false)
				lpp, err := ctl.fmaclient.LauncherPopulationPolicies(stuckTestNamespace).Create(t.Context(), &fmav1alpha1.LauncherPopulationPolicy{
					ObjectMeta: metav1.ObjectMeta{Name: "policy", Namespace: stuckTestNamespace, Generation: 1},
					Spec: fmav1alpha1.LauncherPopulationPolicySpec{
						CountForLauncher: []fmav1alpha1.CountForLauncher{{LauncherConfigName: lc.Name, LauncherCount: desiredCount}},
					},
				}, metav1.CreateOptions{})
				if err != nil {
					t.Fatal(err)
				}
				if err := ctl.lppInformer.GetStore().Add(lpp); err != nil {
					t.Fatal(err)
				}
				ctl.OnAdd(lpp, false)
				node, err := cs.CoreV1().Nodes().Create(t.Context(), testNode(), metav1.CreateOptions{})
				if err != nil {
					t.Fatal(err)
				}
				if err := ctl.nodeInformer.GetStore().Add(node); err != nil {
					t.Fatal(err)
				}
				ctl.OnAdd(node, false)
				processTestDigestQueue(t, ctl)
				snap := ctl.policy.snapshotForKey(stuckTestKey())
				if !snap.exists || snap.desiredCount != int(desiredCount) || snap.templateHash == "" {
					t.Fatalf("policy must establish the initial node population, got %+v", snap)
				}
				for _, name := range []string{"orphan", "bound"} {
					pod := stuckLauncherPod(name)
					pod.Labels[common.NodeNameLabelKey] = stuckTestNode
					pod.Labels[common.ComponentLabelKey] = common.LauncherComponentLabelValue
					pod.Annotations[common.LauncherTemplateHashAnnotationKey] = snap.templateHash
					pod.Spec.NodeSelector = map[string]string{"kubernetes.io/hostname": stuckTestNode}
					pod.Status.Phase = corev1.PodPending
					if name == "bound" {
						pod.Annotations[common.RequesterAnnotationKey] = "requester-uid requester"
					}
					pod, err = cs.CoreV1().Pods(stuckTestNamespace).Create(t.Context(), pod, metav1.CreateOptions{})
					if err != nil {
						t.Fatal(err)
					}
					if err := ctl.podInformer.GetStore().Add(pod); err != nil {
						t.Fatal(err)
					}
					ctl.OnAdd(pod, false)
				}
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
				if event == "delete-tombstone" {
					if err := cs.CoreV1().Nodes().Delete(t.Context(), node.Name, metav1.DeleteOptions{}); err != nil {
						t.Fatal(err)
					}
					if err := ctl.nodeInformer.GetStore().Delete(node); err != nil {
						t.Fatal(err)
					}
					ctl.OnDelete(cache.DeletedFinalStateUnknown{Key: node.Name, Obj: node})
				} else {
					deletingNode := node.DeepCopy()
					deleting := metav1.NewTime(stuckTestNow)
					deletingNode.DeletionTimestamp = &deleting
					deletingNode, err = cs.CoreV1().Nodes().Update(t.Context(), deletingNode, metav1.UpdateOptions{})
					if err != nil {
						t.Fatal(err)
					}
					if err := ctl.nodeInformer.GetStore().Update(deletingNode); err != nil {
						t.Fatal(err)
					}
					ctl.OnUpdate(node, deletingNode)
				}
				if ctl.digestQueue.Queue.Len() != 1 {
					t.Fatalf("Node event must enqueue one digest update, got %d", ctl.digestQueue.Queue.Len())
				}
				processTestDigestQueue(t, ctl)
				if ctl.policy.snapshotForKey(stuckTestKey()).exists {
					t.Error("unavailable Node must be removed from the population digest")
				}
				if ctl.keyQueue.Queue.Len() != 1 {
					t.Fatalf("Node event must enqueue launcher cleanup, got %d keys", ctl.keyQueue.Queue.Len())
				}
				item, _ := ctl.keyQueue.Queue.Get()
				defer ctl.keyQueue.Queue.Done(item)
				if err, _ := ctl.processKeyItem(t.Context(), item); err != nil {
					t.Fatal(err)
				}
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

func processTestDigestQueue(t *testing.T, ctl *controller) {
	t.Helper()
	for ctl.digestQueue.Queue.Len() > 0 {
		item, _ := ctl.digestQueue.Queue.Get()
		err, _ := ctl.processDigestItem(t.Context(), item)
		ctl.digestQueue.Queue.Done(item)
		if err != nil {
			t.Fatalf("processDigestItem(%v): %v", item, err)
		}
	}
}
