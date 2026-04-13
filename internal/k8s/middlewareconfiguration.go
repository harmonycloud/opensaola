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

	"k8s.io/apimachinery/pkg/api/errors"

	"github.com/OpenSaola/opensaola/api/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

func MiddlewareConfigurationGroupVersionKind() schema.GroupVersionKind {
	return schema.GroupVersionKind{
		Group:   "middleware.cn",
		Version: "v1",
		Kind:    "MiddlewareConfiguration",
	}
}

func MiddlewareConfigurationGroupResource() schema.GroupResource {
	return schema.GroupResource{
		Group:    MiddlewareConfigurationGroupVersionKind().Group,
		Resource: MiddlewareConfigurationGroupVersionKind().Kind,
	}
}

// CreateMiddlewareConfiguration creates a MiddlewareConfiguration.
func CreateMiddlewareConfiguration(ctx context.Context, cli client.Client, m *v1.MiddlewareConfiguration) error {
	// First check if it already exists
	_, err := GetMiddlewareConfiguration(ctx, cli, m.Name)
	if err == nil {
		return errors.NewAlreadyExists(MiddlewareConfigurationGroupResource(), m.Name)
	}
	return cli.Create(ctx, m)
}

// DeleteMiddlewareConfiguration deletes a MiddlewareConfiguration.
func DeleteMiddlewareConfiguration(ctx context.Context, cli client.Client, m *v1.MiddlewareConfiguration) error {
	return cli.Delete(ctx, m)
}

// UpdateMiddlewareConfiguration updates a MiddlewareConfiguration.
func UpdateMiddlewareConfiguration(ctx context.Context, cli client.Client, m *v1.MiddlewareConfiguration) error {
	return cli.Update(ctx, m)
}

// GetMiddlewareConfiguration retrieves a MiddlewareConfiguration.
func GetMiddlewareConfiguration(ctx context.Context, cli client.Client, name string) (*v1.MiddlewareConfiguration, error) {
	m := new(v1.MiddlewareConfiguration)
	err := cli.Get(ctx, client.ObjectKey{
		Name: name,
	}, m)
	if err != nil {
		return nil, err
	}
	return m, nil
}

// ListMiddlewareConfigurations lists MiddlewareConfigurations.
func ListMiddlewareConfigurations(ctx context.Context, cli client.Client, labelsSelector client.MatchingLabels) ([]v1.MiddlewareConfiguration, error) {
	list := new(v1.MiddlewareConfigurationList)
	err := cli.List(ctx, list, labelsSelector)
	if err != nil {
		return nil, err
	}

	var results []v1.MiddlewareConfiguration
	for _, item := range list.Items {
		results = append(results, item)
	}
	return results, nil
}

// UpdateMiddlewareConfigurationStatus updates the MiddlewareConfiguration status.
func UpdateMiddlewareConfigurationStatus(ctx context.Context, cli client.Client, m *v1.MiddlewareConfiguration) error {
	attempt := 0
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		// First attempt: use cache (fast path).
		// Retry: use APIReader to bypass stale cache and get latest resourceVersion.
		// (see English comment above)
		m.Status.ObservedGeneration = m.Generation
		var (
			now *v1.MiddlewareConfiguration
			err error
		)
		if attempt > 0 && statusAPIReader != nil {
			now = new(v1.MiddlewareConfiguration)
			if err = statusAPIReader.Get(ctx, client.ObjectKey{Name: m.Name}, now); err != nil {
				return fmt.Errorf("get middleware configuration error: %w", err)
			}
		} else {
			now, err = GetMiddlewareConfiguration(ctx, cli, m.Name)
			if err != nil {
				return fmt.Errorf("get middleware configuration error: %w", err)
			}
		}
		attempt++

		log.FromContext(ctx).V(1).Info("Update MiddlewareConfiguration status", "version", now.ResourceVersion)
		now.Status = m.Status

		// Retry updating the CR
		err = cli.Status().Update(ctx, now)
		if err != nil {
			return fmt.Errorf("update middleware configuration status error: %w", err)
		}
		return nil
	})
}
