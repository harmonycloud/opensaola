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
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/OpenSaola/opensaola/api/v1"
	"github.com/OpenSaola/opensaola/pkg/k8s"
	zeusmetrics "github.com/OpenSaola/opensaola/pkg/metrics"
	"github.com/OpenSaola/opensaola/pkg/resource/logger"
	"github.com/OpenSaola/opensaola/pkg/service/consts"
	"github.com/OpenSaola/opensaola/pkg/service/middlewarepackage"
	corev1 "k8s.io/api/core/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// MiddlewarePackageReconciler reconciles a MiddlewarePackage object
type MiddlewarePackageReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

const secretRequestPrefix = "__secret__/"

//+kubebuilder:rbac:groups=middleware.cn,resources=middlewarepackages,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=middleware.cn,resources=middlewarepackages/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=middleware.cn,resources=middlewarepackages/finalizers,verbs=update
//+kubebuilder:rbac:groups=core,resources=events,verbs=create;patch
//+kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch
//+kubebuilder:rbac:groups=core,resources=secrets/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=core,resources=secrets/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *MiddlewarePackageReconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, retErr error) {
	startTime := time.Now()
	ctx, timer := zeusmetrics.NewReconcileTimer(ctx, "middlewarepackage")
	defer func() {
		zeusmetrics.ObserveReconcile("middlewarepackage", startTime, result.Requeue, result.RequeueAfter, retErr)
		res := zeusmetrics.ReconcileResult(result.Requeue, result.RequeueAfter, retErr)
		timer.Observe(res)
		zeusmetrics.ObserveRequeue("middlewarepackage", result.Requeue, result.RequeueAfter)
		zeusmetrics.ObserveAPIError("middlewarepackage", retErr)
	}()
	if strings.HasPrefix(req.Name, secretRequestPrefix) {
		secretName := strings.TrimPrefix(req.Name, secretRequestPrefix)
		logger.Log.Debugj(map[string]interface{}{
			"amsg": "start processing Secret",
			"key":  types.NamespacedName{Namespace: req.Namespace, Name: secretName}.String(),
		})
		if err := r.HandleSecret(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: req.Namespace, Name: secretName}}); err != nil {
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}
		return ctrl.Result{}, nil
	}

	logger.Log.Debugj(map[string]interface{}{
		"amsg": "start processing MiddlewarePackage",
		"name": req.Name,
	})
	if err := r.HandlePackage(ctx, req); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	return ctrl.Result{}, nil
}

func (r *MiddlewarePackageReconciler) HandlePackage(ctx context.Context, req ctrl.Request) error {
	timer := zeusmetrics.TimerFromContext(ctx)

	// Get MiddlewarePackage
	stop := timer.Start(zeusmetrics.PhaseAPIRead)
	mp, err := k8s.GetMiddlewarePackage(ctx, r.Client, req.Name)
	stop()
	if err != nil {
		return err
	}

	// Validate MiddlewarePackage
	stop = timer.Start(zeusmetrics.PhaseCompute)
	if err = middlewarepackage.Check(ctx, r.Client, mp); err != nil {
		stop()
		return err
	}
	stop()

	// if err = middlewarepackage.HandlePackage(ctx, r.Client, mp); err != nil {
	//	return err
	// }

	return nil
}

func (r *MiddlewarePackageReconciler) HandleSecret(ctx context.Context, req ctrl.Request) error {
	timer := zeusmetrics.TimerFromContext(ctx)

	stop := timer.Start(zeusmetrics.PhaseAPIRead)
	secret, err := k8s.GetSecret(ctx, r.Client, req.Name, req.Namespace)
	stop()
	if err != nil && !apiErrors.IsNotFound(err) {
		return err
	}
	if secret != nil {
		stop = timer.Start(zeusmetrics.PhaseAPIWrite)
		if err = middlewarepackage.HandleSecret(ctx, r.Client, secret, consts.HandleActionPublish); err != nil {
			stop()
			return err
		}
		stop()
	} else {
		stop = timer.Start(zeusmetrics.PhaseAPIWrite)
		if err = middlewarepackage.HandleSecret(ctx, r.Client, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      req.Name,
				Namespace: req.Namespace,
			},
		}, consts.HandleActionDelete); err != nil {
			stop()
			return err
		}
		stop()
	}
	return nil
}

func (r *MiddlewarePackageReconciler) isZeusOperatorSecret(object client.Object) bool {
	if object == nil {
		return false
	}
	return object.GetLabels()[v1.LabelProject] == consts.ProjectOpenSaola
}

func (r *MiddlewarePackageReconciler) secretPredicate() predicate.Predicate {
	annotationChanged := func(oldSecret, newSecret *corev1.Secret, key string) bool {
		oldVal, oldOk := oldSecret.Annotations[key]
		newVal, newOk := newSecret.Annotations[key]
		if oldOk != newOk {
			return true
		}
		return oldVal != newVal
	}

	return predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			return r.isZeusOperatorSecret(e.Object)
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			return r.isZeusOperatorSecret(e.Object)
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			if !r.isZeusOperatorSecret(e.ObjectNew) {
				return false
			}
			oldSecret, okOld := e.ObjectOld.(*corev1.Secret)
			newSecret, okNew := e.ObjectNew.(*corev1.Secret)
			if !okOld || !okNew {
				return true
			}

			// Only enqueue on meaningful content changes; skip metadata-only (labels/resourceVersion) updates to avoid unnecessary reconciles
			if !equality.Semantic.DeepEqual(oldSecret.Data, newSecret.Data) {
				return true
			}
			if annotationChanged(oldSecret, newSecret, v1.LabelInstall) {
				return true
			}
			if annotationChanged(oldSecret, newSecret, v1.LabelUnInstall) {
				return true
			}
			return false
		},
	}
}

func (r *MiddlewarePackageReconciler) secretToRequests(ctx context.Context, object client.Object) []reconcile.Request {
	if !r.isZeusOperatorSecret(object) {
		return nil
	}
	return []reconcile.Request{{
		NamespacedName: types.NamespacedName{
			Namespace: object.GetNamespace(),
			Name:      secretRequestPrefix + object.GetName(),
		},
	}}
}

// SetupWithManager sets up the controller with the Manager.
func (r *MiddlewarePackageReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1.MiddlewarePackage{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(
			&corev1.Secret{},
			handler.EnqueueRequestsFromMapFunc(r.secretToRequests),
			builder.WithPredicates(r.secretPredicate()),
		).
		Named("middlewarepackage").
		Complete(r)
}
