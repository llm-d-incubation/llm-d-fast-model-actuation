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

package dualpods

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/controller/utils"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apitypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	corev1preinformers "k8s.io/client-go/informers/core/v1"
	coreclient "k8s.io/client-go/kubernetes/typed/core/v1"
	corev1listers "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	fmav1alpha1 "github.com/llm-d-incubation/llm-d-fast-model-actuation/api/fma/v1alpha1"
	"github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/api"
	genctlr "github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/controller/generic"
	fmainformers "github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/generated/informers/externalversions"
	fmalisters "github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/generated/listers/fma/v1alpha1"
)

// This package implements the dual-pods controller.

// The controller works in the context of one Kubernetes API namespace.

// A Pod is a server-requesting Pod if it has the server patch annotation.
// A Pod is a bound server-providing Pod if it has an annotation
// with the name "dual-pods.llm-d.ai/requester"; the annotation's value should
// be `requestingPod.UID + " " + requestingPod.Name`.
// A Pod is an unbound server-providing Pod if it (1) is not bound and
// (2) has an annotation
// with name "dual-pods.llm-d.ai/nominal", whose value should be the base64 encoding
// of the SHA-256 hash of bytes that are characteristic of the nominal server-providing Pod
// (including node, GPUs; excluding its name, this annotation, and the identity of the server-requesting Pod).
// This API object metadata is the hard state about binding.

// A bound server-providing Pod normally has an awake inference server,
// with possible exceptions during startup, shutdown, binding, and unbinding.
// An unbound server-providing Pod has an inference server that is sleeping.

// The controller includes its finalizer when creating a bound server-providing Pod,
// and removes it when unbinding or recognizing the exogenous deletion of a server-providing Pod.

// At this interim stage of development, the controller does not request
// deletion of any server-providing Pod. Nor does the controller ever try to bind
// one that is unbound; they are only created in the bound state.

// There are two types of item in the controller's work queue.
// One is a reference to the gpu-map ConfigMap.

// The other type of queue item is a reference to an inference server.
// This reference carries the inference server's UID and the name
// of the server-requesting Pod.
// An inference server's UID is the UID of the server-requesting Pod.

const requesterAnnotationKey = "dual-pods.llm-d.ai/requester"
const nominalHashAnnotationKey = "dual-pods.llm-d.ai/nominal"

const providerFinalizer = "dual-pods.llm-d.ai/provider"
const requesterFinalizer = "dual-pods.llm-d.ai/requester"

const ControllerName = "dual-pods-controller"

// GPUMapName is the name of the ConfigMap(s) parsed to discover the mapping from GPU UUID to location.
// Namespace is the focus namespace.
// Every data item in the ConfigMap is expected to have a name that is the name of a Node
// and a value that is JSON for a map from UUID to index.
const GPUMapName = "gpu-map"

const GPUIndexName = "gpu"

func GPUIndexFunc(obj any) ([]string, error) {
	pod := obj.(*corev1.Pod)
	if len(pod.Annotations[nominalHashAnnotationKey]) == 0 || pod.Spec.NodeName == "" {
		return []string{}, nil
	}
	isIdx, _, err := utils.GetInferenceServerPort(pod)
	if err != nil {
		return []string{}, nil
	}
	isCtr := &pod.Spec.Containers[isIdx]
	eIdx := slices.IndexFunc(isCtr.Env, func(e corev1.EnvVar) bool {
		return e.Name == "CUDA_VISIBLE_DEVICES"
	})
	if eIdx < 0 || len(isCtr.Env[eIdx].Value) == 0 {
		return []string{}, nil
	}
	visibleParts := strings.Split(isCtr.Env[eIdx].Value, ",")
	keys, _ := SliceMap(visibleParts, func(gpu string) (string, error) {
		return pod.Spec.NodeName + " " + strings.Trim(gpu, " "), nil
	})
	return keys, nil
}

const nominalHashIndexName = "nominal"

func nominalHashIndexFunc(obj any) ([]string, error) {
	pod := obj.(*corev1.Pod)
	nominalHash := pod.Annotations[nominalHashAnnotationKey]
	if len(nominalHash) == 0 {
		return []string{}, nil
	}
	return []string{nominalHash}, nil
}

type ControllerConfig struct {
	SleeperLimit                      int
	NumWorkers                        int
	AcceleratorSleepingMemoryLimitMiB int64
}

type Controller interface {
	Start(context.Context) error
}

const sentinelNS = "Senti Nel"

// NewController makes a new dual pods controller.
// The given namespace is the one to focus on.
func (config ControllerConfig) NewController(
	logger klog.Logger,
	coreClient coreclient.CoreV1Interface,
	namespace string,
	corev1PreInformers corev1preinformers.Interface,
	fmaInformerFactory fmainformers.SharedInformerFactory,
) (*controller, error) {
	ctl := &controller{
		enqueueLogger:       logger.WithName(ControllerName),
		coreclient:          coreClient,
		namespace:           namespace,
		podInformer:         corev1PreInformers.Pods().Informer(),
		podLister:           corev1PreInformers.Pods().Lister(),
		cmInformer:          corev1PreInformers.ConfigMaps().Informer(),
		cmLister:            corev1PreInformers.ConfigMaps().Lister(),
		nodeInformer:        corev1PreInformers.Nodes().Informer(),
		nodeLister:          corev1PreInformers.Nodes().Lister(),
		iscInformer:         fmaInformerFactory.Fma().V1alpha1().InferenceServerConfigs().Informer(),
		iscLister:           fmaInformerFactory.Fma().V1alpha1().InferenceServerConfigs().Lister(),
		lcInformer:          fmaInformerFactory.Fma().V1alpha1().LauncherConfigs().Informer(),
		lcLister:            fmaInformerFactory.Fma().V1alpha1().LauncherConfigs().Lister(),
		sleeperLimit:        config.SleeperLimit,
		debugAccelMemory:    config.AcceleratorSleepingMemoryLimitMiB < math.MaxInt32,
		accelMemoryLimitMiB: config.AcceleratorSleepingMemoryLimitMiB,
		nodeNameToData:      map[string]*nodeData{},
	}
	ctl.gpuMap.Store(&map[string]GpuLocation{})
	err := ctl.podInformer.AddIndexers(cache.Indexers{
		inferenceServerConfigIndexName: inferenceServerConfigIndexFunc,
		requesterIndexName:             requesterIndexFunc,
		nominalHashIndexName:           nominalHashIndexFunc,
		GPUIndexName:                   GPUIndexFunc})
	if err != nil { //impossible
		return nil, err
	}
	ctl.KnowsProcessedSync = genctlr.NewKnowsProcessedSync(ControllerName, config.NumWorkers, ctl.process,
		func(distinguisher int) queueItem {
			return cmItem{ObjectName: cache.ObjectName{Name: strconv.FormatInt(int64(distinguisher), 10), Namespace: sentinelNS}}
		},
		func(qi queueItem) bool {
			if cm, ok := qi.(cmItem); ok {
				return cm.Namespace == sentinelNS
			}
			return false
		},
		func(ctx context.Context) {
			logger := klog.FromContext(ctx)
			logger.V(1).Info("All initial items processed")
		})

	_, err = ctl.podInformer.AddEventHandler(ctl)
	if err != nil {
		panic(err)
	}
	_, err = ctl.cmInformer.AddEventHandler(ctl)
	if err != nil {
		panic(err)
	}
	_, err = ctl.iscInformer.AddEventHandler(ctl)
	if err != nil {
		panic(err)
	}
	_, err = ctl.lcInformer.AddEventHandler(ctl)
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
	cmInformer    cache.SharedIndexInformer
	cmLister      corev1listers.ConfigMapLister
	nodeInformer  cache.SharedIndexInformer
	nodeLister    corev1listers.NodeLister
	iscInformer   cache.SharedIndexInformer
	iscLister     fmalisters.InferenceServerConfigLister
	lcInformer    cache.SharedIndexInformer
	lcLister      fmalisters.LauncherConfigLister
	genctlr.KnowsProcessedSync[queueItem]

	sleeperLimit        int
	debugAccelMemory    bool
	accelMemoryLimitMiB int64

	// gpuMaps maps GPU UUID to GpuLocation
	gpuMap atomic.Pointer[map[string]GpuLocation]

	mutex sync.Mutex

	nodeNameToData map[string]*nodeData
}

var _ Controller = &controller{}

type GpuLocation struct {
	Node  string
	Index uint
}

type nodeData struct {
	// InferenceServers maps UID of serve-requesting Pod to data.
	// Access only while holding controller mutex.
	InferenceServers map[apitypes.UID]*serverData

	// Launchers maps name of launcher-based server-providing Pod to launcherData.
	// Access only while holding controller mutex.
	Launchers map[string]*launcherData

	// ItemsMutex may be acquired while holding controller mutex, not vice-versa.
	ItemsMutex sync.Mutex

	// Items is the object references of this node that need to be synced.
	// Hold ItemsMutex while accessing this.
	Items sets.Set[itemOnNode]
}

type itemOnNode interface {
	// process returns (err error, retry bool).
	// There will be a retry iff `retry`.
	process(ctx context.Context, ctl *controller, nodeDat *nodeData) (error, bool)
}

// Internal state about an inference server
type serverData struct {
	RequestingPodName       string
	NominalProvidingPod     *corev1.Pod
	NominalProvidingPodHash string

	// ServerPort is meaningful if NominalProvidingPod is not nil
	ServerPort int16

	// UUIDs of the server's GPUs
	GPUIDs []string

	// Comma-separated list of GPU UUIDs
	GPUIDsStr *string

	GPUIndices    []string
	GPUIndicesStr *string

	ProvidingPodName string

	ReadinessRelayed *bool

	Sleeping *bool

	// RequesterDeleteRequested carries this bit forward without waiting for notification
	// from apiserver. Remember there is no sync between the notification streams for
	// different objects.
	RequesterDeleteRequested bool
}

// nolint
type launcherData struct {
	// Instances is a map,
	// where key is an instance's ID which is the instance' nominal hash,
	// and value is the last used time of the instance.
	Instances map[string]time.Time

	// Accurate indicates whether the set of nominal hash in Instances is accurate.
	Accurate bool
}

type queueItem interface {
	// process returns (err error, retry bool).
	// There will be a retry iff `retry`, error logged if `err != nil`.
	process(ctx context.Context, ctl *controller) (error, bool)
}

type cmItem struct {
	cache.ObjectName
}

type infSvrItem struct {
	UID apitypes.UID
	// RequesterName is the name of the Pod that had this UID
	RequesterName string
}

type infSvrItemType string

const (
	// infSvrItemRequester is for a server-requesting Pod.
	infSvrItemRequester infSvrItemType = "requester"
	// infSvrItemBoundDirectProvider is for a server-providing Pod that
	// is 'direct' (i.e. not launcher-based), and bound to a server-requesting Pod.
	infSvrItemBoundDirectProvider infSvrItemType = "bound_direct_provider"
	// infSvrItemLauncherBasedProvider is for a server-providing Pod that is launcher-based.
	infSvrItemLauncherBasedProvider infSvrItemType = "launcher_based_provider"
	// infSvrItemDontCare is not a real infSvrItemType but only a placeholder
	// saying the corresponding infSvrItem is not relevant to the controller.
	infSvrItemDontCare infSvrItemType = "dont_care"
)

// careAbout returns an infSvrItem and an infSvrItemType.
// The controller cares about server-requesting Pods, bound direct server-providing Pods, and launcher-based server-providing Pods.
// The controller doesn't care about unbound direct providers and other Pods.
func careAbout(pod *corev1.Pod) (item infSvrItem, it infSvrItemType) {
	if len(pod.Annotations[api.ServerPatchAnnotationName]) > 0 || len(pod.Annotations[api.InferenceServerConfigAnnotationName]) > 0 {
		return infSvrItem{pod.UID, pod.Name}, infSvrItemRequester
	}
	requesterStr := pod.Annotations[requesterAnnotationKey]
	requesterParts := strings.Split(requesterStr, " ")
	if len(requesterParts) != 2 {
		return infSvrItem{}, infSvrItemDontCare
	}
	return infSvrItem{apitypes.UID(requesterParts[0]), requesterParts[1]}, infSvrItemBoundDirectProvider
}

const inferenceServerConfigIndexName = "inferenceserverconfig"

func inferenceServerConfigIndexFunc(obj any) ([]string, error) {
	pod := obj.(*corev1.Pod)
	inferenceServerConfigName := pod.Annotations[api.InferenceServerConfigAnnotationName]
	if len(inferenceServerConfigName) == 0 {
		return []string{}, nil
	}
	return []string{inferenceServerConfigName}, nil
}

const requesterIndexName = "requester"

func requesterIndexFunc(obj any) ([]string, error) {
	pod := obj.(*corev1.Pod)
	item, it := careAbout(pod)
	if it == infSvrItemBoundDirectProvider {
		return []string{string(item.UID)}, nil
	}
	return []string{}, nil
}

func (ctl *controller) OnAdd(obj any, isInInitialList bool) {
	switch typed := obj.(type) {
	case *corev1.Pod:
		if item, it := careAbout(typed); it == infSvrItemDontCare {
			ctl.enqueueLogger.V(5).Info("Ignoring add of irrelevant Pod", "name", typed.Name)
			return
		} else {
			nodeName := typed.Spec.NodeName
			if it == infSvrItemBoundDirectProvider || it == infSvrItemLauncherBasedProvider {
				var err error
				nodeName, err = getProviderNodeName(typed)
				if err != nil {
					ctl.enqueueLogger.Error(err, "Failed to determine node of provider")
					return
				}
			} else if it == infSvrItemRequester && nodeName == "" {
				ctl.enqueueLogger.V(5).Info("Ignoring add of non-scheduled server-requesting Pod", "name", typed.Name)
				return
			}
			nd := ctl.getNodeData(nodeName)
			ctl.enqueueLogger.V(5).Info("Enqueuing inference server reference due to notification of add", "nodeName", nodeName, "item", item, "infSvrItemType", it, "isInInitialList", isInInitialList, "resourceVersion", typed.ResourceVersion)
			nd.add(item)
			ctl.Queue.Add(nodeItem{nodeName})
		}
	case *fmav1alpha1.InferenceServerConfig:
		ctl.enqueueRequestersByInferenceServerConfig(typed, isInInitialList)
	case *corev1.ConfigMap:
		if typed.Name != GPUMapName {
			ctl.enqueueLogger.V(5).Info("Ignoring ConfigMap that is not the GPU map", "ref", cache.MetaObjectToName(typed))
			return
		} else {
			item := cmItem{cache.MetaObjectToName(typed)}
			ctl.enqueueLogger.V(5).Info("Enqueuing ConfigMap reference due to notification of add", "item", item, "isInInitialList", isInInitialList, "resourceVersion", typed.ResourceVersion)
			ctl.Queue.Add(item)
		}
	default:
		ctl.enqueueLogger.Error(nil, "Notified of add of unexpected type of object", "type", fmt.Sprintf("%T", obj))
		return
	}
}

func (ctl *controller) OnUpdate(prev, obj any) {
	switch typed := obj.(type) {
	case *corev1.Pod:
		if item, it := careAbout(typed); it == infSvrItemDontCare {
			ctl.enqueueLogger.V(5).Info("Ignoring update of irrelevant Pod", "name", typed.Name)
			return
		} else {
			nodeName := typed.Spec.NodeName
			if it == infSvrItemBoundDirectProvider || it == infSvrItemLauncherBasedProvider {
				var err error
				nodeName, err = getProviderNodeName(typed)
				if err != nil {
					ctl.enqueueLogger.Error(err, "Failed to determine node of provider")
					return
				}
			} else if it == infSvrItemRequester && nodeName == "" {
				ctl.enqueueLogger.V(5).Info("Ignoring update of non-scheduled server-requesting Pod", "name", typed.Name)
				return
			}
			nd := ctl.getNodeData(nodeName)
			ctl.enqueueLogger.V(5).Info("Enqueuing inference server reference due to notification of update", "nodeName", nodeName, "item", item, "infSvrItemType", it, "resourceVersion", typed.ResourceVersion)
			nd.add(item)
			ctl.Queue.Add(nodeItem{nodeName})
		}
	case *fmav1alpha1.InferenceServerConfig:
		ctl.enqueueRequestersByInferenceServerConfig(typed, false)
	case *corev1.ConfigMap:
		if typed.Name != GPUMapName {
			ctl.enqueueLogger.V(5).Info("Ignoring ConfigMap that is not the GPU map", "ref", cache.MetaObjectToName(typed))
			return
		} else {
			item := cmItem{cache.MetaObjectToName(typed)}
			ctl.enqueueLogger.V(5).Info("Enqueuing ConfigMap reference due to notification of update", "item", item, "resourceVersion", typed.ResourceVersion)
			ctl.Queue.Add(item)
		}
	default:
		ctl.enqueueLogger.Error(nil, "Notified of update of unexpected type of object", "type", fmt.Sprintf("%T", obj))
		return
	}
}

func (ctl *controller) OnDelete(obj any) {
	if dfsu, ok := obj.(cache.DeletedFinalStateUnknown); ok {
		obj = dfsu.Obj
	}
	switch typed := obj.(type) {
	case *corev1.Pod:
		if item, it := careAbout(typed); it == infSvrItemDontCare {
			ctl.enqueueLogger.V(5).Info("Ignoring delete of irrelevant Pod", "name", typed.Name)
			return
		} else {
			nodeName := typed.Spec.NodeName
			if it == infSvrItemBoundDirectProvider || it == infSvrItemLauncherBasedProvider {
				var err error
				nodeName, err = getProviderNodeName(typed)
				if err != nil {
					ctl.enqueueLogger.Error(err, "Failed to determine node of provider")
					return
				}
			} else if it == infSvrItemRequester && nodeName == "" {
				ctl.enqueueLogger.V(5).Info("Ignoring delete of non-scheduled server-requesting Pod", "name", typed.Name)
				return
			}
			nd := ctl.getNodeData(nodeName)
			ctl.enqueueLogger.V(5).Info("Enqueuing inference server reference due to notification of delete", "nodeName", nodeName, "item", item, "infSvrItemType", it, "resourceVersion", typed.ResourceVersion)
			nd.add(item)
			ctl.Queue.Add(nodeItem{nodeName})
		}
	case *fmav1alpha1.InferenceServerConfig:
		ctl.enqueueRequestersByInferenceServerConfig(typed, false)
	case *corev1.ConfigMap:
		if typed.Name != GPUMapName {
			ctl.enqueueLogger.V(5).Info("Ignoring ConfigMap that is not the GPU map", "ref", cache.MetaObjectToName(typed))
			return
		} else {
			item := cmItem{cache.MetaObjectToName(typed)}
			ctl.enqueueLogger.V(5).Info("Enqueuing ConfigMap reference due to notification of delete", "item", item, "resourceVersion", typed.ResourceVersion)
			ctl.Queue.Add(item)
		}
	default:
		ctl.enqueueLogger.Error(nil, "Notified of delete of unexpected type of object", "type", fmt.Sprintf("%T", obj))
		return
	}
}

func getProviderNodeName(pod *corev1.Pod) (string, error) {
	nn := pod.Spec.NodeSelector["kubernetes.io/hostname"]
	if nn != "" {
		return nn, nil
	}
	return "", errors.New("no kubernetes.io/hostname test in nodeSelector")
}

func (ctl *controller) Start(ctx context.Context) error {
	if !cache.WaitForNamedCacheSync(ControllerName, ctx.Done(), ctl.cmInformer.HasSynced, ctl.podInformer.HasSynced, ctl.nodeInformer.HasSynced, ctl.iscInformer.HasSynced, ctl.lcInformer.HasSynced) {
		return fmt.Errorf("caches not synced before end of Start context")
	}
	err := ctl.StartWorkers(ctx)
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

func (item cmItem) process(ctx context.Context, ctl *controller) (error, bool) {
	logger := klog.FromContext(ctx)
	cm, err := ctl.coreclient.ConfigMaps(item.Namespace).Get(ctx, item.Name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			ctl.gpuMap.Store(nil)
			return err, false
		}
		return err, true
	}
	oldMap := ctl.gpuMap.Load()
	newMap := map[string]GpuLocation{}
	nodeCount := 0
	additions := 0
	for nodeName, mapStr := range cm.Data {
		var newNodesMap map[string]uint
		err = json.Unmarshal([]byte(mapStr), &newNodesMap)
		if err != nil {
			logger.Error(err, "A GPU map entry failed to parse as JSON", "nodeName", nodeName)
			continue
		}
		for uuid, index := range newNodesMap {
			newLoc := GpuLocation{Node: nodeName, Index: index}
			if oldMap == nil || (*oldMap)[uuid] != newLoc {
				additions++
			}
			newMap[uuid] = newLoc
		}
		nodeCount += 1
	}
	logger.V(1).Info("Parsed GPU map", "numNodes", nodeCount, "numGPUs", len(newMap), "additions", additions)
	ctl.gpuMap.Store(&newMap)
	if additions > 0 {
		ctl.enqueueRequesters(ctx)
	}
	return nil, false
}

func (ctl *controller) enqueueRequesters(ctx context.Context) {
	ctl.mutex.Lock()
	defer ctl.mutex.Unlock()
	logger := klog.FromContext(ctx)
	for nodeName, nodeDat := range ctl.nodeNameToData {
		var some bool
		for infSvrUID, serverDat := range nodeDat.InferenceServers {
			item := infSvrItem{infSvrUID, serverDat.RequestingPodName}
			logger.V(5).Info("Enqueuing inference server because of change to GPU map", "node", nodeName, "item", item)
			nodeDat.add(item)
			some = true
		}
		if some {
			ctl.Queue.Add(nodeItem{nodeName})
		}
	}
}

func (ctl *controller) enqueueRequestersByInferenceServerConfig(isc *fmav1alpha1.InferenceServerConfig, isInInitialList bool) {
	inferenceServerConfigName := isc.Name
	requesters, err := ctl.podInformer.GetIndexer().ByIndex(inferenceServerConfigIndexName, inferenceServerConfigName)
	if err != nil {
		ctl.enqueueLogger.Error(err, "Failed to get server requesting pods that use InferenceServerConfig", "ref", cache.MetaObjectToName(isc))
		return
	}
	nodeNames := sets.New[string]()
	for _, podObj := range requesters {
		pod := podObj.(*corev1.Pod)
		item, it := careAbout(pod)
		if it != infSvrItemRequester {
			// should not happen because of the nature of inferenceServerConfigIndexFunc
			ctl.enqueueLogger.V(5).Info("Ignoring Pod that is not a server-requesting Pod", "pod", pod.Name)
			continue
		}
		nodeName := pod.Spec.NodeName
		if nodeName == "" {
			ctl.enqueueLogger.V(5).Info("Ignoring non-scheduled server-requesting Pod that uses InferenceServerConfig", "pod", pod.Name, "inferenceServerConfigName", inferenceServerConfigName)
			continue
		}
		nd := ctl.getNodeData(nodeName)
		ctl.enqueueLogger.V(5).Info("Enqueuing inference server reference due to notification of InferenceServerConfig", "nodeName", nodeName, "item", item, "infSvrItemType", it, "inferenceServerConfigName", inferenceServerConfigName, "isInInitialList", isInInitialList, "resourceVersion", isc.ResourceVersion)
		nd.add(item)
		nodeNames.Insert(nodeName)
	}
	for nodeName := range nodeNames {
		ctl.Queue.Add(nodeItem{nodeName})
	}
}

func (ctl *controller) getNodeData(nodeName string) *nodeData {
	ctl.mutex.Lock()
	defer ctl.mutex.Unlock()
	ans := ctl.nodeNameToData[nodeName]
	if ans == nil {
		ans = &nodeData{
			Items:            sets.New[itemOnNode](),
			InferenceServers: make(map[apitypes.UID]*serverData),
		}
		ctl.nodeNameToData[nodeName] = ans
	}
	return ans
}

func (nodeDat *nodeData) add(item itemOnNode) {
	nodeDat.ItemsMutex.Lock()
	defer nodeDat.ItemsMutex.Unlock()
	nodeDat.Items.Insert(item)
}

// yankItems returns the currently queued items and empties the queue.
// Caller can access the returned value without synchronization.
func (nodeDat *nodeData) yankItems() sets.Set[itemOnNode] {
	nodeDat.ItemsMutex.Lock()
	defer nodeDat.ItemsMutex.Unlock()
	ans := nodeDat.Items
	nodeDat.Items = sets.New[itemOnNode]()
	return ans
}

func (ctl *controller) getServerData(nodeDat *nodeData, reqName string, reqUID apitypes.UID) *serverData {
	ctl.mutex.Lock()
	defer ctl.mutex.Unlock()
	ans := nodeDat.InferenceServers[reqUID]
	if ans == nil {
		ans = &serverData{RequestingPodName: reqName}
		nodeDat.InferenceServers[reqUID] = ans
	}
	return ans
}

func (ctl *controller) clearServerData(nodeDat *nodeData, uid apitypes.UID) {
	ctl.mutex.Lock()
	defer ctl.mutex.Unlock()
	delete(nodeDat.InferenceServers, uid)
}
