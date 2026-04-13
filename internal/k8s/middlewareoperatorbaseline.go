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
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

func MiddlewareOperatorBaselineGroupVersionKind() schema.GroupVersionKind {
	return schema.GroupVersionKind{
		Group:   "middleware.cn",
		Version: "v1",
		Kind:    "MiddlewareOperatorBaseline",
	}
}

func MiddlewareOperatorBaselineGroupResource() schema.GroupResource {
	return schema.GroupResource{
		Group:    MiddlewareOperatorBaselineGroupVersionKind().Group,
		Resource: MiddlewareOperatorBaselineGroupVersionKind().Kind,
	}
}

// CreateMiddlewareOperatorBaseline creates a MiddlewareOperatorBaseline.
func CreateMiddlewareOperatorBaseline(ctx context.Context, cli client.Client, m *v1.MiddlewareOperatorBaseline) error {
	// First check if it already exists
	_, err := GetMiddlewareOperatorBaseline(ctx, cli, m.Name)
	if err == nil {
		return errors.NewAlreadyExists(MiddlewareOperatorBaselineGroupResource(), m.Name)
	}
	return cli.Create(ctx, m)
}

// DeleteMiddlewareOperatorBaseline deletes a MiddlewareOperatorBaseline.
func DeleteMiddlewareOperatorBaseline(ctx context.Context, cli client.Client, m *v1.MiddlewareOperatorBaseline) error {
	return cli.Delete(ctx, m)
}

// UpdateMiddlewareOperatorBaseline updates a MiddlewareOperatorBaseline.
func UpdateMiddlewareOperatorBaseline(ctx context.Context, cli client.Client, m *v1.MiddlewareOperatorBaseline) error {
	return cli.Update(ctx, m)
}

// GetMiddlewareOperatorBaseline retrieves a MiddlewareOperatorBaseline.
func GetMiddlewareOperatorBaseline(ctx context.Context, cli client.Client, name string) (*v1.MiddlewareOperatorBaseline, error) {
	m := new(v1.MiddlewareOperatorBaseline)
	err := cli.Get(ctx, client.ObjectKey{
		Name: name,
	}, m)
	if err != nil {
		return nil, err
	}
	return m, nil
}

// ListMiddlewareOperatorBaselines lists MiddlewareOperatorBaselines.
func ListMiddlewareOperatorBaselines(ctx context.Context, cli client.Client, labelsSelector client.MatchingLabels) ([]v1.MiddlewareOperatorBaseline, error) {
	list := new(v1.MiddlewareOperatorBaselineList)
	err := cli.List(ctx, list, labelsSelector)
	if err != nil {
		return nil, err
	}

	var results []v1.MiddlewareOperatorBaseline
	for _, item := range list.Items {
		results = append(results, item)
	}
	return results, nil
}

// UpdateMiddlewareOperatorBaselineStatus updates the MiddlewareOperatorBaseline status.
func UpdateMiddlewareOperatorBaselineStatus(ctx context.Context, cli client.Client, m *v1.MiddlewareOperatorBaseline) error {
	attempt := 0
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		// First attempt: use cache (fast path).
		// Retry: use APIReader to bypass stale cache and get latest resourceVersion.
		// (see English comment above)
		m.Status.ObservedGeneration = m.Generation
		var (
			now *v1.MiddlewareOperatorBaseline
			err error
		)
		if attempt > 0 && statusAPIReader != nil {
			now = new(v1.MiddlewareOperatorBaseline)
			if err = statusAPIReader.Get(ctx, client.ObjectKey{Name: m.Name}, now); err != nil {
				return fmt.Errorf("get middleware operator baseline error: %w", err)
			}
		} else {
			now, err = GetMiddlewareOperatorBaseline(ctx, cli, m.Name)
			if err != nil {
				return fmt.Errorf("get middleware operator baseline error: %w", err)
			}
		}
		attempt++

		log.FromContext(ctx).V(1).Info("Update MiddlewareOperatorBaseline status", "version", now.ResourceVersion)
		now.Status = m.Status

		// Retry updating the CR
		err = cli.Status().Update(ctx, now)
		if err != nil {
			return fmt.Errorf("update middleware operator baseline status error: %w", err)
		}
		return nil
	})
}
