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
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	v1 "github.com/OpenSaola/opensaola/api/v1"
)

// TestFinalizerAddRemove_Middleware verifies finalizer add and remove for Middleware objects
func TestFinalizerAddRemove_Middleware(t *testing.T) {
	mid := &v1.Middleware{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-mid",
			Namespace: "test-ns",
		},
	}

	// Add finalizer
	controllerutil.AddFinalizer(mid, v1.FinalizerMiddleware)
	if !controllerutil.ContainsFinalizer(mid, v1.FinalizerMiddleware) {
		t.Fatal("expected finalizer to be present after AddFinalizer")
	}

	// Remove finalizer
	controllerutil.RemoveFinalizer(mid, v1.FinalizerMiddleware)
	if controllerutil.ContainsFinalizer(mid, v1.FinalizerMiddleware) {
		t.Fatal("expected finalizer to be removed after RemoveFinalizer")
	}
}

// TestFinalizerAddRemove_MiddlewareOperator verifies finalizer add and remove for MiddlewareOperator objects
func TestFinalizerAddRemove_MiddlewareOperator(t *testing.T) {
	mo := &v1.MiddlewareOperator{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-mo",
			Namespace: "test-ns",
		},
	}

	// Add finalizer
	controllerutil.AddFinalizer(mo, v1.FinalizerMiddlewareOperator)
	if !controllerutil.ContainsFinalizer(mo, v1.FinalizerMiddlewareOperator) {
		t.Fatal("expected finalizer to be present after AddFinalizer")
	}

	// Idempotency: duplicate add should not panic or duplicate
	controllerutil.AddFinalizer(mo, v1.FinalizerMiddlewareOperator)
	count := 0
	for _, f := range mo.Finalizers {
		if f == v1.FinalizerMiddlewareOperator {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 finalizer, got %d", count)
	}

	// Remove finalizer
	controllerutil.RemoveFinalizer(mo, v1.FinalizerMiddlewareOperator)
	if controllerutil.ContainsFinalizer(mo, v1.FinalizerMiddlewareOperator) {
		t.Fatal("expected finalizer to be removed after RemoveFinalizer")
	}
}
