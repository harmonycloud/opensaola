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

	"github.com/harmonycloud/opensaola/api/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

func MiddlewareActionBaselineGroupVersionKind() schema.GroupVersionKind {
	return schema.GroupVersionKind{
		Group:   "middleware.cn",
		Version: "v1",
		Kind:    "MiddlewareActionBaseline",
	}
}

func MiddlewareActionBaselineGroupResource() schema.GroupResource {
	return schema.GroupResource{
		Group:    MiddlewareActionBaselineGroupVersionKind().Group,
		Resource: MiddlewareActionBaselineGroupVersionKind().Kind,
	}
}

// CreateMiddlewareActionBaseline creates a MiddlewareActionBaseline.
func CreateMiddlewareActionBaseline(ctx context.Context, cli client.Client, m *v1.MiddlewareActionBaseline) error {
	// First check if it already exists
	_, err := GetMiddlewareActionBaseline(ctx, cli, m.Name)
	if err == nil {
		return errors.NewAlreadyExists(MiddlewareActionBaselineGroupResource(), m.Name)
	}
	return cli.Create(ctx, m)
}

// DeleteMiddlewareActionBaseline deletes a MiddlewareActionBaseline.
func DeleteMiddlewareActionBaseline(ctx context.Context, cli client.Client, m *v1.MiddlewareActionBaseline) error {
	return cli.Delete(ctx, m)
}

// UpdateMiddlewareActionBaseline updates a MiddlewareActionBaseline.
func UpdateMiddlewareActionBaseline(ctx context.Context, cli client.Client, m *v1.MiddlewareActionBaseline) error {
	return cli.Update(ctx, m)
}

// GetMiddlewareActionBaseline retrieves a MiddlewareActionBaseline.
func GetMiddlewareActionBaseline(ctx context.Context, cli client.Client, name string) (*v1.MiddlewareActionBaseline, error) {
	m := new(v1.MiddlewareActionBaseline)
	err := cli.Get(ctx, client.ObjectKey{
		Name: name,
	}, m)
	if err != nil {
		return nil, err
	}
	return m, nil
}

// ListMiddlewareActionBaselines lists MiddlewareActionBaselines.
func ListMiddlewareActionBaselines(ctx context.Context, cli client.Client, labelsSelector client.MatchingLabels) ([]v1.MiddlewareActionBaseline, error) {
	list := new(v1.MiddlewareActionBaselineList)
	err := cli.List(ctx, list, labelsSelector)
	if err != nil {
		return nil, err
	}

	var results []v1.MiddlewareActionBaseline
	for _, item := range list.Items {
		results = append(results, item)
	}
	return results, nil
}

// UpdateMiddlewareActionBaselineStatus updates the MiddlewareActionBaseline status.
func UpdateMiddlewareActionBaselineStatus(ctx context.Context, cli client.Client, m *v1.MiddlewareActionBaseline) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		// Get the CR
		m.Status.ObservedGeneration = m.Generation
		now, err := GetMiddlewareActionBaseline(ctx, cli, m.Name)
		if err != nil {
			return fmt.Errorf("get middleware baseline error: %w", err)
		}

		log.FromContext(ctx).V(1).Info("Update MiddlewareActionBaseline status", "version", now.ResourceVersion)
		now.Status = m.Status

		// Retry updating the CR
		err = cli.Status().Update(ctx, now)
		if err != nil {
			return fmt.Errorf("update middleware baseline status error: %w", err)
		}
		return nil
	})
}
