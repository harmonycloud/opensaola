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
	"k8s.io/apimachinery/pkg/api/equality"
	"sync"

	"github.com/OpenSaola/opensaola/api/v1"
	"github.com/OpenSaola/opensaola/internal/resource/logger"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func MiddlewareOperatorGroupVersionKind() schema.GroupVersionKind {
	return schema.GroupVersionKind{
		Group:   "middleware.cn",
		Version: "v1",
		Kind:    "MiddlewareOperator",
	}
}

func MiddlewareOperatorGroupResource() schema.GroupResource {
	return schema.GroupResource{
		Group:    MiddlewareOperatorGroupVersionKind().Group,
		Resource: MiddlewareOperatorGroupVersionKind().Kind,
	}
}

// CreateMiddlewareOperator creates a MiddlewareOperator.
func CreateMiddlewareOperator(ctx context.Context, cli client.Client, m *v1.MiddlewareOperator) error {
	// First check if it already exists
	_, err := GetMiddleware(ctx, cli, m.Name, m.Namespace)
	if err == nil {
		return errors.NewAlreadyExists(MiddlewareGroupResource(), m.Name)
	}
	return cli.Create(ctx, m)
}

func DeleteMiddlewareOperator(ctx context.Context, cli client.Client, m *v1.MiddlewareOperator) error {
	return cli.Delete(ctx, m)
}

func UpdateMiddlewareOperator(ctx context.Context, cli client.Client, m *v1.MiddlewareOperator) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		now, err := GetMiddlewareOperator(ctx, cli, m.Name, m.Namespace)
		if err != nil {
			return err
		}
		now.Spec = m.Spec
		now.Labels = m.Labels
		now.Annotations = m.Annotations
		return cli.Update(ctx, now)
	})
}

var MiddlewareOperatorCache sync.Map

func GetMiddlewareOperator(ctx context.Context, cli client.Client, name, namespace string) (*v1.MiddlewareOperator, error) {
	m := new(v1.MiddlewareOperator)
	err := cli.Get(ctx, client.ObjectKey{
		Name:      name,
		Namespace: namespace,
	}, m)
	if err != nil {
		return nil, err
	}
	// TODO: evaluate whether this cache is still needed
	MiddlewareOperatorCache.Store(types.NamespacedName{Name: name, Namespace: namespace}.String(), m.DeepCopy())
	return m, nil
}

func ListMiddlewareOperators(ctx context.Context, cli client.Client, namespace string, labelsSelector client.MatchingLabels) ([]v1.MiddlewareOperator, error) {
	list := new(v1.MiddlewareOperatorList)
	err := cli.List(ctx, list, client.InNamespace(namespace), labelsSelector)
	if err != nil {
		return nil, err
	}

	var results []v1.MiddlewareOperator
	for _, item := range list.Items {
		results = append(results, item)
	}
	return results, nil
}

// UpdateMiddlewareOperatorRuntimeStatus updates only the 4 fields owned by the runtime controller:
// OperatorStatus, OperatorAvailable, Ready, Runtime.
// Each retry merges based on the latest cluster status, preserving fields written by
// the main controller (State/Conditions/ObservedGeneration/Reason).
func UpdateMiddlewareOperatorRuntimeStatus(
	ctx context.Context, cli client.Client,
	name, namespace string,
	operatorStatus map[string]appsv1.DeploymentStatus,
	operatorAvailable string,
	ready bool,
	runtime string,
) error {
	attempt := 0
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		// First attempt: use cache (fast path).
		// Retry: use APIReader to bypass stale cache and get latest resourceVersion.
		// (see English comment above)
		var (
			now *v1.MiddlewareOperator
			err error
		)
		if attempt > 0 && statusAPIReader != nil {
			now = new(v1.MiddlewareOperator)
			if err = statusAPIReader.Get(ctx, client.ObjectKey{Name: name, Namespace: namespace}, now); err != nil {
				return fmt.Errorf("get MiddlewareOperator error: %w", err)
			}
		} else {
			now, err = GetMiddlewareOperator(ctx, cli, name, namespace)
			if err != nil {
				return fmt.Errorf("get MiddlewareOperator error: %w", err)
			}
		}
		attempt++

		// Only merge runtime fields; preserve other fields written by the main controller.
		now.Status.OperatorStatus = operatorStatus
		now.Status.OperatorAvailable = operatorAvailable
		now.Status.Ready = ready
		now.Status.Runtime = runtime

		err = cli.Status().Update(ctx, now)
		if err != nil {
			return fmt.Errorf("update MiddlewareOperator runtime status error: %w", err)
		}
		return nil
	})
}

// UpdateMiddlewareOperatorStatus updates the MiddlewareOperator status.
func UpdateMiddlewareOperatorStatus(ctx context.Context, cli client.Client, m *v1.MiddlewareOperator) error {
	attempt := 0
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		// First attempt: use cache (fast path).
		// Retry: use APIReader to bypass stale cache and get latest resourceVersion.
		// (see English comment above)
		var (
			now *v1.MiddlewareOperator
			err error
		)
		if attempt > 0 && statusAPIReader != nil {
			now = new(v1.MiddlewareOperator)
			if err = statusAPIReader.Get(ctx, client.ObjectKey{Name: m.Name, Namespace: m.Namespace}, now); err != nil {
				return fmt.Errorf("get MiddlewareOperator error: %w", err)
			}
		} else {
			now, err = GetMiddlewareOperator(ctx, cli, m.Name, m.Namespace)
			if err != nil {
				return fmt.Errorf("get MiddlewareOperator error: %w", err)
			}
		}
		attempt++

		// ObservedGeneration must be monotonically increasing: avoid overwriting with a smaller value.
		// Advancing ObservedGeneration is done by the controller upon successful convergence.
		if m.Status.ObservedGeneration < now.Status.ObservedGeneration {
			m.Status.ObservedGeneration = now.Status.ObservedGeneration
		}

		logger.Log.Debugj(map[string]interface{}{
			"amsg":    "Update MiddlewareOperator status",
			"version": now.ResourceVersion,
		})

		if equality.Semantic.DeepEqual(now.Status, m.Status) {
			return nil
		}

		now.Status = m.Status

		// Retry updating the CR
		err = cli.Status().Update(ctx, now)
		if err != nil {
			return fmt.Errorf("update MiddlewareOperator status error: %w", err)
		}
		return nil
	})
}
