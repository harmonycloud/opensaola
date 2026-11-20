/*
Copyright 2026 The OpenSaola Authors.

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
	"testing"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func TestHandleDeploymentSkipsDeletingMiddlewareOperator(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := v1.AddToScheme(scheme); err != nil {
		t.Fatalf("add OpenSaola scheme: %v", err)
	}
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add apps scheme: %v", err)
	}

	deletingAt := metav1.Now()
	mo := &v1.MiddlewareOperator{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "deleting-operator",
			Namespace:         "test",
			DeletionTimestamp: &deletingAt,
			Finalizers:        []string{v1.FinalizerMiddlewareOperator},
		},
		Status: v1.MiddlewareOperatorStatus{
			Conditions: []metav1.Condition{{
				Type:   v1.CondTypeApplyOperator,
				Status: metav1.ConditionTrue,
			}},
		},
	}
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(mo).Build()
	reconciler := &MiddlewareOperatorReconciler{Client: cli, Scheme: scheme}

	err := reconciler.handleDeployment(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: mo.Name, Namespace: mo.Namespace},
	})
	if err != nil {
		t.Fatalf("deleting MiddlewareOperator deployment reconciliation: %v", err)
	}
}

// A failed status request can return NotFound for a missing /status endpoint.
// Only a missing owner at lookup is terminal. A dependent failure must retry.
func TestMiddlewareOperatorRetriesDependentNotFound(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := v1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	mo := &v1.MiddlewareOperator{ObjectMeta: metav1.ObjectMeta{Name: "publish", Namespace: "test", Generation: 1, Finalizers: []string{v1.FinalizerMiddlewareOperator}}, Spec: v1.MiddlewareOperatorSpec{Baseline: "operator"}}
	cli := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(mo).WithObjects(mo).WithInterceptorFuncs(interceptor.Funcs{
		SubResourceUpdate: func(ctx context.Context, c client.Client, subResource string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
			return apierrors.NewNotFound(schema.GroupResource{Group: v1.GroupVersion.Group, Resource: "middlewareoperators/status"}, obj.GetName())
		},
	}).Build()
	r := &MiddlewareOperatorReconciler{Client: cli, Scheme: scheme, Recorder: record.NewFakeRecorder(10)}
	if _, err := r.Reconcile(t.Context(), reconcile.Request{NamespacedName: client.ObjectKeyFromObject(mo)}); !apierrors.IsNotFound(err) {
		t.Fatalf("expected a retry for a missing status endpoint, got %v", err)
	}
	if result, err := r.Reconcile(t.Context(), reconcile.Request{NamespacedName: types.NamespacedName{Name: "gone", Namespace: mo.Namespace}}); err != nil || !result.IsZero() {
		t.Fatalf("expected missing owner to finish, got %v, %v", result, err)
	}
}
