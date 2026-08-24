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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
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
