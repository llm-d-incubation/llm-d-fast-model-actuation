/*
Copyright 2026 The llm-d-fast-model-actuation Authors.

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

package dualpods

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

func newSleepTestObserver() ObserverCube {
	return prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: "sleep_test_http_latency_seconds",
		Help: "HTTP latency observed by sleep tests.",
	}, []string{"purpose", "method", "status_code"})
}

func TestSleepInferenceServerUsesGracefulWait(t *testing.T) {
	var gotLevel, gotMode string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotLevel = r.URL.Query().Get("level")
		gotMode = r.URL.Query().Get("mode")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	err := sleepInferenceServer(t.Context(), server.URL, time.Second, time.Second, newSleepTestObserver())
	if err != nil {
		t.Fatalf("sleepInferenceServer returned error: %v", err)
	}
	if gotLevel != "1" {
		t.Errorf("sleep level = %q, want 1", gotLevel)
	}
	if gotMode != "wait" {
		t.Errorf("sleep mode = %q, want wait", gotMode)
	}
}

func TestSleepInferenceServerFallsBackToAbortAfterDrainTimeout(t *testing.T) {
	var mutex sync.Mutex
	var gotRequests []string
	waitDone := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mode := r.URL.Query().Get("mode")
		mutex.Lock()
		gotRequests = append(gotRequests, r.URL.Query().Get("level")+":"+mode)
		mutex.Unlock()
		if mode == "wait" {
			select {
			case <-waitDone:
				w.WriteHeader(http.StatusOK)
			case <-r.Context().Done():
			}
			return
		}
		close(waitDone)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	err := sleepInferenceServer(t.Context(), server.URL, 25*time.Millisecond, time.Second, newSleepTestObserver())
	if err != nil {
		t.Fatalf("sleepInferenceServer returned error: %v", err)
	}
	mutex.Lock()
	defer mutex.Unlock()
	if want := []string{"1:wait", "0:abort"}; !reflect.DeepEqual(gotRequests, want) {
		t.Errorf("sleep requests = %v, want %v", gotRequests, want)
	}
}

func TestSleepInferenceServerDoesNotAbortAfterWaitServerError(t *testing.T) {
	var gotModes []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotModes = append(gotModes, r.URL.Query().Get("mode"))
		http.Error(w, "sleep unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	err := sleepInferenceServer(t.Context(), server.URL, time.Second, time.Second, newSleepTestObserver())
	if err == nil {
		t.Fatal("sleepInferenceServer returned nil, want wait error")
	}
	if want := []string{"wait"}; !reflect.DeepEqual(gotModes, want) {
		t.Errorf("sleep modes = %v, want %v", gotModes, want)
	}
}

func TestSleepInferenceServerReturnsAbortFailure(t *testing.T) {
	var mutex sync.Mutex
	var gotModes []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mode := r.URL.Query().Get("mode")
		mutex.Lock()
		gotModes = append(gotModes, r.URL.Query().Get("level")+":"+mode)
		mutex.Unlock()
		if mode == "wait" {
			<-r.Context().Done()
			return
		}
		http.Error(w, "abort unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	err := sleepInferenceServer(t.Context(), server.URL, 25*time.Millisecond, time.Second, newSleepTestObserver())
	if err == nil {
		t.Fatal("sleepInferenceServer returned nil, want abort fallback error")
	}
	if !strings.Contains(err.Error(), "abort fallback failed") {
		t.Errorf("error = %q, want abort fallback context", err)
	}
	mutex.Lock()
	defer mutex.Unlock()
	if want := []string{"1:wait", "0:abort"}; !reflect.DeepEqual(gotModes, want) {
		t.Errorf("sleep modes = %v, want %v", gotModes, want)
	}
}
