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
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

func MiddlewareBaselineGroupVersionKind() schema.GroupVersionKind {
	return schema.GroupVersionKind{
		Group:   "middleware.cn",
		Version: "v1",
		Kind:    "MiddlewareBaseline",
	}
}

func MiddlewareBaselineGroupResource() schema.GroupResource {
	return schema.GroupResource{
		Group:    MiddlewareBaselineGroupVersionKind().Group,
		Resource: MiddlewareBaselineGroupVersionKind().Kind,
	}
}

// CreateMiddlewareBaseline creates a MiddlewareBaseline.
func CreateMiddlewareBaseline(ctx context.Context, cli client.Client, m *v1.MiddlewareBaseline) error {
	// First check if it already exists
	_, err := GetMiddlewareBaseline(ctx, cli, m.Name)
	if err == nil {
		return errors.NewAlreadyExists(MiddlewareBaselineGroupResource(), m.Name)
	}
	return cli.Create(ctx, m)
}

// DeleteMiddlewareBaseline deletes a MiddlewareBaseline.
func DeleteMiddlewareBaseline(ctx context.Context, cli client.Client, m *v1.MiddlewareBaseline) error {
	return cli.Delete(ctx, m)
}

// UpdateMiddlewareBaseline updates a MiddlewareBaseline.
func UpdateMiddlewareBaseline(ctx context.Context, cli client.Client, m *v1.MiddlewareBaseline) error {
	return cli.Update(ctx, m)
}

// GetMiddlewareBaseline retrieves a MiddlewareBaseline.
func GetMiddlewareBaseline(ctx context.Context, cli client.Client, name string) (*v1.MiddlewareBaseline, error) {
	m := new(v1.MiddlewareBaseline)
	err := cli.Get(ctx, client.ObjectKey{
		Name: name,
	}, m)
	if err != nil {
		return nil, err
	}
	return m, nil
}

// ListMiddlewareBaselines lists MiddlewareBaselines.
func ListMiddlewareBaselines(ctx context.Context, cli client.Client, labelsSelector client.MatchingLabels) ([]v1.MiddlewareBaseline, error) {
	list := new(v1.MiddlewareBaselineList)
	err := cli.List(ctx, list, labelsSelector)
	if err != nil {
		return nil, err
	}

	var results []v1.MiddlewareBaseline
	for _, item := range list.Items {
		results = append(results, item)
	}
	return results, nil
}

// UpdateMiddlewareBaselineStatus updates the MiddlewareBaseline status.
func UpdateMiddlewareBaselineStatus(ctx context.Context, cli client.Client, m *v1.MiddlewareBaseline) error {
	attempt := 0
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		// First attempt: use cache (fast path).
		// Retry: use APIReader to bypass stale cache and get latest resourceVersion.
		// (see English comment above)
		m.Status.ObservedGeneration = m.Generation
		var (
			now *v1.MiddlewareBaseline
			err error
		)
		if attempt > 0 && statusAPIReader != nil {
			now = new(v1.MiddlewareBaseline)
			if err = statusAPIReader.Get(ctx, client.ObjectKey{Name: m.Name}, now); err != nil {
				return fmt.Errorf("get middleware baseline error: %w", err)
			}
		} else {
			now, err = GetMiddlewareBaseline(ctx, cli, m.Name)
			if err != nil {
				return fmt.Errorf("get middleware baseline error: %w", err)
			}
		}
		attempt++

		log.FromContext(ctx).V(1).Info("Update MiddlewareBaseline status", "version", now.ResourceVersion)
		now.Status = m.Status

		// Retry updating the CR
		err = cli.Status().Update(ctx, now)
		if err != nil {
			return fmt.Errorf("update middleware baseline status error: %w", err)
		}
		return nil
	})
}
