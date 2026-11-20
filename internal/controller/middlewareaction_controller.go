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

	"github.com/harmonycloud/opensaola/api/v1"
	"github.com/harmonycloud/opensaola/internal/concurrency"
	"github.com/harmonycloud/opensaola/internal/k8s"
	"github.com/harmonycloud/opensaola/internal/service/middlewareaction"
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
func (r *MiddlewareActionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	ctx = withReconcileLogger(ctx, "middlewareaction", "MiddlewareAction", req)

	log.FromContext(ctx).V(1).Info("start processing MiddlewareAction", "name", req.NamespacedName)

	// Get MiddlewareAction
	middlewareAction, err := k8s.GetMiddlewareAction(ctx, r.Client, req.Name, req.Namespace)
	if err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if middlewareAction.Status.State == v1.StateAvailable || middlewareAction.Status.State == v1.StateUnavailable {
		return ctrl.Result{}, nil
	}
	if err := middlewareaction.Execute(ctx, r.Client, middlewareAction); err != nil {
		r.Recorder.Event(middlewareAction, "Warning", "ActionFailed", err.Error())
		return ctrl.Result{}, err
	}
	if middlewareAction.Status.State == v1.StateAvailable {
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
