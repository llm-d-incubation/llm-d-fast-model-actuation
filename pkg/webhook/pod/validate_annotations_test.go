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
	"encoding/json"
	"os"
	"testing"

	admissionv1 "k8s.io/api/admission/v1"
	authv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	pkgapi "github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/api"
)

var testDecoder admission.Decoder

func TestMain(m *testing.M) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		panic(err)
	}
	testDecoder = admission.NewDecoder(scheme)
	os.Exit(m.Run())
}

func TestPodAnnotationValidator_DenyProtectedAnnotationChange(t *testing.T) {
	dec := testDecoder

	v := &PodAnnotationValidator{}
	if err := v.InjectDecoder(dec); err != nil {
		t.Fatalf("inject decoder: %v", err)
	}

	old := &corev1.Pod{}
	old.Annotations = map[string]string{
		pkgapi.RequesterAnnotationName: "old-uid old-name",
	}
	newp := old.DeepCopy()
	newp.Annotations = map[string]string{
		pkgapi.RequesterAnnotationName: "new-uid new-name",
	}

	oldRaw, err := json.Marshal(old)
	if err != nil {
		t.Fatalf("marshal old pod: %v", err)
	}
	newRaw, err := json.Marshal(newp)
	if err != nil {
		t.Fatalf("marshal new pod: %v", err)
	}

	req := admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{Operation: admissionv1.Update, OldObject: runtime.RawExtension{Raw: oldRaw}, Object: runtime.RawExtension{Raw: newRaw}}}

	ctx := klog.NewContext(context.Background(), klog.Background())
	resp := v.Handle(ctx, req)
	if resp.Allowed {
		t.Fatalf("expected update to be denied when protected annotation is changed")
	}
}

func TestPodAnnotationValidator_AllowUnchangedProtectedAnnotation(t *testing.T) {
	dec := testDecoder

	v := &PodAnnotationValidator{}
	if err := v.InjectDecoder(dec); err != nil {
		t.Fatalf("inject decoder: %v", err)
	}

	old := &corev1.Pod{}
	old.Annotations = map[string]string{
		pkgapi.RequesterAnnotationName: "uid name",
	}
	newp := old.DeepCopy()
	newp.Annotations["example.com/foo"] = "bar"

	oldRaw, err := json.Marshal(old)
	if err != nil {
		t.Fatalf("marshal old pod: %v", err)
	}
	newRaw, err := json.Marshal(newp)
	if err != nil {
		t.Fatalf("marshal new pod: %v", err)
	}

	req := admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{Operation: admissionv1.Update, OldObject: runtime.RawExtension{Raw: oldRaw}, Object: runtime.RawExtension{Raw: newRaw}}}

	ctx := klog.NewContext(context.Background(), klog.Background())
	resp := v.Handle(ctx, req)
	if !resp.Allowed {
		t.Fatalf("expected update to be allowed when protected annotation unchanged, got denied: %v", resp.Result)
	}
}

func TestPodAnnotationValidator_DenyProtectedLabelChange(t *testing.T) {
	dec := testDecoder

	v := &PodAnnotationValidator{}
	if err := v.InjectDecoder(dec); err != nil {
		t.Fatalf("inject decoder: %v", err)
	}

	old := &corev1.Pod{}
	old.Labels = map[string]string{
		pkgapi.DualLabelName: "provider",
	}
	newp := old.DeepCopy()
	newp.Labels[pkgapi.DualLabelName] = "other"

	oldRaw, err := json.Marshal(old)
	if err != nil {
		t.Fatalf("marshal old pod: %v", err)
	}
	newRaw, err := json.Marshal(newp)
	if err != nil {
		t.Fatalf("marshal new pod: %v", err)
	}

	req := admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{Operation: admissionv1.Update, OldObject: runtime.RawExtension{Raw: oldRaw}, Object: runtime.RawExtension{Raw: newRaw}}}
	ctx := klog.NewContext(context.Background(), klog.Background())
	resp := v.Handle(ctx, req)
	if resp.Allowed {
		t.Fatalf("expected protected label change to be denied")
	}
}

func TestPodAnnotationValidator_DenyProtectedAnnotationAdditionAndRemoval(t *testing.T) {
	dec := testDecoder

	v := &PodAnnotationValidator{}
	if err := v.InjectDecoder(dec); err != nil {
		t.Fatalf("inject decoder: %v", err)
	}

	// Addition
	old := &corev1.Pod{}
	newp := old.DeepCopy()
	newp.Annotations = map[string]string{
		pkgapi.StatusAnnotationName: "{}",
	}
	oldRaw, err := json.Marshal(old)
	if err != nil {
		t.Fatalf("marshal old pod: %v", err)
	}
	newRaw, err := json.Marshal(newp)
	if err != nil {
		t.Fatalf("marshal new pod: %v", err)
	}
	req := admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{Operation: admissionv1.Update, OldObject: runtime.RawExtension{Raw: oldRaw}, Object: runtime.RawExtension{Raw: newRaw}}}
	ctx := klog.NewContext(context.Background(), klog.Background())
	resp := v.Handle(ctx, req)
	if resp.Allowed {
		t.Fatalf("expected addition of protected annotation to be denied")
	}

	// Removal
	old = &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{pkgapi.RequesterAnnotationName: "uid name"}}}
	newp = old.DeepCopy()
	delete(newp.Annotations, pkgapi.RequesterAnnotationName)
	oldRaw, err = json.Marshal(old)
	if err != nil {
		t.Fatalf("marshal old pod: %v", err)
	}
	newRaw, err = json.Marshal(newp)
	if err != nil {
		t.Fatalf("marshal new pod: %v", err)
	}
	req = admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{Operation: admissionv1.Update, OldObject: runtime.RawExtension{Raw: oldRaw}, Object: runtime.RawExtension{Raw: newRaw}}}
	resp = v.Handle(ctx, req)
	if resp.Allowed {
		t.Fatalf("expected removal of protected annotation to be denied")
	}
}

func TestPodAnnotationValidator_AllowNonUpdateOps(t *testing.T) {
	dec := testDecoder

	v := &PodAnnotationValidator{}
	if err := v.InjectDecoder(dec); err != nil {
		t.Fatalf("inject decoder: %v", err)
	}

	newp := &corev1.Pod{}
	newRaw, err := json.Marshal(newp)
	if err != nil {
		t.Fatalf("marshal new pod: %v", err)
	}
	req := admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{Operation: admissionv1.Create, Object: runtime.RawExtension{Raw: newRaw}}}
	ctx := klog.NewContext(context.Background(), klog.Background())
	resp := v.Handle(ctx, req)
	if !resp.Allowed {
		t.Fatalf("expected non-update operation to be allowed")
	}
}

func TestPodAnnotationValidator_AllowFMAControllerUpdate(t *testing.T) {
	dec := testDecoder

	v := &PodAnnotationValidator{}
	if err := v.InjectDecoder(dec); err != nil {
		t.Fatalf("inject decoder: %v", err)
	}

	old := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{pkgapi.StatusAnnotationName: "old"}}}
	newp := old.DeepCopy()
	newp.Annotations[pkgapi.StatusAnnotationName] = "new"

	oldRaw, err := json.Marshal(old)
	if err != nil {
		t.Fatalf("marshal old pod: %v", err)
	}
	newRaw, err := json.Marshal(newp)
	if err != nil {
		t.Fatalf("marshal new pod: %v", err)
	}

	ar := admissionv1.AdmissionRequest{Operation: admissionv1.Update, OldObject: runtime.RawExtension{Raw: oldRaw}, Object: runtime.RawExtension{Raw: newRaw}}
	ar.UserInfo = authv1.UserInfo{Username: "system:serviceaccount:default:launcher-populator"}
	req := admission.Request{AdmissionRequest: ar}
	ctx := klog.NewContext(context.Background(), klog.Background())
	resp := v.Handle(ctx, req)
	if !resp.Allowed {
		t.Fatalf("expected controller to be allowed to modify protected annotation, got denied: %v", resp.Result)
	}
}

func TestPodAnnotationValidator_AllowFMAControllerUpdateLabel(t *testing.T) {
	dec := testDecoder

	v := &PodAnnotationValidator{}
	if err := v.InjectDecoder(dec); err != nil {
		t.Fatalf("inject decoder: %v", err)
	}

	old := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{pkgapi.DualLabelName: "provider"}}}
	newp := old.DeepCopy()
	newp.Labels[pkgapi.DualLabelName] = "other"

	oldRaw, err := json.Marshal(old)
	if err != nil {
		t.Fatalf("marshal old pod: %v", err)
	}
	newRaw, err := json.Marshal(newp)
	if err != nil {
		t.Fatalf("marshal new pod: %v", err)
	}

	ar := admissionv1.AdmissionRequest{Operation: admissionv1.Update, OldObject: runtime.RawExtension{Raw: oldRaw}, Object: runtime.RawExtension{Raw: newRaw}}
	ar.UserInfo = authv1.UserInfo{Username: "system:serviceaccount:default:dual-pods-controller"}
	req := admission.Request{AdmissionRequest: ar}
	ctx := klog.NewContext(context.Background(), klog.Background())
	resp := v.Handle(ctx, req)
	if !resp.Allowed {
		t.Fatalf("expected dual-pods-controller to be allowed to modify protected label, got denied: %v", resp.Result)
	}
}

func TestPodAnnotationValidator_DenyUserChange(t *testing.T) {
	dec := testDecoder

	v := &PodAnnotationValidator{}
	if err := v.InjectDecoder(dec); err != nil {
		t.Fatalf("inject decoder: %v", err)
	}

	old := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{pkgapi.DualLabelName: "provider"}}}
	newp := old.DeepCopy()
	newp.Labels[pkgapi.DualLabelName] = "other"

	oldRaw, err := json.Marshal(old)
	if err != nil {
		t.Fatalf("marshal old pod: %v", err)
	}
	newRaw, err := json.Marshal(newp)
	if err != nil {
		t.Fatalf("marshal new pod: %v", err)
	}

	ar := admissionv1.AdmissionRequest{Operation: admissionv1.Update, OldObject: runtime.RawExtension{Raw: oldRaw}, Object: runtime.RawExtension{Raw: newRaw}}
	ar.UserInfo = authv1.UserInfo{Username: "alice@example.com"}
	req := admission.Request{AdmissionRequest: ar}
	ctx := klog.NewContext(context.Background(), klog.Background())
	resp := v.Handle(ctx, req)
	if resp.Allowed {
		t.Fatalf("expected non-controller user to be denied when modifying protected label")
	}
}

func TestPodAnnotationValidator_AllowUnboundRequestPodUpdate(t *testing.T) {
	dec := testDecoder

	v := &PodAnnotationValidator{}
	if err := v.InjectDecoder(dec); err != nil {
		t.Fatalf("inject decoder: %v", err)
	}

	old := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{pkgapi.ServerPatchAnnotationName: "p"}}}
	newp := old.DeepCopy()
	newp.Annotations[pkgapi.ServerPatchAnnotationName] = "modified"

	oldRaw, err := json.Marshal(old)
	if err != nil {
		t.Fatalf("marshal old pod: %v", err)
	}
	newRaw, err := json.Marshal(newp)
	if err != nil {
		t.Fatalf("marshal new pod: %v", err)
	}

	ar := admissionv1.AdmissionRequest{Operation: admissionv1.Update, OldObject: runtime.RawExtension{Raw: oldRaw}, Object: runtime.RawExtension{Raw: newRaw}}
	ar.UserInfo = authv1.UserInfo{Username: "alice@example.com"}
	req := admission.Request{AdmissionRequest: ar}
	ctx := klog.NewContext(context.Background(), klog.Background())
	resp := v.Handle(ctx, req)
	if !resp.Allowed {
		t.Fatalf("expected unbound request-def edit to be allowed for non-controller, got denied: %v", resp.Result)
	}
}

func TestPodAnnotationValidator_DenyBoundRequestPodUpdate(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1 to scheme: %v", err)
	}
	dec := admission.NewDecoder(scheme)

	v := &PodAnnotationValidator{}
	if err := v.InjectDecoder(dec); err != nil {
		t.Fatalf("inject decoder: %v", err)
	}

	old := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{pkgapi.ServerPatchAnnotationName: "p"}, Labels: map[string]string{pkgapi.DualLabelName: "provider"}}}
	newp := old.DeepCopy()
	newp.Annotations[pkgapi.ServerPatchAnnotationName] = "modified"

	oldRaw, err := json.Marshal(old)
	if err != nil {
		t.Fatalf("marshal old pod: %v", err)
	}
	newRaw, err := json.Marshal(newp)
	if err != nil {
		t.Fatalf("marshal new pod: %v", err)
	}

	ar := admissionv1.AdmissionRequest{Operation: admissionv1.Update, OldObject: runtime.RawExtension{Raw: oldRaw}, Object: runtime.RawExtension{Raw: newRaw}}
	ar.UserInfo = authv1.UserInfo{Username: ""}
	req := admission.Request{AdmissionRequest: ar}
	ctx := klog.NewContext(context.Background(), klog.Background())
	resp := v.Handle(ctx, req)
	if resp.Allowed {
		t.Fatalf("expected empty-username (non-controller) to be denied for bound request-def edit")
	}
}
