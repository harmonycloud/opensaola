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
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/OpenSaola/opensaola/api/v1"
	"github.com/OpenSaola/opensaola/internal/k8s"
	"github.com/OpenSaola/opensaola/internal/service/middlewarebaseline"
	"github.com/OpenSaola/opensaola/internal/service/middlewareoperatorbaseline"
	"github.com/OpenSaola/opensaola/internal/service/synchronizer"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/cache"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

type CustomResourceWatcher struct {
	GVK       schema.GroupVersionKind // gvk
	Namespace string                  // namespace
	StopChan  chan struct{}           // stop channel
	Counter   atomic.Int32            // reference count (atomic)
}

var CustomResourceWatcherMap sync.Map

func StopAllCRWatchers() {
	CustomResourceWatcherMap.Range(func(key, value any) bool {
		if cw, ok := value.(*CustomResourceWatcher); ok {
			safeClose(cw.StopChan)
		}
		CustomResourceWatcherMap.Delete(key)
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
	}
	cw.Counter.Store(1)
	return cw
}

// StartCRWatcher starts the CR watcher
func StartCRWatcher(ctx context.Context, cli client.Client) error {
	return startCRWatcherImpl(ctx, cli, 0)
}

func startCRWatcherImpl(ctx context.Context, cli client.Client, attempt int) (err error) {
	defer func() {
		r := recover()
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
	log.FromContext(ctx).V(1).Info("watcher get middlewares", "middlewares", middlewares)
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

		// Check if the CR watcher already exists; if so, increment the reference count
		go synchronizer.SyncCustomResourceV2(ctx, cli, cr, &mid)
		cw := NewCRWatcher(cr.GroupVersionKind(), cr.GetNamespace())
		if cwCache, ok := CustomResourceWatcherMap.Load(cw.GetKey()); ok {
			log.FromContext(ctx).Info("CR watcher already exists", "key", cw.GetKey())
			cw = cwCache.(*CustomResourceWatcher)
			cw.Counter.Add(1)
			continue
		}

		log.FromContext(ctx).Info("creating CR watcher", "key", cw.GetKey())
		CustomResourceWatcherMap.Store(cw.GetKey(), cw)
		go k8s.NewInformerOptUnit(ctx, cli, cw.StopChan, cw.GVK, cw.Namespace, NewResourceEventHandlerFuncs(ctx, cli, mid.Name, mid.Namespace))
	}
	return nil
}

// CloseCRWatcher closes a CR watcher
func CloseCRWatcher(ctx context.Context, obj *unstructured.Unstructured) {
	temp := NewCRWatcher(obj.GroupVersionKind(), obj.GetNamespace())
	v, ok := CustomResourceWatcherMap.Load(temp.GetKey())
	if ok {
		cw := v.(*CustomResourceWatcher)
		// Concurrency-safe reference counting: only the transition from 1 -> 0 performs close/delete.
		newVal := cw.Counter.Add(-1)
		if newVal == 0 {
			log.FromContext(ctx).Info("close cr watcher", "key", cw.GetKey())
			log.FromContext(ctx).Info("send stop chan success", "key", cw.GetKey())
			safeClose(cw.StopChan)
			log.FromContext(ctx).Info("close cr watcher success", "key", cw.GetKey())
			CustomResourceWatcherMap.Delete(cw.GetKey())
			log.FromContext(ctx).Info("delete cr watcher success", "key", cw.GetKey())
		} else if newVal < 0 {
			log.FromContext(ctx).Error(nil, "cr watcher refcount < 0", "key", cw.GetKey())
		}
	} else {
		log.FromContext(ctx).Error(nil, "not found cr watcher", "key", temp.GetKey())
	}
}

func safeClose(ch chan struct{}) {
	defer func() {
		if r := recover(); r != nil {
			ctrl.Log.WithName("watcher").Error(fmt.Errorf("panic: %v", r), "panic recovered in watcher safeClose")
		}
	}()
	close(ch)
}

// // GetCustomResourceWithBaseInfo retrieves custom resource base info: gvk, name, namespace
// func GetCustomResourceWithBaseInfo(ctx context.Context, cli client.Client, m *v1.Middleware) (*unstructured.Unstructured, error) {
//	cr := new(unstructured.Unstructured)
//
//	return cr, nil
// }

// NewResourceEventHandlerFuncs creates resource event handler functions
func NewResourceEventHandlerFuncs(ctx context.Context, cli client.Client, name, namespace string) cache.ResourceEventHandlerFuncs {
	return cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			if _, ok := obj.(*unstructured.Unstructured).GetLabels()[v1.LabelPackageName]; !ok {
				return
			}

			log.FromContext(ctx).V(1).Info("CR CREATE event", "obj", obj)

			// // obj is the received custom resource object
			// err := customresource.CoverStatus(ctx, cli, *obj.(*unstructured.Unstructured))
			// if err != nil {
			// 	logger.Log.Errorf("coverStatus error: %v", err)
			// 	return
			// }
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			if _, ok := oldObj.(*unstructured.Unstructured).GetLabels()[v1.LabelPackageName]; !ok {
				return
			}

			log.FromContext(ctx).V(1).Info("CR UPDATE event", "oldObj", oldObj, "newObj", newObj)

			// Handle custom resource update event; oldObj is the old object, newObj is the updated object
			oldVersion := oldObj.(*unstructured.Unstructured).GetResourceVersion()
			newVersion := newObj.(*unstructured.Unstructured).GetResourceVersion()

			log.FromContext(ctx).V(1).Info("CR UPDATE event", "oldVersion", oldVersion, "newVersion", newVersion)

			// Compare versions
			if oldVersion != newVersion {
				// customresource.RestoreIfIllegalUpdate(ctx, cli, oldObj.(*unstructured.Unstructured), newObj.(*unstructured.Unstructured))
			}
			// err := customresource.CoverStatus(ctx, cli, *newObj.(*unstructured.Unstructured))
			// if err != nil {
			// 	logger.Log.Errorf("coverStatus error: %v", err)
			// 	return
			// }
		},
		DeleteFunc: func(obj interface{}) {
			if _, ok := obj.(*unstructured.Unstructured).GetLabels()[v1.LabelPackageName]; !ok {
				return
			}

			// Handle custom resource delete event
			// obj is the deleted custom resource object
			log.FromContext(ctx).V(1).Info("CR DELETE event", "obj", obj)

			// If OwnerReferences no longer exist, stop watching
			for _, reference := range obj.(*unstructured.Unstructured).GetOwnerReferences() {
				// Get middleware
				// var err error
				// middleware := new(v1.Middleware)
				_, err := k8s.GetMiddleware(ctx, cli, reference.Name, obj.(*unstructured.Unstructured).GetNamespace())
				if err != nil {
					if apiErrors.IsNotFound(err) {
						// Stop watching
						cr := obj.(*unstructured.Unstructured)
						CloseCRWatcher(ctx, cr)
						if resourceStop, ok := synchronizer.SyncCustomResourceStopChanMap.Load(fmt.Sprintf(synchronizer.SyncCustomResourceStopChanMapKey, cr.GroupVersionKind().String(), cr.GetNamespace(), cr.GetName())); ok {
							close(resourceStop.(chan struct{}))
							synchronizer.SyncCustomResourceStopChanMap.Delete(fmt.Sprintf(synchronizer.SyncCustomResourceStopChanMapKey, cr.GroupVersionKind().String(), cr.GetNamespace(), cr.GetName()))
						}
					}
					return
				}
			}

			obj.(*unstructured.Unstructured).SetResourceVersion("")
			err := k8s.CreateCustomResource(ctx, cli, obj.(*unstructured.Unstructured))
			if err != nil {
				log.FromContext(ctx).Error(err, "create custom resource error")
				return
			}

			// err = customresource.CoverStatus(ctx, cli, *obj.(*unstructured.Unstructured))
			// if err != nil && !apiErrors.IsNotFound(err) {
			// 	logger.Log.Errorf("coverStatus error: %v", err)
			// 	return
			// }
		},
	}
}

// CompareNewAndPublished compares the new CustomResource with the published CustomResource
// func CompareNewAndPublished(ctx context.Context, cli client.Client, CustomResource *unstructured.Unstructured, m *v1.Middleware) error {
// 	// Get the published CustomResource
// 	publishCustomResource, err := customresource.GetNeedPublishCustomResource(ctx, cli, m)
// 	if err != nil {
// 		return fmt.Errorf("parse CustomResource error: %w", err)
// 	}
//
// 	logger.Log.Debug("comparing new CustomResource with published")
// 	// Compare CustomResource
// 	isSame, err := CompareCustomResourceSpec(ctx, CustomResource, publishCustomResource)
// 	if err != nil {
// 		return fmt.Errorf("compare CustomResource spec error: %w", err)
// 	}
// 	if !isSame {
// 		logger.Log.Debugj(map[string]interface{}{
// 			"amsg":                  "new CustomResource differs from published",
// 			"CustomResource":        CustomResource,
// 			"publishCustomResource": publishCustomResource,
// 		})
//
// 		// Update middleware, sync CustomResource spec
// 		if m.Annotations != nil {
// 			m.Annotations[viper.GetString("annotations.CustomResourcesync")] = strconv.FormatInt(m.Generation, 10)
// 		}
// 		var specByte []byte
// 		specByte, err = json.Marshal(CustomResource.Object["spec"])
// 		if err != nil {
// 			return fmt.Errorf("marshal spec error: %w", err)
// 		}
// 		m.Spec.Parameters = apiruntime.RawExtension{
// 			Raw: specByte,
// 		}
//
// 		err = k8s.UpdateCustomResource(ctx, cli, publishCustomResource)
// 		if err != nil {
// 			return fmt.Errorf("update CustomResource error: %w", err)
// 		}
// 	}
// 	return nil
// }
