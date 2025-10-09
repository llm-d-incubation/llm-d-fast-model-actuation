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
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/spf13/pflag"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apitypes "k8s.io/apimachinery/pkg/types"
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
// A Pod is a server-running Pod if has a controlling OwnerReference to
// a Pod (the server-requesting Pod).

// There are two types of item in the controller's work queue.
// One is a reference to the gpu-map ConfigMap.

// The other type of queue item is a reference to an inference server.
// This reference carries the inference server's UID and the name
// of the server-requesting Pod.
// An inference server's UID is the UID of the server-requesting Pod.

const ControllerName = "dual-pods-controller"

// GPUMapName is the name of the ConfigMap(s) parsed to discover the mapping from GPU UUID to location.
// Namespace is the focus namespace.
// Every data item in the ConfigMap is expected to have a name that is the name of a Node
// and a value that is JSON for a map from UUID to index.
const GPUMapName = "gpu-map"

const runnerFinalizer = "dual-pods.llm-d.ai/runner"

type Controller interface {
	Start(context.Context) error
}

type CommonConfig struct {
	Verbosity int // `-v`
}

func (cc *CommonConfig) AddToFlagSet(name string, flags *pflag.FlagSet) {
	flags.IntVar(&cc.Verbosity, name+"-verbosity", cc.Verbosity, "-v setting for "+name)
}

// NewController makes a new dual pods controller.
// The given namespace is the one to focus on.
func NewController(
	logger klog.Logger,
	coreClient coreclient.CoreV1Interface,
	namespace string,
	corev1PreInformers corev1preinformers.Interface,
	numWorkers int,
) (*controller, error) {
	ctl := &controller{
		enqueueLogger:    logger.WithName(ControllerName),
		coreclient:       coreClient,
		namespace:        namespace,
		podInformer:      corev1PreInformers.Pods().Informer(),
		podLister:        corev1PreInformers.Pods().Lister(),
		cmInformer:       corev1PreInformers.ConfigMaps().Informer(),
		cmLister:         corev1PreInformers.ConfigMaps().Lister(),
		nodeInformer:     corev1PreInformers.Nodes().Informer(),
		nodeLister:       corev1PreInformers.Nodes().Lister(),
		inferenceServers: make(map[apitypes.UID]*serverData),
	}
	ctl.gpuMap.Store(&map[string]GpuLocation{})
	ctl.QueueAndWorkers = genctlr.NewQueueAndWorkers(string(ControllerName), numWorkers, ctl.process)
	_, err := ctl.podInformer.AddEventHandler(ctl)
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

	// gpuMaps maps GPU UUID to GpuLocation
	gpuMap atomic.Pointer[map[string]GpuLocation]

	mutex sync.Mutex

	// inferenceServers maps UID of serve-requesting Pod to data
	inferenceServers map[apitypes.UID]*serverData
}

var _ Controller = &controller{}

type GpuLocation struct {
	Node  string
	Index uint
}

// Internal state about an inference server
type serverData struct {
	RequestingPodName string
	GPUIndices        *string
	ReadinessRelayed  *bool

	// RequesterDeleteRequested carries this bit forward without waiting for notification
	// from apiserver. Remember there is no sync between the notification streams for
	// different objects.
	RequesterDeleteRequested bool
}

type queueItem interface {
	// process returns (err error, retry bool).
	// There will be a retry iff `retry || err != nil`.
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

// careAbout returns infSvrItem, podIsRequester, have
func (ctl *controller) careAbout(pod *corev1.Pod) (infSvrItem, bool, bool) {
	if len(pod.Annotations[api.ServerPatchAnnotationName]) > 0 {
		return infSvrItem{pod.UID, pod.Name}, true, true
	}
	owner, have := GetOwner(pod)
	return owner, false, have
}

func (ctl *controller) OnAdd(obj any, isInInitialList bool) {
	switch typed := obj.(type) {
	case *corev1.Pod:
		if item, isReq, owned := ctl.careAbout(typed); !owned {
			ctl.enqueueLogger.V(5).Info("Ignoring irrelevant Pod", "name", typed.Name)
			return
		} else {
			ctl.enqueueLogger.V(5).Info("Enqueuing inference server reference due to notification of add", "item", item, "isReq", isReq, "isInInitialList", isInInitialList, "resourceVersion", typed.ResourceVersion)
			ctl.Queue.Add(item)
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
		if item, isReq, owned := ctl.careAbout(typed); !owned {
			ctl.enqueueLogger.V(5).Info("Ignoring irrelevant Pod", "name", typed.Name)
			return
		} else {
			ctl.enqueueLogger.V(5).Info("Enqueuing inference server reference due to notification of update", "item", item, "isReq", isReq, "resourceVersion", typed.ResourceVersion)
			ctl.Queue.Add(item)
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
		if item, isReq, owned := ctl.careAbout(typed); !owned {
			ctl.enqueueLogger.V(5).Info("Ignoring irrelevant Pod", "name", typed.Name)
			return
		} else {
			ctl.enqueueLogger.V(5).Info("Enqueuing inference server reference due to notification of delete", "item", item, "isReq", isReq, "resourceVersion", typed.ResourceVersion)
			ctl.Queue.Add(item)
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
// There will be a retry iff `retry || err != nil`.
func (ctl *controller) process(ctx context.Context, item queueItem) (error, bool) {
	return item.process(ctx, ctl)
}

func (item cmItem) process(ctx context.Context, ctl *controller) (error, bool) {
	logger := klog.FromContext(ctx)
	cm, err := ctl.coreclient.ConfigMaps(item.Namespace).Get(ctx, item.Name, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			ctl.gpuMap.Store(nil)
			logger.V(1).Info("ConfigMap " + GPUMapName + " does not exist")
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
	for infSvrUID, serverDat := range ctl.inferenceServers {
		item := infSvrItem{infSvrUID, serverDat.RequestingPodName}
		logger.V(5).Info("Enqueuing inference server because of change to GPU map", "item", item)
		ctl.Queue.Add(item)
	}
}

func (ctl *controller) getServerData(reqName string, reqUID apitypes.UID) *serverData {
	ctl.mutex.Lock()
	defer ctl.mutex.Unlock()
	ans := ctl.inferenceServers[reqUID]
	if ans == nil {
		ans = &serverData{RequestingPodName: reqName}
		ctl.inferenceServers[reqUID] = ans
	}
	return ans
}

func (ctl *controller) clearServerData(uid apitypes.UID) {
	ctl.mutex.Lock()
	defer ctl.mutex.Unlock()
	delete(ctl.inferenceServers, uid)
}
