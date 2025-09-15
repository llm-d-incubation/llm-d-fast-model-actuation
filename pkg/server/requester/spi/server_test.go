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

package spi

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Helper function to read from a channel with timeout
func readFromChannelWithTimeout(ch <-chan string, timeout time.Duration) (string, bool) {
	select {
	case val := <-ch:
		return val, true
	case <-time.After(timeout):
		return "", false
	}
}

func TestIpPutHandler_ValidIP(t *testing.T) {
	ipCh := make(chan string, 1)
	handler := ipPutHandler(ipCh)

	body := bytes.NewBufferString("192.168.1.100")
	req := httptest.NewRequest(http.MethodPut, "/ip", body)
	req.Header.Set("Content-Type", "text/plain")

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}

	ip, ok := readFromChannelWithTimeout(ipCh, time.Second)
	if !ok || ip != "192.168.1.100" {
		t.Errorf("expected IP to be sent to channel, got: %q", ip)
	}
}

func TestIpPutHandler_InvalidContentType(t *testing.T) {
	ipCh := make(chan string, 1)
	handler := ipPutHandler(ipCh)

	req := httptest.NewRequest(http.MethodPut, "/ip", bytes.NewBufferString("192.168.1.100"))
	req.Header.Set("Content-Type", "application/json")

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnsupportedMediaType {
		t.Errorf("expected 415 Unsupported Media Type, got %d", rr.Code)
	}
}

func TestIpPutHandler_InvalidIP(t *testing.T) {
	ipCh := make(chan string, 1)
	handler := ipPutHandler(ipCh)

	body := bytes.NewBufferString("not-an-ip")
	req := httptest.NewRequest(http.MethodPut, "/ip", body)
	req.Header.Set("Content-Type", "text/plain")

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", rr.Code)
	}
}

func TestIpDeleteHandler(t *testing.T) {
	ipCh := make(chan string, 1)
	handler := ipDeleteHandler(ipCh)

	req := httptest.NewRequest(http.MethodDelete, "/ip", nil)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}

	ip, ok := readFromChannelWithTimeout(ipCh, time.Second)
	if !ok || ip != "" {
		t.Errorf("expected empty string sent to channel, got: %q", ip)
	}
}
