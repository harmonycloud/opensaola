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
	"reflect"

	"github.com/OpenSaola/opensaola/internal/concurrency"
	"github.com/OpenSaola/opensaola/internal/k8s"
	appsv1 "k8s.io/api/apps/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

// MiddlewareOperatorRuntimeReconciler is solely responsible for syncing Deployment runtime state to MiddlewareOperator.status.
type MiddlewareOperatorRuntimeReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=middleware.cn,resources=middlewareoperators,verbs=get;list;watch
//+kubebuilder:rbac:groups=middleware.cn,resources=middlewareoperators/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch
//+kubebuilder:rbac:groups=apps,resources=deployments/status,verbs=get;list;watch

func (r *MiddlewareOperatorRuntimeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	deployment := &appsv1.Deployment{}
	if err := r.Get(ctx, req.NamespacedName, deployment); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	moName, err := middlewareOperatorNameFromDeployment(deployment)
	if err != nil {
		log.FromContext(ctx).Error(err, "Deployment has invalid ownerReferences", "namespace", deployment.Namespace, "name", deployment.Name)
		return ctrl.Result{}, err
	}

	mo, err := k8s.GetMiddlewareOperator(ctx, r.Client, moName, deployment.Namespace)
	if err != nil {
		if apiErrors.IsNotFound(err) {
			log.FromContext(ctx).Error(err, "Deployment corresponding MiddlewareOperator not found", "namespace", deployment.Namespace, "deploymentName", deployment.Name, "moName", moName)
		}
		return ctrl.Result{}, err
	}

	// Build runtime fields and write via a dedicated merge function to avoid overwriting
	// State/Conditions/ObservedGeneration/Reason managed by the main controller.
	operatorStatus := mo.Status.OperatorStatus
	if operatorStatus == nil {
		operatorStatus = make(map[string]appsv1.DeploymentStatus)
	}
	operatorStatus[deployment.Name] = deployment.Status

	if err := k8s.UpdateMiddlewareOperatorRuntimeStatus(
		ctx, r.Client, moName, deployment.Namespace,
		operatorStatus,
		fmt.Sprintf("%d/%d", deployment.Status.AvailableReplicas, deployment.Status.Replicas),
		deployment.Status.ReadyReplicas > 0 &&
			deployment.Status.ReadyReplicas == deployment.Status.Replicas &&
			deployment.Status.AvailableReplicas > 0,
		deriveRuntimePhase(&deployment.Status),
	); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *MiddlewareOperatorRuntimeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&appsv1.Deployment{}, builder.WithPredicates(predicate.Funcs{
			CreateFunc: func(e event.CreateEvent) bool {
				deployment, ok := e.Object.(*appsv1.Deployment)
				return ok && deployment != nil && hasMiddlewareOperatorOwner(deployment)
			},
			UpdateFunc: func(e event.UpdateEvent) bool {
				oldDeployment, oldOK := e.ObjectOld.(*appsv1.Deployment)
				newDeployment, newOK := e.ObjectNew.(*appsv1.Deployment)
				if !oldOK || !newOK || oldDeployment == nil || newDeployment == nil {
					return false
				}
				if !hasMiddlewareOperatorOwner(newDeployment) && !hasMiddlewareOperatorOwner(oldDeployment) {
					return false
				}
				return deploymentRuntimeStatusChanged(oldDeployment, newDeployment)
			},
			DeleteFunc:  func(event.DeleteEvent) bool { return false },
			GenericFunc: func(event.GenericEvent) bool { return false },
		})).
		Named("middlewareoperator-runtime").
		WithOptions(concurrency.ControllerOptions("MIDDLEWAREOPERATOR_RUNTIME", 1)).
		Complete(r)
}

// hasMiddlewareOperatorOwner returns true if the Deployment has at least one
// ownerReference with Kind == "MiddlewareOperator". Used by the predicate to
// silently skip unrelated Deployments before they enter Reconcile.
// Deployments that pass this check but have an invalid ownerReference structure
// (e.g. multiple owners, empty name) will still be caught and logged in Reconcile.
func hasMiddlewareOperatorOwner(deployment *appsv1.Deployment) bool {
	for _, ref := range deployment.GetOwnerReferences() {
		if ref.Kind == "MiddlewareOperator" {
			return true
		}
	}
	return false
}

func middlewareOperatorNameFromDeployment(deployment *appsv1.Deployment) (string, error) {
	ownerReferences := deployment.GetOwnerReferences()
	if len(ownerReferences) != 1 {
		return "", fmt.Errorf("expected exactly 1 ownerReference, got %d", len(ownerReferences))
	}
	ownerReference := ownerReferences[0]
	if ownerReference.Kind != "MiddlewareOperator" {
		return "", fmt.Errorf("unexpected owner kind %q", ownerReference.Kind)
	}
	if ownerReference.Name == "" {
		return "", fmt.Errorf("ownerReference name is empty")
	}
	return ownerReference.Name, nil
}

func deploymentRuntimeStatusChanged(oldDeployment, newDeployment *appsv1.Deployment) bool {
	if !reflect.DeepEqual(oldDeployment.OwnerReferences, newDeployment.OwnerReferences) {
		return true
	}
	return !reflect.DeepEqual(oldDeployment.Status, newDeployment.Status)
}
