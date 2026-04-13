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

package synchronizer

// resource_cache_informer.go provides a namespace-scoped shared informer manager
// that replaces full-List polling of 7 native K8s resource types every 10 seconds.

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"

	"github.com/OpenSaola/opensaola/internal/resource/logger"
)

// ────────────────────────────────────────────────────────────────────────────
// Public interface
// ────────────────────────────────────────────────────────────────────────────

// NsResourceCache provides local-cache queries for all native resources within a namespace.
type NsResourceCache interface {
	ListStatefulSets(ns string) []appsv1.StatefulSet
	ListDeployments(ns string) []appsv1.Deployment
	ListDaemonSets(ns string) []appsv1.DaemonSet
	ListReplicaSets(ns string) []appsv1.ReplicaSet
	ListPods(ns string) []corev1.Pod
	ListServices(ns string) []corev1.Service
	ListPVCs(ns string) []corev1.PersistentVolumeClaim
}

// ────────────────────────────────────────────────────────────────────────────
// Package-level singleton
// ────────────────────────────────────────────────────────────────────────────

// globalManager is the package-level singleton of NsInformerManager.
var globalManager *NsInformerManager

// globalManagerMu guards init/stop of globalManager.
var globalManagerMu sync.Mutex

// StartNsInformerManager initialises the global NsInformerManager with the provided
// rest.Config and starts its internal goroutines. It is idempotent: calling it
// more than once returns the existing instance without error.
func StartNsInformerManager(ctx context.Context, cfg *rest.Config) (*NsInformerManager, error) {
	globalManagerMu.Lock()
	defer globalManagerMu.Unlock()

	if globalManager != nil {
		return globalManager, nil
	}

	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("NsInformerManager: build clientset: %w", err)
	}

	m := &NsInformerManager{
		clientset: cs,
		entries:   sync.Map{},
	}
	m.rootCtx, m.rootCancel = context.WithCancel(ctx)
	globalManager = m

	logger.Log.Infof("NsInformerManager started")
	return m, nil
}

// StopNsInformerManager shuts down the global NsInformerManager and all its
// namespace-level informers.
func StopNsInformerManager() {
	globalManagerMu.Lock()
	defer globalManagerMu.Unlock()

	if globalManager == nil {
		return
	}
	globalManager.stopAll()
	globalManager = nil
	logger.Log.Infof("NsInformerManager stopped")
}

// GetNsInformerManager returns the global singleton. Returns nil if not yet started.
func GetNsInformerManager() *NsInformerManager {
	globalManagerMu.Lock()
	defer globalManagerMu.Unlock()
	return globalManager
}

// ────────────────────────────────────────────────────────────────────────────
// nsEntry — per-namespace state
// ────────────────────────────────────────────────────────────────────────────

// nsEntry holds all state for a single namespace's informer set.
type nsEntry struct {
	// cancel stops this namespace's informer factory.
	cancel context.CancelFunc

	// refCount tracks how many middlewares have registered against this namespace.
	refCount atomic.Int32

	// midKeys tracks which middlewares are registered (value is struct{}).
	midKeys sync.Map

	// factory is the shared informer factory scoped to this namespace.
	factory informers.SharedInformerFactory

	// synced becomes true once WaitForCacheSync completes for all 7 informers.
	synced atomic.Bool
}

// ────────────────────────────────────────────────────────────────────────────
// NsInformerManager
// ────────────────────────────────────────────────────────────────────────────

// NsInformerManager manages namespace-scoped shared informer factories.
// Register / Unregister calls maintain reference counts so each namespace's
// informers are started exactly once and torn down when no longer needed.
type NsInformerManager struct {
	clientset kubernetes.Interface

	// entries maps namespace string to *nsEntry.
	entries sync.Map

	// eventCallback is invoked on any Add/Update/Delete event in a namespace.
	eventCallback func(ns string)
	callbackMu    sync.RWMutex

	rootCtx    context.Context
	rootCancel context.CancelFunc
}

// SetEventCallback registers a callback that is invoked whenever any resource
// event fires within a namespace. Intended for use by the debouncer layer.
func (m *NsInformerManager) SetEventCallback(fn func(ns string)) {
	m.callbackMu.Lock()
	defer m.callbackMu.Unlock()
	m.eventCallback = fn
}

// notifyNamespace calls the registered event callback for the given namespace.
func (m *NsInformerManager) notifyNamespace(ns string) {
	m.callbackMu.RLock()
	fn := m.eventCallback
	m.callbackMu.RUnlock()

	if fn != nil {
		fn(ns)
	}
}

// Register ensures informers for ns are running and increments the reference count
// for midKey. If this is the first registration for ns, it starts 7 typed informers
// and blocks until their caches are synced or ctx is cancelled.
func (m *NsInformerManager) Register(ctx context.Context, ns, midKey string) error {
	// Fast path: entry already exists.
	if raw, ok := m.entries.Load(ns); ok {
		e := raw.(*nsEntry)
		if _, loaded := e.midKeys.LoadOrStore(midKey, struct{}{}); !loaded {
			e.refCount.Add(1)
			logger.Log.Infof("NsInformerManager: ns=%s midKey=%s registered (refCount=%d)", ns, midKey, e.refCount.Load())
		}
		return nil
	}

	// Slow path: create new entry.
	nsCtx, nsCancel := context.WithCancel(m.rootCtx)

	factory := informers.NewSharedInformerFactoryWithOptions(
		m.clientset,
		0, // resyncPeriod=0: pure watch, no periodic resync
		informers.WithNamespace(ns),
		informers.WithTweakListOptions(nil),
	)

	entry := &nsEntry{
		cancel:  nsCancel,
		factory: factory,
	}
	entry.refCount.Store(1)
	entry.midKeys.Store(midKey, struct{}{})

	// If another goroutine raced us, use the winner's entry.
	actual, loaded := m.entries.LoadOrStore(ns, entry)
	if loaded {
		nsCancel() // discard our factory
		e := actual.(*nsEntry)
		if _, alreadyIn := e.midKeys.LoadOrStore(midKey, struct{}{}); !alreadyIn {
			e.refCount.Add(1)
			logger.Log.Infof("NsInformerManager: ns=%s midKey=%s registered (refCount=%d, raced)", ns, midKey, e.refCount.Load())
		}
		return nil
	}

	// Register event handlers for all 7 resource types.
	handler := m.buildHandler(ns)
	m.registerHandlers(factory, handler)

	// Start the factory and all registered informers.
	factory.Start(nsCtx.Done())

	logger.Log.Infof("NsInformerManager: ns=%s started informers, waiting for cache sync...", ns)

	// WaitForCacheSync with timeout derived from ctx.
	syncCtx, syncCancel := context.WithTimeout(ctx, 60*time.Second)
	defer syncCancel()

	synced := factory.WaitForCacheSync(syncCtx.Done())
	for resType, ok := range synced {
		if !ok {
			logger.Log.Warnf("NsInformerManager: ns=%s informer %v not synced (ctx cancelled or timed out)", ns, resType)
		}
	}

	entry.synced.Store(true)
	logger.Log.Infof("NsInformerManager: ns=%s midKey=%s registered (refCount=1, cache synced)", ns, midKey)
	return nil
}

// Unregister decrements the reference count for midKey in ns. When the count
// reaches zero all informers for ns are stopped and the entry is removed.
func (m *NsInformerManager) Unregister(ns, midKey string) {
	raw, ok := m.entries.Load(ns)
	if !ok {
		return
	}
	e := raw.(*nsEntry)

	if _, existed := e.midKeys.LoadAndDelete(midKey); !existed {
		// midKey was never registered; nothing to do.
		return
	}

	remaining := e.refCount.Add(-1)
	logger.Log.Infof("NsInformerManager: ns=%s midKey=%s unregistered (refCount=%d)", ns, midKey, remaining)

	if remaining <= 0 {
		// Stop informers and remove entry.
		e.cancel()
		m.entries.Delete(ns)
		logger.Log.Infof("NsInformerManager: ns=%s all informers stopped and entry removed", ns)
	}
}

// GetCache returns the NsResourceCache for ns. Returns nil if ns is not registered
// or its cache has not yet synced.
func (m *NsInformerManager) GetCache(ns string) NsResourceCache {
	raw, ok := m.entries.Load(ns)
	if !ok {
		return nil
	}
	e := raw.(*nsEntry)
	if !e.synced.Load() {
		return nil
	}
	return &nsCache{factory: e.factory, ns: ns}
}

// stopAll cancels all namespace contexts and clears the entries map.
func (m *NsInformerManager) stopAll() {
	m.entries.Range(func(key, value any) bool {
		e := value.(*nsEntry)
		e.cancel()
		m.entries.Delete(key)
		return true
	})
	if m.rootCancel != nil {
		m.rootCancel()
	}
}

// ────────────────────────────────────────────────────────────────────────────
// Event handler helpers
// ────────────────────────────────────────────────────────────────────────────

// buildHandler constructs a ResourceEventHandlerFuncs that calls notifyNamespace(ns)
// on every Add, Update, and Delete event.
func (m *NsInformerManager) buildHandler(ns string) cache.ResourceEventHandlerFuncs {
	notify := func(_ any) { m.notifyNamespace(ns) }
	return cache.ResourceEventHandlerFuncs{
		AddFunc:    notify,
		UpdateFunc: func(_, newObj any) { m.notifyNamespace(ns) },
		DeleteFunc: notify,
	}
}

// registerHandlers adds the unified event handler to all 7 informers.
func (m *NsInformerManager) registerHandlers(factory informers.SharedInformerFactory, handler cache.ResourceEventHandlerFuncs) {
	mustAdd := func(informer cache.SharedIndexInformer) {
		if _, err := informer.AddEventHandler(handler); err != nil {
			// Non-fatal: log and continue; cache still works without callback.
			logger.Log.Warnf("NsInformerManager: AddEventHandler failed: %v", err)
		}
	}

	mustAdd(factory.Apps().V1().StatefulSets().Informer())
	mustAdd(factory.Apps().V1().Deployments().Informer())
	mustAdd(factory.Apps().V1().DaemonSets().Informer())
	mustAdd(factory.Apps().V1().ReplicaSets().Informer())
	mustAdd(factory.Core().V1().Pods().Informer())
	mustAdd(factory.Core().V1().Services().Informer())
	mustAdd(factory.Core().V1().PersistentVolumeClaims().Informer())
}

// ────────────────────────────────────────────────────────────────────────────
// nsCache — NsResourceCache implementation
// ────────────────────────────────────────────────────────────────────────────

// nsCache wraps a SharedInformerFactory to satisfy NsResourceCache.
type nsCache struct {
	factory informers.SharedInformerFactory
	ns      string
}

// ListStatefulSets returns all StatefulSets in ns from the local cache.
func (c *nsCache) ListStatefulSets(ns string) []appsv1.StatefulSet {
	items, err := c.factory.Apps().V1().StatefulSets().Lister().StatefulSets(ns).List(labels.Everything())
	if err != nil {
		logger.Log.Warnf("nsCache.ListStatefulSets ns=%s: %v", ns, err)
		return nil
	}
	result := make([]appsv1.StatefulSet, 0, len(items))
	for _, p := range items {
		result = append(result, *p)
	}
	return result
}

// ListDeployments returns all Deployments in ns from the local cache.
func (c *nsCache) ListDeployments(ns string) []appsv1.Deployment {
	items, err := c.factory.Apps().V1().Deployments().Lister().Deployments(ns).List(labels.Everything())
	if err != nil {
		logger.Log.Warnf("nsCache.ListDeployments ns=%s: %v", ns, err)
		return nil
	}
	result := make([]appsv1.Deployment, 0, len(items))
	for _, p := range items {
		result = append(result, *p)
	}
	return result
}

// ListDaemonSets returns all DaemonSets in ns from the local cache.
func (c *nsCache) ListDaemonSets(ns string) []appsv1.DaemonSet {
	items, err := c.factory.Apps().V1().DaemonSets().Lister().DaemonSets(ns).List(labels.Everything())
	if err != nil {
		logger.Log.Warnf("nsCache.ListDaemonSets ns=%s: %v", ns, err)
		return nil
	}
	result := make([]appsv1.DaemonSet, 0, len(items))
	for _, p := range items {
		result = append(result, *p)
	}
	return result
}

// ListReplicaSets returns all ReplicaSets in ns from the local cache.
func (c *nsCache) ListReplicaSets(ns string) []appsv1.ReplicaSet {
	items, err := c.factory.Apps().V1().ReplicaSets().Lister().ReplicaSets(ns).List(labels.Everything())
	if err != nil {
		logger.Log.Warnf("nsCache.ListReplicaSets ns=%s: %v", ns, err)
		return nil
	}
	result := make([]appsv1.ReplicaSet, 0, len(items))
	for _, p := range items {
		result = append(result, *p)
	}
	return result
}

// ListPods returns all Pods in ns from the local cache.
func (c *nsCache) ListPods(ns string) []corev1.Pod {
	items, err := c.factory.Core().V1().Pods().Lister().Pods(ns).List(labels.Everything())
	if err != nil {
		logger.Log.Warnf("nsCache.ListPods ns=%s: %v", ns, err)
		return nil
	}
	result := make([]corev1.Pod, 0, len(items))
	for _, p := range items {
		result = append(result, *p)
	}
	return result
}

// ListServices returns all Services in ns from the local cache.
func (c *nsCache) ListServices(ns string) []corev1.Service {
	items, err := c.factory.Core().V1().Services().Lister().Services(ns).List(labels.Everything())
	if err != nil {
		logger.Log.Warnf("nsCache.ListServices ns=%s: %v", ns, err)
		return nil
	}
	result := make([]corev1.Service, 0, len(items))
	for _, p := range items {
		result = append(result, *p)
	}
	return result
}

// ListPVCs returns all PersistentVolumeClaims in ns from the local cache.
func (c *nsCache) ListPVCs(ns string) []corev1.PersistentVolumeClaim {
	items, err := c.factory.Core().V1().PersistentVolumeClaims().Lister().PersistentVolumeClaims(ns).List(labels.Everything())
	if err != nil {
		logger.Log.Warnf("nsCache.ListPVCs ns=%s: %v", ns, err)
		return nil
	}
	result := make([]corev1.PersistentVolumeClaim, 0, len(items))
	for _, p := range items {
		result = append(result, *p)
	}
	return result
}
