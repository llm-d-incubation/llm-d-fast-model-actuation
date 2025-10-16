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
	"slices"
	"strings"
	"sync"
	"sync/atomic"

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

	"github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/api"
	genctlr "github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/controller/generic"
)

// This package implements the dual-pods controller.

// The controller works in the context of one Kubernetes API namespace.

// A Pod is a server-requesting Pod if it has the server patch annotation.
// A Pod is a bound server-running Pod if it has an annotation
// with the name "dual-pods.llm-d.ai/requester"; the annotations's value should
// be `requestingPod.UID + " " + requestingPod.Name`.
// A Pod is an unbound server-running Pod if it (1) is not bound and
// (2) has an annotation
// with name "dual-pods.llm-d.ai/nominal", whose value should be the base64 encoding
// of the SHA-256 hash of bytes that are characteristic of the nominal server-running Pod
// (including node, GPUs; excluding its name, this annotation, and the identity of the server-requesting Pod).
// This API object metadata is the hard state about binding.

// A bound server-running Pod normally has an awake inference server,
// with possible exceptions during startup, shutdown, binding, and unbinding.
// An unbound server-running Pod has an inference server that is sleeping.

// The controller includes its finalizer when creating a bound server-running Pod,
// and removes it when unbinding or recognizing the exogenous deletion of a server-running Pod.

// At this interim stage of development, the controller does not request
// deletion of any server-running Pod. Nor does the controller ever try to bind
// one that is unbound; they are only created in the bound state.

// There are two types of item in the controller's work queue.
// One is a reference to the gpu-map ConfigMap.

// The other type of queue item is a reference to an inference server.
// This reference carries the inference server's UID and the name
// of the server-requesting Pod.
// An inference server's UID is the UID of the server-requesting Pod.

const requesterAnnotationKey = "dual-pods.llm-d.ai/requester"
const nominalHashAnnotationKey = "dual-pods.llm-d.ai/nominal"

const runnerFinalizer = "dual-pods.llm-d.ai/runner"
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
	isIdx, _, err := getInferenceServerPort(pod)
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
	SleeperLimit int
	NumWorkers   int
}

type Controller interface {
	Start(context.Context) error
}

// NewController makes a new dual pods controller.
// The given namespace is the one to focus on.
func (config ControllerConfig) NewController(
	logger klog.Logger,
	coreClient coreclient.CoreV1Interface,
	namespace string,
	corev1PreInformers corev1preinformers.Interface,
) (*controller, error) {
	ctl := &controller{
		enqueueLogger:  logger.WithName(ControllerName),
		coreclient:     coreClient,
		namespace:      namespace,
		podInformer:    corev1PreInformers.Pods().Informer(),
		podLister:      corev1PreInformers.Pods().Lister(),
		cmInformer:     corev1PreInformers.ConfigMaps().Informer(),
		cmLister:       corev1PreInformers.ConfigMaps().Lister(),
		nodeInformer:   corev1PreInformers.Nodes().Informer(),
		nodeLister:     corev1PreInformers.Nodes().Lister(),
		sleeperLimit:   config.SleeperLimit,
		nodeNameToData: map[string]*nodeData{},
	}
	ctl.gpuMap.Store(&map[string]GpuLocation{})
	err := ctl.podInformer.AddIndexers(cache.Indexers{
		requesterIndexName:   requesterIndexFunc,
		nominalHashIndexName: nominalHashIndexFunc,
		GPUIndexName:         GPUIndexFunc})
	if err != nil { //impossible
		return nil, err
	}
	ctl.QueueAndWorkers = genctlr.NewQueueAndWorkers(string(ControllerName), config.NumWorkers, ctl.process)
	_, err = ctl.podInformer.AddEventHandler(ctl)
	if err != nil {
		panic(err)
	}
	_, err = ctl.cmInformer.AddEventHandler(ctl)
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
	genctlr.QueueAndWorkers[queueItem]

	sleeperLimit int

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
	// inferenceServers maps UID of serve-requesting Pod to data.
	// Access only while holding controller mutex.
	InferenceServers map[apitypes.UID]*serverData

	// ItemsMutex may be acquired while holding controller mutex, not vice-versa.
	ItemsMutex sync.Mutex

	Items sets.Set[itemOnNode]
}

type itemOnNode interface {
	// process returns (err error, retry bool).
	// There will be a retry iff `retry || err != nil`.
	process(ctx context.Context, ctl *controller, nodeDat *nodeData) (error, bool)
}

// Internal state about an inference server
type serverData struct {
	RequestingPodName     string
	NominalRunningPod     *corev1.Pod
	NominalRunningPodHash string

	// ServerPort is meaningful if NominalRunningPod is not nil
	ServerPort int16

	GPUIndices    []string
	GPUIndicesStr *string

	ReadinessRelayed *bool

	Sleeping *bool

	// RequesterDeleteRequested carries this bit forward without waiting for notification
	// from apiserver. Remember there is no sync between the notification streams for
	// different objects.
	RequesterDeleteRequested bool
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

// careAbout returns infSvrItem, podIsRequester, have.
// Returns have=true for both requesters and bound runners,
// have=false for unbound runners and other Pods.
func careAbout(pod *corev1.Pod) (infSvrItem, bool, bool) {
	if len(pod.Annotations[api.ServerPatchAnnotationName]) > 0 {
		return infSvrItem{pod.UID, pod.Name}, true, true
	}
	requesterStr := pod.Annotations[requesterAnnotationKey]
	requesterParts := strings.Split(requesterStr, " ")
	if len(requesterParts) != 2 {
		return infSvrItem{}, false, false
	}
	return infSvrItem{apitypes.UID(requesterParts[0]), requesterParts[1]}, false, true
}

const requesterIndexName = "requester"

func requesterIndexFunc(obj any) ([]string, error) {
	pod := obj.(*corev1.Pod)
	item, isReq, have := careAbout(pod)
	if have && !isReq {
		return []string{string(item.UID)}, nil
	}
	return []string{}, nil
}

func (ctl *controller) OnAdd(obj any, isInInitialList bool) {
	switch typed := obj.(type) {
	case *corev1.Pod:
		if item, isReq, owned := careAbout(typed); !owned {
			ctl.enqueueLogger.V(5).Info("Ignoring add of irrelevant Pod", "name", typed.Name)
			return
		} else {
			nodeName := typed.Spec.NodeName
			if !isReq {
				var err error
				nodeName, err = getRunnerNodeName(typed)
				if err != nil {
					ctl.enqueueLogger.Error(err, "Failed to determine node of runner")
					return
				}
			} else if nodeName == "" {
				ctl.enqueueLogger.V(5).Info("Ignoring add of non-scheduled server-requesting Pod", "name", typed.Name)
				return
			}
			nd := ctl.getNodeData(nodeName)
			ctl.enqueueLogger.V(5).Info("Enqueuing inference server reference due to notification of add", "nodeName", nodeName, "item", item, "isReq", isReq, "isInInitialList", isInInitialList, "resourceVersion", typed.ResourceVersion)
			nd.add(item)
			ctl.Queue.Add(nodeItem{nodeName})
		}
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
		if item, isReq, owned := careAbout(typed); !owned {
			ctl.enqueueLogger.V(5).Info("Ignoring update of irrelevant Pod", "name", typed.Name)
			return
		} else {
			nodeName := typed.Spec.NodeName
			if !isReq {
				var err error
				nodeName, err = getRunnerNodeName(typed)
				if err != nil {
					ctl.enqueueLogger.Error(err, "Failed to determine node of runner")
					return
				}
			} else if nodeName == "" {
				ctl.enqueueLogger.V(5).Info("Ignoring update of non-scheduled server-requesting Pod", "name", typed.Name)
				return
			}
			nd := ctl.getNodeData(nodeName)
			ctl.enqueueLogger.V(5).Info("Enqueuing inference server reference due to notification of update", "nodeName", nodeName, "item", item, "isReq", isReq, "resourceVersion", typed.ResourceVersion)
			nd.add(item)
			ctl.Queue.Add(nodeItem{nodeName})
		}
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
		if item, isReq, owned := careAbout(typed); !owned {
			ctl.enqueueLogger.V(5).Info("Ignoring delete of irrelevant Pod", "name", typed.Name)
			return
		} else {
			nodeName := typed.Spec.NodeName
			if !isReq {
				var err error
				nodeName, err = getRunnerNodeName(typed)
				if err != nil {
					ctl.enqueueLogger.Error(err, "Failed to determine node of runner")
					return
				}
			} else if nodeName == "" {
				ctl.enqueueLogger.V(5).Info("Ignoring delete of non-scheduled server-requesting Pod", "name", typed.Name)
				return
			}
			nd := ctl.getNodeData(nodeName)
			ctl.enqueueLogger.V(5).Info("Enqueuing inference server reference due to notification of delete", "nodeName", nodeName, "item", item, "isReq", isReq, "resourceVersion", typed.ResourceVersion)
			nd.add(item)
			ctl.Queue.Add(nodeItem{nodeName})
		}
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

func getRunnerNodeName(pod *corev1.Pod) (string, error) {
	nn := pod.Spec.NodeSelector["kubernetes.io/hostname"]
	if nn != "" {
		return nn, nil
	}
	return "", errors.New("no kubernetes.io/hostname test in nodeSelector")
}

func (ctl *controller) Start(ctx context.Context) error {
	if !cache.WaitForNamedCacheSync(ControllerName, ctx.Done(), ctl.cmInformer.HasSynced, ctl.podInformer.HasSynced, ctl.nodeInformer.HasSynced) {
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
