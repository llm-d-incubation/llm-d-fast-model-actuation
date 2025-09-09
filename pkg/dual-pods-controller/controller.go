package controller

import (
	"context"
	"fmt"

	"github.com/spf13/pflag"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	corev1preinformers "k8s.io/client-go/informers/core/v1"
	coreclient "k8s.io/client-go/kubernetes/typed/core/v1"
	corev1listers "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	genctlr "github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/generic-controller"
)

const ControllerName = "dual-pods-controller"

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
func NewController(
	logger klog.Logger,
	coreClient coreclient.CoreV1Interface,
	corev1PreInformers corev1preinformers.Interface,
	numWorkers int,
) (*controller, error) {
	ctl := &controller{
		enqueueLogger: logger.WithName(ControllerName),
		coreclient:    coreClient,
		podInformer:   corev1PreInformers.Pods().Informer(),
		podLister:     corev1PreInformers.Pods().Lister(),
	}
	ctl.QueueAndWorkers = genctlr.NewQueueAndWorkers(string(ControllerName), numWorkers, ctl.process, makeSentinel, isSentinel, ctl.onceProcessedSync)
	_, err := ctl.podInformer.AddEventHandler(ctl)
	if err != nil {
		panic(err)
	}
	return ctl, nil
}

type controller struct {
	enqueueLogger klog.Logger
	coreclient    coreclient.CoreV1Interface
	podInformer   cache.SharedIndexInformer
	podLister     corev1listers.PodLister
	genctlr.QueueAndWorkers[typedRef]
}

var _ Controller = &controller{}

type typedRef struct {
	Kind string
	cache.ObjectName
}

func (ref typedRef) String() string {
	return ref.Kind + ":" + ref.ObjectName.String()
}

const podKind = "Pod"
const sentinelKind = "Senti nel"

func (ctl *controller) OnAdd(obj any, isInInitialList bool) {
	var kind string
	var objM metav1.Object
	switch typed := obj.(type) {
	case *corev1.Pod:
		objM = typed
		kind = podKind
		ref := typedRef{kind, cache.MetaObjectToName(objM)}
		ctl.enqueueLogger.V(5).Info("Enqueuing reference due to notification of add", "ref", ref, "isInInitialList", isInInitialList)
		ctl.Queue.Add(ref)
	default:
		ctl.enqueueLogger.Error(nil, "Notified of add of unexpected type of object", "type", fmt.Sprintf("%T", obj))
	}
}

func (ctl *controller) OnUpdate(prev, obj any) {
	var kind string
	var objM metav1.Object
	switch typed := obj.(type) {
	case *corev1.Pod:
		objM = typed
		kind = podKind
		ref := typedRef{kind, cache.MetaObjectToName(objM)}
		ctl.enqueueLogger.V(5).Info("Enqueuing reference due to notification of update", "ref", ref)
		ctl.Queue.Add(ref)
	default:
		ctl.enqueueLogger.Error(nil, "Notified of update of unexpected type of object", "type", fmt.Sprintf("%T", obj))
	}
}

func (ctl *controller) OnDelete(obj any) {
	if dfsu, ok := obj.(cache.DeletedFinalStateUnknown); ok {
		obj = dfsu.Obj
	}
	var kind string
	var objM metav1.Object
	switch typed := obj.(type) {
	case *corev1.Pod:
		objM = typed
		kind = podKind
		ref := typedRef{kind, cache.MetaObjectToName(objM)}
		ctl.enqueueLogger.V(5).Info("Enqueuing reference due to notification of delete", "ref", ref)
		ctl.Queue.Add(ref)
	default:
		ctl.enqueueLogger.Error(nil, "Notified of delete of unexpected type of object", "type", fmt.Sprintf("%T", obj))
	}
}

func (ctl *controller) Start(ctx context.Context) error {
	if !cache.WaitForNamedCacheSync(ControllerName, ctx.Done(), ctl.podInformer.HasSynced) {
		return fmt.Errorf("caches not synced before end of Start context")
	}
	err := ctl.StartWorkers(ctx)
	if err != nil {
		return fmt.Errorf("failed to start workers: %w", err)
	}
	return nil
}

func makeSentinel(idx int) typedRef {
	return typedRef{sentinelKind, cache.ObjectName{Name: fmt.Sprintf("%d", idx)}}
}

func isSentinel(item typedRef) bool {
	return item.Kind == sentinelKind
}

func (ctl *controller) onceProcessedSync(context.Context) {
}

func (ctl *controller) process(ctx context.Context, ref typedRef) (error, bool) {
	logger := klog.FromContext(ctx)
	switch ref.Kind {
	case podKind:
		return ctl.processPod(ctx, ref.ObjectName)
	default:
		logger.Error(nil, "Asked to process unexpected Kind of object", "kind", ref.Kind)
		return nil, false
	}
}

func (ctl *controller) processPod(ctx context.Context, podRef cache.ObjectName) (error, bool) {
	logger := klog.FromContext(ctx)
	logger.V(5).Info("Processing Pod", "name", podRef.Name)
	return nil, false
}
