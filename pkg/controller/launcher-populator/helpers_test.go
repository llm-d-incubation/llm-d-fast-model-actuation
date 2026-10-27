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
	"fmt"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/component-base/metrics/legacyregistry"
	testingclock "k8s.io/utils/clock/testing"
)

func TestCreateLaunchersMetrics(t *testing.T) {
	for _, tc := range []struct {
		name      string
		failAt    int
		successes uint64
		failures  uint64
	}{
		{name: "success", successes: 3},
		{name: "failure", failAt: 1, failures: 1},
		{name: "partial", failAt: 2, successes: 1, failures: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctl, cs, _ := newStuckTestController(t, stuckTestNow)
			key := stuckTestKey()
			key.LauncherConfigName = "create-metrics-" + tc.name
			defer launcherCreateSecsHistograms.DeleteLabelValues(ctl.namespace, key.LauncherConfigName, "true")
			defer launcherCreateSecsHistograms.DeleteLabelValues(ctl.namespace, key.LauncherConfigName, "false")
			calls := 0
			cs.PrependReactor("create", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
				calls++
				ctl.clock.(*testingclock.FakeClock).Step(125 * time.Millisecond)
				if calls == tc.failAt {
					return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "launcher", errors.New("denied"))
				}
				pod := action.(k8stesting.CreateAction).GetObject().(*corev1.Pod).DeepCopy()
				pod.Name = fmt.Sprintf("launcher-%d", calls)
				pod.UID = types.UID(pod.Name)
				return true, pod, nil
			})

			err := ctl.createLaunchers(t.Context(), testNode(), key, 3, nodeTemplate())
			if tc.failAt == 0 && err != nil {
				t.Fatal(err)
			}
			if tc.failAt != 0 && !apierrors.IsForbidden(err) {
				t.Fatalf("createLaunchers error = %v, want Forbidden", err)
			}
			if uint64(calls) != tc.successes+tc.failures {
				t.Fatalf("Create calls = %d, want %d", calls, tc.successes+tc.failures)
			}

			families, err := legacyregistry.DefaultGatherer.Gather()
			if err != nil {
				t.Fatal(err)
			}
			counts := map[string]uint64{}
			sums := map[string]float64{}
			for _, family := range families {
				if family.GetName() != "fma_launcher_create_seconds" {
					continue
				}
				for _, metric := range family.GetMetric() {
					labels := map[string]string{}
					for _, label := range metric.GetLabel() {
						labels[label.GetName()] = label.GetValue()
					}
					if labels["lcfg_name"] != key.LauncherConfigName {
						continue
					}
					if labels["exported_namespace"] != ctl.namespace {
						t.Errorf("exported_namespace = %q, want %q", labels["exported_namespace"], ctl.namespace)
					}
					counts[labels["success"]] = metric.GetHistogram().GetSampleCount()
					sums[labels["success"]] = metric.GetHistogram().GetSampleSum()
				}
			}
			for success, want := range map[string]uint64{"true": tc.successes, "false": tc.failures} {
				if counts[success] != want {
					t.Errorf("success=%s count = %d, want %d", success, counts[success], want)
				}
				if sums[success] != float64(want)*0.125 {
					t.Errorf("success=%s duration sum = %g, want %g", success, sums[success], float64(want)*0.125)
				}
			}
		})
	}
}
