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

	v1 "github.com/OpenSaola/opensaola/api/v1"
	"github.com/OpenSaola/opensaola/internal/k8s"
	zeusmetrics "github.com/OpenSaola/opensaola/pkg/metrics"
	"github.com/OpenSaola/opensaola/internal/resource/logger"
	"github.com/OpenSaola/opensaola/internal/service/middlewareoperatorbaseline"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

// MiddlewareOperatorBaselineReconciler reconciles a MiddlewareOperatorBaseline object
type MiddlewareOperatorBaselineReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=middleware.cn,resources=middlewareoperatorbaselines,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=middleware.cn,resources=middlewareoperatorbaselines/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=middleware.cn,resources=middlewareoperatorbaselines/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the MiddlewareOperatorBaseline object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.20.2/pkg/reconcile
func (r *MiddlewareOperatorBaselineReconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, retErr error) {
	startTime := time.Now()
	_, timer := zeusmetrics.NewReconcileTimer(ctx, "middlewareoperatorbaseline")
	defer func() {
		zeusmetrics.ObserveReconcile("middlewareoperatorbaseline", startTime, result.Requeue, result.RequeueAfter, retErr)
		res := zeusmetrics.ReconcileResult(result.Requeue, result.RequeueAfter, retErr)
		timer.Observe(res)
		zeusmetrics.ObserveRequeue("middlewareoperatorbaseline", result.Requeue, result.RequeueAfter)
		zeusmetrics.ObserveAPIError("middlewareoperatorbaseline", retErr)
	}()
	zlog := log.FromContext(ctx)

	logger.Log.Debugj(map[string]interface{}{
		"amsg": "start processing middlewareOperatorBaseline",
		"req":  req,
	})

	// Get middlewareOperatorBaseline
	stop := timer.Start(zeusmetrics.PhaseAPIRead)
	middlewareOperatorBaseline, err := k8s.GetMiddlewareOperatorBaseline(ctx, r.Client, req.Name)
	stop()
	if err != nil {
		if !errors.IsNotFound(err) {
			zlog.Error(err, "failed to get middlewareOperatorBaseline")
		}
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	stop = timer.Start(zeusmetrics.PhaseCompute)
	if err := middlewareoperatorbaseline.Check(ctx, r.Client, middlewareOperatorBaseline); err != nil {
		stop()
		zlog.Error(err, "failed to validate middlewareOperatorBaseline")
		return ctrl.Result{}, err
	}
	stop()

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *MiddlewareOperatorBaselineReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1.MiddlewareOperatorBaseline{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Named("middlewareoperatorbaseline").
		Complete(r)
}
