/*
Copyright 2026 The OpenSaola Authors.

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

package middleware

import (
	"context"
	"fmt"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	"github.com/harmonycloud/opensaola/internal/k8s"
	"github.com/harmonycloud/opensaola/internal/service/customresource"
	"github.com/harmonycloud/opensaola/internal/service/status"
	"github.com/harmonycloud/opensaola/internal/service/synchronizer"
	"github.com/harmonycloud/opensaola/internal/service/watcher"
	"github.com/harmonycloud/opensaola/pkg/tools/ctxkeys"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	PrimaryResourceRebuildFailedReason = "PrimaryResourceRebuildFailed"
	PrimaryResourceCheckFailedReason   = "PrimaryResourceCheckFailed"
)

// RebuildMissingPrimaryCustomResource repairs only an absent primary resource.
// Existing steady-state resources are never rendered or replayed. A failed
// rebuild has its own retry path so retrying cannot execute side-effecting
// PreActions or republish unrelated Configuration resources.
//
// recovered means a missing-resource recovery completed, including a concurrent
// creation verified by the writer. It does not assert that this call created it.
func RebuildMissingPrimaryCustomResource(ctx context.Context, cli client.Client, m *v1.Middleware) (recovered bool, err error) {
	if m == nil {
		return false, fmt.Errorf("middleware is nil")
	}
	if m.DeletionTimestamp != nil || v1.IsMiddlewareReconcileWriteSuspended(m.Annotations, m.Status.Conditions) {
		return false, nil
	}
	// A failed GET is not evidence that we attempted a POST. Remember an actual
	// interrupted recreation across transient read failures; safe rendering is
	// only necessary for that case when the object is present again.
	prior := meta.FindStatusCondition(m.Status.Conditions, v1.CondTypeApplyCluster)
	retryRebuild := prior != nil && prior.Status == metav1.ConditionFalse && prior.ObservedGeneration == m.Generation && prior.Reason == PrimaryResourceRebuildFailedReason
	reason := PrimaryResourceCheckFailedReason
	if retryRebuild {
		reason = PrimaryResourceRebuildFailedReason
	}
	defer func() {
		if err != nil {
			condition := status.GetCondition(ctx, &m.Status.Conditions, v1.CondTypeApplyCluster)
			condition.Failed(ctx, err.Error(), m.Generation)
			condition.Reason = reason
		} else if recovered {
			status.GetCondition(ctx, &m.Status.Conditions, v1.CondTypeApplyCluster).Success(ctx, m.Generation)
		}
	}()

	gvk, err := customresource.HandleGvk(ctx, cli, m)
	if err != nil {
		return false, fmt.Errorf("resolve primary resource for missing-resource recovery: %w", err)
	}
	writer := k8s.NewManagedResourceWriter(cli)
	live := &unstructured.Unstructured{}
	live.SetGroupVersionKind(*gvk)
	err = writer.Reader.Get(ctx, client.ObjectKeyFromObject(m), live)
	if err == nil {
		if err = validateRebuildPrimaryOwner(m, live); err != nil {
			return false, err
		}
		if !retryRebuild {
			if prior != nil && prior.Status == metav1.ConditionFalse && prior.ObservedGeneration == m.Generation && prior.Reason == PrimaryResourceCheckFailedReason {
				status.GetCondition(ctx, &m.Status.Conditions, v1.CondTypeApplyCluster).Success(ctx, m.Generation)
			}
			return false, nil
		}
		// Create may have succeeded before intent recording failed. Complete only
		// that interrupted recovery, using the create-only writer below.
	} else if !apierrors.IsNotFound(err) {
		return false, fmt.Errorf("read primary resource for missing-resource recovery: %w", err)
	}

	cr, err := RenderPrimaryCustomResource(ctx, cli, m)
	if err != nil {
		return false, fmt.Errorf("render missing primary resource safely: %w", err)
	}
	if cr.GroupVersionKind() != *gvk || client.ObjectKeyFromObject(cr) != client.ObjectKeyFromObject(m) {
		return false, fmt.Errorf("rendered primary resource target changed during missing-resource recovery")
	}
	scheme, err := ctxkeys.SchemeFrom(ctx)
	if err != nil {
		return false, fmt.Errorf("get scheme for missing-resource recovery: %w", err)
	}
	if err = ctrl.SetControllerReference(m, cr, scheme); err != nil {
		return false, fmt.Errorf("set missing primary resource owner: %w", err)
	}
	reason = PrimaryResourceRebuildFailedReason
	if err = writer.RecreateMissing(ctx, m, cr, ""); err != nil {
		return false, fmt.Errorf("recover missing primary resource: %w", err)
	}
	if _, _, err = watcher.EnsureCRWatcher(ctx, cli, cr, m.Name, m.Namespace); err != nil {
		return false, fmt.Errorf("observe recovered primary resource: %w", err)
	}
	observerMID := m.DeepCopy()
	go func() {
		if syncErr := synchronizer.SyncCustomResourceV2(ctx, cli, cr, observerMID); syncErr != nil {
			log.FromContext(ctx).Error(syncErr, "recovered primary resource sync exited", "gvk", cr.GroupVersionKind(), "namespace", cr.GetNamespace(), "name", cr.GetName())
		}
	}()
	return true, nil
}

func validateRebuildPrimaryOwner(m *v1.Middleware, cr *unstructured.Unstructured) error {
	ref := metav1.GetControllerOf(cr)
	if ref == nil || m.UID == "" || cr.GetUID() == "" {
		return fmt.Errorf("primary resource %s %s/%s has no valid Middleware owner identity", cr.GetKind(), cr.GetNamespace(), cr.GetName())
	}
	gv, err := schema.ParseGroupVersion(ref.APIVersion)
	if err != nil || gv.Group != v1.GroupVersion.Group || ref.Kind != "Middleware" || ref.Name != m.Name || ref.UID != m.UID || cr.GetNamespace() != m.Namespace {
		return fmt.Errorf("primary resource %s %s/%s has a different controller owner", cr.GetKind(), cr.GetNamespace(), cr.GetName())
	}
	if cr.GetDeletionTimestamp() != nil {
		return fmt.Errorf("primary resource %s %s/%s is still deleting", cr.GetKind(), cr.GetNamespace(), cr.GetName())
	}
	return nil
}
