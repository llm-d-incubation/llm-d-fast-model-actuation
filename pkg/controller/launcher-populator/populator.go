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

package launcherpopulator

import (
	"context"
	"fmt"
	"strings"

	"github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/controller/common"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/utils/ptr"

	corev1preinformers "k8s.io/client-go/informers/core/v1"
	coreclient "k8s.io/client-go/kubernetes/typed/core/v1"
	corev1listers "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	fmav1alpha1 "github.com/llm-d-incubation/llm-d-fast-model-actuation/api/fma/v1alpha1"
	genctlr "github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/controller/generic"
	"github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/controller/utils"
	fmaclientv1alpha1 "github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/generated/clientset/versioned/typed/fma/v1alpha1"
	fmainformers "github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/generated/informers/externalversions"
	fmalisters "github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/generated/listers/fma/v1alpha1"
)

const ControllerName = "launcher-populator"

type Controller interface {
	Start(context.Context) error
}

// NewController makes a new dual pods controller.
// The given namespace is the one to focus on.
func NewController(
	logger klog.Logger,
	coreClient coreclient.CoreV1Interface,
	fmaClient fmaclientv1alpha1.FmaV1alpha1Interface,
	namespace string,
	corev1PreInformers corev1preinformers.Interface,
	fmaInformerFactory fmainformers.SharedInformerFactory,
) (*controller, error) {
	ctl := &controller{
		enqueueLogger: logger.WithName(ControllerName),
		coreclient:    coreClient,
		fmaclient:     fmaClient,
		namespace:     namespace,
		podInformer:   corev1PreInformers.Pods().Informer(),
		podLister:     corev1PreInformers.Pods().Lister(),
		nodeInformer:  corev1PreInformers.Nodes().Informer(),
		nodeLister:    corev1PreInformers.Nodes().Lister(),
		lppInformer:   fmaInformerFactory.Fma().V1alpha1().LauncherPopulationPolicies().Informer(),
		lppLister:     fmaInformerFactory.Fma().V1alpha1().LauncherPopulationPolicies().Lister(),
		lcInformer:    fmaInformerFactory.Fma().V1alpha1().LauncherConfigs().Informer(),
		lcLister:      fmaInformerFactory.Fma().V1alpha1().LauncherConfigs().Lister(),
	}

	// Use a single worker thread to ensure sequential processing of LauncherPopulationPolicy updates
	// Prevents race conditions when multiple threads simultaneously modify the same node/configuration pairs
	ctl.QueueAndWorkers = genctlr.NewQueueAndWorkers(ControllerName, 1, ctl.process)
	_, err := ctl.podInformer.AddEventHandler(ctl)
	if err != nil {
		panic(err)
	}

	_, err = ctl.lppInformer.AddEventHandler(ctl)
	if err != nil {
		panic(err)
	}
	_, err = ctl.lcInformer.AddEventHandler(ctl)
	if err != nil {
		panic(err)
	}
	_, err = ctl.nodeInformer.AddEventHandler(ctl)
	if err != nil {
		panic(err)
	}

	return ctl, nil
}

type controller struct {
	enqueueLogger klog.Logger
	coreclient    coreclient.CoreV1Interface
	fmaclient     fmaclientv1alpha1.FmaV1alpha1Interface
	namespace     string
	podInformer   cache.SharedIndexInformer
	podLister     corev1listers.PodLister
	nodeInformer  cache.SharedIndexInformer
	nodeLister    corev1listers.NodeLister
	lppInformer   cache.SharedIndexInformer
	lppLister     fmalisters.LauncherPopulationPolicyLister
	lcInformer    cache.SharedIndexInformer
	lcLister      fmalisters.LauncherConfigLister
	genctlr.KnowsProcessedSync[queueItem]
}

var _ Controller = &controller{}

type queueItem interface {
	// process returns (err error, retry bool).
	// There will be a retry iff `retry`, error logged if `err != nil`.
	process(ctx context.Context, ctl *controller) (error, bool)
}

type lppItem struct {
	cache.ObjectName
}

type lcItem struct {
	cache.ObjectName
}

func (ctl *controller) OnAdd(obj any, isInInitialList bool) {
	switch typed := obj.(type) {
	case *fmav1alpha1.LauncherPopulationPolicy:
		ctl.enqueueLogger.V(5).Info("Enqueuing LauncherPopulationPolicy reference due to notification of add", "name", typed.Name)
		item := lppItem{cache.MetaObjectToName(typed)}
		ctl.Queue.Add(item)
	case *fmav1alpha1.LauncherConfig:
		ctl.enqueueLogger.V(5).Info("Enqueuing LauncherConfig reference due to notification of add", "name", typed.Name)
		item := lcItem{cache.MetaObjectToName(typed)}
		ctl.Queue.Add(item)
	default:
		ctl.enqueueLogger.V(5).Info("Notified of add of object of ignored type", "type", fmt.Sprintf("%T", obj))
		return
	}
}

func (ctl *controller) OnUpdate(prev, obj any) {
	switch typed := obj.(type) {
	case *fmav1alpha1.LauncherPopulationPolicy:
		ctl.enqueueLogger.V(5).Info("Enqueuing LauncherPopulationPolicy reference due to notification of update", "name", typed.Name)
		item := lppItem{cache.MetaObjectToName(typed)}
		ctl.Queue.Add(item)
	case *fmav1alpha1.LauncherConfig:
		ctl.enqueueLogger.V(5).Info("Enqueuing LauncherConfig reference due to notification of update", "name", typed.Name)
		item := lcItem{cache.MetaObjectToName(typed)}
		ctl.Queue.Add(item)
	default:
		ctl.enqueueLogger.V(5).Info("Notified of update of object of ignored type", "type", fmt.Sprintf("%T", obj))
		return
	}
}

func (ctl *controller) OnDelete(obj any) {
	if dfsu, ok := obj.(cache.DeletedFinalStateUnknown); ok {
		obj = dfsu.Obj
	}
	switch typed := obj.(type) {
	case *fmav1alpha1.LauncherPopulationPolicy:
		ctl.enqueueLogger.V(5).Info("Enqueuing LauncherPopulationPolicy reference due to notification of delete", "name", typed.Name)
		item := lppItem{cache.MetaObjectToName(typed)}
		ctl.Queue.Add(item)
	default:
		ctl.enqueueLogger.V(5).Info("Notified of delete of object of ignored type", "type", fmt.Sprintf("%T", obj))
		return
	}
}

func (ctl *controller) Start(ctx context.Context) error {
	if !cache.WaitForNamedCacheSync(ControllerName, ctx.Done(), ctl.lppInformer.HasSynced, ctl.lcInformer.HasSynced, ctl.podInformer.HasSynced, ctl.nodeInformer.HasSynced) {
		return fmt.Errorf("caches not synced before end of Start context")
	}
	err := ctl.QueueAndWorkers.StartWorkers(ctx)
	if err != nil {
		return fmt.Errorf("failed to start workers: %w", err)
	}
	return nil
}

// process returns (err error, retry bool).
// There will be a retry iff `retry`, error logged if `err != nil`.
func (ctl *controller) process(ctx context.Context, item queueItem) (error, bool) {
	return item.process(ctx, ctl)
}

func (item lppItem) process(ctx context.Context, ctl *controller) (error, bool) {
	return ctl.reconcileFromPolicies(ctx)
}

func (item lcItem) process(ctx context.Context, ctl *controller) (error, bool) {
	// No special treatment for any particular LauncherConfig;
	// missing LauncherConfigs are handled inside buildDesiredStateFromPolicies.
	return ctl.reconcileFromPolicies(ctx)
}

// reconcileFromPolicies builds the desired state from all policies and reconciles
// all launcher pods accordingly. It is the common implementation shared by
// lppItem.process and lcItem.process.
func (ctl *controller) reconcileFromPolicies(ctx context.Context) (error, bool) {
	logger := klog.FromContext(ctx)

	// Build desired state from all policies
	populationPolicy, err := ctl.buildDesiredStateFromPolicies(ctx)
	if err != nil {
		logger.Error(err, "Failed to build desired state from policies")
		return err, true
	}

	logger.Info("Final population policy", "policy", MapToLoggable(populationPolicy))

	// Adjust launcher pods according to final requirements
	needsRequeue, err := ctl.reconcileAllLaunchers(ctx, populationPolicy)
	if err != nil {
		logger.Error(err, "Failed to reconcile launchers")
		return err, true
	}

	return nil, needsRequeue
}

// buildDesiredStateFromPolicies builds the desired state map from all policies.
// It reads each LauncherConfig from the informer's local cache to verify existence
// and obtain the current spec. LauncherConfigs that do not exist are skipped.
func (ctl *controller) buildDesiredStateFromPolicies(ctx context.Context) (map[NodeLauncherKey]DesiredStateEntry, error) {
	logger := klog.FromContext(ctx)

	policies, err := ctl.lppLister.List(labels.Everything())
	if err != nil {
		return nil, fmt.Errorf("failed to list LauncherPopulationPolicies: %w", err)
	}

	// lcStatusReported tracks LauncherConfigs whose Status has already been updated this
	// reconcile cycle. A single LauncherConfig may be referenced by multiple policies or
	// multiple count rules; we only need one Status update per LC per cycle.
	lcStatusReported := make(map[string]struct{})

	desired := make(map[NodeLauncherKey]DesiredStateEntry)
	for _, lpp := range policies {
		nodes, selectorErrs, err := ctl.getMatchingNodes(ctx, lpp.Spec.EnhancedNodeSelector)
		// Ensure proper status for lpp.
		if statusErr := ctl.setLPPStatusErrors(ctx, lpp, selectorErrs); statusErr != nil {
			logger.Error(statusErr, "Failed to set Status for policy", "policy", lpp.Name)
		}
		if err != nil {
			// This is an infrastructure error: the lister failed to list nodes.
			logger.Error(err, "Failed to get matching nodes for policy", "policy", lpp.Name)
			continue
		}

		for _, countRule := range lpp.Spec.CountForLauncher {
			// Read the LauncherConfig from informer's local cache to verify existence
			// and get the current spec (needed for A3: spec-change detection)
			lc, err := ctl.lcLister.LauncherConfigs(ctl.namespace).Get(countRule.LauncherConfigName)
			if err != nil {
				if apierrors.IsNotFound(err) {
					logger.Info("LauncherConfig referenced in policy does not exist, skipping",
						"config", countRule.LauncherConfigName, "policy", lpp.Name)
					continue
				}
				return nil, fmt.Errorf("failed to get LauncherConfig %s: %w", countRule.LauncherConfigName, err)
			}

			// Validate the PodTemplate once per LauncherConfig, not once per node.
			// This is a user error if the inference server container is missing.
			var lcTemplateErrs []string
			if templateErr := utils.ValidateLauncherPodTemplate(lc.Spec.PodTemplate); templateErr != nil {
				logger.Error(templateErr, "Invalid PodTemplate in LauncherConfig, reporting in Status",
					"config", countRule.LauncherConfigName, "policy", lpp.Name)
				lcTemplateErrs = []string{templateErr.Error()}
			}
			// Unconditionally ensure the LauncherConfig Status reflects the current state.
			// setLCStatusErrors is idempotent and skips the API call if Status is already correct.
			// Guard with lcStatusReported so that a LC referenced by multiple policies or
			// multiple count rules only triggers one Status update per reconcile cycle.
			if _, alreadyReported := lcStatusReported[lc.Name]; !alreadyReported {
				if statusErr := ctl.setLCStatusErrors(ctx, lc, lcTemplateErrs); statusErr != nil {
					logger.Error(statusErr, "Failed to set Status for LauncherConfig", "config", countRule.LauncherConfigName)
				}
				lcStatusReported[lc.Name] = struct{}{}
			}
			if len(lcTemplateErrs) > 0 {
				continue
			}

			for _, node := range nodes {
				key := NodeLauncherKey{
					NodeName:           node.Name,
					LauncherConfigName: countRule.LauncherConfigName,
				}
				ownerRef := metav1.OwnerReference{
					APIVersion:         fmav1alpha1.SchemeGroupVersion.String(),
					Kind:               "LauncherConfig",
					Name:               lc.Name,
					UID:                lc.UID,
					Controller:         ptr.To(false),
					BlockOwnerDeletion: ptr.To(false),
				}
				if entry, exists := desired[key]; !exists || countRule.LauncherCount > entry.Count {
					desired[key] = DesiredStateEntry{
						Count:                  countRule.LauncherCount,
						LauncherConfigSpec:     &lc.Spec,
						LauncherConfigOwnerRef: ownerRef,
					}
				}
			}
		}
	}

	return desired, nil
}

// getMatchingNodes returns nodes that match the EnhancedNodeSelector.
// It returns three values: the matched nodes, user-facing selector errors (non-nil when the
// LabelSelector itself is malformed — this is a user configuration error), and an internal
// error (non-nil for unexpected infrastructure failures such as lister errors).
// Callers should handle selectorErrs and err independently.
func (ctl *controller) getMatchingNodes(ctx context.Context, selector fmav1alpha1.EnhancedNodeSelector) ([]corev1.Node, []string, error) {
	// Convert the label selector. A failure here is a user error (malformed LabelSelector).
	labelSelector, selectorErr := metav1.LabelSelectorAsSelector(&selector.LabelSelector)
	if selectorErr != nil {
		return nil, []string{fmt.Sprintf("invalid label selector: %v", selectorErr)}, nil
	}
	nodes, err := ctl.nodeLister.List(labelSelector)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to list nodes using nodeLister: %w", err)
	}

	var matchedNodes []corev1.Node
	for _, node := range nodes {
		if matchesResourceConditions(node.Status.Allocatable, selector.AllocatableResources) {
			matchedNodes = append(matchedNodes, *node)
		}
	}
	return matchedNodes, nil, nil
}

// reconcileAllLaunchers adjusts all launcher pods according to final requirements.
// It returns true if a requeue is needed (deletions were performed or are in progress),
// so that creations happen only after deletions have taken effect.
func (ctl *controller) reconcileAllLaunchers(ctx context.Context, desired map[NodeLauncherKey]DesiredStateEntry) (bool, error) {
	logger := klog.FromContext(ctx)

	// Group by node to process each node separately
	nodeGroups := make(map[string][]NodeLauncherKey)
	for key := range desired {
		nodeGroups[key.NodeName] = append(nodeGroups[key.NodeName], key)
	}

	anyRequeueNeeded := false
	for nodeName, keys := range nodeGroups {
		needsRequeue, err := ctl.reconcileLaunchersOnSingleNode(ctx, nodeName, keys, desired)
		if err != nil {
			logger.Error(err, "Failed to reconcile launchers on node", "node", nodeName)
			anyRequeueNeeded = true
			continue
		}
		anyRequeueNeeded = anyRequeueNeeded || needsRequeue
	}

	return anyRequeueNeeded, nil
}

// reconcileLaunchersOnSingleNode handles all LauncherConfigs for a single node.
// For each LauncherConfig, it does deletions immediately as they are identified
// and remembers creations called for. If any deletions were performed (or are in
// progress from a previous cycle), it returns true to request a requeue so that
// creations happen only after deletions have taken effect, minimizing peak resource
// consumption on the node.
// So that when a LauncherConfig changes, each corresponding launcher Pod that is
// not bound to a server-requesting Pod is deleted and replaced.
func (ctl *controller) reconcileLaunchersOnSingleNode(ctx context.Context, nodeName string, keys []NodeLauncherKey, desired map[NodeLauncherKey]DesiredStateEntry) (bool, error) {
	logger := klog.FromContext(ctx)

	node, err := ctl.nodeLister.Get(nodeName)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("Node no longer exists, skipping reconciliation", "node", nodeName)
			return false, nil
		}
		logger.Error(err, "Unexpected error from node lister (should be impossible), will retry", "node", nodeName)
		return true, nil
	}

	didDelete := false
	deletionInProgress := false // tracks pods already being deleted (DeletionTimestamp set)
	deletionShortfall := false  // excess-pod deletion loop could not delete as many as needed

	type creationInfo struct {
		key   NodeLauncherKey
		count int
		spec  *fmav1alpha1.LauncherConfigSpec
		owner metav1.OwnerReference
	}
	var creations []creationInfo

	// Process each LauncherConfig on this node
	for _, key := range keys {
		entry := desired[key]

		currentLaunchers, err := ctl.getCurrentLaunchersOnNode(ctx, key)
		if err != nil {
			// The only error possible here is from the lister, which should never fail in practice.
			// Log and skip this config rather than aborting the entire reconciliation.
			logger.Error(err, "Failed to get current launchers for config",
				"node", nodeName, "config", key.LauncherConfigName)
			continue
		}

		// Compute the nominal hash for spec-change detection.
		// BuildLauncherPodFromTemplate computes a hash of the fully built pod spec
		// and stores it as the LauncherConfigHashAnnotationKey annotation.
		// The PodTemplate has already been validated in buildDesiredStateFromPolicies;
		// any LC with an invalid template is excluded from the desired map.
		nominalHash := ""
		if entry.LauncherConfigSpec != nil {
			nominalPod, err := utils.BuildLauncherPodFromTemplate(
				entry.LauncherConfigSpec.PodTemplate, ctl.namespace, key.NodeName, key.LauncherConfigName)
			if err != nil {
				// Should not happen: template was already validated upstream.
				logger.Error(err, "Unexpected error building nominal pod (template should have been validated)",
					"node", nodeName, "config", key.LauncherConfigName)
			} else {
				nominalHash = nominalPod.Annotations[string(common.LauncherConfigHashAnnotationKey)]
			}
		}

		// Categorize current pods: separate live unbound current-spec pods from stale/unbound ones
		var liveBoundCount int
		var liveUnboundCurrentPods []*corev1.Pod // live, unbound, spec matches current LauncherConfig
		var staleUnboundPods []*corev1.Pod       // live, unbound, spec is stale

		for _, pod := range currentLaunchers {
			// Skip pods already being deleted
			if pod.DeletionTimestamp != nil {
				deletionInProgress = true
				continue
			}

			isBound, _ := ctl.isLauncherBoundToServerRequestingPod(pod)
			if isBound {
				liveBoundCount++
				continue
			}

			// Check if pod spec is stale (LauncherConfig changed)
			if nominalHash != "" {
				podHash := pod.Annotations[string(common.LauncherConfigHashAnnotationKey)]
				if podHash != nominalHash {
					staleUnboundPods = append(staleUnboundPods, pod)
					continue
				}
			}

			liveUnboundCurrentPods = append(liveUnboundCurrentPods, pod)
		}

		// Delete stale pods immediately (spec changed → delete and replace)
		staleNotDeleted := 0
		for _, pod := range staleUnboundPods {
			err := ctl.coreclient.Pods(pod.Namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{
				Preconditions: &metav1.Preconditions{UID: &pod.UID, ResourceVersion: &pod.ResourceVersion},
			})
			if err != nil {
				if apierrors.IsNotFound(err) || apierrors.IsGone(err) {
					logger.Info("Stale launcher pod already deleted", "pod", pod.Name)
					continue
				}
				if apierrors.IsConflict(err) {
					// Pod was modified (e.g. bound) since we read it; skip deletion.
					staleNotDeleted++
					logger.Info("Stale launcher pod was modified since read, skipping deletion", "pod", pod.Name)
					continue
				}
				return false, fmt.Errorf("failed to delete stale launcher pod %s: %w", pod.Name, err)
			}
			logger.Info("Deleted stale launcher pod (spec changed)",
				"pod", pod.Name,
				"node", nodeName,
				"config", key.LauncherConfigName)
			didDelete = true
		}

		// Calculate diff based on effective remaining pods after stale deletion
		effectiveRemaining := liveBoundCount + len(liveUnboundCurrentPods) + staleNotDeleted
		diff := entry.Count - int32(effectiveRemaining)

		logger.Info("Analyzed config on node",
			"node", nodeName,
			"config", key.LauncherConfigName,
			"current", effectiveRemaining,
			"stale", len(staleUnboundPods),
			"desired", entry.Count,
			"diff", diff)

		if diff < 0 {
			// Need to delete excess pods from live unbound current pods
			numToDelete := int(-diff)
			for i := len(liveUnboundCurrentPods) - 1; i >= 0 && numToDelete > 0; i-- {
				pod := liveUnboundCurrentPods[i]
				err := ctl.coreclient.Pods(pod.Namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{
					Preconditions: &metav1.Preconditions{UID: &pod.UID, ResourceVersion: &pod.ResourceVersion},
				})
				if err != nil {
					if apierrors.IsNotFound(err) || apierrors.IsGone(err) {
						logger.Info("Launcher pod already deleted", "pod", pod.Name)
						numToDelete--
						continue
					}
					if apierrors.IsConflict(err) {
						// Pod was modified (e.g. bound) since we read it; skip deletion.
						logger.Info("Launcher pod was modified since read, skipping deletion", "pod", pod.Name)
						continue
					}
					return false, fmt.Errorf("failed to delete launcher pod %s: %w", pod.Name, err)
				}
				logger.Info("Deleted excess launcher pod",
					"pod", pod.Name,
					"node", nodeName,
					"config", key.LauncherConfigName)
				didDelete = true
				numToDelete--
			}
			if numToDelete > 0 {
				deletionShortfall = true
			}
		} else if diff > 0 {
			// Remember creations called for (will be executed only if no deletions)
			creations = append(creations, creationInfo{
				key:   key,
				count: int(diff),
				spec:  entry.LauncherConfigSpec,
				owner: entry.LauncherConfigOwnerRef,
			})
		}
	}

	// If any deletions were performed or are in progress, or the desired reduction
	// in launcher count was not fully achieved, requeue for later. This ensures that
	// deletions take effect before any creations happen, so that freed resources are
	// available for newly created pods.
	if didDelete || deletionInProgress || deletionShortfall {
		logger.Info("Requeuing for creation later",
			"node", nodeName,
			"didDelete", didDelete,
			"deletionInProgress", deletionInProgress,
			"deletionShortfall", deletionShortfall)
		return true, nil
	}

	// No deletions needed, proceed with planned creations
	totalCreated := 0
	for _, creation := range creations {
		if err := ctl.createLaunchers(ctx, *node, creation.key, creation.count, creation.spec, creation.owner); err != nil {
			logger.Error(err, "Failed to create launchers for config",
				"node", nodeName,
				"config", creation.key.LauncherConfigName,
				"count", creation.count)
			return false, err
		}
		totalCreated += creation.count
		logger.Info("Created launchers for config",
			"node", nodeName,
			"config", creation.key.LauncherConfigName,
			"created", creation.count)
	}

	logger.Info("Completed reconciliation for node",
		"node", nodeName,
		"configs_processed", len(keys),
		"created", totalCreated)

	return false, nil
}

// getCurrentLaunchersOnNode returns launcher pods for a specific config on a specific node
func (ctl *controller) getCurrentLaunchersOnNode(ctx context.Context, key NodeLauncherKey) ([]*corev1.Pod, error) {
	launcherLabels := map[string]string{
		common.ComponentLabelKey:          common.LauncherComponentLabelValue,
		common.LauncherConfigNameLabelKey: key.LauncherConfigName,
		common.NodeNameLabelKey:           key.NodeName,
	}
	// Use podLister's List method with label selector
	pods, err := ctl.podLister.List(labels.SelectorFromSet(launcherLabels))
	if err != nil {
		return nil, fmt.Errorf("failed to list pods with launcher labels: %w", err)
	}

	return pods, nil
}

// createLaunchers creates the specified number of launcher pods on a node
// using the given LauncherConfig spec and owner reference directly (no additional lookup needed).
func (ctl *controller) createLaunchers(ctx context.Context, node corev1.Node, key NodeLauncherKey, count int, lcSpec *fmav1alpha1.LauncherConfigSpec, lcOwnerRef metav1.OwnerReference) error {
	logger := klog.FromContext(ctx)

	// Create the specified number of launcher pods
	for i := 0; i < count; i++ {
		pod, err := utils.BuildLauncherPodFromTemplate(lcSpec.PodTemplate, ctl.namespace, key.NodeName, key.LauncherConfigName)
		if err != nil {
			return fmt.Errorf("failed to build launcher pod: %w", err)
		}
		pod.OwnerReferences = []metav1.OwnerReference{lcOwnerRef}

		if _, err := ctl.coreclient.Pods(pod.Namespace).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("failed to create launcher pod: %w", err)
		}
		logger.Info("Created launcher pod", "pod", pod.GenerateName, "node", node.Name)
	}
	return nil
}

// isLauncherBoundToServerRequestingPod checks if the launcher pod is bound to any server-requesting pod
func (ctl *controller) isLauncherBoundToServerRequestingPod(launcherPod *corev1.Pod) (bool, string) {
	// Check if the launcher pod has annotations indicating assignment to a server-requesting pod
	requesterAnnotationValue, exists := launcherPod.Annotations[common.RequesterAnnotationKey]
	if !exists {
		return false, ""
	}

	// Verify the format of the annotation value: should be "UID name"
	parts := strings.Split(requesterAnnotationValue, " ")
	if len(parts) != 2 {
		return false, "" // Invalid format
	}

	// Optionally verify that the referenced pod actually exists
	// @TODO if need, we can append the check logic in further PR

	return true, parts[1]
}
