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

	"github.com/opensaola/opensaola/api/v1"
	"github.com/opensaola/opensaola/internal/cache"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

func MiddlewareGroupVersionKind() schema.GroupVersionKind {
	return schema.GroupVersionKind{
		Group:   "middleware.cn",
		Version: "v1",
		Kind:    "Middleware",
	}
}

func MiddlewareGroupResource() schema.GroupResource {
	return schema.GroupResource{
		Group:    MiddlewareGroupVersionKind().Group,
		Resource: MiddlewareGroupVersionKind().Kind,
	}
}

// CreateMiddleware creates a Middleware.
func CreateMiddleware(ctx context.Context, cli client.Client, m *v1.Middleware) error {
	// First check if it already exists
	_, err := GetMiddleware(ctx, cli, m.Name, m.Namespace)
	if err == nil {
		return errors.NewAlreadyExists(MiddlewareGroupResource(), m.Name)
	}
	return cli.Create(ctx, m)
}

func DeleteMiddleware(ctx context.Context, cli client.Client, m *v1.Middleware) error {
	return cli.Delete(ctx, m)
}

func UpdateMiddleware(ctx context.Context, cli client.Client, m *v1.Middleware) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		now, err := GetMiddleware(ctx, cli, m.Name, m.Namespace)
		if err != nil {
			return err
		}
		now.Spec = m.Spec
		now.Labels = m.Labels
		now.Annotations = m.Annotations
		return cli.Update(ctx, now)
	})
}

var MiddlewareCache = cache.New[string, v1.Middleware](0)

func GetMiddleware(ctx context.Context, cli client.Client, name, namespace string) (*v1.Middleware, error) {
	m := new(v1.Middleware)
	err := cli.Get(ctx, client.ObjectKey{
		Name:      name,
		Namespace: namespace,
	}, m)
	if err != nil {
		return nil, err
	}
	// TODO: evaluate whether this cache is still needed
	MiddlewareCache.Set(types.NamespacedName{Name: name, Namespace: namespace}.String(), *m.DeepCopy())
	return m, nil
}

func ListMiddlewares(ctx context.Context, cli client.Client, namespace string, labelsSelector client.MatchingLabels) ([]v1.Middleware, error) {
	list := new(v1.MiddlewareList)
	err := cli.List(ctx, list, client.InNamespace(namespace), labelsSelector)
	if err != nil {
		return nil, err
	}

	var results []v1.Middleware
	for _, item := range list.Items {
		results = append(results, item)
	}
	return results, nil
}

// UpdateMiddlewareStatus updates the Middleware status.
func UpdateMiddlewareStatus(ctx context.Context, cli client.Client, m *v1.Middleware) error {
	attempt := 0
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var (
			now *v1.Middleware
			err error
		)
		// First attempt: use cache (fast path).
		// Retry: use APIReader to bypass stale cache and get latest resourceVersion.
		// (see English comment above)
		if attempt > 0 && statusAPIReader != nil {
			now = new(v1.Middleware)
			if err = statusAPIReader.Get(ctx, client.ObjectKey{Name: m.Name, Namespace: m.Namespace}, now); err != nil {
				return fmt.Errorf("get middleware error: %w", err)
			}
		} else {
			now, err = GetMiddleware(ctx, cli, m.Name, m.Namespace)
			if err != nil {
				return fmt.Errorf("get middleware error: %w", err)
			}
		}
		attempt++

		// ObservedGeneration must be monotonically increasing: avoid overwriting with a smaller value.
		if m.Status.ObservedGeneration < now.Status.ObservedGeneration {
			m.Status.ObservedGeneration = now.Status.ObservedGeneration
		}

		// if now.Status.ObservedGeneration >= m.Status.ObservedGeneration {
		// 	return nil
		// }

		log.FromContext(ctx).V(1).Info("Update Middleware status", "version", now.ResourceVersion)

		// isSame, err := tools.CompareJson(ctx, now.Status, m.Status)
		// if err != nil {
		// 	return err
		// }
		//
		// if !isSame {
		// Compare whether status has changed
		if equality.Semantic.DeepEqual(now.Status, m.Status) {
			return nil
		}
		now.Status = m.Status

		// Retry updating the CR
		err = cli.Status().Update(ctx, now)
		if err != nil {
			return fmt.Errorf("update middleware status error: %w", err)
		}
		// }
		return nil
	})
}

// PatchMiddlewareStatusFields reads the latest Middleware from API Server,
// applies the mutate function to modify only specific fields of its status,
// then writes back. Unlike UpdateMiddlewareStatus which does full replacement,
// this only modifies the fields touched by mutate, preventing concurrent
// writers (e.g. SyncCustomResourceV2) from overwriting controller's state/conditions.
// (see English comment above)
func PatchMiddlewareStatusFields(ctx context.Context, cli client.Client, name, namespace string, mutate func(*v1.MiddlewareStatus)) error {
	attempt := 0
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		now := new(v1.Middleware)
		var err error
		if attempt > 0 && statusAPIReader != nil {
			err = statusAPIReader.Get(ctx, client.ObjectKey{Name: name, Namespace: namespace}, now)
		} else {
			err = cli.Get(ctx, client.ObjectKey{Name: name, Namespace: namespace}, now)
		}
		if err != nil {
			return err
		}
		attempt++

		before := now.Status.DeepCopy()
		mutate(&now.Status)
		if equality.Semantic.DeepEqual(*before, now.Status) {
			return nil
		}
		return cli.Status().Update(ctx, now)
	})
}
