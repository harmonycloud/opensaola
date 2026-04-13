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
	"sync"

	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/types"

	"github.com/OpenSaola/opensaola/api/v1"
	"github.com/OpenSaola/opensaola/pkg/resource/logger"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func MiddlewarePackageGroupVersionKind() schema.GroupVersionKind {
	return schema.GroupVersionKind{
		Group:   "middleware.cn",
		Version: "v1",
		Kind:    "MiddlewarePackage",
	}
}

func MiddlewarePackageGroupResource() schema.GroupResource {
	return schema.GroupResource{
		Group:    MiddlewarePackageGroupVersionKind().Group,
		Resource: MiddlewarePackageGroupVersionKind().Kind,
	}
}

// CreateMiddlewarePackage creates a MiddlewarePackage.
func CreateMiddlewarePackage(ctx context.Context, cli client.Client, m *v1.MiddlewarePackage) error {
	// First check if it already exists
	_, err := GetMiddlewarePackage(ctx, cli, m.Name)
	if err == nil {
		return apiErrors.NewAlreadyExists(MiddlewarePackageGroupResource(), m.Name)
	}
	return cli.Create(ctx, m)
}

// DeleteMiddlewarePackage deletes a MiddlewarePackage.
func DeleteMiddlewarePackage(ctx context.Context, cli client.Client, m *v1.MiddlewarePackage) error {
	return cli.Delete(ctx, m)
}

// UpdateMiddlewarePackage updates a MiddlewarePackage.
func UpdateMiddlewarePackage(ctx context.Context, cli client.Client, m *v1.MiddlewarePackage) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		now, err := GetMiddlewarePackage(ctx, cli, m.Name)
		if err != nil {
			return err
		}
		now.Spec = m.Spec
		now.Labels = m.Labels
		now.Annotations = m.Annotations
		return cli.Update(ctx, now)
	})
}

var MiddlewarePackageCache sync.Map

// GetMiddlewarePackage retrieves a MiddlewarePackage.
func GetMiddlewarePackage(ctx context.Context, cli client.Client, name string) (*v1.MiddlewarePackage, error) {
	m := new(v1.MiddlewarePackage)
	return m, retry.RetryOnConflict(retry.DefaultRetry, func() error {
		err := cli.Get(ctx, client.ObjectKey{
			Name: name,
		}, m)
		if err != nil {
			return errors.Wrap(err, "get middleware package error")
		}
		// Cache the MiddlewarePackage
		MiddlewarePackageCache.Store(types.NamespacedName{Name: name}, m)
		return nil
	})
}

// ListMiddlewarePackages lists MiddlewarePackages.
func ListMiddlewarePackages(ctx context.Context, cli client.Client, labelsSelector client.MatchingLabels) ([]v1.MiddlewarePackage, error) {
	list := new(v1.MiddlewarePackageList)
	err := cli.List(ctx, list, labelsSelector)
	if err != nil {
		return nil, err
	}

	var results []v1.MiddlewarePackage
	for _, item := range list.Items {
		results = append(results, item)
	}
	return results, nil
}

// UpdateMiddlewarePackageStatus updates the MiddlewarePackage status.
func UpdateMiddlewarePackageStatus(ctx context.Context, cli client.Client, m *v1.MiddlewarePackage) error {
	attempt := 0
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		// First attempt: use cache (fast path).
		// Retry: use APIReader to bypass stale cache and get latest resourceVersion.
		// (see English comment above)
		m.Status.ObservedGeneration = m.Generation
		var (
			now *v1.MiddlewarePackage
			err error
		)
		if attempt > 0 && statusAPIReader != nil {
			now = new(v1.MiddlewarePackage)
			if err = statusAPIReader.Get(ctx, client.ObjectKey{Name: m.Name}, now); err != nil {
				return fmt.Errorf("get middleware package error: %w", err)
			}
		} else {
			now, err = GetMiddlewarePackage(ctx, cli, m.Name)
			if err != nil {
				return fmt.Errorf("get middleware package error: %w", err)
			}
		}
		attempt++

		logger.Log.Debugj(map[string]interface{}{
			"amsg":    "Update MiddlewarePackage status",
			"version": now.ResourceVersion,
		})
		now.Status = m.Status

		// Retry updating the CR
		err = cli.Status().Update(ctx, now)
		if err != nil {
			return fmt.Errorf("update middleware configuration status error: %w", err)
		}
		return nil
	})
}
