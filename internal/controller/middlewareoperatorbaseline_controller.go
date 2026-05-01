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

	v1 "github.com/harmonycloud/opensaola/api/v1"
	"github.com/harmonycloud/opensaola/internal/k8s"
	"github.com/harmonycloud/opensaola/internal/service/middlewareoperatorbaseline"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

// MiddlewareOperatorBaselineReconciler reconciles a MiddlewareOperatorBaseline object
type MiddlewareOperatorBaselineReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

//+kubebuilder:rbac:groups=middleware.cn,resources=middlewareoperatorbaselines,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=middleware.cn,resources=middlewareoperatorbaselines/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=middleware.cn,resources=middlewareoperatorbaselines/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *MiddlewareOperatorBaselineReconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, retErr error) {
	l := log.FromContext(ctx).WithValues("reconcileID", fmt.Sprintf("%s/%d", req.Name, time.Now().UnixMilli()))
	ctx = log.IntoContext(ctx, l)

	log.FromContext(ctx).V(1).Info("start processing middlewareOperatorBaseline", "req", req)

	// Get middlewareOperatorBaseline
	middlewareOperatorBaseline, err := k8s.GetMiddlewareOperatorBaseline(ctx, r.Client, req.Name)
	if err != nil {
		if !errors.IsNotFound(err) {
			log.FromContext(ctx).Error(err, "failed to get middlewareOperatorBaseline")
		}
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if err := middlewareoperatorbaseline.Check(ctx, r.Client, middlewareOperatorBaseline); err != nil {
		r.Recorder.Event(middlewareOperatorBaseline, "Warning", "ValidationFailed", err.Error())
		log.FromContext(ctx).Error(err, "failed to validate middlewareOperatorBaseline")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *MiddlewareOperatorBaselineReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1.MiddlewareOperatorBaseline{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Named("middlewareoperatorbaseline").
		Complete(r)
}
