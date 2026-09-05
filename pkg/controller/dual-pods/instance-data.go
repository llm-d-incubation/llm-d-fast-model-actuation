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
	"time"

	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
)

// ensureLogTailLogged will, if it has not already been done, read the tail of the log
// of the given instance and log it in a huge log message of this controller.
func (ctl *controller) ensureLogTailLogged(ctx context.Context, instab instanceTable, launcherName, instanceID string, gpuUUIDs []string, lClient *LauncherClient) {
	if alreadyLogged, _ := instab.getLogTailLogged(instanceID); alreadyLogged {
		return
	}
	logger := klog.FromContext(ctx)
	tail, err := lClient.GetLog(ctx, instanceID, nil, ptr.To(int(100*1000)))
	if err != nil {
		logger.V(3).Info("Failed to fetch log tail of instance", "launcherName", launcherName, "instanceID", instanceID, "err", err)
		return
	}
	logger.V(2).Info("Fetched tail of log of instance", "launcherName", launcherName, "instanceID", instanceID, "gpuUUIDs", gpuUUIDs, "tail", tail)
	instab.setLogTailLogged(instanceID, true)
}

// Call only within `nodeItem.process.`
func (instab instanceTable) getLastUsedIfNotStopped(instanceID string) (time.Time, bool) {
	if instDat, have := instab[instanceID]; have && !instDat.Stopped {
		return instDat.LastUsed, true
	}
	return time.Time{}, false
}

// Call only within `nodeItem.process.`
func (instab instanceTable) getLogTailLogged(instanceID string) (bool, bool) {
	if instDat, have := instab[instanceID]; have {
		return instDat.LogTailLogged, true
	}
	return false, false
}

// Call only within `nodeItem.process.`
func (instab instanceTable) setLastUsed(instanceID string, lastUsed time.Time) {
	instDat := instab[instanceID]
	if instDat == nil {
		instDat = &instanceData{LastUsed: lastUsed}
		instab[instanceID] = instDat
	} else {
		instDat.LastUsed = lastUsed
	}
}

// Call only within `nodeItem.process.`
func (instab instanceTable) setLogTailLogged(instanceID string, logged bool) {
	instDat := instab[instanceID]
	if instDat == nil {
		instDat = &instanceData{LogTailLogged: logged}
		instab[instanceID] = instDat
	} else {
		instDat.LogTailLogged = logged
	}
}

// Call only within `nodeItem.process.`
func (instab instanceTable) setStopped(instanceID string, stopped bool) {
	instDat := instab[instanceID]
	if instDat == nil {
		instDat = &instanceData{Stopped: stopped}
		instab[instanceID] = instDat
	} else {
		instDat.Stopped = stopped
	}
}

// Getter is the type signature of reading from a Go-language map.
type Getter[Dom, Rng any] = func(Dom) (Rng, bool)

func dropHave[Base any](base Base, _ bool) Base { return base }
