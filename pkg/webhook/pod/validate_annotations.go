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

package pod

import (
	"context"
	"fmt"
	"strings"

	admissionv1 "k8s.io/api/admission/v1"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	api "github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/api"
	dualpods "github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/controller/dual-pods"
	launcherpopulator "github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/controller/launcher-populator"
)

// PodAnnotationValidator rejects updates that mutate annotations and labels
// used by FMA controllers to link server-requesting and server-providing Pods.
type PodAnnotationValidator struct {
	decoder admission.Decoder
}

// Handle validates Pod update admission requests and enforces the annotation/label
// immutability policy used by FMA controllers. It only evaluates Update
// operations, decodes the old and new Pod objects, and then checks a fixed set
// of protected annotations and labels that are used to associate server-
// requesting and server-providing Pods. User-initiated updates that change
// these protected fields are rejected. When a Pod is already bound (has the
// dual label), changes to request-defining annotations are additionally
// restricted; these are allowed only when the request originates from an
// identified controller service account. Controller service accounts are
// detected by exact parsing of the `system:serviceaccount:<ns>:<sa>` username
// and matching the service account name against a small whitelist.
func (v *PodAnnotationValidator) Handle(ctx context.Context, req admission.Request) admission.Response {
	logger := klog.FromContext(ctx).WithName("pod-annotation-validator")
	if v.decoder == nil {
		return admission.Errored(500, fmt.Errorf("decoder not initialized"))
	}
	if req.Operation != admissionv1.Update {
		return admission.Allowed("only validating updates")
	}

	var oldPod corev1.Pod
	if err := v.decoder.DecodeRaw(req.OldObject, &oldPod); err != nil {
		logger.Error(err, "failed to decode old pod")
		return admission.Errored(400, err)
	}
	var newPod corev1.Pod
	if err := v.decoder.DecodeRaw(req.Object, &newPod); err != nil {
		logger.Error(err, "failed to decode new pod")
		return admission.Errored(400, err)
	}

	// Determine whether the request originates from a known controller
	// service account. Usernames for service accounts are of the form
	// "system:serviceaccount:<ns>:<sa>". We parse and match the service account
	// name exactly to avoid false positives from suffix matches.
	userIsController := false
	if req.UserInfo.Username != "" {
		ua := req.UserInfo
		if strings.HasPrefix(ua.Username, "system:serviceaccount:") {
			parts := strings.Split(ua.Username, ":")
			if len(parts) == 4 {
				saName := parts[3]
				if saName == launcherpopulator.ControllerName || saName == dualpods.ControllerName {
					userIsController = true
				}
			}
		}
	}

	// Annotation keys whose values cannot be changed by users
	protectedAnns := []string{
		api.StatusAnnotationName,
		api.AcceleratorsAnnotationName,
		api.LauncherBasedAnnotationName,
		api.RequesterAnnotationName,
		api.NominalHashAnnotationName,
	}
	// Label keys whose values cannot be changed by users
	protectedLabels := []string{
		api.DualLabelName,
		api.SleepingLabelName,
	}

	// Reject any mutation (addition, removal, or value change) of these annotations/labels
	for _, k := range protectedAnns {
		oldV, oldOK := oldPod.Annotations[k]
		newV, newOK := newPod.Annotations[k]
		if oldOK != newOK || oldV != newV {
			if userIsController {
				// allow controller updates to controller-managed fields
				continue
			}
			msg := fmt.Sprintf("annotation %s is managed by FMA controllers and cannot be modified directly", k)
			return admission.Denied(msg)
		}
	}
	for _, k := range protectedLabels {
		oldV, oldOK := oldPod.Labels[k]
		newV, newOK := newPod.Labels[k]
		if oldOK != newOK || oldV != newV {
			if userIsController {
				// allow controller updates to controller-managed fields
				continue
			}
			msg := fmt.Sprintf("label %s is managed by FMA controllers and cannot be modified directly", k)
			return admission.Denied(msg)
		}
	}

	// When a Pod is a server-requesting Pod that is already bound
	// (i.e., it has a dual label), forbid changes to what it is requesting.
	// Changes before binding are allowed.
	isRequestingOld := len(oldPod.Annotations[api.ServerPatchAnnotationName]) > 0 || len(oldPod.Annotations[api.InferenceServerConfigAnnotationName]) > 0
	if isRequestingOld {
		if _, bound := oldPod.Labels[api.DualLabelName]; bound {
			requestKeys := []string{
				api.ServerPatchAnnotationName, // TODO: will go away with M3
				api.InferenceServerConfigAnnotationName,
				api.AdminPortAnnotationName,
			}
			// Controller exemption handled earlier by `userIsController`.
			for _, k := range requestKeys {
				oldV, oldOK := oldPod.Annotations[k]
				newV, newOK := newPod.Annotations[k]
				if oldOK != newOK || oldV != newV {
					if userIsController { // allow controller updates
						continue
					}
					msg := fmt.Sprintf("mutation of request-defining annotation %s while bound is not allowed", k)
					return admission.Denied(msg)
				}
			}
		}
	}

	return admission.Allowed("ok")
}

func (v *PodAnnotationValidator) InjectDecoder(d admission.Decoder) error {
	v.decoder = d
	return nil
}
