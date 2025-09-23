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

package api

// This file defines interface of the stub.

// AcceleratorQueryPath is the path part of the URL that is polled
// to get the server-requesting container's associated accelerators.
// The scheme of the URL is HTTP.
// The host part of the URL is something that refers to the
// server-requesting container.
// A 200 response's body must contain a JSON array of strings.
// Each string identifies one accelerator, in a way that
// is appropriate for the software used to access the accelerators.
const AcceleratorQueryPath = "/v1/dual-pod/accelerators"

// BecomeReadyPath is the path to POST to in order to set readiness to true
const BecomeReadyPath = "/v1/become-ready"

// BecomeUnreadyPath is the path to POST to in order to set readiness to false
const BecomeUnreadyPath = "/v1/become-unready"

// ReadyPath is where to send a GET to query readiness
const ReadyPath = "/ready"
