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

	"github.com/OpenSaola/opensaola/api/v1"
	"github.com/OpenSaola/opensaola/internal/cache"
	"github.com/OpenSaola/opensaola/internal/resource/logger"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func MiddlewareActionGroupVersionKind() schema.GroupVersionKind {
	return schema.GroupVersionKind{
		Group:   "middleware.cn",
		Version: "v1",
		Kind:    "MiddlewareAction",
	}
}

func MiddlewareActionGroupResource() schema.GroupResource {
	return schema.GroupResource{
		Group:    MiddlewareActionGroupVersionKind().Group,
		Resource: MiddlewareActionGroupVersionKind().Kind,
	}
}

// CreateMiddlewareAction creates a MiddlewareAction.
func CreateMiddlewareAction(ctx context.Context, cli client.Client, m *v1.MiddlewareAction) error {
	// First check if it already exists
	_, err := GetMiddleware(ctx, cli, m.Name, m.Namespace)
	if err == nil {
		return errors.NewAlreadyExists(MiddlewareGroupResource(), m.Name)
	}
	return cli.Create(ctx, m)
}

func DeleteMiddlewareAction(ctx context.Context, cli client.Client, m *v1.MiddlewareAction) error {
	return cli.Delete(ctx, m)
}

func UpdateMiddlewareAction(ctx context.Context, cli client.Client, m *v1.MiddlewareAction) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		now, err := GetMiddlewareAction(ctx, cli, m.Name, m.Namespace)
		if err != nil {
			return err
		}
		now.Spec = m.Spec
		return cli.Update(ctx, now)
	})
}

var MiddlewareActionCache = cache.New[string, v1.MiddlewareAction](0)

func GetMiddlewareAction(ctx context.Context, cli client.Client, name, namespace string) (*v1.MiddlewareAction, error) {
	m := new(v1.MiddlewareAction)
	err := cli.Get(ctx, client.ObjectKey{
		Name:      name,
		Namespace: namespace,
	}, m)
	if err != nil {
		return nil, err
	}
	// TODO: evaluate whether this cache is still needed
	MiddlewareActionCache.Set(types.NamespacedName{Name: name, Namespace: namespace}.String(), *m.DeepCopy())
	return m, nil
}

func ListMiddlewareActions(ctx context.Context, cli client.Client, namespace string, labelsSelector client.MatchingLabels) ([]v1.MiddlewareAction, error) {
	list := new(v1.MiddlewareActionList)
	err := cli.List(ctx, list, client.InNamespace(namespace), labelsSelector)
	if err != nil {
		return nil, err
	}

	var results []v1.MiddlewareAction
	for _, item := range list.Items {
		results = append(results, item)
	}
	return results, nil
}

// UpdateMiddlewareActionStatus updates the MiddlewareAction status.
func UpdateMiddlewareActionStatus(ctx context.Context, cli client.Client, m *v1.MiddlewareAction) error {
	attempt := 0
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		// First attempt: use cache (fast path).
		// Retry: use APIReader to bypass stale cache and get latest resourceVersion.
		// (see English comment above)
		m.Status.ObservedGeneration = m.Generation
		var (
			now *v1.MiddlewareAction
			err error
		)
		if attempt > 0 && statusAPIReader != nil {
			now = new(v1.MiddlewareAction)
			if err = statusAPIReader.Get(ctx, client.ObjectKey{Name: m.Name, Namespace: m.Namespace}, now); err != nil {
				return fmt.Errorf("get MiddlewareAction error: %w", err)
			}
		} else {
			now, err = GetMiddlewareAction(ctx, cli, m.Name, m.Namespace)
			if err != nil {
				return fmt.Errorf("get MiddlewareAction error: %w", err)
			}
		}
		attempt++

		logger.Log.Debugj(map[string]interface{}{
			"amsg":    "Update MiddlewareAction status",
			"version": now.ResourceVersion,
		})

		// Skip status write if no change
		if equality.Semantic.DeepEqual(now.Status, m.Status) {
			return nil
		}
		now.Status = m.Status

		// Retry updating the CR
		err = cli.Status().Update(ctx, now)
		if err != nil {
			return fmt.Errorf("update MiddlewareAction status error: %w", err)
		}
		return nil
	})
}
