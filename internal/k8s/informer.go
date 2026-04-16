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

package k8s

import (
	"context"
	"fmt"
	"runtime"
	"time"

	"github.com/harmonycloud/opensaola/internal/k8s/kubeclient"
	"k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	apiruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/tools/cache"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// NewInformerOptUnit creates a new informer operation unit.
func NewInformerOptUnit(ctx context.Context, cli client.Client, stopCh chan struct{}, gvk schema.GroupVersionKind, ns string, rehf cache.ResourceEventHandlerFuncs) error {
	return newInformerOptUnitImpl(ctx, cli, stopCh, gvk, ns, rehf, 0)
}

func newInformerOptUnitImpl(ctx context.Context, cli client.Client, stopCh chan struct{}, gvk schema.GroupVersionKind, ns string, rehf cache.ResourceEventHandlerFuncs, attempt int) (err error) {
	logger := log.FromContext(ctx)
	// Panic recovery
	defer func() {
		if err != nil {
			logger.Error(err, "NewInformerOptUnit error")
			return
		}
		if r := recover(); r != nil {
			logger.Error(fmt.Errorf("panic: %v", r), "NewInformerOptUnit")

			buf := make([]byte, 1024)
			n := runtime.Stack(buf, false)
			fmt.Printf("Stack trace:\n%s\n", string(buf[:n]))

			nextAttempt := attempt + 1
			delay := CalcPanicBackoff(nextAttempt)
			logger.Info("NewInformerOptUnit panic backoff restart", "attempt", nextAttempt, "delay", delay)
			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
			}
			go func() {
				_ = newInformerOptUnitImpl(ctx, cli, stopCh, gvk, ns, rehf, nextAttempt)
			}()
		}
	}()

	dynClient, err := kubeclient.GetDynClient()
	if err != nil {
		logger.Error(err, "Error building dynamic clientset")
		return err
	}

	resyncPeriod := 30 * time.Second
	cfg, err := ctrl.GetConfig()
	if err != nil {
		return fmt.Errorf("get kubeconfig error: %w", err)
	}
	discoveryClient, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return fmt.Errorf("new discovery client error: %w", err)
	}
	gvr, err := GetGroupVersionResource(discoveryClient, gvk)
	if err != nil {
		logger.Error(err, "Error getting GroupVersionResource")
		return err
	}

	informer := cache.NewSharedIndexInformer(
		&cache.ListWatch{
			ListWithContextFunc: func(ctx context.Context, options v1.ListOptions) (obj apiruntime.Object, err error) {
				defer func() {
					if r := recover(); r != nil {
						err = fmt.Errorf("informer list panic: %v", r)
					}
				}()
				if dynClient == nil {
					return nil, fmt.Errorf("dynamic client is nil")
				}
				return dynClient.Resource(gvr).Namespace(ns).List(ctx, options)
			},
			WatchFuncWithContext: func(ctx context.Context, options v1.ListOptions) (wi watch.Interface, err error) {
				defer func() {
					if r := recover(); r != nil {
						err = fmt.Errorf("informer watch panic: %v", r)
					}
				}()
				if dynClient == nil {
					return nil, fmt.Errorf("dynamic client is nil")
				}
				return dynClient.Resource(gvr).Namespace(ns).Watch(ctx, options)
			},
		},
		&unstructured.Unstructured{},
		resyncPeriod,
		cache.Indexers{
			"apiVersion": func(obj interface{}) ([]string, error) {
				unstructuredObj, ok := obj.(*unstructured.Unstructured)
				if !ok {
					return nil, fmt.Errorf("could not cast to *unstructured.Unstructured: %+v", obj)
				}
				return []string{unstructuredObj.GetAPIVersion()}, nil
			},
			"kind": func(obj interface{}) ([]string, error) {
				unstructuredObj, ok := obj.(*unstructured.Unstructured)
				if !ok {
					return nil, fmt.Errorf("could not cast to *unstructured.Unstructured: %+v", obj)
				}
				return []string{unstructuredObj.GetKind()}, nil
			},
		},
	)

	_, err = informer.AddEventHandler(rehf)
	if err != nil {
		logger.Error(err, "Error adding event handler")
		return err
	}

	logger.Info("Start watching", "gvk", gvk.String(), "ns", ns)

	// Ensure the informer exits when ctx is cancelled, preventing goroutine leaks during leader switch or manager stop.
	go func() {
		select {
		case <-ctx.Done():
			safeClose(stopCh)
		case <-stopCh:
		}
	}()

	informer.Run(stopCh)

	logger.Info("Stop watching", "gvk", gvk.String(), "ns", ns)
	return nil
}

func safeClose(ch chan struct{}) {
	defer func() {
		if r := recover(); r != nil {
			ctrl.Log.WithName("k8s").Error(fmt.Errorf("panic: %v", r), "panic recovered in informer safeClose")
		}
	}()
	close(ch)
}

// GetGroupVersionResource resolves a GroupVersionResource from a GroupVersionKind.
func GetGroupVersionResource(client discovery.ServerResourcesInterface, gvk schema.GroupVersionKind) (schema.GroupVersionResource, error) {
	resourceList, err := client.ServerResourcesForGroupVersion(gvk.GroupVersion().String())
	if err != nil {
		return schema.GroupVersionResource{}, err
	}

	for _, resource := range resourceList.APIResources {
		if resource.Kind == gvk.Kind {
			return schema.GroupVersionResource{
				Group:    gvk.Group,
				Version:  gvk.Version,
				Resource: resource.Name,
			}, nil
		}
	}

	return schema.GroupVersionResource{}, errors.NewNotFound(schema.GroupResource{}, "")
}
