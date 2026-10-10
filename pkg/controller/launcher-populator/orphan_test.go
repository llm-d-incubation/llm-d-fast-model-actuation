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
	"testing"

	fmav1alpha1 "github.com/llm-d-incubation/llm-d-fast-model-actuation/api/fma/v1alpha1"
	genctlr "github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/controller/generic"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	corev1 "k8s.io/api/core/v1"
	corev1listers "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"

	"github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/controller/common"
)

func TestReconcileOrphansWithoutNode(t *testing.T) {
	for _, templateHash := range []string{testTemplateHash, "stale-template"} {
		t.Run(templateHash, func(t *testing.T) {
			orphan := stuckLauncherPod("orphan")
			orphan.Annotations[common.LauncherTemplateHashAnnotationKey] = templateHash
			orphan.Spec.NodeSelector = map[string]string{"kubernetes.io/hostname": stuckTestNode}
			orphan.Status.Phase = corev1.PodPending
			bound := stuckLauncherPod("bound")
			bound.Annotations[common.RequesterAnnotationKey] = "requester-uid requester"
			ctl, cs, _ := newStuckTestController(stuckTestNow, orphan, bound)
			ctl.nodeLister = corev1listers.NewNodeLister(cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{}))

			err, requeue := ctl.reconcileKey(t.Context(), stuckTestKey(), 0, testTemplateHash, nil, []*corev1.Pod{orphan, bound})
			if err != nil {
				t.Fatalf("reconcileKey: %v", err)
			}
			if !requeue {
				t.Error("expected requeue to observe launcher deletion")
			}
			if podExists(t, cs, orphan.Name) {
				t.Error("unbound orphan remains after its target Node was deleted")
			}
			if !podExists(t, cs, bound.Name) {
				t.Error("bound launcher must be left for the dual-pods controller")
			}
		})
	}
}

func TestReconcileDoesNotCreateWithoutNode(t *testing.T) {
	ctl, cs, _ := newStuckTestController(stuckTestNow)
	ctl.nodeLister = corev1listers.NewNodeLister(cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{}))
	err, _ := ctl.reconcileKey(t.Context(), stuckTestKey(), 1, testTemplateHash, nodeTemplate(), nil)
	if err != nil {
		t.Fatalf("reconcileKey: %v", err)
	}
	if len(cs.Actions()) != 0 {
		t.Fatalf("expected no API calls for a missing Node, got %v", cs.Actions())
	}
}

func TestReconcileUnavailableNodeWithDesiredPopulation(t *testing.T) {
	for _, missing := range []bool{true, false} {
		name := "deleting"
		if missing {
			name = "missing"
		}
		t.Run(name, func(t *testing.T) {
			orphan := stuckLauncherPod("orphan")
			bound := stuckLauncherPod("bound")
			bound.Annotations[common.RequesterAnnotationKey] = "requester-uid requester"
			ctl, cs, _ := newStuckTestController(stuckTestNow, orphan, bound)
			indexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
			if !missing {
				node := testNode()
				deleting := metav1.NewTime(stuckTestNow)
				node.DeletionTimestamp = &deleting
				if err := indexer.Add(node); err != nil {
					t.Fatal(err)
				}
			}
			ctl.nodeLister = corev1listers.NewNodeLister(indexer)
			err, requeue := ctl.reconcileKey(t.Context(), stuckTestKey(), 2, testTemplateHash, nodeTemplate(), []*corev1.Pod{orphan, bound})
			if err != nil {
				t.Fatalf("reconcileKey: %v", err)
			}
			if !requeue {
				t.Error("expected requeue to observe launcher deletion")
			}
			if podExists(t, cs, orphan.Name) {
				t.Error("unbound launcher remains on unavailable Node despite unchanged desired population")
			}
			if !podExists(t, cs, bound.Name) {
				t.Error("bound launcher must be left for the dual-pods controller")
			}
		})
	}
}

func TestNodeDeletionCleansLaunchersWithoutPolicyChange(t *testing.T) {
	for _, missing := range []bool{true, false} {
		name := "deleting"
		if missing {
			name = "deleted"
		}
		t.Run(name, func(t *testing.T) {
			orphan := stuckLauncherPod("orphan")
			orphan.Labels[common.NodeNameLabelKey] = stuckTestNode
			orphan.Labels[common.ComponentLabelKey] = common.LauncherComponentLabelValue
			bound := stuckLauncherPod("bound")
			bound.Labels[common.NodeNameLabelKey] = stuckTestNode
			bound.Labels[common.ComponentLabelKey] = common.LauncherComponentLabelValue
			bound.Annotations[common.RequesterAnnotationKey] = "requester-uid requester"
			ctl, cs, _ := newStuckTestController(stuckTestNow, orphan, bound)
			ctl.policy = newDigestedPolicy()
			ctl.metrics = newMetricsState()
			ctl.policy.getEntry(stuckTestNode, stuckTestLauncherConfig, true).desiredCount = 2
			ctl.policy.lcs[stuckTestLauncherConfig] = &lcDigest{object: &fmav1alpha1.LauncherConfig{}, nodeIndependent: nodeTemplate(), templateHash: testTemplateHash}
			podIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})
			for _, pod := range []*corev1.Pod{orphan, bound} {
				if err := podIndexer.Add(pod); err != nil {
					t.Fatal(err)
				}
			}
			ctl.podLister = corev1listers.NewPodLister(podIndexer)
			indexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
			if !missing {
				node := testNode()
				deleting := metav1.NewTime(stuckTestNow)
				node.DeletionTimestamp = &deleting
				if err := indexer.Add(node); err != nil {
					t.Fatal(err)
				}
			}
			ctl.nodeLister = corev1listers.NewNodeLister(indexer)
			queue := genctlr.NewQueueAndWorkers(ControllerName+"-test", 1, ctl.processKeyItem)
			ctl.keyQueue = &queue
			defer queue.Queue.ShutDown()
			if err, _ := ctl.processDigestItem(t.Context(), funcItem{Kind: kindNode, Name: stuckTestNode}); err != nil {
				t.Fatal(err)
			}
			if queue.Queue.Len() != 1 {
				t.Fatalf("Node deletion did not schedule launcher cleanup: queue length %d", queue.Queue.Len())
			}
			item, _ := queue.Queue.Get()
			defer queue.Queue.Done(item)
			if err, _ := ctl.processKeyItem(t.Context(), item); err != nil {
				t.Fatal(err)
			}
			if podExists(t, cs, orphan.Name) {
				t.Error("Node deletion left an unbound launcher without a policy change")
			}
			if !podExists(t, cs, bound.Name) {
				t.Error("bound launcher must be left for the dual-pods controller")
			}
		})
	}
}
