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
	namespace string,
	corev1PreInformers corev1preinformers.Interface,
	fmaInformerFactory fmainformers.SharedInformerFactory,
) (*controller, error) {
	ctl := &controller{
		enqueueLogger: logger.WithName(ControllerName),
		coreclient:    coreClient,
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
		ctl.enqueueLogger.V(5).Info("Notified of add of type of ignored object", "type", fmt.Sprintf("%T", obj))
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
		ctl.enqueueLogger.V(5).Info("Notified of update of type of ignored object", "type", fmt.Sprintf("%T", obj))
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
		ctl.enqueueLogger.V(5).Info("Notified of delete of type of ignored object", "type", fmt.Sprintf("%T", obj))
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
	logger := klog.FromContext(ctx)

	// Build desired state from all policies
	populationPolicy, err := ctl.buildDesiredStateFromPolicies(ctx, nil)
	if err != nil {
		logger.Error(err, "Failed to build desired state from policies")
		return err, true
	}

	logger.Info("Final population policy", "policy", MapToLoggable(populationPolicy))

	// Adjust launcher pods according to final requirements
	if err := ctl.reconcileAllLaunchers(ctx, populationPolicy); err != nil {
		logger.Error(err, "Failed to reconcile launchers")
		return err, true
	}
	return nil, false
}

func (item lcItem) process(ctx context.Context, ctl *controller) (error, bool) {
	logger := klog.FromContext(ctx)

	// Get the LauncherConfig
	lc, err := ctl.lcLister.LauncherConfigs(ctl.namespace).Get(item.Name)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("LauncherConfig does not exist yet, skipping reconciliation", "name", item.Name)
			return nil, false
		}
		logger.Error(err, "Failed to get LauncherConfig", "name", item.Name)
		return err, true
	}

	// Build desired state only for policies that reference this LauncherConfig
	populationPolicy, err := ctl.buildDesiredStateFromPolicies(ctx, &item.Name)
	if err != nil {
		logger.Error(err, "Failed to build desired state from policies")
		return err, true
	}

	logger.Info("Final population policy for LauncherConfig", "config", item.Name, "policy", MapToLoggable(populationPolicy))

	// Reconcile launchers for this LauncherConfig
	if err := ctl.reconcileAllLaunchers(ctx, populationPolicy); err != nil {
		logger.Error(err, "Failed to reconcile launchers for LauncherConfig", "name", lc.Name)
		return err, true
	}

	logger.Info("Successfully reconciled launchers for LauncherConfig", "name", lc.Name)
	return nil, false
}

// buildDesiredStateFromPolicies builds the desired state map from all policies.
// If filterByConfig is provided, only policies referencing that config are considered.
func (ctl *controller) buildDesiredStateFromPolicies(ctx context.Context, filterByConfig *string) (map[NodeLauncherKey]int32, error) {
	logger := klog.FromContext(ctx)

	policies, err := ctl.lppLister.List(labels.Everything())
	if err != nil {
		return nil, fmt.Errorf("failed to list LauncherPopulationPolicies: %w", err)
	}

	desired := make(map[NodeLauncherKey]int32)
	for _, lpp := range policies {
		nodes, err := ctl.getMatchingNodes(ctx, lpp.Spec.EnhancedNodeSelector)
		if err != nil {
			logger.Error(err, "Failed to get matching nodes for policy", "policy", lpp.Name)
			continue
		}

		for _, countRule := range lpp.Spec.CountForLauncher {
			if filterByConfig != nil && countRule.LauncherConfigName != *filterByConfig {
				continue
			}
			for _, node := range nodes {
				key := NodeLauncherKey{
					NodeName:           node.Name,
					LauncherConfigName: countRule.LauncherConfigName,
				}
				if current, exists := desired[key]; !exists || countRule.LauncherCount > current {
					desired[key] = countRule.LauncherCount
				}
			}
		}
	}

	return desired, nil
}

// getMatchingNodes returns nodes that match the EnhancedNodeSelector
func (ctl *controller) getMatchingNodes(ctx context.Context, selector fmav1alpha1.EnhancedNodeSelector) ([]corev1.Node, error) {
	// Use label selector to filter nodes
	labelSelector, err := metav1.LabelSelectorAsSelector(&selector.LabelSelector)
	if err != nil {
		return nil, fmt.Errorf("failed to convert label selector: %w", err)
	}
	nodes, err := ctl.nodeLister.List(labelSelector)
	if err != nil {
		return nil, fmt.Errorf("failed to list nodes using nodeLister: %w", err)
	}

	var matchedNodes []corev1.Node
	for _, node := range nodes {
		if matchesResourceConditions(node.Status.Allocatable, selector.AllocatableResources) {
			matchedNodes = append(matchedNodes, *node)
		}
	}
	return matchedNodes, nil
}

// reconcileAllLaunchers adjusts all launcher pods according to final requirements.
// It processes each node separately to ensure deletions happen before creations,
// minimizing peak memory consumption on each node.
func (ctl *controller) reconcileAllLaunchers(ctx context.Context, desired map[NodeLauncherKey]int32) error {
	logger := klog.FromContext(ctx)

	// Group by node to optimize resource usage across all LauncherConfigs on each node
	nodeGroups := make(map[string][]NodeLauncherKey)
	for key := range desired {
		nodeGroups[key.NodeName] = append(nodeGroups[key.NodeName], key)
	}

	// Process each node separately to ensure deletions happen before creations
	// at the node level, minimizing peak memory consumption
	for nodeName, keys := range nodeGroups {
		if err := ctl.reconcileLaunchersOnSingleNode(ctx, nodeName, keys, desired); err != nil {
			logger.Error(err, "Failed to reconcile launchers on node", "node", nodeName)
			continue
		}
	}

	return nil
}

// reconcileLaunchersOnSingleNode handles all LauncherConfigs for a single node.
// It ensures that ALL deletions happen BEFORE ANY creations on this node,
// optimizing memory usage by freeing resources before allocating new ones.
func (ctl *controller) reconcileLaunchersOnSingleNode(ctx context.Context, nodeName string, keys []NodeLauncherKey, desired map[NodeLauncherKey]int32) error {
	logger := klog.FromContext(ctx)

	node, err := ctl.nodeLister.Get(nodeName)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("Node no longer exists, skipping reconciliation", "node", nodeName)
			return nil
		}
		return fmt.Errorf("failed to get node %s: %w", nodeName, err)
	}

	// Phase 1: Collect all pods that need to be deleted across all configs on this node
	type deletionInfo struct {
		key    NodeLauncherKey
		pod    *corev1.Pod
		reason string
	}
	var allDeletions []deletionInfo

	// Phase 2: Collect all pods that need to be created across all configs on this node
	type creationInfo struct {
		key   NodeLauncherKey
		count int
	}
	var allCreations []creationInfo

	// Analyze each LauncherConfig on this node
	for _, key := range keys {
		desiredCount := desired[key]

		currentLaunchers, err := ctl.getCurrentLaunchersOnNode(ctx, key)
		if err != nil {
			logger.Error(err, "Failed to get current launchers for config",
				"node", nodeName, "config", key.LauncherConfigName)
			continue
		}

		currentCount := int32(len(currentLaunchers))
		diff := desiredCount - currentCount

		logger.Info("Analyzing config on node",
			"node", nodeName,
			"config", key.LauncherConfigName,
			"current", currentCount,
			"desired", desiredCount,
			"diff", diff)

		if diff < 0 {
			// Need to delete some pods for this config
			numToDelete := int(-diff)
			collectedForDeletion := 0
			for i := 0; i < len(currentLaunchers) && collectedForDeletion < numToDelete; i++ {
				pod := currentLaunchers[len(currentLaunchers)-1-i]
				isBound, requesterPodName := ctl.isLauncherBoundToServerRequestingPod(pod)
				if isBound {
					logger.V(5).Info("Skipping deletion of bound pod",
						"pod", pod.Name, "server-requesting pod", requesterPodName)
					continue
				}
				allDeletions = append(allDeletions, deletionInfo{
					key:    key,
					pod:    pod,
					reason: "excess launcher",
				})
				collectedForDeletion++
			}
		} else if diff > 0 {
			// Need to create some pods for this config
			allCreations = append(allCreations, creationInfo{
				key:   key,
				count: int(diff),
			})
		}
	}

	// Execute Phase 1: Delete all marked pods FIRST (across all configs on this node)
	deletedCount := 0
	var conflictErrors []string
	for _, del := range allDeletions {
		if err := ctl.coreclient.Pods(del.pod.Namespace).Delete(ctx, del.pod.Name, metav1.DeleteOptions{
			Preconditions: &metav1.Preconditions{
				ResourceVersion: &del.pod.ResourceVersion,
			},
		}); err != nil {
			if apierrors.IsNotFound(err) {
				logger.Info("Launcher pod already deleted", "pod", del.pod.Name)
				deletedCount++
				continue
			}
			if apierrors.IsConflict(err) {
				logger.Info("Launcher pod version conflict, skipping deletion",
					"pod", del.pod.Name, "error", err)
				conflictErrors = append(conflictErrors, del.pod.Name)
				continue
			}
			return fmt.Errorf("failed to delete launcher pod %s: %w", del.pod.Name, err)
		}
		logger.Info("Deleted launcher pod (Phase 1)",
			"pod", del.pod.Name,
			"node", nodeName,
			"config", del.key.LauncherConfigName,
			"reason", del.reason)
		deletedCount++
	}

	if len(conflictErrors) > 0 {
		return fmt.Errorf("encountered %d version conflicts during deletion (pods: %v), will retry",
			len(conflictErrors), conflictErrors)
	}

	if deletedCount < len(allDeletions) {
		logger.Info("Fewer pods deleted than planned",
			"planned", len(allDeletions),
			"actual", deletedCount)
	} else if deletedCount > 0 {
		logger.Info("Completed Phase 1: Deletions",
			"node", nodeName,
			"deleted", deletedCount)
	}

	// Execute Phase 2: Create all needed pods AFTER deletions complete
	totalCreated := 0
	for _, creation := range allCreations {
		if err := ctl.createLaunchers(ctx, *node, creation.key, creation.count); err != nil {
			logger.Error(err, "Failed to create launchers for config",
				"node", nodeName,
				"config", creation.key.LauncherConfigName,
				"count", creation.count)
			return err
		}
		totalCreated += creation.count
		logger.Info("Created launchers for config (Phase 2)",
			"node", nodeName,
			"config", creation.key.LauncherConfigName,
			"created", creation.count)
	}

	if totalCreated > 0 {
		logger.Info("Completed Phase 2: Creations",
			"node", nodeName,
			"created", totalCreated)
	}

	logger.Info("Completed reconciliation for node",
		"node", nodeName,
		"configs_processed", len(keys),
		"pods_deleted", deletedCount,
		"pods_created", totalCreated)

	return nil
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
func (ctl *controller) createLaunchers(ctx context.Context, node corev1.Node, key NodeLauncherKey, count int) error {
	logger := klog.FromContext(ctx)
	// Fetch the LauncherConfig
	var launcherConfig *fmav1alpha1.LauncherConfig
	launcherConfigName := key.LauncherConfigName
	launcherConfig, err := ctl.lcLister.LauncherConfigs(ctl.namespace).Get(launcherConfigName)
	if err != nil {
		return fmt.Errorf("failed to get LauncherConfig %s/%s: %+v", ctl.namespace, launcherConfigName, err)
	}

	// Create the specified number of launcher pods
	for i := 0; i < count; i++ {
		pod, err := utils.BuildLauncherPodFromTemplate(launcherConfig.Spec.PodTemplate, ctl.namespace, key.NodeName, key.LauncherConfigName)
		if err != nil {
			return fmt.Errorf("failed to build launcher pod: %w", err)
		}
		pod.GenerateName = fmt.Sprintf("launcher-%s-", launcherConfig.Name)
		// Set owner reference pointing to LauncherConfig
		ownerRef := *metav1.NewControllerRef(launcherConfig, fmav1alpha1.SchemeGroupVersion.WithKind("LauncherConfig"))
		ownerRef.BlockOwnerDeletion = ptr.To(false)
		pod.OwnerReferences = []metav1.OwnerReference{
			ownerRef,
		}

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
