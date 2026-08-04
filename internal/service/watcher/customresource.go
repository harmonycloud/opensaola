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

package watcher

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"time"

	"github.com/harmonycloud/opensaola/api/v1"
	"github.com/harmonycloud/opensaola/internal/k8s"
	"github.com/harmonycloud/opensaola/internal/service/middlewarebaseline"
	"github.com/harmonycloud/opensaola/internal/service/middlewareoperatorbaseline"
	"github.com/harmonycloud/opensaola/internal/service/synchronizer"
	"k8s.io/apimachinery/pkg/api/equality"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

type CustomResourceWatcher struct {
	GVK       schema.GroupVersionKind // gvk
	Namespace string                  // namespace
	StopChan  chan struct{}           // stop channel

	done chan struct{}

	membersMu sync.Mutex
	members   map[string]struct{}
	stopped   bool
}

var CustomResourceWatcherMap sync.Map

func StopAllCRWatchers() {
	CustomResourceWatcherMap.Range(func(key, value any) bool {
		if cw, ok := value.(*CustomResourceWatcher); ok {
			cw.Stop()
			CustomResourceWatcherMap.CompareAndDelete(key, cw)
		}
		return true
	})
}

func (w *CustomResourceWatcher) GetKey() string {
	return fmt.Sprintf("%s/%s", w.Namespace, w.GVK.String())
}

// NewCRWatcher creates a custom resource watcher
func NewCRWatcher(gvk schema.GroupVersionKind, ns string) *CustomResourceWatcher {
	cw := &CustomResourceWatcher{
		GVK:       gvk,
		Namespace: ns,
		StopChan:  make(chan struct{}),
		done:      make(chan struct{}),
		members:   make(map[string]struct{}),
	}
	return cw
}

// Done is closed once the informer supervisor has released this watcher.
func (w *CustomResourceWatcher) Done() <-chan struct{} {
	return w.done
}

// Stop is idempotent. The watcher owns StopChan; informer workers only receive
// from it and never close it.
func (w *CustomResourceWatcher) Stop() {
	if w == nil {
		return
	}
	w.membersMu.Lock()
	w.stopLocked()
	w.membersMu.Unlock()
}

func (w *CustomResourceWatcher) stopLocked() {
	if w.stopped {
		return
	}
	w.stopped = true
	close(w.StopChan)
}

func (w *CustomResourceWatcher) addMember(name string) bool {
	if w == nil || name == "" {
		return false
	}
	w.membersMu.Lock()
	defer w.membersMu.Unlock()
	if w.stopped {
		return false
	}
	w.members[name] = struct{}{}
	return true
}

func (w *CustomResourceWatcher) removeMember(name string) (removed, empty bool) {
	if w == nil || name == "" {
		return false, false
	}
	w.membersMu.Lock()
	defer w.membersMu.Unlock()
	if _, ok := w.members[name]; !ok {
		return false, len(w.members) == 0
	}
	delete(w.members, name)
	if len(w.members) == 0 {
		w.stopLocked()
		return true, true
	}
	return true, false
}

type informerRunner func(context.Context, client.Client, <-chan struct{}, schema.GroupVersionKind, string, cache.ResourceEventHandlerFuncs) error

// EnsureCRWatcher joins the namespace/GVK watcher for cr, creating a
// self-healing informer supervisor when needed. Membership is keyed by CR name,
// so releasing one Middleware cannot stop another CR sharing the watcher.
func EnsureCRWatcher(ctx context.Context, cli client.Client, cr *unstructured.Unstructured, middlewareName, middlewareNamespace string) (*CustomResourceWatcher, bool, error) {
	return ensureCRWatcher(
		ctx,
		cli,
		cr,
		NewResourceEventHandlerFuncs(ctx, cli, middlewareName, middlewareNamespace),
		k8s.NewInformerOptUnit,
		k8s.CalcPanicBackoff,
	)
}

func ensureCRWatcher(ctx context.Context, cli client.Client, cr *unstructured.Unstructured, handler cache.ResourceEventHandlerFuncs, runInformer informerRunner, retryBackoff func(int) time.Duration) (*CustomResourceWatcher, bool, error) {
	if cr == nil {
		return nil, false, errors.New("custom resource is nil")
	}
	if cr.GetName() == "" {
		return nil, false, errors.New("custom resource name is empty")
	}

	for {
		candidate := NewCRWatcher(cr.GroupVersionKind(), cr.GetNamespace())
		actual, loaded := CustomResourceWatcherMap.LoadOrStore(candidate.GetKey(), candidate)
		if loaded {
			existing, ok := actual.(*CustomResourceWatcher)
			if !ok {
				return nil, false, fmt.Errorf("custom resource watcher %s has unexpected type %T", candidate.GetKey(), actual)
			}
			if existing.addMember(cr.GetName()) {
				return existing, false, nil
			}
			CustomResourceWatcherMap.CompareAndDelete(candidate.GetKey(), existing)
			continue
		}

		if !candidate.addMember(cr.GetName()) {
			CustomResourceWatcherMap.CompareAndDelete(candidate.GetKey(), candidate)
			continue
		}
		log.FromContext(ctx).Info("creating CR watcher", "key", candidate.GetKey())
		go candidate.run(ctx, cli, handler, runInformer, retryBackoff)
		return candidate, true, nil
	}
}

func (w *CustomResourceWatcher) run(ctx context.Context, cli client.Client, handler cache.ResourceEventHandlerFuncs, runInformer informerRunner, retryBackoff func(int) time.Duration) {
	defer close(w.done)
	defer CustomResourceWatcherMap.CompareAndDelete(w.GetKey(), w)

	go func() {
		select {
		case <-ctx.Done():
			w.Stop()
		case <-w.StopChan:
		}
	}()

	for attempt := 1; ; attempt++ {
		select {
		case <-ctx.Done():
			return
		case <-w.StopChan:
			return
		default:
		}

		err := runInformer(ctx, cli, w.StopChan, w.GVK, w.Namespace, handler)
		select {
		case <-ctx.Done():
			return
		case <-w.StopChan:
			return
		default:
		}
		if err == nil {
			err = errors.New("custom resource informer exited unexpectedly")
		}
		log.FromContext(ctx).Error(err, "custom resource informer exited; retrying", "gvk", w.GVK, "namespace", w.Namespace, "attempt", attempt)

		delay := retryBackoff(attempt)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return
		case <-w.StopChan:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return
		case <-timer.C:
		}
	}
}

// StartCRWatcher starts the CR watcher
func StartCRWatcher(ctx context.Context, cli client.Client) error {
	return startCRWatcherImpl(ctx, cli, 0)
}

func startCRWatcherImpl(ctx context.Context, cli client.Client, attempt int) (err error) {
	defer func() {
		r := recover()
		// Leader loss cancels this worker normally. It must not run global cleanup
		// after a replacement leader has started its own watcher sessions.
		if ctx.Err() != nil {
			return
		}
		if err != nil || r != nil {
			log.FromContext(ctx).Error(fmt.Errorf("panic: %v error: %v", r, err), "StartCRWatcher panic")

			buf := make([]byte, 1024)
			n := runtime.Stack(buf, false)
			fmt.Printf("Stack trace:\n%s\n", string(buf[:n]))

			nextAttempt := attempt + 1
			delay := k8s.CalcPanicBackoff(nextAttempt)
			log.FromContext(ctx).Info("StartCRWatcher panic backoff restart", "attempt", nextAttempt, "delay", delay)

			// Prevent informer/sync goroutine accumulation and stale global Map entries after panic restart.
			StopAllCRWatchers()
			synchronizer.StopAllSyncCustomResources()

			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
			}
			go func() {
				_ = startCRWatcherImpl(ctx, cli, nextAttempt)
			}()
		}
	}()

	// Initial list fetch: ensure apiserver is available before starting watch (supports ctx cancellation)
	log.FromContext(ctx).Info("start cr watcher waiting for initial list")
	var middlewares []v1.Middleware
	for {
		middlewares, err = k8s.ListMiddlewares(ctx, cli, "", nil)
		if err == nil {
			break
		}
		log.FromContext(ctx).Error(err, "get middlewares error")
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	log.FromContext(ctx).Info("start cr watcher initial list done")

	// Start watching
	log.FromContext(ctx).V(1).Info("watcher get middlewares", "count", len(middlewares), "middlewares", middlewareLogRefs(middlewares))
	for _, mid := range middlewares {
		// Get CR
		var (
			baseline                              v1.MiddlewareBaseline
			operatorBaseline                      v1.MiddlewareOperatorBaseline
			cr                                    *unstructured.Unstructured
			gvk                                   schema.GroupVersionKind
			name, namespace, operatorBaselineName string
		)

		baseline, err = middlewarebaseline.Get(ctx, cli, mid.Spec.Baseline, mid.Labels[v1.LabelPackageName])
		if err != nil {
			log.FromContext(ctx).Error(err, "get baseline error")
			continue
		}

		if mid.Spec.OperatorBaseline.Name != "" {
			operatorBaselineName = mid.Spec.OperatorBaseline.Name
		} else {
			operatorBaselineName = baseline.Spec.OperatorBaseline.Name
		}

		if operatorBaselineName != "" {
			operatorBaseline, err = middlewareoperatorbaseline.Get(ctx, cli, operatorBaselineName, mid.Labels[v1.LabelPackageName])
			if err != nil {
				log.FromContext(ctx).Error(err, "get operator baseline error")
				continue
			}

			for _, temp := range operatorBaseline.Spec.GVKs {
				if temp.Name == baseline.Spec.OperatorBaseline.GvkName {
					gvk = schema.GroupVersionKind{
						Group:   temp.Group,
						Kind:    temp.Kind,
						Version: temp.Version,
					}
					break
				}
			}
		} else {
			gvk = schema.GroupVersionKind{
				Group:   baseline.Spec.GVK.Group,
				Kind:    baseline.Spec.GVK.Kind,
				Version: baseline.Spec.GVK.Version,
			}
		}
		name = mid.Name
		namespace = mid.Namespace

		cr, err = k8s.GetCustomResource(ctx, cli, name, namespace, gvk)
		if err != nil {
			log.FromContext(ctx).Error(err, "get custom resource error")
			continue
		}

		cr.SetGroupVersionKind(gvk)
		cr.SetName(name)
		cr.SetNamespace(namespace)
		log.FromContext(ctx).V(1).Info("found CR to watch", "gvk", gvk, "namespace", namespace, "name", name)

		// Sync registration and dynamic CR watching are independent goroutines. The
		// Add handler also notifies, so either ordering observes the initial state.
		crForSync := cr.DeepCopy()
		go func() {
			if syncErr := synchronizer.SyncCustomResourceV2(ctx, cli, crForSync, &mid); syncErr != nil {
				log.FromContext(ctx).Error(syncErr, "custom resource sync exited with error", "gvk", crForSync.GroupVersionKind(), "namespace", crForSync.GetNamespace(), "name", crForSync.GetName())
			}
		}()
		if _, _, watcherErr := EnsureCRWatcher(ctx, cli, cr, mid.Name, mid.Namespace); watcherErr != nil {
			return watcherErr
		}
	}
	return nil
}

// ReleaseCRWatcher removes obj from its shared namespace/GVK watcher. A watcher
// is stopped only after its final member is released.
func ReleaseCRWatcher(ctx context.Context, obj *unstructured.Unstructured) {
	if obj == nil {
		return
	}
	key := fmt.Sprintf("%s/%s", obj.GetNamespace(), obj.GroupVersionKind().String())
	value, ok := CustomResourceWatcherMap.Load(key)
	if !ok {
		return
	}
	cw, ok := value.(*CustomResourceWatcher)
	if !ok {
		return
	}
	removed, empty := cw.removeMember(obj.GetName())
	if !removed || !empty {
		return
	}
	log.FromContext(ctx).Info("close cr watcher", "key", cw.GetKey())
	CustomResourceWatcherMap.CompareAndDelete(cw.GetKey(), cw)
}

// CloseCRWatcher is retained for callers that use the older name.
func CloseCRWatcher(ctx context.Context, obj *unstructured.Unstructured) {
	ReleaseCRWatcher(ctx, obj)
}

type customResourceUpdateNotifier func(namespace, middlewareName string)

// NewResourceEventHandlerFuncs creates resource event handler functions.
func NewResourceEventHandlerFuncs(ctx context.Context, cli client.Client, name, namespace string) cache.ResourceEventHandlerFuncs {
	return newResourceEventHandlerFuncs(ctx, cli, notifyCustomResourceSync)
}

func newResourceEventHandlerFuncs(ctx context.Context, cli client.Client, notify customResourceUpdateNotifier) cache.ResourceEventHandlerFuncs {
	return cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			cr, ok := customResourceEventObject(obj)
			if !ok {
				return
			}
			if _, ok := cr.GetLabels()[v1.LabelPackageName]; !ok {
				return
			}

			log.FromContext(ctx).V(1).Info("CR CREATE event", customResourceLogFields("cr", cr)...)
			// Initial informer LIST objects arrive as Add events. Notify here so a
			// status transition between SyncV2's first read and LIST cannot be lost.
			notify(cr.GetNamespace(), middlewareNameForCustomResource(cr))
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			oldCR, ok := customResourceEventObject(oldObj)
			if !ok {
				return
			}
			newCR, ok := customResourceEventObject(newObj)
			if !ok {
				return
			}
			if _, ok := oldCR.GetLabels()[v1.LabelPackageName]; !ok {
				return
			}

			// Handle custom resource update event; oldObj is the old object, newObj is the updated object
			oldVersion := oldCR.GetResourceVersion()

			logFields := customResourceLogFields("new", newCR)
			logFields = append(logFields, "oldResourceVersion", oldVersion, "oldGeneration", oldCR.GetGeneration())
			log.FromContext(ctx).V(1).Info("CR UPDATE event", logFields...)

			// Compare versions
			if customResourceStatusChanged(oldCR, newCR) {
				notify(newCR.GetNamespace(), middlewareNameForCustomResource(newCR))
			}
		},
		DeleteFunc: func(obj interface{}) {
			cr, ok := customResourceEventObject(obj)
			if !ok {
				return
			}
			if _, ok := cr.GetLabels()[v1.LabelPackageName]; !ok {
				return
			}

			// Handle custom resource delete event
			// obj is the deleted custom resource object
			log.FromContext(ctx).V(1).Info("CR DELETE event", customResourceLogFields("cr", cr)...)

			// If OwnerReferences no longer exist, stop watching
			for _, reference := range cr.GetOwnerReferences() {
				mid, err := k8s.GetMiddleware(ctx, cli, reference.Name, cr.GetNamespace())
				if err != nil {
					if apiErrors.IsNotFound(err) {
						// Stop watching
						ReleaseCRWatcher(ctx, cr)
						synchronizer.StopSyncCustomResource(fmt.Sprintf(synchronizer.SyncCustomResourceStopChanMapKey, cr.GroupVersionKind().String(), cr.GetNamespace(), cr.GetName()))
					}
					return
				}
				if v1.IsMiddlewareReconcileWriteSuspended(mid.GetAnnotations(), mid.Status.Conditions) {
					log.FromContext(ctx).Info("skipping custom resource rebuild because owning Middleware reconciliation is suspended",
						"middleware", mid.Name,
						"namespace", mid.Namespace,
						"gvk", cr.GroupVersionKind().String(),
						"customResource", cr.GetName(),
						"annotation", v1.AnnotationSuspendReconcile,
					)
					return
				}
			}

			cr.SetResourceVersion("")
			err := k8s.CreateCustomResource(ctx, cli, cr)
			if err != nil {
				log.FromContext(ctx).Error(err, "create custom resource error")
				return
			}

		},
	}
}

func customResourceStatusChanged(oldCR, newCR *unstructured.Unstructured) bool {
	if oldCR.GetResourceVersion() == newCR.GetResourceVersion() {
		return false
	}
	return !equality.Semantic.DeepEqual(oldCR.Object["status"], newCR.Object["status"])
}

func middlewareNameForCustomResource(cr *unstructured.Unstructured) string {
	for _, reference := range cr.GetOwnerReferences() {
		if reference.Kind == "Middleware" && reference.APIVersion == v1.GroupVersion.String() && reference.Name != "" {
			return reference.Name
		}
	}
	return cr.GetName()
}

func notifyCustomResourceSync(namespace, middlewareName string) {
	if middlewareName != "" && synchronizer.NotifyMiddleware(namespace, middlewareName) {
		return
	}
	synchronizer.NotifyNamespace(namespace)
}

func customResourceEventObject(obj interface{}) (*unstructured.Unstructured, bool) {
	cr, ok := obj.(*unstructured.Unstructured)
	return cr, ok
}

func customResourceLogFields(prefix string, cr *unstructured.Unstructured) []interface{} {
	if cr == nil {
		return []interface{}{prefix + "Nil", true}
	}
	labels := cr.GetLabels()
	return []interface{}{
		prefix + "APIVersion", cr.GetAPIVersion(),
		prefix + "Kind", cr.GetKind(),
		prefix + "Namespace", cr.GetNamespace(),
		prefix + "Name", cr.GetName(),
		prefix + "ResourceVersion", cr.GetResourceVersion(),
		prefix + "Generation", cr.GetGeneration(),
		prefix + "PackageName", labels[v1.LabelPackageName],
		prefix + "PackageVersion", labels[v1.LabelPackageVersion],
		prefix + "Component", labels[v1.LabelComponent],
		prefix + "Definition", labels["middleware.cn/definition"],
		prefix + "OwnerReferences", len(cr.GetOwnerReferences()),
	}
}

func middlewareLogRefs(middlewares []v1.Middleware) []string {
	const limit = 20
	count := len(middlewares)
	if count == 0 {
		return nil
	}
	logCount := count
	if logCount > limit {
		logCount = limit
	}
	refs := make([]string, 0, logCount+1)
	for i := 0; i < logCount; i++ {
		refs = append(refs, fmt.Sprintf("%s/%s", middlewares[i].Namespace, middlewares[i].Name))
	}
	if count > limit {
		refs = append(refs, fmt.Sprintf("...+%d", count-limit))
	}
	return refs
}
