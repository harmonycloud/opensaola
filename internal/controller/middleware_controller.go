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

package controller

import (
	"context"
	"errors"
	"fmt"
	"time"

	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"

	"github.com/opensaola/opensaola/api/v1"
	"github.com/opensaola/opensaola/internal/concurrency"
	"github.com/opensaola/opensaola/internal/k8s"
	metrics "github.com/opensaola/opensaola/pkg/metrics"
	"github.com/opensaola/opensaola/internal/service/consts"
	"github.com/opensaola/opensaola/internal/service/middleware"
	"github.com/opensaola/opensaola/pkg/tools/ctxkeys"
	"k8s.io/apimachinery/pkg/api/equality"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

// MiddlewareReconciler reconciles a Middleware object
type MiddlewareReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

//+kubebuilder:rbac:groups=middleware.cn,resources=middlewares,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=middleware.cn,resources=middlewares/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=middleware.cn,resources=middlewares/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the Middleware object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.20.2/pkg/reconcile
func (r *MiddlewareReconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, retErr error) {
	startTime := time.Now()
	ctx, timer := metrics.NewReconcileTimer(ctx, "middleware")
	defer func() {
		metrics.ObserveReconcile("middleware", startTime, result.Requeue, result.RequeueAfter, retErr)
		res := metrics.ReconcileResult(result.Requeue, result.RequeueAfter, retErr)
		timer.Observe(res)
		metrics.ObserveRequeue("middleware", result.Requeue, result.RequeueAfter)
		metrics.ObserveAPIError("middleware", retErr)
	}()

	l := log.FromContext(ctx).WithValues("reconcileID", fmt.Sprintf("%s/%d", req.Name, time.Now().UnixMilli()))
	ctx = log.IntoContext(ctx, l)

	log.FromContext(ctx).V(1).Info("start processing middleware", "req", req)

	var err error
	// Get middleware
	mid := new(v1.Middleware)
	stop := timer.Start(metrics.PhaseAPIRead)
	if mid, err = k8s.GetMiddleware(ctx, r.Client, req.Name, req.Namespace); err != nil {
		stop()
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	stop()

	if !mid.DeletionTimestamp.IsZero() {
		log.FromContext(ctx).Info("Middleware entering deletion branch",
			"name", mid.Name,
			"namespace", mid.Namespace,
			"hasFinalizer", controllerutil.ContainsFinalizer(mid, v1.FinalizerMiddleware),
			"deletionTimestamp", mid.GetDeletionTimestamp(),
			"packageName", mid.GetLabels()[v1.LabelPackageName],
			"configurationsCount", len(mid.Spec.Configurations),
		)
		if controllerutil.ContainsFinalizer(mid, v1.FinalizerMiddleware) {
			start := time.Now()
			resolved, usedLegacy, legacyReason, resolveErr := middleware.ResolveDeleteContext(ctx, r.Client, mid)
			path := "mainline"
			if usedLegacy {
				path = "legacy"
			}
			if resolveErr != nil {
				if usedLegacy {
					metrics.ObserveLegacyDelete("middleware", "error", start)
				}
				log.FromContext(ctx).Error(resolveErr, "Middleware delete context resolution failed",
					"name", mid.Name,
					"namespace", mid.Namespace,
					"path", path,
					"finalizer_action", "keep",
					"legacy_reason", legacyReason,
					"cleanup_result", "error",
				)
				return ctrl.Result{}, resolveErr
			}
			if cleanErr := middleware.HandleResource(ctx, r.Client, consts.HandleActionDelete, resolved); cleanErr != nil {
				if usedLegacy {
					metrics.ObserveLegacyDelete("middleware", "error", start)
				}
				log.FromContext(ctx).Error(cleanErr, "Middleware cleanup failed",
					"name", mid.Name,
					"namespace", mid.Namespace,
					"path", path,
					"finalizer_action", "keep",
					"legacy_reason", legacyReason,
					"cleanup_result", "error",
				)
				return ctrl.Result{}, cleanErr
			}
			if updateErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
				latest, getErr := k8s.GetMiddleware(ctx, r.Client, req.Name, req.Namespace)
				if getErr != nil {
					return getErr
				}
				controllerutil.RemoveFinalizer(latest, v1.FinalizerMiddleware)
				return r.Update(ctx, latest)
			}); updateErr != nil {
				if !apiErrors.IsNotFound(updateErr) {
					if usedLegacy {
						metrics.ObserveLegacyDelete("middleware", "error", start)
					}
					return ctrl.Result{}, updateErr
				}
			}
			if usedLegacy {
				metrics.ObserveLegacyDelete("middleware", "success", start)
			}
			r.Recorder.Event(mid, "Normal", "Deleted", "Middleware cleanup completed")
			log.FromContext(ctx).Info("Middleware deletion cleanup completed",
				"name", mid.Name,
				"namespace", mid.Namespace,
				"path", path,
				"finalizer_action", "remove",
				"legacy_reason", legacyReason,
				"cleanup_result", "success",
			)
		} else {
			log.FromContext(ctx).Info("Middleware has no finalizer on deletion, skipping cleanup",
				"warning", true,
				"name", mid.Name,
				"namespace", mid.Namespace,
				"path", "mainline",
				"finalizer_action", "missing",
				"legacy_reason", "",
				"cleanup_result", "skipped",
			)
		}
		return ctrl.Result{}, nil
	}
	if !controllerutil.ContainsFinalizer(mid, v1.FinalizerMiddleware) {
		controllerutil.AddFinalizer(mid, v1.FinalizerMiddleware)
		if updateErr := r.Update(ctx, mid); updateErr != nil {
			log.FromContext(ctx).Error(updateErr, "failed to add finalizer for middleware", "namespace", mid.Namespace, "name", mid.Name)
			return ctrl.Result{}, updateErr
		}
		metrics.ObserveFinalizerBackfill("middleware", "success")
		log.FromContext(ctx).Info("Middleware finalizer added",
			"name", mid.Name,
			"namespace", mid.Namespace,
			"path", "mainline",
			"finalizer_action", "add",
			"legacy_reason", "",
			"cleanup_result", "skipped",
		)
		return ctrl.Result{Requeue: true}, nil
	}

	var (
		generation         = mid.Generation
		observedGeneration = mid.Status.ObservedGeneration
	)

	defer func() {
		// Status convergence:
		// - Updating is a transient state during the upgrade flow and should not persist due to errors/retries
		// - Errors should be reflected as Unavailable + reason, not perpetual Updating
		state := v1.StateAvailable
		reason := ""

		// Remove stale Updating condition only when:
		// 1. No upgrade is in progress (no update annotation), AND
		// 2. All other conditions are True (the issue has been resolved)
		if _, hasUpdate := mid.GetAnnotations()[v1.LabelUpdate]; !hasUpdate {
			allOtherTrue := true
			for _, c := range mid.Status.Conditions {
				if c.Type != "Updating" && c.Status != metav1.ConditionTrue {
					allOtherTrue = false
					break
				}
			}
			if allOtherTrue {
				filtered := mid.Status.Conditions[:0]
				for _, c := range mid.Status.Conditions {
					if c.Type != "Updating" {
						filtered = append(filtered, c)
					}
				}
				mid.Status.Conditions = filtered
			}
		}

		for _, condition := range mid.Status.Conditions {
			if condition.Status == metav1.ConditionFalse {
				state = v1.StateUnavailable
				reason = condition.Message
				break
			}
		}

		// If this reconcile failed but no condition is marked False yet, fall back to writing the error as reason.
		if retErr != nil && state == v1.StateAvailable {
			state = v1.StateUnavailable
			reason = retErr.Error()
		}

		// If the update annotation (middleware.cn/update) exists, prefer showing Updating (unless already Unavailable).
		if _, ok := mid.GetAnnotations()[v1.LabelUpdate]; ok && state == v1.StateAvailable {
			state = v1.StateUpdating
			reason = ""
		}

		if state != mid.Status.State {
			log.FromContext(ctx).Info("state transition", "from", string(mid.Status.State), "to", string(state), "reason", reason)
		}

		mid.Status.State = state
		mid.Status.Reason = reason

		// Only advance ObservedGeneration when this reconcile has converged with no requeue needed, to avoid incorrectly marking as processed during failures/retries.
		// Note: result is a named return value, readable inside defer.
		if retErr == nil && !result.Requeue && result.RequeueAfter == 0 {
			if mid.Status.ObservedGeneration < mid.Generation {
				mid.Status.ObservedGeneration = mid.Generation
			}
		}

		stopStatus := timer.Start(metrics.PhaseStatusWrite)
		err = k8s.UpdateMiddlewareStatus(ctx, r.Client, mid)
		stopStatus()
		if err != nil {
			log.FromContext(ctx).Error(err, "failed to update middleware status", "namespace", mid.Namespace, "name", mid.Name)
		}
	}()

	stop = timer.Start(metrics.PhaseCompute)
	if err = middleware.Check(ctx, r.Client, mid); err != nil {
		stop()
		r.Recorder.Event(mid, "Warning", "ValidationFailed", err.Error())
		log.FromContext(ctx).Error(err, "middleware check failed", "namespace", mid.Namespace, "name", mid.Name)
		return ctrl.Result{}, err
	}
	stop()

	stop = timer.Start(metrics.PhaseCompute)
	if err = middleware.ReplacePackage(ctxkeys.WithScheme(ctx, r.Scheme), r.Client, mid); err != nil {
		stop()
		if errors.Is(err, consts.ErrPackageNotReady) {
			r.Recorder.Event(mid, "Warning", "PackageNotReady", "Package not ready, will retry")
			log.FromContext(ctx).Info("middleware package not ready, requeuing", "namespace", mid.Namespace, "name", mid.Name, "requeueAfter", 5*time.Second)
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}
		if errors.Is(err, consts.ErrPackageInstallFailed) {
			log.FromContext(ctx).Info("middleware package install failed, aborting", "warning", true, "namespace", mid.Namespace, "name", mid.Name, "err", err)
			return ctrl.Result{}, nil
		}
		r.Recorder.Event(mid, "Warning", "UpgradeFailed", err.Error())
		return ctrl.Result{}, fmt.Errorf("upgrade failed: %w", err)
	}
	stop()

	// Compare generation with observedGeneration
	// observedGeneration == 0 means initial publish
	// generation > observedGeneration or State == Updating means update is needed
	if observedGeneration == 0 {
		stop = timer.Start(metrics.PhaseAPIWrite)
		if err = middleware.HandleResource(ctxkeys.WithScheme(ctx, r.Scheme), r.Client, consts.HandleActionPublish, mid); err != nil {
			stop()
			log.FromContext(ctx).Error(err, "middleware build failed", "namespace", mid.Namespace, "name", mid.Name)
			return ctrl.Result{}, err
		}
		stop()
		r.Recorder.Event(mid, "Normal", "Published", "Middleware published successfully")
	} else if generation > observedGeneration || mid.Status.State == v1.StateUpdating {
		stop = timer.Start(metrics.PhaseAPIWrite)
		if err = middleware.HandleResource(ctxkeys.WithScheme(ctx, r.Scheme), r.Client, consts.HandleActionUpdate, mid); err != nil {
			stop()
			log.FromContext(ctx).Error(err, "middleware update failed", "namespace", mid.Namespace, "name", mid.Name)
			return ctrl.Result{}, err
		}
		stop()
		r.Recorder.Event(mid, "Normal", "Updated", "Middleware updated successfully")
	}
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *MiddlewareReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Define predicate filter
	pred := predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			// Skip status-only update events
			oldObj := e.ObjectOld.(*v1.Middleware)
			newObj := e.ObjectNew.(*v1.Middleware)

			// Compare whether status has changed
			if !equality.Semantic.DeepEqual(oldObj.Status, newObj.Status) {
				return false // Skip status updates
			}

			// Allow metadata or other field update events
			return true
		},
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&v1.Middleware{}).
		Named("middleware").
		WithEventFilter(pred).
		WithOptions(concurrency.ControllerOptions("MIDDLEWARE", 1)).
		Complete(r)
}
