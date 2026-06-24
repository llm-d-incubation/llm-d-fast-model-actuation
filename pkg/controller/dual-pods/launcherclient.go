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

package dualpods

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/prometheus/client_golang/prometheus"
)

type LauncherClient struct {
	baseURL *url.URL

	// An ObserverVec that needs values for labels purpose, method, status_code.
	// May be `nil` when `isclessLatencyHistograms` is not.
	latencyHistograms prometheus.ObserverVec

	// An ObserverVec that needs values for labels purpose, method, isc_name, and status_code.
	isclessLatencyHistograms prometheus.ObserverVec
}

type launcherError struct {
	StatusCode int
	Err        error
}

func (e *launcherError) Error() string {
	return fmt.Sprintf("launcher error %d: %v", e.StatusCode, e.Err)
}

const (
	VllmConfigISCNameAnnotationKey       = "isc-name"
	VllmConfigInferencePortAnnotationKey = "inference-port"

	// InstanceStatusStopped is the status value reported by the launcher
	// when a vLLM instance's process has terminated.
	InstanceStatusStopped = "stopped"
)

// Make a new one.
// latencyHistograms needs values for labels purpose, method, status_code
func NewLauncherClient(baseURL string, latencyHistograms prometheus.ObserverVec) (*LauncherClient, error) {
	return newLauncherClient(baseURL, nil, latencyHistograms)
}

// newLauncherClient is a more general form of NewLauncherClient.
// isclessLatencyHistograms  needs values for labels purpose, method, isc_name, and status_code.
// latencyHistograms needs values for labels purpose, method, status_code.
// latencyHistograms may be nil if isclessLatencyHistograms is not;
// in this case the first method call to this LauncherClient will set latencyHistograms
// by currying isclessLatencyHistograms
// with a value for the `isc_name` label, using the returned value in `GetInstanceState`
// if that is the first method called and it succeeds and otherwise using the empty string.
func newLauncherClient(baseURL string, isclessLatencyHistograms, latencyHistograms prometheus.ObserverVec) (*LauncherClient, error) {
	parsedURL, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("invalid base URL: %w", err)
	}

	c := &LauncherClient{
		baseURL:                  parsedURL,
		latencyHistograms:        latencyHistograms,
		isclessLatencyHistograms: isclessLatencyHistograms,
	}

	return c, nil
}

// VllmConfig matches the launcher API schema.
type VllmConfig struct {
	Options     string            `json:"options"`
	GpuUUIDs    []string          `json:"gpu_uuids,omitempty"`
	EnvVars     map[string]string `json:"env_vars,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

// InstanceState returned by launcher API.
type InstanceState struct {
	InstanceID string `json:"instance_id"`
	Status     string `json:"status"`
	VllmConfig `json:",inline"`
}

// AllInstancesState response.
type AllInstancesState struct {
	TotalInstances   int             `json:"total_instances"`
	RunningInstances int             `json:"running_instances"`
	Instances        []InstanceState `json:"instances"`
}

// Generic response for creation and deletion.
type InstanceActionResult struct {
	Status     string `json:"status"`
	InstanceID string `json:"instance_id,omitempty"`
}

// CreateInstance creates a new instance with a generated ID.
func (c *LauncherClient) CreateInstance(
	ctx context.Context,
	cfg VllmConfig,
) (*InstanceActionResult, error) {
	return c.create(ctx, "/v2/vllm/instances", http.MethodPost, cfg)
}

// CreateNamedInstance creates a new instance with a fixed ID.
func (c *LauncherClient) CreateNamedInstance(
	ctx context.Context,
	instanceID string,
	cfg VllmConfig,
) (*InstanceActionResult, error) {
	path := fmt.Sprintf("/v2/vllm/instances/%s", instanceID)
	return c.create(ctx, path, http.MethodPut, cfg)
}

// GetInstanceState returns the state of a single instance.
func (c *LauncherClient) GetInstanceState(
	ctx context.Context,
	instanceID string,
) (*InstanceState, error) {
	path := fmt.Sprintf("/v2/vllm/instances/%s", instanceID)
	var out InstanceState
	if err := c.fullDo(ctx, "get_instance_state", http.MethodGet, path, nil, &out, func() {
		iscName := out.Annotations[VllmConfigISCNameAnnotationKey]
		c.latencyHistograms = c.isclessLatencyHistograms.MustCurryWith(prometheus.Labels{"isc_name": iscName})
	}); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListInstances returns all instances with status.
func (c *LauncherClient) ListInstances(
	ctx context.Context,
) (*AllInstancesState, error) {
	var out AllInstancesState
	if err := c.do(ctx, "list_instances", http.MethodGet, "/v2/vllm/instances", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListInstanceIDs returns instance IDs only.
func (c *LauncherClient) ListInstanceIDs(
	ctx context.Context,
) ([]string, error) {
	type resp struct {
		InstanceIDs []string `json:"instance_ids"`
		Count       int      `json:"count"`
	}

	var out resp
	if err := c.do(ctx, "list_instance_ids", http.MethodGet, "/v2/vllm/instances?detail=false", nil, &out); err != nil {
		return nil, err
	}
	return out.InstanceIDs, nil
}

// DeleteInstance removes a specific instance.
func (c *LauncherClient) DeleteInstance(
	ctx context.Context,
	instanceID string,
) (*InstanceActionResult, error) {
	path := fmt.Sprintf("/v2/vllm/instances/%s", instanceID)
	var out InstanceActionResult
	if err := c.do(ctx, "delete_instance", http.MethodDelete, path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteAllInstances removes all instances.
func (c *LauncherClient) DeleteAllInstances(
	ctx context.Context,
) (map[string]interface{}, error) {
	var out map[string]interface{}
	if err := c.do(ctx, "delete_all_instances", http.MethodDelete, "/v2/vllm/instances", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *LauncherClient) Health(
	ctx context.Context,
) error {
	return c.do(ctx, "get_health", http.MethodGet, "/health", nil, nil)
}

func (c *LauncherClient) create(
	ctx context.Context,
	path string,
	method string,
	cfg VllmConfig,
) (*InstanceActionResult, error) {
	var out InstanceActionResult
	if err := c.do(ctx, "create_instance", method, path, cfg, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// IsInstanceNotFoundError returns true when the launcher reports the instance
// does not exist.
func IsInstanceNotFoundError(err error) bool {
	var launcherErr *launcherError
	return errors.As(err, &launcherErr) && launcherErr.StatusCode == http.StatusNotFound
}

func (c *LauncherClient) do(
	ctx context.Context,
	purpose string,
	method string,
	path string,
	body any,
	out any,
) error {
	return c.fullDo(ctx, purpose, method, path, body, out, nil)
}

// complete is called after attempted HTTP call and response parse and,
// regardless of success of those,
// must ensure that c.latencyHistograms is not nil
func (c *LauncherClient) fullDo(
	ctx context.Context,
	purpose string,
	method string,
	path string,
	body any,
	out any,
	complete func(),
) error {
	parsed, err := url.Parse(path)
	if err != nil {
		return fmt.Errorf("failed to parse URL %q: %w", path, err)
	}
	u := c.baseURL.ResolveReference(parsed)
	var oc ObserverCube
	switch {
	case c.latencyHistograms != nil:
		oc = c.latencyHistograms
	case complete != nil:
		oc = completeThenSubscript{c, complete}
	default:
		c.latencyHistograms = c.isclessLatencyHistograms.MustCurryWith(prometheus.Labels{"isc_name": ""})
		oc = c.latencyHistograms
	}
	statusCode, err := doHTTP(ctx, purpose, method, u.String(), oc, body, out)

	if err != nil || statusCode >= 300 {
		return &launcherError{StatusCode: statusCode, Err: err}
	}

	return nil
}

type completeThenSubscript struct {
	c        *LauncherClient
	complete func()
}

func (w completeThenSubscript) WithLabelValues(vals ...string) prometheus.Observer {
	w.complete()
	return w.c.latencyHistograms.WithLabelValues(vals...)
}
