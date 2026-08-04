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

// UpdateMiddlewareStatus updates controller-owned Middleware status fields.
// The runtime-owned CustomResources field (the initial primary-CR seed and
// SyncCustomResourceV2) is preserved from the latest API object so a deferred
// controller update cannot overwrite a newer runtime observation.
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

		log.FromContext(ctx).V(1).Info("Update Middleware status", "version", now.ResourceVersion)

		desiredStatus := *m.Status.DeepCopy()
		desiredStatus.CustomResources = now.Status.CustomResources
		// Pause snapshots are changed only through PatchMiddlewareStatusFields.
		// A normal controller/service status write may be based on an older
		// reconcile copy, so always retain the latest durable pause baseline.
		desiredStatus.ReconcilePause = now.Status.ReconcilePause
		if desiredStatus.RenderedConfigurationResourcesGeneration < now.Status.RenderedConfigurationResourcesGeneration {
			desiredStatus.RenderedConfigurationResources = now.Status.RenderedConfigurationResources
			desiredStatus.RenderedConfigurationResourcesGeneration = now.Status.RenderedConfigurationResourcesGeneration
		}

		// Compare whether status has changed
		if equality.Semantic.DeepEqual(now.Status, desiredStatus) {
			return nil
		}
		now.Status = desiredStatus

		// Retry updating the CR
		err = cli.Status().Update(ctx, now)
		if err != nil {
			return fmt.Errorf("update middleware status error: %w", err)
		}
		return nil
	})
}

// CompleteMiddlewareReconcileResume atomically persists a calculated primary
// CR override and consumes the pause annotation. It intentionally updates only
// the controller-owned override and annotations, preserving any concurrent edit
// to the rest of Middleware spec.
func CompleteMiddlewareReconcileResume(ctx context.Context, cli client.Client, name, namespace string, generation int64, snapshotHash string, expectedPolicy v1.ReconcileResumePolicy, overrides *v1.ReconcileOverrides) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		now, err := GetMiddleware(ctx, cli, name, namespace)
		if err != nil {
			return err
		}
		if now.Generation != generation {
			return fmt.Errorf("middleware generation changed during reconcile resume: got %d, want %d", now.Generation, generation)
		}
		if snapshotHash != "" && (now.Status.ReconcilePause == nil || now.Status.ReconcilePause.Hash != snapshotHash) {
			return fmt.Errorf("middleware reconcile pause snapshot changed during resume")
		}
		policy, ok := v1.ReconcileResumePolicyFor(now.GetAnnotations())
		if !ok || policy != expectedPolicy {
			return fmt.Errorf("middleware reconcile resume policy changed during resume: got %q, want %q", policy, expectedPolicy)
		}

		now.Spec.ReconcileOverrides = overrides
		if now.Annotations == nil {
			now.Annotations = map[string]string{}
		}
		// Keep the policy until the following desired-state apply succeeds. It
		// is the durable approval that prevents a direct annotation removal from
		// accidentally replaying stale desired state.
		delete(now.Annotations, v1.AnnotationSuspendReconcile)
		return cli.Update(ctx, now)
	})
}

// FinalizeMiddlewareReconcileResume consumes a resume policy only after the
// desired primary CR apply has succeeded. It preserves every other concurrent
// metadata change.
func FinalizeMiddlewareReconcileResume(ctx context.Context, cli client.Client, name, namespace, snapshotHash string, expectedPolicy v1.ReconcileResumePolicy) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		now, err := GetMiddleware(ctx, cli, name, namespace)
		if err != nil {
			return err
		}
		if snapshotHash != "" && (now.Status.ReconcilePause == nil || now.Status.ReconcilePause.Hash != snapshotHash) {
			return fmt.Errorf("middleware reconcile pause snapshot changed before resume finalization")
		}
		policy, ok := v1.ReconcileResumePolicyFor(now.GetAnnotations())
		if !ok {
			return fmt.Errorf("middleware reconcile resume policy missing before finalization")
		}
		if policy != expectedPolicy {
			return fmt.Errorf("middleware reconcile resume policy changed before finalization: got %q, want %q", policy, expectedPolicy)
		}
		delete(now.Annotations, v1.AnnotationResumePolicy)
		return cli.Update(ctx, now)
	})
}

// PatchMiddlewareStatusFields reads the latest Middleware from API Server,
// applies the mutate function to modify only specific fields of its status,
// then writes back. This lets runtime writers modify only the fields they own,
// preventing concurrent writers (e.g. SyncCustomResourceV2) from overwriting
// controller-owned state/conditions.
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
