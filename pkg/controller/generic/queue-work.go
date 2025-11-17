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
	"time"

	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
)

// This generic controller is authored by Mike Spreitzer.

// QueueAndWorkers is generic code for a typical controller's workqueue and worker goroutines
// that pull from that queue.
type QueueAndWorkers[Item comparable] struct {
	ControllerName string
	Queue          workqueue.TypedRateLimitingInterface[Item]
	NumWorkers     int
	Process        func(ctx context.Context, item Item) (err error, retry bool)

	earlySync func(context.Context, Item) *bool
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

// NewQueueAndWorkers makes a new QueueAndWorkers.
func newQueueAndWorkers[Item comparable](
	controllerName string,
	numWorkers int,
	process func(ctx context.Context, item Item) (err error, retry bool),
	earlySync func(context.Context, Item) *bool,
) QueueAndWorkers[Item] {
	ans := QueueAndWorkers[Item]{
		ControllerName: controllerName,
		Queue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.DefaultTypedControllerRateLimiter[Item](),
			workqueue.TypedRateLimitingQueueConfig[Item]{
				Name: controllerName,
			}),
		NumWorkers: numWorkers,
		Process:    process,
		earlySync:  earlySync,
	}
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
