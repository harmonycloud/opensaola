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

package controller

import (
	"context"
	"errors"
	"fmt"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	"github.com/harmonycloud/opensaola/internal/k8s"
	"github.com/harmonycloud/opensaola/internal/service/middleware"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	reconcilePauseSnapshotMessage  = "primary custom resource desired spec snapshot captured for suspend-reconcile"
	reconcileResumeRequiredMessage = "set middleware.cn/resume-policy=merge to adopt actual CR changes or apply to replay desired state"
)

// handleMiddlewareReconcilePause handles the Middleware-only pause state
// machine. It returns handled=true whenever normal reconciliation must not
// proceed to child-resource writes in this pass.
func (r *MiddlewareReconciler) handleMiddlewareReconcilePause(ctx context.Context, mid *v1.Middleware) (handled bool, result ctrl.Result, err error) {
	annotations := mid.GetAnnotations()
	policy, policyValid := v1.ReconcileResumePolicyFor(annotations)
	policyRaw := annotations[v1.AnnotationResumePolicy]
	suspended := v1.IsReconcileSuspended(annotations)
	paused := hasReconcilePaused(mid.Status.Conditions)

	if suspended {
		if policyRaw != "" && !policyValid {
			return true, ctrl.Result{}, r.recordMiddlewarePauseFailure(ctx, mid, v1.CondReasonReconcileAdoptionFailed,
				fmt.Sprintf("unsupported %s value %q", v1.AnnotationResumePolicy, policyRaw))
		}
		switch policy {
		case v1.ReconcileResumePolicyMerge:
			return r.mergePausedMiddleware(ctx, mid)
		case v1.ReconcileResumePolicyApply:
			if err := k8s.CompleteMiddlewareReconcileResume(ctx, r.Client, mid.Name, mid.Namespace, mid.Generation, snapshotHash(mid), v1.ReconcileResumePolicyApply, mid.Spec.ReconcileOverrides); err != nil {
				return true, ctrl.Result{}, err
			}
			log.FromContext(ctx).Info("Middleware reconcile resume approved with apply policy", "name", mid.Name, "namespace", mid.Namespace)
			return true, ctrl.Result{Requeue: true}, nil
		default:
			return r.captureMiddlewarePause(ctx, mid)
		}
	}

	if !paused {
		return false, ctrl.Result{}, nil
	}
	if !policyValid {
		// A direct deletion of suspend-reconcile previously caused an immediate
		// forced apply. Keep the durable pause marker and require an explicit
		// recovery decision instead.
		return true, ctrl.Result{}, r.recordMiddlewarePauseFailure(ctx, mid, v1.CondReasonReconcileAdoptionFailed, reconcileResumeRequiredMessage)
	}
	if policy == v1.ReconcileResumePolicyMerge && !reconcileAdoptionSucceeded(mid.Status.Conditions) {
		return r.mergePausedMiddleware(ctx, mid)
	}
	if policy == v1.ReconcileResumePolicyMerge {
		matches, verifyErr := middleware.PausedPrimaryCustomResourceSpecMatchesAdoption(ctx, r.Client, mid.Status.ReconcilePause)
		if verifyErr != nil || !matches {
			// Treat a second actual-spec change as a new L input. Re-running the
			// same B/L/D merge is safe: unchanged adopted paths remain accepted,
			// while a conflicting second change remains paused and visible.
			return r.mergePausedMiddleware(ctx, mid)
		}
	}
	// An apply policy is already the explicit decision to replay desired state.
	// A successful merge policy has persisted its override and may now apply it.
	return false, ctrl.Result{}, nil
}

func (r *MiddlewareReconciler) captureMiddlewarePause(ctx context.Context, mid *v1.Middleware) (bool, ctrl.Result, error) {
	if mid.Status.ReconcilePause != nil {
		if err := r.patchMiddlewarePauseStatus(ctx, mid, func(status *v1.MiddlewareStatus) {
			markReconcilePaused(ctx, &status.Conditions, mid.Generation)
			clearReconcileAdoptionCondition(&status.Conditions)
		}); err != nil {
			return true, ctrl.Result{}, err
		}
		return true, ctrl.Result{}, nil
	}

	snapshot, err := middleware.BuildReconcilePauseSnapshot(ctx, r.Client, mid)
	if err != nil {
		return true, ctrl.Result{}, r.recordMiddlewarePauseFailure(ctx, mid, v1.CondReasonReconcileSnapshotFailed, fmt.Sprintf("capture reconcile pause snapshot: %v", err))
	}
	if err := r.patchMiddlewarePauseStatus(ctx, mid, func(status *v1.MiddlewareStatus) {
		if status.ReconcilePause == nil {
			status.ReconcilePause = snapshot
		}
		markReconcilePaused(ctx, &status.Conditions, mid.Generation)
		clearReconcileAdoptionCondition(&status.Conditions)
	}); err != nil {
		return true, ctrl.Result{}, err
	}
	log.FromContext(ctx).Info("Middleware reconciliation is suspended with a durable desired-state snapshot",
		"name", mid.Name,
		"namespace", mid.Namespace,
		"annotation", v1.AnnotationSuspendReconcile,
		"snapshotHash", snapshot.Hash,
	)
	return true, ctrl.Result{}, nil
}

func (r *MiddlewareReconciler) mergePausedMiddleware(ctx context.Context, mid *v1.Middleware) (bool, ctrl.Result, error) {
	snapshot := mid.Status.ReconcilePause
	if snapshot == nil {
		return true, ctrl.Result{}, r.recordMiddlewarePauseFailure(ctx, mid, v1.CondReasonReconcileSnapshotFailed,
			"cannot merge a paused Middleware without a desired-spec snapshot; use resume-policy=apply to replay desired state")
	}
	adoption, err := middleware.AdoptPausedPrimaryCustomResource(ctx, r.Client, mid, snapshot)
	if err != nil {
		var conflict *middleware.ReconcilePauseMergeConflictError
		if errors.As(err, &conflict) {
			return true, ctrl.Result{}, r.recordMiddlewarePauseFailure(ctx, mid, v1.CondReasonReconcileAdoptionConflict,
				fmt.Sprintf("actual and desired state both changed: %v", conflict.Paths))
		}
		return true, ctrl.Result{}, r.recordMiddlewarePauseFailure(ctx, mid, v1.CondReasonReconcileAdoptionFailed,
			fmt.Sprintf("adopt actual primary custom resource changes: %v", err))
	}
	if err := k8s.CompleteMiddlewareReconcileResume(ctx, r.Client, mid.Name, mid.Namespace, mid.Generation, snapshot.Hash, v1.ReconcileResumePolicyMerge, adoption.Overrides); err != nil {
		return true, ctrl.Result{}, err
	}
	if err := r.patchMiddlewarePauseStatus(ctx, mid, func(status *v1.MiddlewareStatus) {
		if status.ReconcilePause != nil && status.ReconcilePause.Hash == snapshot.Hash {
			status.ReconcilePause.AdoptedLiveSpecHash = adoption.LiveSpecHash
		}
		setReconcileAdoptionCondition(ctx, &status.Conditions, metav1.ConditionTrue, v1.CondReasonReconcileAdoptionSucceeded,
			"actual primary custom resource spec changes were adopted into reconcileOverrides", mid.Generation)
	}); err != nil {
		return true, ctrl.Result{}, err
	}
	log.FromContext(ctx).Info("Middleware reconcile resume merged actual primary custom resource changes",
		"name", mid.Name,
		"namespace", mid.Namespace,
		"snapshotHash", snapshot.Hash,
		"hasOverrides", adoption.Overrides != nil,
	)
	return true, ctrl.Result{Requeue: true}, nil
}

func (r *MiddlewareReconciler) recordMiddlewarePauseFailure(ctx context.Context, mid *v1.Middleware, reason, message string) error {
	return r.patchMiddlewarePauseStatus(ctx, mid, func(status *v1.MiddlewareStatus) {
		markReconcilePaused(ctx, &status.Conditions, mid.Generation)
		setReconcileAdoptionCondition(ctx, &status.Conditions, metav1.ConditionFalse, reason, message, mid.Generation)
	})
}

func (r *MiddlewareReconciler) patchMiddlewarePauseStatus(ctx context.Context, mid *v1.Middleware, mutate func(*v1.MiddlewareStatus)) error {
	return k8s.PatchMiddlewareStatusFields(ctx, r.Client, mid.Name, mid.Namespace, mutate)
}

func (r *MiddlewareReconciler) finalizeMiddlewareReconcileResume(ctx context.Context, mid *v1.Middleware) error {
	policy, ok := v1.ReconcileResumePolicyFor(mid.GetAnnotations())
	if !ok {
		return fmt.Errorf("middleware reconcile resume policy missing before finalization")
	}
	expectedSnapshotHash := snapshotHash(mid)
	if err := k8s.FinalizeMiddlewareReconcileResume(ctx, r.Client, mid.Name, mid.Namespace, expectedSnapshotHash, policy); err != nil {
		return err
	}
	cleared := false
	if err := r.patchMiddlewarePauseStatus(ctx, mid, func(status *v1.MiddlewareStatus) {
		if expectedSnapshotHash != "" && (status.ReconcilePause == nil || status.ReconcilePause.Hash != expectedSnapshotHash) {
			return
		}
		status.ReconcilePause = nil
		clearReconcilePaused(&status.Conditions)
		// A previous failed merge must not keep the Middleware Unavailable after
		// an explicitly approved apply or a later merged apply has succeeded.
		clearReconcileAdoptionCondition(&status.Conditions)
		cleared = true
	}); err != nil {
		return err
	}
	if !cleared {
		return fmt.Errorf("middleware reconcile pause snapshot changed before status finalization")
	}
	clearReconcilePaused(&mid.Status.Conditions)
	clearReconcileAdoptionCondition(&mid.Status.Conditions)
	return nil
}

func snapshotHash(mid *v1.Middleware) string {
	if mid.Status.ReconcilePause == nil {
		return ""
	}
	return mid.Status.ReconcilePause.Hash
}

func reconcileAdoptionSucceeded(conditions []metav1.Condition) bool {
	for _, condition := range conditions {
		if condition.Type == v1.CondTypeReconcileAdoption &&
			condition.Status == metav1.ConditionTrue &&
			condition.Reason == v1.CondReasonReconcileAdoptionSucceeded {
			return true
		}
	}
	return false
}
