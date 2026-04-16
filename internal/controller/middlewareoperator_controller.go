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

	"github.com/harmonycloud/opensaola/api/v1"
	"github.com/harmonycloud/opensaola/internal/concurrency"
	"github.com/harmonycloud/opensaola/internal/k8s"
	"github.com/harmonycloud/opensaola/internal/service/consts"
	"github.com/harmonycloud/opensaola/internal/service/middlewareoperator"
	"github.com/harmonycloud/opensaola/internal/service/status"
	metrics "github.com/harmonycloud/opensaola/pkg/metrics"
	"github.com/harmonycloud/opensaola/pkg/tools/ctxkeys"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

// MiddlewareOperatorReconciler reconciles a MiddlewareOperator object
type MiddlewareOperatorReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

var errMiddlewareOperatorFinalizerAdded = errors.New("middlewareoperator finalizer added")

//+kubebuilder:rbac:groups=middleware.cn,resources=middlewareoperators,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=middleware.cn,resources=middlewareoperators/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=middleware.cn,resources=middlewareoperators/finalizers,verbs=update
//+kubebuilder:rbac:groups=core,resources=events,verbs=create;patch
//+kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=apps,resources=deployments/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=apps,resources=deployments/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.20.2/pkg/reconcile
func (r *MiddlewareOperatorReconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, retErr error) {
	startTime := time.Now()
	ctx, timer := metrics.NewReconcileTimer(ctx, "middlewareoperator")
	defer func() {
		metrics.ObserveReconcile("middlewareoperator", startTime, result.Requeue, result.RequeueAfter, retErr)
		res := metrics.ReconcileResult(result.Requeue, result.RequeueAfter, retErr)
		timer.Observe(res)
		metrics.ObserveRequeue("middlewareoperator", result.Requeue, result.RequeueAfter)
		metrics.ObserveAPIError("middlewareoperator", retErr)
	}()

	l := log.FromContext(ctx).WithValues("reconcileID", fmt.Sprintf("%s/%d", req.Name, time.Now().UnixMilli()))
	ctx = log.IntoContext(ctx, l)

	log.FromContext(ctx).V(1).Info("start processing middlewareOperator", "req", req)

	// Handle middlewareOperator
	if err := r.handleMiddlewareOperator(ctx, req); err != nil {
		if errors.Is(err, errMiddlewareOperatorFinalizerAdded) {
			return ctrl.Result{Requeue: true}, nil
		}
		if errors.Is(err, consts.NoOperator) {
			return ctrl.Result{}, nil
		}
		if errors.Is(err, consts.ErrPackageNotReady) {
			log.FromContext(ctx).Info("package not ready, requeuing", "requeueAfter", 5*time.Second)
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}
		if errors.Is(err, consts.ErrPackageInstallFailed) {
			log.FromContext(ctx).Info("package install failed, aborting upgrade", "warning", true, "err", err)
			return ctrl.Result{}, nil
		}
		if errors.Is(err, consts.ErrPackageUnavailableExceeded) {
			log.FromContext(ctx).Info("package unavailable for too long, aborting upgrade", "warning", true, "err", err)
			return ctrl.Result{}, nil
		}
		log.FromContext(ctx).Error(err, "failed to handle MiddlewareOperator", "namespace", req.Namespace, "name", req.Name)
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Handle deployment
	if err := r.handleDeployment(ctx, req); err != nil {
		log.FromContext(ctx).Error(err, "failed to handle deployment for MiddlewareOperator", "namespace", req.Namespace, "name", req.Name)
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	return ctrl.Result{}, nil
}

// handleMiddlewareOperator handles the middlewareOperator reconcile logic.
// Uses named return value retErr so the deferred status-write logic can detect and propagate errors.
func (r *MiddlewareOperatorReconciler) handleMiddlewareOperator(ctx context.Context, req ctrl.Request) (retErr error) {
	timer := metrics.TimerFromContext(ctx)

	// Get middlewareOperator
	stop := timer.Start(metrics.PhaseAPIRead)
	mo, err := k8s.GetMiddlewareOperator(ctx, r.Client, req.Name, req.Namespace)
	stop()
	if err != nil {
		return err
	}

	// Finalizer logic (always enabled)
	if !mo.DeletionTimestamp.IsZero() {
		// Object is being deleted, perform cleanup
		if controllerutil.ContainsFinalizer(mo, v1.FinalizerMiddlewareOperator) {
			start := time.Now()
			resolved, usedLegacy, legacyReason, resolveErr := middlewareoperator.ResolveDeleteContext(ctx, r.Client, mo)
			path := "mainline"
			if usedLegacy {
				path = "legacy"
			}
			if resolveErr != nil {
				if usedLegacy {
					metrics.ObserveLegacyDelete("middlewareoperator", "error", start)
				}
				log.FromContext(ctx).Error(resolveErr, "MiddlewareOperator delete context resolution failed",
					"name", mo.Name,
					"namespace", mo.Namespace,
					"path", path,
					"finalizer_action", "pending",
					"legacy_reason", legacyReason,
					"cleanup_result", "error",
				)
				return resolveErr
			}
			if cleanErr := middlewareoperator.HandleResource(ctx, r.Client, consts.HandleActionDelete, resolved); cleanErr != nil {
				if usedLegacy {
					metrics.ObserveLegacyDelete("middlewareoperator", "error", start)
				}
				log.FromContext(ctx).Error(cleanErr, "MiddlewareOperator cleanup failed",
					"name", mo.Name,
					"namespace", mo.Namespace,
					"path", path,
					"finalizer_action", "pending",
					"legacy_reason", legacyReason,
					"cleanup_result", "error",
				)
				return cleanErr
			}
			if updateErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
				latest := &v1.MiddlewareOperator{}
				if getErr := r.Get(ctx, types.NamespacedName{Name: mo.Name, Namespace: mo.Namespace}, latest); getErr != nil {
					return getErr
				}
				controllerutil.RemoveFinalizer(latest, v1.FinalizerMiddlewareOperator)
				return r.Update(ctx, latest)
			}); updateErr != nil {
				if usedLegacy {
					metrics.ObserveLegacyDelete("middlewareoperator", "error", start)
				}
				return updateErr
			}
			if usedLegacy {
				metrics.ObserveLegacyDelete("middlewareoperator", "success", start)
			}
			k8s.MiddlewareOperatorCache.Delete(req.NamespacedName.String())
			r.Recorder.Event(mo, "Normal", "Deleted", "MiddlewareOperator cleanup completed")
			log.FromContext(ctx).Info("MiddlewareOperator deletion cleanup completed",
				"name", mo.Name,
				"namespace", mo.Namespace,
				"path", path,
				"finalizer_action", "remove",
				"legacy_reason", legacyReason,
				"cleanup_result", "success",
			)
		} else {
			log.FromContext(ctx).Info("MiddlewareOperator has no finalizer on deletion, skipping cleanup",
				"warning", true,
				"name", mo.Name,
				"namespace", mo.Namespace,
				"path", "mainline",
				"finalizer_action", "missing",
				"legacy_reason", "",
				"cleanup_result", "skipped",
			)
		}
		return nil
	}
	// Object is not being deleted, ensure finalizer exists
	if !controllerutil.ContainsFinalizer(mo, v1.FinalizerMiddlewareOperator) {
		controllerutil.AddFinalizer(mo, v1.FinalizerMiddlewareOperator)
		if updateErr := r.Update(ctx, mo); updateErr != nil {
			log.FromContext(ctx).Error(updateErr, "failed to add finalizer for MiddlewareOperator", "namespace", mo.Namespace, "name", mo.Name)
			return updateErr
		}
		metrics.ObserveFinalizerBackfill("middlewareoperator", "success")
		log.FromContext(ctx).Info("MiddlewareOperator finalizer added",
			"name", mo.Name,
			"namespace", mo.Namespace,
			"path", "mainline",
			"finalizer_action", "add",
			"legacy_reason", "",
			"cleanup_result", "skipped",
		)
		return errMiddlewareOperatorFinalizerAdded
	}

	var (
		generation         = mo.Generation
		observedGeneration = mo.Status.ObservedGeneration
	)

	defer func() {
		state := v1.StateAvailable
		reason := ""
		noOperator := middlewareoperator.IsNoOperatorResource(mo) || errors.Is(retErr, consts.NoOperator)
		if !noOperator {
			// Remove stale Updating condition only when:
			// 1. No upgrade is in progress (no update annotation), AND
			// 2. All other conditions are True (the issue has been resolved)
			// This keeps the user aware of upgrade failures (Unavailable) until
			// the problem is actually fixed, but allows recovery once everything
			// else is healthy.
			if _, hasUpdate := mo.GetAnnotations()[v1.LabelUpdate]; !hasUpdate {
				allOtherTrue := true
				for _, c := range mo.Status.Conditions {
					if c.Type != "Updating" && c.Status != metav1.ConditionTrue {
						allOtherTrue = false
						break
					}
				}
				if allOtherTrue {
					filtered := mo.Status.Conditions[:0]
					for _, c := range mo.Status.Conditions {
						if c.Type != "Updating" {
							filtered = append(filtered, c)
						}
					}
					mo.Status.Conditions = filtered
				}
			}

			for _, condition := range mo.Status.Conditions {
				if condition.Status == metav1.ConditionFalse {
					state = v1.StateUnavailable
					reason = condition.Message
					break
				}
			}

			// When reconcile returns an error but no condition is marked False yet, fall back to writing Unavailable with the error.
			if retErr != nil && state == v1.StateAvailable {
				state = v1.StateUnavailable
				reason = retErr.Error()
			}

			// If the update annotation (middleware.cn/update) exists, prefer showing Updating (unless already Unavailable).
			if _, ok := mo.GetAnnotations()[v1.LabelUpdate]; ok && state == v1.StateAvailable {
				state = v1.StateUpdating
				reason = ""
			}
		}

		if state != mo.Status.State {
			log.FromContext(ctx).Info("state transition", "from", string(mo.Status.State), "to", string(state), "reason", reason)
		}

		mo.Status.State = state
		mo.Status.Reason = reason

		// Only advance ObservedGeneration when this reconcile has converged successfully and is not in Updating state.
		if (retErr == nil || noOperator) && mo.Status.State != v1.StateUpdating {
			if mo.Status.ObservedGeneration < mo.Generation {
				mo.Status.ObservedGeneration = mo.Generation
			}
		}

		stopStatus := timer.Start(metrics.PhaseStatusWrite)
		statusErr := k8s.UpdateMiddlewareOperatorStatus(ctx, r.Client, mo)
		stopStatus()
		if statusErr != nil {
			log.FromContext(ctx).Error(statusErr, "failed to update middlewareOperator status")
			// If business logic succeeded but status write failed, propagate the error so the controller requeues.
			if retErr == nil {
				retErr = statusErr
			}
		}
	}()

	stop = timer.Start(metrics.PhaseCompute)
	if err = middlewareoperator.Check(ctx, r.Client, mo); err != nil {
		stop()
		r.Recorder.Event(mo, "Warning", "ValidationFailed", err.Error())
		log.FromContext(ctx).Error(err, "failed to validate MiddlewareOperator", "namespace", mo.Namespace, "name", mo.Name)
		return fmt.Errorf("failed to validate middlewareOperatorBaseline: %w", err)
	}
	stop()
	stop = timer.Start(metrics.PhaseCompute)
	if err = middlewareoperator.ReplacePackage(ctxkeys.WithScheme(ctx, r.Scheme), r.Client, mo); err != nil {
		stop()
		log.FromContext(ctx).Error(err, "failed to replace package for MiddlewareOperator", "namespace", mo.Namespace, "name", mo.Name)
		return fmt.Errorf("upgrade failed: %w", err)
	}
	stop()
	if middlewareoperator.IsNoOperatorResource(mo) {
		return consts.NoOperator
	}
	// Compare generation
	// observedGeneration == 0 means initial publish
	// generation > observedGeneration or State == Updating means update is needed
	if observedGeneration == 0 {
		stop = timer.Start(metrics.PhaseAPIWrite)
		if err = middlewareoperator.HandleResource(ctxkeys.WithScheme(ctx, r.Scheme), r.Client, consts.HandleActionPublish, mo); err != nil {
			stop()
			return fmt.Errorf("failed to generate resources: %w", err)
		}
		stop()
		r.Recorder.Event(mo, "Normal", "Published", "MiddlewareOperator published successfully")
	} else if generation > observedGeneration || mo.Status.State == v1.StateUpdating { // actual > observed means update needed
		stop = timer.Start(metrics.PhaseAPIWrite)
		if err = middlewareoperator.HandleResource(ctxkeys.WithScheme(ctx, r.Scheme), r.Client, consts.HandleActionUpdate, mo); err != nil {
			stop()
			return fmt.Errorf("failed to update resources: %w", err)
		}
		stop()
		r.Recorder.Event(mo, "Normal", "Updated", "MiddlewareOperator updated successfully")
	}

	return nil
}

// handleDeployment handles the deployment reconcile logic.
func (r *MiddlewareOperatorReconciler) handleDeployment(ctx context.Context, req ctrl.Request) error {
	timer := metrics.TimerFromContext(ctx)

	stop := timer.Start(metrics.PhaseAPIRead)
	mo, err := k8s.GetMiddlewareOperator(ctx, r.Client, req.Name, req.Namespace)
	stop()
	if err != nil {
		return err
	}
	if _, ok := mo.Annotations[v1.LabelUpdate]; ok {
		log.FromContext(ctx).Info("MiddlewareOperator is updating, please try again later", "warning", true, "name", req.Name)
		return nil
	}
	// Get deployment
	stop = timer.Start(metrics.PhaseAPIRead)
	deployment, err := k8s.GetDeployment(ctx, r.Client, req.Name, req.Namespace)
	stop()
	if err == nil {
		// Compare the actual deployment against the published deployment for diffs
		stop = timer.Start(metrics.PhaseCompute)
		err = middlewareoperator.CompareDeployment(ctxkeys.WithScheme(ctx, r.Scheme), r.Client, deployment, mo)
		stop()
		if err != nil {
			return fmt.Errorf("failed to compare deployment with published deployment: %w", err)
		}
	} else if apiErrors.IsNotFound(err) {
		conditionApplyOperator := status.GetCondition(ctx, new(mo.Status.Conditions), v1.CondTypeApplyOperator)
		if conditionApplyOperator.Status == metav1.ConditionTrue {
			// Regenerate resources
			stop = timer.Start(metrics.PhaseAPIWrite)
			if err = middlewareoperator.HandleResource(ctxkeys.WithScheme(ctx, r.Scheme), r.Client, consts.HandleActionPublish, mo); err != nil {
				stop()
				r.Recorder.Event(mo, "Warning", "BuildResource", err.Error())
				return fmt.Errorf("failed to regenerate resources: %w", err)
			}
			stop()
		}
	} else {
		return fmt.Errorf("failed to get deployment: %w", err)
	}
	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *MiddlewareOperatorReconciler) SetupWithManager(mgr ctrl.Manager) error {
	b := ctrl.NewControllerManagedBy(mgr)

	// For: MiddlewareOperator — skip status-only updates, allow spec/metadata changes.
	// Cannot use GenerationChangedPredicate because annotation changes (e.g. upgrade-triggered
	// middleware.cn/update) do not bump generation, which would cause missed upgrade reconciles.
	// Consistent with the Middleware controller filtering strategy.
	moPred := predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldObj := e.ObjectOld.(*v1.MiddlewareOperator)
			newObj := e.ObjectNew.(*v1.MiddlewareOperator)
			if !equality.Semantic.DeepEqual(oldObj.Status, newObj.Status) {
				return false
			}
			return true
		},
	}
	b = b.For(&v1.MiddlewareOperator{}, builder.WithPredicates(moPred))

	// Owns: Deployment — skip Deployment status-only updates (only enqueue on spec/labels/annotations changes)
	b = b.Owns(&appsv1.Deployment{}, builder.WithPredicates(predicate.GenerationChangedPredicate{}))

	return b.Named("middlewareoperator").WithOptions(concurrency.ControllerOptions("MIDDLEWAREOPERATOR", 1)).Complete(r)
}

// deriveRuntimePhase derives a runtime phase summary from the Deployment status.
func deriveRuntimePhase(status *appsv1.DeploymentStatus) string {
	// Check if there are any replicas
	if status.Replicas == 0 {
		return "Scaled to Zero"
	}

	// Check if all replicas are ready
	if status.ReadyReplicas == status.Replicas && status.AvailableReplicas > 0 {
		return "Ready"
	}

	// Check if any replicas have failed
	for _, cond := range status.Conditions {
		if cond.Type == appsv1.DeploymentReplicaFailure && cond.Status == "True" {
			return "ReplicaFailure"
		}
		if cond.Type == appsv1.DeploymentProgressing && cond.Status == "False" {
			return "Unavailable"
		}
	}

	// Check if rollout is in progress
	if status.UpdatedReplicas < status.Replicas || status.ReadyReplicas < status.Replicas {
		return "Progressing"
	}

	// Default to unknown
	return "Unknown"
}
