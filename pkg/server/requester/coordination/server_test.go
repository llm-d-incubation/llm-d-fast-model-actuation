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

package coordination

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	stubapi "github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/spi"
)

func FuzzServer(f *testing.F) {
	f.Add(true)
	f.Add(false)
	gpuIDs := []string{"abc-def", "dead-beef"}
	var ready atomic.Bool
	ctx, cancel := context.WithCancel(f.Context())
	port := "28083"
	go func() {
		err := RunWithGPUUUIDs(ctx, port, &ready, nil, gpuIDs)
		if err != nil {
			f.Logf("Run failed: %s", err.Error())
		}
	}()
	paths := map[bool]string{false: stubapi.BecomeUnreadyPath, true: stubapi.BecomeReadyPath}
	f.Fuzz(func(t *testing.T, beReady bool) {
		path := paths[beReady]
		resp, err := http.Post("http://localhost:"+port+path, "text/plain", nil)
		if err != nil {
			t.Fatalf("Failed to POST to %s: %s", path, err.Error())
		} else if resp.StatusCode != http.StatusOK {
			t.Errorf("POST returned unexpected status %v", resp.StatusCode)
		} else if got := ready.Load(); got != beReady {
			t.Logf("Expected %v, got %v", beReady, got)
		} else {
			t.Logf("Successful test of %v", beReady)
		}

		resp, err = http.Get("http://localhost:" + port + stubapi.AcceleratorQueryPath)
		var gotIDs []string
		if err != nil {
			t.Fatalf("Failed to GET to %s: %s", stubapi.AcceleratorQueryPath, err.Error())
		} else if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s returned unexpected status %v", stubapi.AcceleratorQueryPath, resp.StatusCode)
		} else if respBytes, err := io.ReadAll(resp.Body); err != nil {
			t.Errorf("Failed to read response body: %s", err.Error())
		} else if err = json.Unmarshal(respBytes, &gotIDs); err != nil {
			t.Errorf("Failed to unmarshal response body %q: %s", string(respBytes), err.Error())
		} else if !slices.Equal(gotIDs, gpuIDs) {
			t.Errorf("GPU ID query returned %#v instead of %#v", gotIDs, gpuIDs)
		} else {
			t.Logf("Successful test of %#v", gpuIDs)
		}

	})
	cancel()
}

func TestLogChunking(t *testing.T) {
	var rightLog [600]byte
	for idx := range 600 {
		rightLog[idx] = '0' + byte(rand.IntN(10))
	}
	var logBuilder strings.Builder
	ctx, cancel := context.WithCancel(t.Context())
	var ready atomic.Bool
	port := "28084"
	go func() {
		err := RunWithGPUUUIDs(ctx, port, &ready, &logBuilder, []string{"x"})
		if err != nil {
			t.Logf("Run failed: %s", err.Error())
		}
	}()
	for {
		curLen := logBuilder.Len()
		var startPos int
		if curLen > 590 {
			break
		}
		if curLen > 0 {
			startPos = rand.IntN((curLen + 600) / 2)
		}
		chunkLen := 100 + rand.IntN(100)
		if startPos > 400 {
			chunkLen = rand.IntN(600 - startPos)
		}
		chunkReader := strings.NewReader(string(rightLog[startPos : startPos+chunkLen]))
		resp, err := http.Post(fmt.Sprintf("http://localhost:%s%s?%s=%d", port, stubapi.SetLogPath, stubapi.LogStartPosParam, startPos), "text/plain", chunkReader)
		if err != nil {
			t.Fatalf("Failed: %s", err.Error())
		}
		expectBad := startPos > curLen
		if resp.StatusCode >= 500 {
			respBody, _ := io.ReadAll(resp.Body)
			t.Fatalf("At curLen=%d, startPos=%d, chunkLen=%d, got internal error: %s\n%s", curLen, startPos, chunkLen, resp.Status, respBody)
		}
		gotBad := resp.StatusCode >= 400
		if expectBad != gotBad {
			t.Fatalf("At curLen=%d, startPos=%d, chunkLen=%d, expectedBad=%v but got status %d", curLen, startPos, chunkLen, expectBad, resp.StatusCode)
		} else if expectBad {
			t.Logf("At curLen=%d, startPos=%d, chunkLen=%d, got correct 4XX", curLen, startPos, chunkLen)
		} else {
			t.Logf("At curLen=%d, startPos=%d, chunkLen=%d, successful chunk write", curLen, startPos, chunkLen)
		}
		currentLog := logBuilder.String()
		if len(currentLog) < startPos+chunkLen && !expectBad {
			t.Fatalf("At curLen=%d, startPos=%d, chunkLen=%d, resulting log builder len=%d", curLen, startPos, chunkLen, logBuilder.Len())
		}
		if currentLog != string(rightLog[:len(currentLog)]) {
			t.Fatalf("At curLen=%d, startPos=%d, chunkLen=%d, wrong log contents", curLen, startPos, chunkLen)
		}
	}
	cancel()
}
