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

package launcherpool

import (
	"context"
	"fmt"

	fmav1alpha1 "github.com/llm-d-incubation/llm-d-fast-model-actuation/api/fma/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	//apierrors "k8s.io/apimachinery/pkg/api/errors"
	//metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	fmainformers "github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/generated/informers/externalversions"
	fmalisters "github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/generated/listers/fma/v1alpha1"

	corev1 "k8s.io/api/core/v1"
	corev1preinformers "k8s.io/client-go/informers/core/v1"
	coreclient "k8s.io/client-go/kubernetes/typed/core/v1"
	corev1listers "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	genctlr "github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/controller/generic"
)

const ControllerName = "launcher-pool-populator"

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

func (ctl *controller) OnAdd(obj any, isInInitialList bool) {
	switch typed := obj.(type) {
	case *fmav1alpha1.LauncherPopulationPolicy:
		ctl.enqueueLogger.V(5).Info("Enqueuing LauncherPopulationPolicy reference due to notification of add", "name", typed.Name)
		item := lppItem{cache.MetaObjectToName(typed)}
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
	// Get the list of LauncherPopulationPolicies
	policies, err := ctl.lppLister.List(labels.Everything())
	if err != nil {
		logger.Error(err, "Failed to list LauncherPopulationPolicies")
		return err, true // Return error and retry
	}

	// If needed, process the retrieved policies here
	// For example: iterate through policies to perform corresponding business logic

	logger.Info("Successfully listed LauncherPopulationPolicies", "count", len(policies))

	// Build the PopulationPolicy map, storing the maximum count for each (Node, LauncherConfig) pair
	populationPolicy := make(map[NodeLauncherKey]int32)
	for _, lpp := range policies {
		// Get matching nodes
		nodes, err := ctl.getMatchingNodes(ctx, lpp.Spec.EnhancedNodeSelector)
		if err != nil {
			logger.Error(err, "Failed to get matching nodes for policy", "policy", lpp.Name)
			return err, true
		}
		logger.Info("Found matching nodes", "count", len(nodes), "policy", lpp.Name)
		// For each CountForLauncher rule
		for _, countRule := range lpp.Spec.CountForLauncher {
			for _, node := range nodes {
				key := NodeLauncherKey{
					NodeName:           node.Name,
					LauncherConfigName: countRule.LauncherConfigName,
				}
				currentCount, exists := populationPolicy[key]
				logger.Info("Current count for node", "node", node.Name, "launcherConfigName",
					countRule.LauncherConfigName, "launcherCount", countRule.LauncherCount, "currentCount", currentCount, "exists", exists)

				// Take the maximum value (rule: when multiple CountForLauncher apply to the same pair, take the maximum)
				if !exists || countRule.LauncherCount > currentCount {
					populationPolicy[key] = countRule.LauncherCount
					logger.Info("Updated population policy",
						"node", node.Name,
						"config", countRule.LauncherConfigName,
						"count", countRule.LauncherCount,
						"policy", lpp.Name)
				}
			}
		}
	}

	logger.Info("Final population policy", "policy", MapToLoggable(populationPolicy))
	// Adjust launcher pods according to final requirements
	if err := ctl.reconcileAllLaunchers(ctx, populationPolicy); err != nil {
		logger.Error(err, "Failed to reconcile launchers")
		return err, true
	}
	return nil, false
}

// reconcileAllLaunchers adjusts all launcher pods according to final requirements
func (ctl *controller) reconcileAllLaunchers(ctx context.Context, desired map[NodeLauncherKey]int32) error {
	logger := klog.FromContext(ctx)
	// Reconcile for each (Node, LauncherConfig) pair
	for key, desiredCount := range desired {
		if err := ctl.reconcileLaunchersOnNode(ctx, key, desiredCount); err != nil {
			logger.Error(err, "Failed to reconcile launchers on node",
				"node", key.NodeName,
				"config", key.LauncherConfigName)
			// Continue processing other combinations
		}
	}
	// TODO: Clean up unnecessary launcher pods (those that exist in the cluster but not in desired)
	// This requires tracking which launcher pods were created by us
	return nil
}

// reconcileLaunchersOnNode ensures the number of launchers with a specific launcher config on a node matches the requirement
func (ctl *controller) reconcileLaunchersOnNode(ctx context.Context, key NodeLauncherKey, desiredCount int32) error {
	logger := klog.FromContext(ctx)
	// Get node object
	nodeName := key.NodeName
	launcherConfigName := key.LauncherConfigName
	node, err := ctl.nodeLister.Get(nodeName)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("Node no longer exists, skipping reconciliation", "node", nodeName)
			return nil
		}
		return fmt.Errorf("failed to get node %s: %w", nodeName, err)
	}
	// Get current launchers
	currentLaunchers, err := ctl.getCurrentLaunchersOnNode(ctx, key)
	if err != nil {
		return fmt.Errorf("failed to get current launchers: %w", err)
	}
	currentCount := int32(len(currentLaunchers))
	diff := desiredCount - currentCount
	logger.Info("Reconciling launchers on node",
		"node", nodeName,
		"config", launcherConfigName,
		"current", currentCount,
		"desired", desiredCount,
		"diff", diff)
	if diff > 0 {
		// Need to create more launchers
		err := ctl.createLaunchers(ctx, *node, key, int(diff))
		if err != nil {
			return fmt.Errorf("failed to create launchers: %w", err)
		}
	} else if diff < 0 {
		// Need to delete excess launchers
		err := ctl.deleteExcessLaunchers(ctx, currentLaunchers, int(-diff))
		if err != nil {
			return fmt.Errorf("failed to delete excess launchers: %w", err)
		}
	}
	return nil
}

// getCurrentLaunchersOnNode returns launcher pods for a specific config on a specific node
func (ctl *controller) getCurrentLaunchersOnNode(ctx context.Context, key NodeLauncherKey) ([]corev1.Pod, error) {
	launcherLabels := map[string]string{
		ComponentLabelKey:          LauncherComponentLabelValue,
		LauncherConfigNameLabelKey: key.LauncherConfigName,
	}
	// Use podLister's List method with label selector
	pods, err := ctl.podLister.List(labels.SelectorFromSet(launcherLabels))
	if err != nil {
		return nil, fmt.Errorf("failed to list pods with launcher labels: %w", err)
	}

	// Filter pods that are on the specified node
	var filteredPods []corev1.Pod
	for _, pod := range pods {
		if pod.Spec.NodeName == key.NodeName {
			filteredPods = append(filteredPods, *pod)
		}
	}

	return filteredPods, nil
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
		pod := ctl.buildPodFromTemplate(launcherConfig.Spec.PodTemplate, key)
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

// deleteExcessLaunchers deletes the specified number of launcher pods
func (ctl *controller) deleteExcessLaunchers(ctx context.Context, launchers []corev1.Pod, count int) error {
	logger := klog.FromContext(ctx)
	// Delete the specified number of launcher pods (starting from the end)
	for i := 0; i < count && i < len(launchers); i++ {
		pod := launchers[len(launchers)-1-i]
		if err := ctl.coreclient.Pods(pod.Namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{}); err != nil {
			return fmt.Errorf("failed to delete launcher pod %s: %w", pod.Name, err)
		}
		logger.Info("Deleted launcher pod", "pod", pod.Name)
	}
	return nil
}

// buildPodFromTemplate creates a pod from a template and assigns it to a node
func (ctl *controller) buildPodFromTemplate(template corev1.PodTemplateSpec, key NodeLauncherKey) *corev1.Pod {
	pod := &corev1.Pod{
		ObjectMeta: template.ObjectMeta,
		Spec:       template.Spec,
	}
	pod.Namespace = ctl.namespace
	// Ensure labels are set
	if pod.Labels == nil {
		pod.Labels = make(map[string]string)
	}
	pod.Labels[ComponentLabelKey] = LauncherComponentLabelValue
	pod.Labels[LauncherGeneratedByLabelKey] = LauncherGeneratedByLabelValue
	pod.Labels[LauncherConfigNameLabelKey] = key.LauncherConfigName
	// Assign to specific node
	pod.Spec.NodeName = key.NodeName
	return pod
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
