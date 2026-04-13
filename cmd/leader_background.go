/*
Copyright 2025 The OpenSaola Authors.

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

package main

import (
	"context"

	"github.com/OpenSaola/opensaola/internal/resource"
	"github.com/OpenSaola/opensaola/internal/service/synchronizer"
	"github.com/OpenSaola/opensaola/internal/service/watcher"
	"github.com/OpenSaola/opensaola/pkg/tools/ctxkeys"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

type leaderBackgroundTasks struct {
	cli    client.Client
	scheme *runtime.Scheme
	// cfg is the REST config used to build the NsInformerManager clientset.
	cfg *rest.Config
}

var _ manager.Runnable = (*leaderBackgroundTasks)(nil)
var _ manager.LeaderElectionRunnable = (*leaderBackgroundTasks)(nil)

func (t *leaderBackgroundTasks) NeedLeaderElection() bool {
	return true
}

func (t *leaderBackgroundTasks) Start(ctx context.Context) error {
	l := log.FromContext(ctx).WithName("leaderBackgroundTasks")

	// Leader may switch/re-enter; ensure global state is clean to prevent stale stopChan/Map from blocking watcher/sync startup or leaking goroutines.
	watcher.StopAllCRWatchers()
	synchronizer.StopAllSyncCustomResources()

	// Start the namespace-scoped informer manager used by SyncCustomResourceV2.
	if t.cfg != nil {
		if _, err := synchronizer.StartNsInformerManager(ctx, t.cfg); err != nil {
			l.Error(err, "start NsInformerManager")
		} else {
			// Wire informer events to the debouncer layer.
			synchronizer.GetNsInformerManager().SetEventCallback(func(ns string) {
				synchronizer.NotifyNamespace(ns)
			})
		}
	} else {
		l.Info("no REST config provided, NsInformerManager will not start")
	}

	go func() {
		if err := watcher.StartCRWatcher(ctxkeys.WithScheme(ctx, t.scheme), t.cli); err != nil {
			l.Error(err, "StartCRWatcher failed")
		}
	}()
	go resource.InitCacheCleanupTimer(ctx)
	go resource.InitActionsCleanupTimer(ctx, t.cli)

	<-ctx.Done()

	// Shut down debouncer registry and informer manager on leader loss / process exit.
	synchronizer.StopAllDebouncers()
	synchronizer.StopNsInformerManager()
	watcher.StopAllCRWatchers()
	synchronizer.StopAllSyncCustomResources()
	return nil
}
