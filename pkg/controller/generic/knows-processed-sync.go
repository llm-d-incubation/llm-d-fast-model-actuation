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

// This generic controller is authored by Mike Spreitzer.
package generic

import (
	"context"
	"sync"
	"sync/atomic"

	"k8s.io/klog/v2"
)

// KnowsProcessedSync extends the functionality of QueueAndWorkers with the ability to
// report when all of the initial load of workqueue items ---
// those enqueued before the informers report `HasSynced` --- have been processed by
// workers; this is what the `HasProcessedSync` method reports.
// This is done by stuffing the queue with `NumWorkers` sentinel values between
// those initial items and all the rest. Each worker, when it pops a sentinel, waits for
// all the others to also pop a sentinel before continuing.
type KnowsProcessedSync[Item comparable] struct {
	QueueAndWorkers[Item]

	onceProcessedSync func(context.Context)

	// sentinel is a special value enqueued to delineate between
	// the initial batch of items and the rest.
	makeSentinel func(int) Item
	isSentinel   func(Item) bool

	// wg tracks the workers' completion of initial batch of items
	wg            *sync.WaitGroup
	processedSync *atomic.Bool
}

func NewKnowsProcessedSync[Item comparable](
	controllerName string,
	numWorkers int,
	process func(ctx context.Context, item Item) (err error, retry bool),
	makeSentinel func(distinguisher int) Item,
	isSentinel func(Item) bool,
	onceProcessedSync func(context.Context),
) KnowsProcessedSync[Item] {
	kps := KnowsProcessedSync[Item]{
		onceProcessedSync: onceProcessedSync,
		makeSentinel:      makeSentinel,
		isSentinel:        isSentinel,
		wg:                &sync.WaitGroup{},
		processedSync:     &atomic.Bool{},
	}
	kps.QueueAndWorkers = newQueueAndWorkers(controllerName, numWorkers, process, kps.earlySync)
	return kps
}

func (ctl *KnowsProcessedSync[Item]) earlySync(ctx context.Context, objRef Item) *bool {
	if !ctl.isSentinel(objRef) {
		return nil
	}
	ans := true
	logger := klog.FromContext(ctx)
	logger.V(3).Info("Done processing initial batch of items; waiting for others", "sentinel", objRef)
	ctl.wg.Done()
	ctl.wg.Wait()
	logger.V(3).Info("Resuming normal processing of items", "sentinel", objRef)
	return &ans
}

// StartWorkers launches the workers.
// Call this after the initial batch of items has been enqueued.
// ctx has already been specialized to this controller.
func (ctl *KnowsProcessedSync[Item]) StartWorkers(ctx context.Context) error {
	logger := klog.FromContext(ctx)
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
	return ctl.QueueAndWorkers.StartWorkers(ctx)
}

// HasProcessedSync indicates whether the initial batch of items has been processed.
// May only be called if the constructor was given non-nil sentinel functions.
func (ctl *KnowsProcessedSync[Item]) HasProcessedSync() bool {
	return ctl.processedSync.Load()
}
