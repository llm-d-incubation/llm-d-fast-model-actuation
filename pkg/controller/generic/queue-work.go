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

package generic

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
)

const (
	controllerQueuePerItemRetryMaxDelay = 20 * time.Second
)

// QueueAndWorkers is generic code for a typical controller's workqueue and worker goroutines
// that pull from that queue.
type QueueAndWorkers[Item comparable] struct {
	ControllerName string
	Queue          workqueue.TypedRateLimitingInterface[Item]
	NumWorkers     int
	Process        func(ctx context.Context, item Item) (err error, retry bool)

	earlySync func(context.Context, Item) *bool

	syncsStarted  *atomic.Int32
	syncsFinished *atomic.Int32

	// Always holds a time.Time.
	// This is approximately the last time that work on a queue item
	// started or stopped.
	approxLastWorkTime *atomic.Value

	// stopChan is normally open.
	// Closing it suppresses further work on queue items.
	stopChan chan struct{}
}

// NewQueueAndWorkers makes a new QueueAndWorkers.
// Iff `process` returns `retry==true` then the item will be
// requeued for retry.
func NewQueueAndWorkers[Item comparable](
	controllerName string,
	numWorkers int,
	process func(ctx context.Context, item Item) (err error, retry bool),
) QueueAndWorkers[Item] {
	return newQueueAndWorkers(controllerName, numWorkers, process, noEarlySync[Item])
}

func noEarlySync[Item comparable](context.Context, Item) *bool {
	return nil
}

// newQueueAndWorkers makes a new QueueAndWorkers.
// Syncing each item begins with calling `earlySync` on that item;
// a return of `nil` means to proceed to `process`,
// a return of `&true` means to move on to the next item,
// a return of `&false` means to halt processing.
func newQueueAndWorkers[Item comparable](
	controllerName string,
	numWorkers int,
	process func(ctx context.Context, item Item) (err error, retry bool),
	earlySync func(context.Context, Item) *bool,
) QueueAndWorkers[Item] {
	ans := QueueAndWorkers[Item]{
		ControllerName: controllerName,
		Queue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.NewTypedWithMaxWaitRateLimiter(
				workqueue.DefaultTypedControllerRateLimiter[Item](),
				controllerQueuePerItemRetryMaxDelay,
			),
			workqueue.TypedRateLimitingQueueConfig[Item]{
				Name: controllerName,
			}),
		NumWorkers: numWorkers,
		Process:    process,
		earlySync:  earlySync,

		syncsStarted:       new(atomic.Int32),
		syncsFinished:      new(atomic.Int32),
		approxLastWorkTime: new(atomic.Value),
		stopChan:           make(chan struct{}),
	}
	ans.approxLastWorkTime.Store(time.Now())
	return ans
}

// StartWorkers launches the workers.
// Call this after the initial batch of items has been enqueued.
// ctx has already been specialized to this controller.
func (ctl *QueueAndWorkers[Item]) StartWorkers(ctx context.Context) error {
	logger := klog.FromContext(ctx)
	for workerIdx := range ctl.NumWorkers {
		workLogger := logger.WithValues("worker", workerIdx)
		workCtx := klog.NewContext(ctx, workLogger)
		workLogger.V(3).Info("Launching worker")
		go func() {
			wait.UntilWithContext(workCtx, func(ctx context.Context) { ctl.runWorker(ctx) }, time.Second)
			workLogger.V(3).Info("Finished worker")
		}()
	}
	logger.V(1).Info("Started workers", "numWorkers", ctl.NumWorkers)
	return nil
}

// StopTestWorkers is a hack for use in tests of clients of this controller.
// Calling this suppresses further work on queue items.
// This would only be used near the end of a test,
// when no further queue item processing is intended and the whole
// controller is about to become unreachable.
// It is the first simplest step in the direction of giving tests
// control over the scheduling of processing of queue items.
func (ctl *QueueAndWorkers[Item]) StopTestWorkers(ctx context.Context) {
	logger := klog.FromContext(ctx)
	logger.Info("Stopping workers", "controller", ctl.ControllerName)
	close(ctl.stopChan)
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
	select {
	case <-ctl.stopChan:
		logger.Info("Worker aborted for testing")
		return false
	default:
	}
	ctl.syncsStarted.Add(1)
	ctl.approxLastWorkTime.Store(time.Now())
	defer func() {
		ctl.Queue.Done(objRef)
		ctl.syncsFinished.Add(1)
		ctl.approxLastWorkTime.Store(time.Now())
	}()
	logger.V(4).Info("Popped workqueue item", "item", objRef, "itemType", fmt.Sprintf("%T", objRef))
	if ans := ctl.earlySync(ctx, objRef); ans != nil {
		return *ans
	}
	var err error
	var retry bool
	defer func() {
		if err == nil {
			if retry {
				// Nothing went wrong but this item needs to be requeued for reprocessing.
				ctl.Queue.AddRateLimited(objRef)
				logger.V(4).Info("Processed workqueue item successfully, requeued for follow-up.", "item", objRef)
			} else {
				// If no error occurs we Forget this item so it does not
				// get queued again until another change happens.
				ctl.Queue.Forget(objRef)
				logger.V(4).Info("Processed workqueue item successfully.", "item", objRef)
			}
		} else if retry {
			ctl.Queue.AddRateLimited(objRef)
			logger.V(4).Info("Encountered transient error while processing workqueue item; do not be alarmed, this will be retried later", "item", objRef, "err", err)
		} else {
			ctl.Queue.Forget(objRef)
			logger.Error(err, "Failed to process workqueue item", "item", objRef)
		}
	}()
	err, retry = ctl.Process(ctx, objRef)
	return true
}

// Status is a snapshot of the state of the controller.
// This state is not maintained atomically, and this snapshot
// holds values from slightly different times.
type Status struct {
	QueueDepth    int
	SyncsStarted  int
	SyncsFinished int

	// ApproxLastWorkTime is updated at each change
	// to either of the sync counts, at nearly the same time.
	ApproxLastWorkTime time.Time
}

func (status Status) IsIdle() bool {
	return status.QueueDepth == 0 && status.SyncsFinished == status.SyncsStarted
}

// TotalQueued returns the cumulative total over the controller's lifetime.
// It may be a little off, due to the asynchrony between the components.
func (status Status) TotalQueued() int { return status.QueueDepth + status.SyncsStarted }

func (ctl *QueueAndWorkers[Item]) GetStatus() Status {
	qd := ctl.Queue.Len()
	alc, _ := ctl.approxLastWorkTime.Load().(time.Time)
	return Status{
		QueueDepth:         qd,
		SyncsStarted:       int(ctl.syncsStarted.Load()),
		SyncsFinished:      int(ctl.syncsFinished.Load()),
		ApproxLastWorkTime: alc,
	}
}
