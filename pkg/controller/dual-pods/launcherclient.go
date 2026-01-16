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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

type LauncherClient struct {
	baseURL    *url.URL
	httpClient *http.Client
}

func NewLauncherClient(baseURL string) (*LauncherClient, error) {
	parsedURL, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("invalid base URL: %w", err)
	}

	c := &LauncherClient{
		baseURL: parsedURL,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}

	return c, nil
}

// VllmConfig matches the launcher API schema.
type VllmConfig struct {
	Options string                 `json:"options"`
	EnvVars map[string]interface{} `json:"env_vars,omitempty"`
}

// InstanceStatus returned by status APIs.
type InstanceStatus struct {
	InstanceID string `json:"instance_id"`
	Status     string `json:"status"`
}

// AllInstancesStatus response.
type AllInstancesStatus struct {
	TotalInstances   int              `json:"total_instances"`
	RunningInstances int              `json:"running_instances"`
	Instances        []InstanceStatus `json:"instances"`
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

// GetInstanceStatus returns the status of a single instance.
func (c *LauncherClient) GetInstanceStatus(
	ctx context.Context,
	instanceID string,
) (*InstanceStatus, error) {
	path := fmt.Sprintf("/v2/vllm/instances/%s", instanceID)
	var out InstanceStatus
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListInstances returns all instances with status.
func (c *LauncherClient) ListInstances(
	ctx context.Context,
) (*AllInstancesStatus, error) {
	var out AllInstancesStatus
	if err := c.do(ctx, http.MethodGet, "/v2/vllm/instances", nil, &out); err != nil {
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
	if err := c.do(ctx, http.MethodGet, "/v2/vllm/instances?detail=false", nil, &out); err != nil {
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
	if err := c.do(ctx, http.MethodDelete, path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteAllInstances removes all instances.
func (c *LauncherClient) DeleteAllInstances(
	ctx context.Context,
) (map[string]interface{}, error) {
	var out map[string]interface{}
	if err := c.do(ctx, http.MethodDelete, "/v2/vllm/instances", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *LauncherClient) Health(
	ctx context.Context,
) error {
	return c.do(ctx, http.MethodGet, "/health", nil, nil)
}

func (c *LauncherClient) create(
	ctx context.Context,
	path string,
	method string,
	cfg VllmConfig,
) (*InstanceActionResult, error) {
	var out InstanceActionResult
	if err := c.do(ctx, method, path, cfg, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *LauncherClient) do(
	ctx context.Context,
	method string,
	path string,
	body any,
	out any,
) error {
	u := c.baseURL.ResolveReference(&url.URL{Path: path})

	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request: %w", err)
		}
		reqBody = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, u.String(), reqBody)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("launcher error %d: %s", resp.StatusCode, string(b))
	}

	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}

	return nil
}
