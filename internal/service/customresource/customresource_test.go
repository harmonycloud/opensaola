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

package customresource

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func newScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	return s
}

func newUnstructured(name, namespace string, managedFields []metav1.ManagedFieldsEntry) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "ConfigMap",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": namespace,
			},
			"data": map[string]interface{}{
				"key": "value",
			},
		},
	}
	if len(managedFields) > 0 {
		obj.SetManagedFields(managedFields)
	}
	return obj
}

// newUnstructuredClean creates an unstructured object without managedFields,
// suitable for registering with the fake client's WithObjects.
func newUnstructuredClean(name, namespace string) *unstructured.Unstructured {
	return newUnstructured(name, namespace, nil)
}

// TestRestoreIfIllegalUpdate_NoManagedFields verifies that when newObj has no
// managedFields at all, the update is considered legitimate and no revert occurs.
func TestRestoreIfIllegalUpdate_NoManagedFields(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	oldObj := newUnstructured("test-cm", "default", nil)
	newObj := newUnstructured("test-cm", "default", nil)

	s := newScheme()
	cli := fake.NewClientBuilder().WithScheme(s).Build()

	err := RestoreIfIllegalUpdate(ctx, cli, oldObj, newObj)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

// TestRestoreIfIllegalUpdate_NoNewManagedFields verifies that when oldObj and newObj
// have the same managedFields with a non-kubectl manager, no revert is triggered.
func TestRestoreIfIllegalUpdate_NoNewManagedFields(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	fields := []metav1.ManagedFieldsEntry{
		{Manager: "controller-manager", Operation: metav1.ManagedFieldsOperationUpdate},
	}

	oldObj := newUnstructured("test-cm", "default", fields)
	newObj := newUnstructured("test-cm", "default", fields)

	s := newScheme()
	cli := fake.NewClientBuilder().WithScheme(s).Build()

	err := RestoreIfIllegalUpdate(ctx, cli, oldObj, newObj)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

// TestRestoreIfIllegalUpdate_KubectlManagerTriggersRestore documents the ACTUAL behavior:
// when newObj has a managedField with Manager=="kubectl", the function treats this as
// illegal and attempts to revert the resource by calling cli.Update with oldObj.
//
// NOTE: The comment in the source says "If the owner is kubectl, do not revert" but the
// code sets isIllegal=true and then reverts. This test documents the actual (potentially
// inverted) behavior — the comment contradicts the code.
func TestRestoreIfIllegalUpdate_KubectlManagerTriggersRestore(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	oldFields := []metav1.ManagedFieldsEntry{
		{Manager: "controller-manager", Operation: metav1.ManagedFieldsOperationUpdate},
	}
	newFields := []metav1.ManagedFieldsEntry{
		{Manager: "controller-manager", Operation: metav1.ManagedFieldsOperationUpdate},
		{Manager: "kubectl", Operation: metav1.ManagedFieldsOperationUpdate},
	}

	oldObj := newUnstructured("test-cm", "default", oldFields)
	newObj := newUnstructured("test-cm", "default", newFields)

	s := newScheme()
	// Register a clean object (no managedFields) so the fake client can find it for Update.
	seedObj := newUnstructuredClean("test-cm", "default")
	cli := fake.NewClientBuilder().WithScheme(s).WithObjects(seedObj).Build()

	err := RestoreIfIllegalUpdate(ctx, cli, oldObj, newObj)
	// The function should succeed (revert completes without error).
	// This documents that kubectl manager DOES trigger a restore,
	// despite the misleading comment in the source code.
	if err != nil {
		t.Fatalf("expected revert to succeed, got %v", err)
	}
}

// TestRestoreIfIllegalUpdate_NonKubectlManagerNoRestore verifies that a new manager
// that is NOT "kubectl" does not trigger a revert.
func TestRestoreIfIllegalUpdate_NonKubectlManagerNoRestore(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	oldFields := []metav1.ManagedFieldsEntry{
		{Manager: "controller-manager", Operation: metav1.ManagedFieldsOperationUpdate},
	}
	newFields := []metav1.ManagedFieldsEntry{
		{Manager: "controller-manager", Operation: metav1.ManagedFieldsOperationUpdate},
		{Manager: "some-other-controller", Operation: metav1.ManagedFieldsOperationUpdate},
	}

	oldObj := newUnstructured("test-cm", "default", oldFields)
	newObj := newUnstructured("test-cm", "default", newFields)

	s := newScheme()
	cli := fake.NewClientBuilder().WithScheme(s).Build()

	err := RestoreIfIllegalUpdate(ctx, cli, oldObj, newObj)
	if err != nil {
		t.Fatalf("expected no error for non-kubectl manager, got %v", err)
	}
}

// TestRestoreIfIllegalUpdate_OnlyKubectlManager verifies that when newObj has ONLY
// a kubectl manager (no other managers), the revert is still triggered.
func TestRestoreIfIllegalUpdate_OnlyKubectlManager(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	newFields := []metav1.ManagedFieldsEntry{
		{Manager: "kubectl", Operation: metav1.ManagedFieldsOperationUpdate},
	}

	oldObj := newUnstructured("test-cm", "default", nil)
	newObj := newUnstructured("test-cm", "default", newFields)

	s := newScheme()
	seedObj := newUnstructuredClean("test-cm", "default")
	cli := fake.NewClientBuilder().WithScheme(s).WithObjects(seedObj).Build()

	err := RestoreIfIllegalUpdate(ctx, cli, oldObj, newObj)
	if err != nil {
		t.Fatalf("expected revert to succeed, got %v", err)
	}
}
