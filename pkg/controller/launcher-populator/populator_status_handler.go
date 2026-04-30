package launcherpopulator

import (
	"context"
	"slices"

	fmav1alpha1 "github.com/llm-d-incubation/llm-d-fast-model-actuation/api/fma/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
)

// setLPPStatusErrors sets the LauncherPopulationPolicy's Status.Errors to desiredErrors using
// Server-Side Apply. Pass nil or an empty slice to clear all errors.
// The call is skipped when the current Status already matches (same errors and same
// ObservedGeneration), avoiding unnecessary API round-trips.
func (ctl *controller) setLPPStatusErrors(ctx context.Context, lpp *fmav1alpha1.LauncherPopulationPolicy, desiredErrors []string) error {
	if lpp.Status.ObservedGeneration == lpp.Generation && slices.Equal(lpp.Status.Errors, desiredErrors) {
		// Status already reflects the desired state; nothing to do.
		return nil
	}
	lppCopy := lpp.DeepCopy()
	lppCopy.Status = fmav1alpha1.LauncherPopulationPolicyStatus{
		ObservedGeneration: lpp.Generation,
		Errors:             desiredErrors,
	}
	echo, err := ctl.fmaclient.LauncherPopulationPolicies(lppCopy.Namespace).Update(ctx, lppCopy, metav1.UpdateOptions{FieldManager: ControllerName})
	resourceVersion := ""
	if err == nil {
		resourceVersion = echo.ResourceVersion
	}
	klog.FromContext(ctx).V(4).Info("Updated LauncherPopulationPolicyStatus", "name", lppCopy.Name, "observedGeneration", lppCopy.Generation, "errors", desiredErrors, "resourceVersion", resourceVersion)
	return err
}

// setLCStatusErrors sets the LauncherConfig's Status.Errors to desiredErrors.
// Pass nil or an empty slice to clear all errors.
// The call is skipped when the current Status already matches (same errors and same
// ObservedGeneration), avoiding unnecessary API round-trips.
func (ctl *controller) setLCStatusErrors(ctx context.Context, lc *fmav1alpha1.LauncherConfig, desiredErrors []string) error {
	if lc.Status.ObservedGeneration == lc.Generation && slices.Equal(lc.Status.Errors, desiredErrors) {
		// Status already reflects the desired state; nothing to do.
		return nil
	}
	lcCopy := lc.DeepCopy()
	lcCopy.Status = fmav1alpha1.LauncherConfigStatus{
		ObservedGeneration: lc.Generation,
		Errors:             desiredErrors,
	}
	echo, err := ctl.fmaclient.LauncherConfigs(lcCopy.Namespace).UpdateStatus(ctx, lcCopy, metav1.UpdateOptions{FieldManager: ControllerName})
	resourceVersion := ""
	if err == nil {
		resourceVersion = echo.ResourceVersion
	}
	klog.FromContext(ctx).V(4).Info("Updated LauncherConfigStatus", "name", lcCopy.Name, "observedGeneration", lcCopy.Generation, "errors", desiredErrors, "resourceVersion", resourceVersion)
	return err
}
