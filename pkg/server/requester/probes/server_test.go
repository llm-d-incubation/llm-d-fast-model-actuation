/*
Copyright 2025 The llm-d Authors.

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

package probes

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"

	stubapi "github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/spi"
)

func FuzzServer(f *testing.F) {
	f.Add(true)
	f.Add(false)
	var ready atomic.Bool
	ctx, cancel := context.WithCancel(f.Context())
	port := "28082"
	go func() {
		err := Run(ctx, port, &ready)
		if err != nil {
			f.Logf("Run failed: %s", err.Error())
		}
	}()
	f.Fuzz(func(t *testing.T, beReady bool) {
		ready.Store(beReady)
		resp, err := http.Get("http://localhost:" + port + stubapi.ReadyPath)
		expectedStatus := http.StatusOK
		if !beReady {
			expectedStatus = http.StatusServiceUnavailable
		}
		if err != nil {
			t.Fatalf("Failed to query readiness: %s", err.Error())
		} else if resp.StatusCode != expectedStatus {
			t.Fatalf("Expected %d, got %d", expectedStatus, resp.StatusCode)
		} else {
			t.Logf("Successful test of ready=%v", beReady)
		}
	})
	cancel()
}
