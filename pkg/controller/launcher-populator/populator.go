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
	"time"

	"github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/controller/common"
	"github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/controller/utils"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"

	corev1preinformers "k8s.io/client-go/informers/core/v1"
	coreclient "k8s.io/client-go/kubernetes/typed/core/v1"
	corev1listers "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"
	"k8s.io/utils/clock"

	fmav1alpha1 "github.com/llm-d-incubation/llm-d-fast-model-actuation/api/fma/v1alpha1"
	genctlr "github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/controller/generic"
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
	expectationTimeout time.Duration,
	stuckSchedulingThreshold time.Duration,
	stuckStartingThreshold time.Duration,
) (*controller, error) {
	registerMetrics()
	ctl := &controller{
		enqueueLogger:            logger.WithName(ControllerName),
		coreclient:               coreClient,
		fmaclient:                fmaClient,
		namespace:                namespace,
		podInformer:              corev1PreInformers.Pods().Informer(),
		podLister:                corev1PreInformers.Pods().Lister(),
		nodeInformer:             corev1PreInformers.Nodes().Informer(),
		nodeLister:               corev1PreInformers.Nodes().Lister(),
		lppInformer:              fmaInformerFactory.Fma().V1alpha1().LauncherPopulationPolicies().Informer(),
		lppLister:                fmaInformerFactory.Fma().V1alpha1().LauncherPopulationPolicies().Lister(),
		lcInformer:               fmaInformerFactory.Fma().V1alpha1().LauncherConfigs().Informer(),
		lcLister:                 fmaInformerFactory.Fma().V1alpha1().LauncherConfigs().Lister(),
		expectations:             newPendingExpectations(expectationTimeout),
		stuckSchedulingThreshold: stuckSchedulingThreshold,
		stuckStartingThreshold:   stuckStartingThreshold,
		clock:                    clock.RealClock{},
		metrics:                  newMetricsState(),
	}
	ctl.policy = newDigestedPolicy()

	// digestQueue carries funcItem (LC/LPP/Node references). Single worker, so
	// digest mutations are serial. KnowsProcessedSync emits the
	// onDigestSyncProcessed hook after the initial batch drains, which is when
	// keyQueue's workers start.
	digestQueue := genctlr.NewKnowsProcessedSync[queueItem](
		ControllerName+"-digest", 1,
		ctl.processDigestItem,
		makeDigestSentinel, isDigestSentinel,
		ctl.onDigestSyncProcessed,
	)
	ctl.digestQueue = &digestQueue

	// keyQueue carries keyItem (NodeLauncherKey reconciliation requests).
	// Multiple workers process keys in parallel; concurrency safety relies on
	// digestedPolicy.mu (RLock for snapshot reads, Lock for digest mutations
	// performed by digestQueue's single worker).
	keyQueue := genctlr.NewQueueAndWorkers[keyItem](
		ControllerName+"-key", 4,
		ctl.processKeyItem,
	)
	ctl.keyQueue = &keyQueue

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

	// digestQueue processes API-object references (LC/LPP/Node). Single worker;
	// the SOLE writer of ctl.policy.
	digestQueue *genctlr.KnowsProcessedSync[queueItem]

	// keyQueue processes per-NodeLauncherKey reconciliation. Started by
	// onDigestSyncProcessed once the initial batch has drained.
	keyQueue *genctlr.QueueAndWorkers[keyItem]

	// expectations tracks pending Pod create/delete mutations not yet reflected
	// in the informer's local cache. This prevents the controller from making
	// decisions based on stale cache state between reconcile cycles.
	expectations *pendingExpectations

	// policy holds the digested view of all LauncherPopulationPolicies,
	// LauncherConfigs, and their per-(node, LC) desired state.
	// It is the single source of truth for per-key reconciliation.
	// Written only by the digestQueue worker; read by keyQueue workers.
	policy *digestedPolicy

	// stuckSchedulingThreshold is the minimum age (since creation) at which an
	// unscheduled launcher is reported in the "stuck_scheduling" phase of the
	// fma_launcher_pod_count metric. It does not change reconcile behavior.
	stuckSchedulingThreshold time.Duration

	// stuckStartingThreshold is the minimum age (since scheduling) at which a
	// scheduled, not-yet-Ready launcher is reported in the "stuck_starting"
	// phase of the fma_launcher_pod_count metric. It does not change reconcile
	// behavior.
	stuckStartingThreshold time.Duration

	// clock is the time source for metric classification; real in production,
	// a fake in tests so time-dependent classification can be controlled.
	clock clock.Clock

	// metrics backs the fma_launcher_pod_count gauge; it is updated from
	// processKey (per-key launcher tallies) and from updateDigestForLC (whether a
	// LauncherConfig object exists).
	metrics *metricsState
}

var _ Controller = &controller{}

// queueItem is the work-queue element type for the digest queue. Concrete
// elements are funcItem (LPP/LC/Node references) plus sentinel funcItems
// (kind=kindSentinel) emitted by KnowsProcessedSync. processDigestItem
// dispatches on the dynamic type. The element type is a value-typed struct so
// the workqueue can compare/hash entries safely; closures must NOT be embedded
// because Go function values are not comparable and would panic at runtime
// when the workqueue deduplicates entries.
type queueItem interface{}

// resourceKind identifies the kind of cluster resource a funcItem refers to.
type resourceKind uint8

const (
	kindLPP resourceKind = iota
	kindLC
	kindNode
	kindSentinel // emitted by KnowsProcessedSync to mark end of initial batch
)

// funcItem is a digest-level update request, identified by (kind, name).
// Dispatch happens in processDigestItem.
type funcItem struct {
	Kind resourceKind
	Name string
}

// keyItem identifies a (node, LC) pair for per-key reconciliation.
type keyItem struct {
	NodeLauncherKey
}

// makeDigestSentinel produces a unique sentinel funcItem per worker. The
// distinguisher in the name keeps sentinels mutually distinct so they are not
// deduplicated by the workqueue.
func makeDigestSentinel(distinguisher int) queueItem {
	return funcItem{Kind: kindSentinel, Name: fmt.Sprintf("sentinel-%d", distinguisher)}
}

// isDigestSentinel reports whether item is a sentinel emitted by
// KnowsProcessedSync.
func isDigestSentinel(item queueItem) bool {
	f, ok := item.(funcItem)
	return ok && f.Kind == kindSentinel
}

// isLauncherPod returns true if the Pod is a launcher pod managed by this controller.
// It checks for the presence of the LauncherConfigNameLabelKey label, which is
// exclusively set by the controller when creating launcher Pods and is protected
// from external modification by a ValidatingAdmissionPolicy.
func isLauncherPod(pod *corev1.Pod) bool {
	_, exists := pod.Labels[common.LauncherConfigNameLabelKey]
	return exists
}

func (ctl *controller) OnAdd(obj any, isInInitialList bool) {
	switch typed := obj.(type) {
	case *corev1.Pod:
		if !isLauncherPod(typed) {
			ctl.enqueueLogger.V(5).Info("Ignored add of non-launcher Pod", "name", typed.Name)
			return
		}
		nodeName := typed.Labels[common.NodeNameLabelKey]
		lcName := typed.Labels[common.LauncherConfigNameLabelKey]
		ctl.enqueueLogger.V(5).Info("Enqueuing key due to launcher Pod add",
			"name", typed.Name, "uid", typed.UID, "resourceVersion", typed.ResourceVersion,
			"node", nodeName, "config", lcName)
		ctl.keyQueue.Queue.Add(keyItem{NodeLauncherKey{NodeName: nodeName, LauncherConfigName: lcName}})
	case *corev1.Node:
		ctl.enqueueLogger.V(5).Info("Enqueuing Node reference due to Node add", "name", typed.Name)
		ctl.digestQueue.Queue.Add(funcItem{Kind: kindNode, Name: typed.Name})
	case *fmav1alpha1.LauncherPopulationPolicy:
		ctl.enqueueLogger.V(5).Info("Enqueuing LauncherPopulationPolicy reference due to LPP add", "name", typed.Name)
		ctl.digestQueue.Queue.Add(funcItem{Kind: kindLPP, Name: typed.Name})
	case *fmav1alpha1.LauncherConfig:
		ctl.enqueueLogger.V(5).Info("Enqueuing LauncherConfig reference due to LC add", "name", typed.Name)
		ctl.digestQueue.Queue.Add(funcItem{Kind: kindLC, Name: typed.Name})
	default:
		ctl.enqueueLogger.V(5).Info("Notified of add of object of ignored type", "type", fmt.Sprintf("%T", obj))
		return
	}
}

func (ctl *controller) OnUpdate(prev, obj any) {
	switch typed := obj.(type) {
	case *corev1.Pod:
		if !isLauncherPod(typed) {
			ctl.enqueueLogger.V(5).Info("Ignored update of non-launcher Pod", "name", typed.Name)
			return
		}
		nodeName := typed.Labels[common.NodeNameLabelKey]
		lcName := typed.Labels[common.LauncherConfigNameLabelKey]
		prevRV := ""
		if prevPod, ok := prev.(*corev1.Pod); ok {
			prevRV = prevPod.ResourceVersion
		}
		ctl.enqueueLogger.V(5).Info("Enqueuing key due to launcher Pod update",
			"name", typed.Name, "uid", typed.UID,
			"prevResourceVersion", prevRV, "resourceVersion", typed.ResourceVersion,
			"node", nodeName, "config", lcName)
		ctl.keyQueue.Queue.Add(keyItem{NodeLauncherKey{NodeName: nodeName, LauncherConfigName: lcName}})
	case *corev1.Node:
		ctl.enqueueLogger.V(5).Info("Enqueuing Node reference due to Node update", "name", typed.Name)
		ctl.digestQueue.Queue.Add(funcItem{Kind: kindNode, Name: typed.Name})
	case *fmav1alpha1.LauncherPopulationPolicy:
		ctl.enqueueLogger.V(5).Info("Enqueuing LauncherPopulationPolicy reference due to LPP update", "name", typed.Name)
		ctl.digestQueue.Queue.Add(funcItem{Kind: kindLPP, Name: typed.Name})
	case *fmav1alpha1.LauncherConfig:
		ctl.enqueueLogger.V(5).Info("Enqueuing LauncherConfig reference due to LC update", "name", typed.Name)
		ctl.digestQueue.Queue.Add(funcItem{Kind: kindLC, Name: typed.Name})
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
	case *corev1.Pod:
		if !isLauncherPod(typed) {
			ctl.enqueueLogger.V(5).Info("Ignored delete of non-launcher Pod", "name", typed.Name)
			return
		}
		nodeName := typed.Labels[common.NodeNameLabelKey]
		lcName := typed.Labels[common.LauncherConfigNameLabelKey]
		ctl.enqueueLogger.V(5).Info("Enqueuing key due to launcher Pod delete",
			"name", typed.Name, "uid", typed.UID, "resourceVersion", typed.ResourceVersion,
			"node", nodeName, "config", lcName)
		ctl.keyQueue.Queue.Add(keyItem{NodeLauncherKey{NodeName: nodeName, LauncherConfigName: lcName}})
	case *corev1.Node:
		ctl.enqueueLogger.V(5).Info("Enqueuing Node reference due to Node delete", "name", typed.Name)
		ctl.digestQueue.Queue.Add(funcItem{Kind: kindNode, Name: typed.Name})
	case *fmav1alpha1.LauncherPopulationPolicy:
		ctl.enqueueLogger.V(5).Info("Enqueuing LauncherPopulationPolicy reference due to LPP delete", "name", typed.Name)
		ctl.digestQueue.Queue.Add(funcItem{Kind: kindLPP, Name: typed.Name})
	case *fmav1alpha1.LauncherConfig:
		ctl.enqueueLogger.V(5).Info("Enqueuing LauncherConfig reference due to LC delete", "name", typed.Name)
		ctl.digestQueue.Queue.Add(funcItem{Kind: kindLC, Name: typed.Name})
	default:
		ctl.enqueueLogger.V(5).Info("Notified of delete of object of ignored type", "type", fmt.Sprintf("%T", obj))
		return
	}
}

func (ctl *controller) Start(ctx context.Context) error {
	if !cache.WaitForNamedCacheSync(ControllerName, ctx.Done(), ctl.lppInformer.HasSynced, ctl.lcInformer.HasSynced, ctl.podInformer.HasSynced, ctl.nodeInformer.HasSynced) {
		return fmt.Errorf("caches not synced before end of Start context")
	}

	// Start digest workers. KnowsProcessedSync appends sentinels behind the
	// initial batch and invokes onDigestSyncProcessed when those sentinels are
	// drained, which is when keyQueue starts its workers.
	return ctl.digestQueue.StartWorkers(ctx)
}

// onDigestSyncProcessed is invoked by KnowsProcessedSync after the initial
// batch of digest items has been drained. It starts keyQueue workers, which
// process per-NodeLauncherKey reconciliation requests.
func (ctl *controller) onDigestSyncProcessed(ctx context.Context) {
	logger := klog.FromContext(ctx)
	logger.V(1).Info("Initial digest batch processed; starting key workers",
		"lpps", len(ctl.policy.lpps), "lcs", len(ctl.policy.lcs), "keys", ctl.keyQueue.Queue.Len())
	if err := ctl.keyQueue.StartWorkers(ctx); err != nil {
		logger.Error(err, "Failed to start key workers")
	}
}

// processDigestItem is the work function for the digest queue. Sentinels are
// intercepted by KnowsProcessedSync.earlySync and never reach this function.
// Acquires the policy write lock for the duration of the dispatch so all
// mutations within one updateDigestForX call appear atomic to keyQueue
// workers reading via snapshotForKey.
func (ctl *controller) processDigestItem(ctx context.Context, item queueItem) (error, bool) {
	f, ok := item.(funcItem)
	if !ok {
		return fmt.Errorf("unknown digest queue item type: %T", item), false
	}
	ctl.policy.mu.Lock()
	defer ctl.policy.mu.Unlock()
	var err error
	switch f.Kind {
	case kindLPP:
		err = ctl.updateDigestForLPP(ctx, f.Name)
	case kindLC:
		err = ctl.updateDigestForLC(ctx, f.Name)
	case kindNode:
		err = ctl.updateDigestForNode(ctx, f.Name)
	default:
		return fmt.Errorf("unknown funcItem kind: %d", f.Kind), false
	}
	if err != nil {
		return err, true
	}
	return nil, false
}

// processKeyItem is the work function for the key queue.
func (ctl *controller) processKeyItem(ctx context.Context, item keyItem) (error, bool) {
	return ctl.processKey(ctx, item.NodeLauncherKey)
}

// processKey reconciles launchers for a single NodeLauncherKey.
// It snapshots the desired state from the digest under RLock, then drops the
// lock before issuing K8s API calls so multiple workers can proceed in parallel.
func (ctl *controller) processKey(ctx context.Context, key NodeLauncherKey) (error, bool) {
	logger := klog.FromContext(ctx)

	snap := ctl.policy.snapshotForKey(key)

	// List the launcher Pods once and reuse the slice for both the metric and the
	// reconcile below. Recording here, before the branches that may return early,
	// keeps the metric up to date even when a Node is deleted or the key is handsOff.
	pods, err := ctl.listPodsFromCache(key)
	if err != nil {
		return fmt.Errorf("listing launcher Pods for node %s config %s: %w",
			key.NodeName, key.LauncherConfigName, err), true
	}
	ctl.recordLauncherPhases(key, pods, snap.templateHash)

	if !snap.exists {
		// Key not in digest: no policy wants this (node, LC) pair.
		// Clean up any orphaned launchers.
		logger.V(4).Info("Key not in digest, cleaning up orphaned launchers",
			"node", key.NodeName, "config", key.LauncherConfigName)
		return ctl.reconcileKey(ctx, key, 0, snap.templateHash, nil, pods)
	}

	if snap.desiredCount < 0 {
		// User error (LC missing or invalid template): take no action.
		logger.V(4).Info("Key is handsOff due to user error, skipping",
			"node", key.NodeName, "config", key.LauncherConfigName)
		return nil, false
	}

	return ctl.reconcileKey(ctx, key, snap.desiredCount, snap.templateHash, snap.nodeIndependentLauncherTemplate, pods)
}

// reconcileKey adjusts launcher pods for a single NodeLauncherKey to match the
// desired count. cachePods is the caller's launcher-Pod cache snapshot for the
// key, reused to avoid a second cache List.
func (ctl *controller) reconcileKey(ctx context.Context, key NodeLauncherKey, desiredCount int, templateHash string, nodeIndependentLauncherTemplate *corev1.Pod, cachePods []*corev1.Pod) (error, bool) {
	logger := klog.FromContext(ctx)

	// Check node existence.
	node, err := ctl.nodeLister.Get(key.NodeName)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.V(4).Info("Node no longer exists, skipping key reconciliation", "node", key.NodeName)
			return nil, false
		}
		return err, true
	}

	// Get current launchers and check expectations.
	currentLaunchers, expectStatus, err := ctl.getCurrentLaunchersOnNode(ctx, key, cachePods)
	if err != nil {
		logger.Error(err, "Failed to get current launchers", "node", key.NodeName, "config", key.LauncherConfigName)
		return err, true
	}
	if expectStatus == ExpectationsWaiting {
		return nil, true // requeue
	}

	// Categorize pods.
	var liveBoundCount int
	var liveUnboundCurrentPods []*corev1.Pod
	var staleUnboundPods []*corev1.Pod
	deletionInProgress := false

	for _, pod := range currentLaunchers {
		if pod.DeletionTimestamp != nil {
			deletionInProgress = true
			continue
		}
		isBound, _ := isLauncherBoundToServerRequestingPod(pod)
		if isBound {
			liveBoundCount++
			continue
		}
		if templateHash != pod.Annotations[common.LauncherTemplateHashAnnotationKey] {
			staleUnboundPods = append(staleUnboundPods, pod)
			continue
		}
		liveUnboundCurrentPods = append(liveUnboundCurrentPods, pod)
	}

	// Delete stale pods.
	didDelete := false
	staleNotDeleted := 0
	for _, pod := range staleUnboundPods {
		err := ctl.coreclient.Pods(pod.Namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{
			Preconditions: &metav1.Preconditions{UID: &pod.UID, ResourceVersion: &pod.ResourceVersion},
		})
		if err != nil {
			if apierrors.IsNotFound(err) || apierrors.IsGone(err) {
				ctl.expectations.expectDeletion(key, pod.UID)
				logger.Info("Stale launcher pod already deleted", "pod", pod.Name, "uid", pod.UID)
				continue
			}
			if apierrors.IsConflict(err) {
				staleNotDeleted++
				continue
			}
			return fmt.Errorf("failed to delete stale launcher pod %s: %w", pod.Name, err), true
		}
		ctl.expectations.expectDeletion(key, pod.UID)
		logger.Info("Deleted stale launcher pod", "pod", pod.Name, "uid", pod.UID, "node", key.NodeName)
		didDelete = true
	}

	// Calculate diff.
	effectiveRemaining := liveBoundCount + len(liveUnboundCurrentPods) + staleNotDeleted
	diff := desiredCount - effectiveRemaining

	logger.Info("Analyzed key",
		"node", key.NodeName, "config", key.LauncherConfigName,
		"current", effectiveRemaining, "stale", len(staleUnboundPods),
		"desired", desiredCount, "diff", diff)

	// Delete excess pods.
	if diff < 0 {
		numToDelete := -diff
		for i := len(liveUnboundCurrentPods) - 1; i >= 0 && numToDelete > 0; i-- {
			pod := liveUnboundCurrentPods[i]
			err := ctl.coreclient.Pods(pod.Namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{
				Preconditions: &metav1.Preconditions{UID: &pod.UID, ResourceVersion: &pod.ResourceVersion},
			})
			if err != nil {
				if apierrors.IsNotFound(err) || apierrors.IsGone(err) {
					ctl.expectations.expectDeletion(key, pod.UID)
					numToDelete--
					continue
				}
				if apierrors.IsConflict(err) {
					continue
				}
				return fmt.Errorf("failed to delete launcher pod %s: %w", pod.Name, err), true
			}
			ctl.expectations.expectDeletion(key, pod.UID)
			logger.Info("Deleted excess launcher pod", "pod", pod.Name, "uid", pod.UID, "node", key.NodeName)
			didDelete = true
			numToDelete--
		}
	}

	// If any deletions happened or are in progress, requeue before creating.
	if didDelete || deletionInProgress {
		return nil, true
	}

	// Create pods if needed.
	if diff > 0 {
		nodeSpecificLauncherTemplate := utils.SpecializeLauncherTemplateToNode(nodeIndependentLauncherTemplate, node.Name)
		if err := ctl.createLaunchers(ctx, node, key, diff, nodeSpecificLauncherTemplate); err != nil {
			return err, true
		}
		logger.Info("Created launchers", "node", key.NodeName, "config", key.LauncherConfigName, "count", diff)
	}

	return nil, false
}

// getCurrentLaunchersOnNode compares pending expectations for a specific
// config on a specific node against the caller's cache snapshot (cachePods), then
// returns one of:
//   - ExpectationsSatisfied with the cache snapshot, when no expectations are
//     pending (or all have been satisfied by the current snapshot);
//   - ExpectationsWaiting with a nil slice, indicating the caller should
//     requeue without acting on this key;
//   - ExpectationsTimedOut with the authoritative apiserver snapshot, after
//     resetting the (now stale) expectations.
func (ctl *controller) getCurrentLaunchersOnNode(ctx context.Context, key NodeLauncherKey, cachePods []*corev1.Pod) ([]*corev1.Pod, ExpectationStatus, error) {
	logger := klog.FromContext(ctx)

	presentUIDs := sets.New[types.UID]()
	for _, p := range cachePods {
		presentUIDs.Insert(p.UID)
	}

	status := ctl.expectations.check(key, presentUIDs)
	switch status {
	case ExpectationsSatisfied:
		// Fast path: informer cache is considered up-to-date.
		return cachePods, status, nil

	case ExpectationsWaiting:
		// Pending mutations not yet reflected; caller should requeue.
		logger.V(4).Info("Expectations not yet satisfied, requesting requeue",
			"node", key.NodeName, "config", key.LauncherConfigName)
		return nil, status, nil

	case ExpectationsTimedOut:
		// Timeout: fall back to direct apiserver query and reset expectations.
		logger.Info("Expectations timed out, falling back to apiserver query",
			"node", key.NodeName, "config", key.LauncherConfigName)
		ctl.expectations.reset(key)
		pods, err := ctl.listPodsFromApiserver(ctx, key)
		return pods, status, err
	}

	// unreachable
	return nil, ExpectationsSatisfied, nil
}
