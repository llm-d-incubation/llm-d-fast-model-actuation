package genericcontroller

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
)

// This generic controller is authored by Mike Spreitzer.

// QueueAndWorkers is generic code for a typical controller's workqueue and worker goroutines
// that pull from that queue.
// Untypically, this can also report when all of the initial load of workqueue items ---
// those enqueued before the informers report `HasSynced` --- have been processed by
// workers; this is what the `HasProcessedSync` method reports.
// This is done by stuffing the queue with `NumWorkers` sentinel values between
// those initial items and all the rest. Each worker, when it pops a sentinel, waits for
// all the others to also pop a sentinel before continuing.
type QueueAndWorkers[Item comparable] struct {
	ControllerName    string
	Queue             workqueue.TypedRateLimitingInterface[Item]
	NumWorkers        int
	Process           func(ctx context.Context, item Item) (err error, retry bool)
	onceProcessedSync func(context.Context)

	// sentinel is a special value enqueued to delineate between
	// the initial batch of items and the rest.
	makeSentinel func(int) Item
	isSentinel   func(Item) bool

	// wg tracks the workers' completion of initial batch of items
	wg            *sync.WaitGroup
	processedSync *atomic.Bool
}

// NewQueueAndWorkers makes a new QueueAndWorkers.
// Either makeSentinel and isSentinel are both nil or they both are not nil.
func NewQueueAndWorkers[Item comparable](
	controllerName string,
	numWorkers int,
	process func(ctx context.Context, item Item) (err error, retry bool),
	makeSentinel func(distinguisher int) Item,
	isSentinel func(Item) bool,
	onceProcessedSync func(context.Context),
) QueueAndWorkers[Item] {
	if (makeSentinel == nil) != (isSentinel == nil) {
		panic("sentintel incoherence")
	}
	ans := QueueAndWorkers[Item]{
		ControllerName: controllerName,
		Queue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.DefaultTypedControllerRateLimiter[Item](),
			workqueue.TypedRateLimitingQueueConfig[Item]{
				Name: controllerName,
			}),
		NumWorkers:        numWorkers,
		Process:           process,
		onceProcessedSync: onceProcessedSync,
		makeSentinel:      makeSentinel,
		isSentinel:        isSentinel,
		wg:                &sync.WaitGroup{},
		processedSync:     &atomic.Bool{},
	}
	return ans
}

// StartWorkers launches the workers.
// Call this after the initial batch of items has been enqueued.
// ctx has already been specialized to this controller.
func (ctl *QueueAndWorkers[Item]) StartWorkers(ctx context.Context) error {
	logger := klog.FromContext(ctx)
	if ctl.makeSentinel != nil {
		ctl.wg.Add(ctl.NumWorkers)
		go func() {
			ctl.wg.Wait()
			logger.V(1).Info("All workers have finished processing initial items")
			ctl.processedSync.Store(true)
			ctl.onceProcessedSync(ctx)
		}()
		for worker := range ctl.NumWorkers {
			ctl.Queue.Add(ctl.makeSentinel(worker))
		}
	}
	for workerIdx := range ctl.NumWorkers {
		workLogger := logger.WithValues("worker", workerIdx)
		workCtx := klog.NewContext(ctx, workLogger)
		workLogger.V(3).Info("Launching worker")
		go func() {
			wait.UntilWithContext(workCtx, func(ctx context.Context) { ctl.runWorker(ctx) }, time.Second)
			workLogger.V(3).Info("Fnished worker")
		}()
	}
	logger.V(1).Info("Started workers", "numWorkers", ctl.NumWorkers)
	return nil
}

func (ctl *QueueAndWorkers[Item]) runWorker(ctx context.Context) {
	for ctl.processNextWorkItem(ctx) {
	}
}

func (ctl *QueueAndWorkers[Item]) processNextWorkItem(ctx context.Context) bool {
	logger := klog.FromContext(ctx)
	objRef, shutdown := ctl.Queue.Get()
	if shutdown {
		return false
	}
	defer ctl.Queue.Done(objRef)
	logger.V(4).Info("Popped workqueue item", "item", objRef)
	if ctl.isSentinel != nil && ctl.isSentinel(objRef) {
		logger.V(3).Info("Done processing initial batch of items; waiting for others", "sentinel", objRef)
		ctl.wg.Done()
		ctl.wg.Wait()
		logger.V(3).Info("Resuming normal processing of items", "sentinel", objRef)
		return true
	}

	var err error
	var retry bool
	defer func() {
		if err == nil {
			// If no error occurs we Forget this item so it does not
			// get queued again until another change happens.
			ctl.Queue.Forget(objRef)
			logger.V(4).Info("Processed workqueue item successfully.", "item", objRef, "itemType", fmt.Sprintf("%T", objRef))
		} else if retry {
			ctl.Queue.AddRateLimited(objRef)
			logger.V(4).Info("Encountered transient error while processing workqueue item; do not be alarmed, this will be retried later", "item", objRef, "itemType", fmt.Sprintf("%T", objRef), "err", err)
		} else {
			ctl.Queue.Forget(objRef)
			logger.Error(err, "Failed to process workqueue item", "item", objRef, "itemType", fmt.Sprintf("%T", objRef))
		}
	}()
	err, retry = ctl.Process(ctx, objRef)
	return true
}

// HasProcessedSync indicates whether the initial batch of items has been processed.
// May only be called if the constructor was given non-nil sentinel functions.
func (ctl *QueueAndWorkers[Item]) HasProcessedSync() bool {
	if ctl.makeSentinel == nil {
		panic("no sentinels")
	}
	return ctl.processedSync.Load()
}
