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

	v1 "github.com/harmonycloud/opensaola/api/v1"
	"github.com/harmonycloud/opensaola/internal/concurrency"
	"github.com/harmonycloud/opensaola/internal/k8s"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

// MiddlewareOperatorRuntimeReconciler is solely responsible for syncing Deployment runtime state to MiddlewareOperator.status.
type MiddlewareOperatorRuntimeReconciler struct {
	client.Client
	Scheme    *runtime.Scheme
	APIReader client.Reader
}

//+kubebuilder:rbac:groups=middleware.cn,resources=middlewareoperators,verbs=get;list;watch
//+kubebuilder:rbac:groups=middleware.cn,resources=middlewareoperators/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=apps,resources=deployments;replicasets,verbs=get;list;watch
//+kubebuilder:rbac:groups="",resources=pods;persistentvolumeclaims;events,verbs=get;list;watch

// Preserve the owner's identity when enqueueing. After a deletion the
// Deployment no longer exists, and a different MO can have the same name.
type runtimeDeploymentRequest struct {
	Deployment client.ObjectKey
	OwnerName  string
	OwnerUID   types.UID
	Primary    bool
}

func runtimeDeploymentRequests(_ context.Context, obj client.Object) []runtimeDeploymentRequest {
	d, ok := obj.(*appsv1.Deployment)
	if !ok {
		return nil
	}
	ref, err := middlewareOperatorOwnerFromDeployment(d)
	if err != nil || ref.UID == "" || (metav1.GetControllerOf(d) != nil && (ref.Controller == nil || !*ref.Controller)) {
		return nil
	}
	gv, err := schema.ParseGroupVersion(ref.APIVersion)
	if err != nil || gv.Group != v1.GroupVersion.Group {
		return nil
	}
	primary := d.Name == ref.Name && ref.Controller != nil && *ref.Controller && d.Annotations[v1.LabelConfigurations] == ""
	return []runtimeDeploymentRequest{{Deployment: client.ObjectKeyFromObject(d), OwnerName: ref.Name, OwnerUID: ref.UID, Primary: primary}}
}

// An initial informer list after an upgrade or restart also repairs status
// for previously recorded Deployments whose delete event was missed.
func recordedRuntimeRequests(_ context.Context, obj client.Object) []runtimeDeploymentRequest {
	mo, ok := obj.(*v1.MiddlewareOperator)
	if !ok {
		return nil
	}
	requests := make([]runtimeDeploymentRequest, 0, len(mo.Status.OperatorStatus)+1)
	hasSummary := mo.Status.Runtime != "" || mo.Status.Ready || mo.Status.OperatorAvailable != ""
	for name := range mo.Status.OperatorStatus {
		// A same-name MCF may have a detail without ever having had a primary.
		if name != mo.Name || !hasSummary {
			requests = append(requests, runtimeDeploymentRequest{Deployment: client.ObjectKey{Namespace: mo.Namespace, Name: name}, OwnerName: mo.Name, OwnerUID: mo.UID})
		}
	}
	if hasSummary {
		requests = append(requests, runtimeDeploymentRequest{Deployment: client.ObjectKeyFromObject(mo), OwnerName: mo.Name, OwnerUID: mo.UID, Primary: true})
	}
	return requests
}

func (r *MiddlewareOperatorRuntimeReconciler) Reconcile(ctx context.Context, req runtimeDeploymentRequest) (ctrl.Result, error) {
	ctx = withReconcileLogger(ctx, "middlewareoperator-runtime", "Deployment", ctrl.Request{NamespacedName: req.Deployment})
	reader := r.APIReader
	if reader == nil {
		reader = r.Client
	}
	ownerKey := client.ObjectKey{Namespace: req.Deployment.Namespace, Name: req.OwnerName}
	mo := &v1.MiddlewareOperator{}
	if err := reader.Get(ctx, ownerKey, mo); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if req.OwnerUID == "" || mo.UID != req.OwnerUID || mo.DeletionTimestamp != nil {
		return ctrl.Result{}, nil
	}

	deployment := &appsv1.Deployment{}
	err := reader.Get(ctx, req.Deployment, deployment)
	if err != nil && !apiErrors.IsNotFound(err) {
		return ctrl.Result{}, err
	}
	var detail *appsv1.DeploymentStatus
	var summary *k8s.OperatorRuntimeSummary
	noOperator := mo.Annotations[v1.LabelNoOperator] == "true"
	if req.Primary {
		summary = &k8s.OperatorRuntimeSummary{}
		if !noOperator {
			summary.Runtime = "DeploymentMissing"
		}
	}
	if err == nil {
		current := runtimeDeploymentRequests(ctx, deployment)
		if len(current) == 1 && current[0].OwnerUID == mo.UID && current[0].OwnerName == mo.Name {
			detail = &deployment.Status
			if current[0].Primary && !noOperator {
				snapshot := r.collectRuntimeObjects(ctx, deployment)
				diagnostic := deriveRuntimeDiagnostic(mo.Name, deployment, snapshot.replicaSets, snapshot.pods, snapshot.pvcs, snapshot.events, snapshot.err)
				summary = &k8s.OperatorRuntimeSummary{Available: fmt.Sprintf("%d/%d", deployment.Status.AvailableReplicas, deployment.Status.Replicas), Ready: deploymentRuntimeReady(deployment) && diagnostic == "Ready", Runtime: diagnostic}
				if deployment.DeletionTimestamp != nil {
					summary.Ready = false
					summary.Runtime = "DeploymentTerminating"
				}
			}
		}
	}
	err = k8s.UpdateMiddlewareOperatorRuntimeStatus(ctx, r.Client, ownerKey, mo.UID, req.Deployment.Name, detail, summary)
	return ctrl.Result{}, err
}

func (r *MiddlewareOperatorRuntimeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.APIReader = mgr.GetAPIReader()
	return builder.TypedControllerManagedBy[runtimeDeploymentRequest](mgr).
		Watches(&v1.MiddlewareOperator{}, handler.TypedEnqueueRequestsFromMapFunc(recordedRuntimeRequests), builder.WithPredicates(predicate.Funcs{
			UpdateFunc: func(e event.UpdateEvent) bool {
				return e.ObjectOld.GetAnnotations()[v1.LabelNoOperator] != e.ObjectNew.GetAnnotations()[v1.LabelNoOperator]
			},
			DeleteFunc:  func(event.DeleteEvent) bool { return false },
			GenericFunc: func(event.GenericEvent) bool { return false },
		})).
		Watches(&appsv1.Deployment{}, handler.TypedEnqueueRequestsFromMapFunc(runtimeDeploymentRequests), builder.WithPredicates(predicate.Funcs{
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
			DeleteFunc: func(e event.DeleteEvent) bool {
				d, ok := e.Object.(*appsv1.Deployment)
				return ok && hasMiddlewareOperatorOwner(d)
			},
			GenericFunc: func(event.GenericEvent) bool { return false },
		})).
		Watches(
			&corev1.Pod{},
			handler.TypedEnqueueRequestsFromMapFunc(r.runtimePodToDeploymentRequests),
			builder.WithPredicates(predicate.Funcs{
				CreateFunc: func(e event.CreateEvent) bool {
					return e.Object != nil
				},
				UpdateFunc: func(e event.UpdateEvent) bool {
					oldPod, oldOK := e.ObjectOld.(*corev1.Pod)
					newPod, newOK := e.ObjectNew.(*corev1.Pod)
					return oldOK && newOK && runtimePodChanged(oldPod, newPod)
				},
				DeleteFunc:  func(event.DeleteEvent) bool { return false },
				GenericFunc: func(event.GenericEvent) bool { return false },
			}),
		).
		Watches(
			&corev1.PersistentVolumeClaim{},
			handler.TypedEnqueueRequestsFromMapFunc(r.runtimePVCToDeploymentRequests),
			builder.WithPredicates(predicate.Funcs{
				CreateFunc: func(e event.CreateEvent) bool {
					return e.Object != nil
				},
				UpdateFunc: func(e event.UpdateEvent) bool {
					oldPVC, oldOK := e.ObjectOld.(*corev1.PersistentVolumeClaim)
					newPVC, newOK := e.ObjectNew.(*corev1.PersistentVolumeClaim)
					return oldOK && newOK && !reflect.DeepEqual(oldPVC.Status, newPVC.Status)
				},
				DeleteFunc:  func(event.DeleteEvent) bool { return false },
				GenericFunc: func(event.GenericEvent) bool { return false },
			}),
		).
		Watches(
			&corev1.Event{},
			handler.TypedEnqueueRequestsFromMapFunc(r.runtimeEventToDeploymentRequests),
			builder.WithPredicates(predicate.Funcs{
				CreateFunc: func(e event.CreateEvent) bool {
					eventObj, ok := e.Object.(*corev1.Event)
					return ok && eventObj != nil && isInterestingEvent(*eventObj)
				},
				UpdateFunc: func(e event.UpdateEvent) bool {
					oldEvent, oldOK := e.ObjectOld.(*corev1.Event)
					newEvent, newOK := e.ObjectNew.(*corev1.Event)
					return oldOK && newOK && isInterestingEvent(*newEvent) && runtimeEventChanged(oldEvent, newEvent)
				},
				DeleteFunc:  func(event.DeleteEvent) bool { return false },
				GenericFunc: func(event.GenericEvent) bool { return false },
			}),
		).
		Named("middlewareoperator-runtime").
		WithOptions(concurrency.TypedControllerOptions[runtimeDeploymentRequest]("MIDDLEWAREOPERATOR_RUNTIME", 1)).
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

func middlewareOperatorOwnerFromDeployment(deployment *appsv1.Deployment) (*metav1.OwnerReference, error) {
	ownerReferences := deployment.GetOwnerReferences()
	var controllerOwners []metav1.OwnerReference
	var middlewareOperatorOwners []metav1.OwnerReference
	for _, ownerReference := range ownerReferences {
		if ownerReference.Kind != "MiddlewareOperator" {
			continue
		}
		middlewareOperatorOwners = append(middlewareOperatorOwners, ownerReference)
		if ownerReference.Controller != nil && *ownerReference.Controller {
			controllerOwners = append(controllerOwners, ownerReference)
		}
	}
	if len(controllerOwners) > 1 {
		return nil, fmt.Errorf("expected at most 1 controller MiddlewareOperator ownerReference, got %d", len(controllerOwners))
	}
	if len(controllerOwners) == 1 {
		if controllerOwners[0].Name == "" {
			return nil, fmt.Errorf("controller MiddlewareOperator ownerReference name is empty")
		}
		return &controllerOwners[0], nil
	}
	if len(middlewareOperatorOwners) == 0 {
		return nil, fmt.Errorf("missing MiddlewareOperator ownerReference")
	}
	if len(middlewareOperatorOwners) > 1 {
		return nil, fmt.Errorf("expected exactly 1 MiddlewareOperator ownerReference when controller owner is absent, got %d", len(middlewareOperatorOwners))
	}
	if middlewareOperatorOwners[0].Name == "" {
		return nil, fmt.Errorf("MiddlewareOperator ownerReference name is empty")
	}
	return &middlewareOperatorOwners[0], nil
}

func deploymentRuntimeStatusChanged(oldDeployment, newDeployment *appsv1.Deployment) bool {
	if oldDeployment.Generation != newDeployment.Generation || !oldDeployment.DeletionTimestamp.Equal(newDeployment.DeletionTimestamp) || oldDeployment.Annotations[v1.LabelConfigurations] != newDeployment.Annotations[v1.LabelConfigurations] || !reflect.DeepEqual(oldDeployment.OwnerReferences, newDeployment.OwnerReferences) {
		return true
	}
	return !reflect.DeepEqual(oldDeployment.Status, newDeployment.Status)
}
