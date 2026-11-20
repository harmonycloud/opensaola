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
	"github.com/harmonycloud/opensaola/internal/cache"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
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
	_, err := GetMiddlewareOperator(ctx, cli, m.Name, m.Namespace)
	if err == nil {
		return errors.NewAlreadyExists(MiddlewareOperatorGroupResource(), m.Name)
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

var MiddlewareOperatorCache = cache.New[string, v1.MiddlewareOperator](0)

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
	MiddlewareOperatorCache.Set(types.NamespacedName{Name: name, Namespace: namespace}.String(), *m.DeepCopy())
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

// OperatorRuntimeSummary describes the primary Deployment reported by its MiddlewareOperator.
type OperatorRuntimeSummary struct {
	Available string
	Ready     bool
	Runtime   string
}

// UpdateMiddlewareOperatorRuntimeStatus records one Deployment observation. A nil detail
// removes that Deployment. Only the primary Deployment can change the optional summary.
func UpdateMiddlewareOperatorRuntimeStatus(
	ctx context.Context, cli client.Client, owner client.ObjectKey, ownerUID types.UID,
	deploymentName string, detail *appsv1.DeploymentStatus, summary *OperatorRuntimeSummary,
) error {
	if ownerUID == "" || deploymentName == "" {
		return fmt.Errorf("runtime update for MiddlewareOperator %s requires an owner UID and deployment name", owner)
	}
	if summary != nil && deploymentName != owner.Name {
		return fmt.Errorf("deployment %q cannot set the runtime summary for MiddlewareOperator %s", deploymentName, owner)
	}

	reader := client.Reader(cli)
	if statusAPIReader != nil {
		reader = statusAPIReader
	}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var now v1.MiddlewareOperator
		if err := reader.Get(ctx, owner, &now); err != nil {
			return client.IgnoreNotFound(err)
		}
		if now.UID != ownerUID || !now.DeletionTimestamp.IsZero() {
			return nil
		}

		base := now.DeepCopy()
		if detail == nil {
			delete(now.Status.OperatorStatus, deploymentName)
		} else {
			if now.Status.OperatorStatus == nil {
				now.Status.OperatorStatus = make(map[string]appsv1.DeploymentStatus)
			}
			now.Status.OperatorStatus[deploymentName] = *detail
		}
		if summary != nil {
			now.Status.OperatorAvailable = summary.Available
			now.Status.Ready = summary.Ready
			now.Status.Runtime = summary.Runtime
		}
		if equality.Semantic.DeepEqual(base.Status, now.Status) {
			return nil
		}

		patch := client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})
		return client.IgnoreNotFound(cli.Status().Patch(ctx, &now, patch))
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
		if m.UID != "" && m.UID != now.UID {
			return fmt.Errorf("MiddlewareOperator %s/%s was replaced before its status update", m.Namespace, m.Name)
		}

		// ObservedGeneration must be monotonically increasing: avoid overwriting with a smaller value.
		// Advancing ObservedGeneration is done by the controller upon successful convergence.
		if m.Status.ObservedGeneration < now.Status.ObservedGeneration {
			m.Status.ObservedGeneration = now.Status.ObservedGeneration
		}
		m.Status.OperatorStatus = now.Status.OperatorStatus
		m.Status.OperatorAvailable = now.Status.OperatorAvailable
		m.Status.Ready = now.Status.Ready
		m.Status.Runtime = now.Status.Runtime
		// Configuration publication owns these fields and updates them between
		// writes within one generation. A general status write must preserve them.
		m.Status.RenderedConfigurationResources = now.Status.RenderedConfigurationResources
		m.Status.RenderedConfigurationResourcesGeneration = now.Status.RenderedConfigurationResourcesGeneration
		m.Status.PendingConfigurationWrite = now.Status.PendingConfigurationWrite

		log.FromContext(ctx).V(1).Info("Update MiddlewareOperator status", "version", now.ResourceVersion)

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
