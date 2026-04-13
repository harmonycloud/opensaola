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
	"time"

	"github.com/OpenSaola/opensaola/api/v1"
	"github.com/OpenSaola/opensaola/internal/k8s"
	zeusmetrics "github.com/OpenSaola/opensaola/pkg/metrics"
	"github.com/OpenSaola/opensaola/internal/resource/logger"
	"github.com/OpenSaola/opensaola/internal/service/middlewareactionbaseline"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

// MiddlewareActionBaselineReconciler reconciles a MiddlewareActionBaseline object
type MiddlewareActionBaselineReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=middleware.cn,resources=middlewareactionbaselines,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=middleware.cn,resources=middlewareactionbaselines/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=middleware.cn,resources=middlewareactionbaselines/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the MiddlewareActionBaseline object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.20.2/pkg/reconcile
func (r *MiddlewareActionBaselineReconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, retErr error) {
	startTime := time.Now()
	_, timer := zeusmetrics.NewReconcileTimer(ctx, "middlewareactionbaseline")
	defer func() {
		zeusmetrics.ObserveReconcile("middlewareactionbaseline", startTime, result.Requeue, result.RequeueAfter, retErr)
		res := zeusmetrics.ReconcileResult(result.Requeue, result.RequeueAfter, retErr)
		timer.Observe(res)
		zeusmetrics.ObserveRequeue("middlewareactionbaseline", result.Requeue, result.RequeueAfter)
		zeusmetrics.ObserveAPIError("middlewareactionbaseline", retErr)
	}()
	logger.Log.Debugj(map[string]interface{}{"amsg": "start processing middlewareActionBaseline", "req": req})

	// Get MiddlewareActionBaseline
	stop := timer.Start(zeusmetrics.PhaseAPIRead)
	middlewareActionBaseline, err := k8s.GetMiddlewareActionBaseline(ctx, r.Client, req.Name)
	stop()
	if err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Validate middlewareActionBaseline
	stop = timer.Start(zeusmetrics.PhaseCompute)
	if err = middlewareactionbaseline.Check(ctx, r.Client, middlewareActionBaseline); err != nil {
		stop()
		return ctrl.Result{}, err
	}
	stop()
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *MiddlewareActionBaselineReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1.MiddlewareActionBaseline{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Named("middlewareactionbaseline").
		Complete(r)
}
