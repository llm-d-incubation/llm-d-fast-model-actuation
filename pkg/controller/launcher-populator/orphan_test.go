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
