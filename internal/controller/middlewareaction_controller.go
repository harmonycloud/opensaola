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
	"fmt"
	"time"

	"github.com/opensaola/opensaola/internal/service/middlewareactionbaseline"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/opensaola/opensaola/api/v1"
	"github.com/opensaola/opensaola/internal/concurrency"
	"github.com/opensaola/opensaola/internal/k8s"
	"github.com/opensaola/opensaola/internal/service/middlewareaction"
	metrics "github.com/opensaola/opensaola/pkg/metrics"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

// MiddlewareActionReconciler reconciles a MiddlewareAction object
type MiddlewareActionReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

//+kubebuilder:rbac:groups=middleware.cn,resources=middlewareactions,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=middleware.cn,resources=middlewareactions/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=middleware.cn,resources=middlewareactions/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the MiddlewareAction object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.20.2/pkg/reconcile
func (r *MiddlewareActionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, retErr error) {
	startTime := time.Now()
	_, timer := metrics.NewReconcileTimer(ctx, "middlewareaction")
	defer func() {
		metrics.ObserveReconcile("middlewareaction", startTime, result.Requeue, result.RequeueAfter, retErr)
		res := metrics.ReconcileResult(result.Requeue, result.RequeueAfter, retErr)
		timer.Observe(res)
		metrics.ObserveRequeue("middlewareaction", result.Requeue, result.RequeueAfter)
		metrics.ObserveAPIError("middlewareaction", retErr)
	}()

	l := log.FromContext(ctx).WithValues("reconcileID", fmt.Sprintf("%s/%d", req.Name, time.Now().UnixMilli()))
	ctx = log.IntoContext(ctx, l)

	log.FromContext(ctx).V(1).Info("start processing MiddlewareAction", "name", req.NamespacedName)

	// Get MiddlewareAction
	stop := timer.Start(metrics.PhaseAPIRead)
	middlewareAction, err := k8s.GetMiddlewareAction(ctx, r.Client, req.Name, req.Namespace)
	stop()
	if err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if middlewareAction.Status.State != "" {
		return ctrl.Result{}, nil
	}

	defer func() {
		middlewareAction.Status.State = v1.StateAvailable
		middlewareAction.Status.Reason = ""
		for _, condition := range middlewareAction.Status.Conditions {
			if condition.Status == metav1.ConditionFalse {
				middlewareAction.Status.State = v1.StateUnavailable
				middlewareAction.Status.Reason = metav1.StatusReason(fmt.Sprintf("%s,%s", condition.Type, condition.Message))
				break
			} else if condition.Status == metav1.ConditionUnknown {
				middlewareAction.Status.State = v1.StateUnavailable
				middlewareAction.Status.Reason = metav1.StatusReason(fmt.Sprintf("%s,%s", condition.Type, condition.Message))
				break
			}
		}

		stopStatus := timer.Start(metrics.PhaseStatusWrite)
		err = k8s.UpdateMiddlewareActionStatus(ctx, r.Client, middlewareAction)
		stopStatus()
		if err != nil {
			log.FromContext(ctx).Error(err, "failed to update MiddlewareAction status", "namespace", req.Namespace, "name", req.Name)
		}
	}()

	stop = timer.Start(metrics.PhaseCompute)
	if err = middlewareaction.Check(ctx, r.Client, middlewareAction); err != nil {
		stop()
		r.Recorder.Event(middlewareAction, "Warning", "ValidationFailed", err.Error())
		log.FromContext(ctx).Error(err, "failed to validate MiddlewareAction", "namespace", req.Namespace, "name", req.Name)
		return ctrl.Result{}, err
	}
	stop()

	// Get the operatorbaseline from the package
	stop = timer.Start(metrics.PhaseAPIRead)
	middlewareActionBaseline, err := middlewareactionbaseline.Get(ctx, r.Client, middlewareAction.Spec.Baseline, middlewareAction.Labels[v1.LabelPackageName])
	stop()
	if err != nil {
		log.FromContext(ctx).Error(err, "failed to get MiddlewareActionBaseline for MiddlewareAction", "namespace", req.Namespace, "name", req.Name)
		return ctrl.Result{}, err
	}

	if middlewareActionBaseline.Spec.BaselineType != v1.WorkflowPreAction {
		stop = timer.Start(metrics.PhaseAPIWrite)
		if err = middlewareaction.Execute(ctx, r.Client, middlewareAction); err != nil {
			stop()
			r.Recorder.Event(middlewareAction, "Warning", "ExecutionFailed", err.Error())
			log.FromContext(ctx).Error(err, "failed to execute MiddlewareAction", "namespace", req.Namespace, "name", req.Name)
			return ctrl.Result{}, err
		}
		stop()
		r.Recorder.Event(middlewareAction, "Normal", "Executed", "Action executed successfully")
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *MiddlewareActionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1.MiddlewareAction{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Named("middlewareaction").
		WithOptions(concurrency.ControllerOptions("MIDDLEWAREACTION", 1)).
		Complete(r)
}
