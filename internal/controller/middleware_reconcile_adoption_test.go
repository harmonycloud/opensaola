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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestMiddlewarePauseMergeStateMachine(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := v1.AddToScheme(scheme); err != nil {
		t.Fatalf("add OpenSaola scheme: %v", err)
	}
	gvk := schema.GroupVersionKind{Group: "dr.controller.test.io", Version: "v1", Kind: "Database"}
	scheme.AddKnownTypeWithName(gvk, &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(gvk.GroupVersion().WithKind("DatabaseList"), &unstructured.UnstructuredList{})

	const (
		name         = "demo"
		namespace    = "middleware"
		baselineName = "baseline-controller-adoption"
		packageName  = "package-controller-adoption"
	)
	baseline := &v1.MiddlewareBaseline{
		ObjectMeta: metav1.ObjectMeta{Name: baselineName, Labels: map[string]string{v1.LabelPackageName: packageName}},
		Spec:       v1.MiddlewareBaselineSpec{GVK: v1.GVK{Group: gvk.Group, Version: gvk.Version, Kind: gvk.Kind}},
	}
	mid := &v1.Middleware{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   namespace,
			Labels:      map[string]string{v1.LabelPackageName: packageName},
			Annotations: map[string]string{v1.AnnotationSuspendReconcile: "true"},
		},
		Spec: v1.MiddlewareSpec{
			Baseline:   baselineName,
			Parameters: runtime.RawExtension{Raw: []byte(`{"replicas":3,"storage":"10Gi"}`)},
		},
	}
	live := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": gvk.GroupVersion().String(),
		"kind":       gvk.Kind,
		"metadata": map[string]any{
			"name":      name,
			"namespace": namespace,
			"uid":       "controller-dr-resource-uid",
		},
		"spec": map[string]any{"replicas": int64(3), "storage": "10Gi"},
	}}
	live.SetGroupVersionKind(gvk)
	cli := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&v1.Middleware{}).WithObjects(baseline, mid, live).Build()
	r := &MiddlewareReconciler{Client: cli, Scheme: scheme}

	// First paused reconcile captures B and does not write the actual CR.
	current := &v1.Middleware{}
	if err := cli.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, current); err != nil {
		t.Fatalf("get Middleware: %v", err)
	}
	handled, result, err := r.handleMiddlewareReconcilePause(ctx, current)
	if err != nil || !handled || result.Requeue {
		t.Fatalf("capture pause = handled:%t result:%#v err:%v", handled, result, err)
	}
	if err := cli.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, current); err != nil {
		t.Fatalf("get paused Middleware: %v", err)
	}
	if current.Status.ReconcilePause == nil || !hasReconcilePaused(current.Status.Conditions) {
		t.Fatalf("pause state was not persisted: %#v", current.Status)
	}

	// The DR writer changes the primary CR; normal desired state changes an
	// unrelated field before recovery is approved.
	actual := &unstructured.Unstructured{}
	actual.SetGroupVersionKind(gvk)
	if err := cli.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, actual); err != nil {
		t.Fatalf("get primary CR: %v", err)
	}
	actual.Object["spec"] = map[string]any{"replicas": int64(5), "storage": "10Gi"}
	if err := cli.Update(ctx, actual); err != nil {
		t.Fatalf("update primary CR: %v", err)
	}
	current.Spec.Parameters = runtime.RawExtension{Raw: []byte(`{"replicas":3,"storage":"20Gi"}`)}
	current.Annotations[v1.AnnotationResumePolicy] = string(v1.ReconcileResumePolicyMerge)
	if err := cli.Update(ctx, current); err != nil {
		t.Fatalf("request merge resume: %v", err)
	}

	if err := cli.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, current); err != nil {
		t.Fatalf("get merge-requested Middleware: %v", err)
	}
	handled, result, err = r.handleMiddlewareReconcilePause(ctx, current)
	if err != nil || !handled || !result.Requeue {
		t.Fatalf("merge pause = handled:%t result:%#v err:%v", handled, result, err)
	}
	if err := cli.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, current); err != nil {
		t.Fatalf("get merged Middleware: %v", err)
	}
	if _, exists := current.Annotations[v1.AnnotationSuspendReconcile]; exists {
		t.Fatalf("suspend annotation remained after merge: %#v", current.Annotations)
	}
	if current.Spec.ReconcileOverrides == nil || string(current.Spec.ReconcileOverrides.SpecPatch.Raw) != `{"replicas":5}` {
		t.Fatalf("merged override = %#v, want replicas=5", current.Spec.ReconcileOverrides)
	}
	adoption := middlewareReconcileAdoptionCondition(current.Status.Conditions, v1.CondTypeReconcileAdoption)
	if adoption == nil || adoption.Status != metav1.ConditionTrue || adoption.Reason != v1.CondReasonReconcileAdoptionSucceeded {
		t.Fatalf("merge adoption condition = %#v", adoption)
	}

	// Finalization occurs only after the normal desired-state apply succeeds.
	if err := r.finalizeMiddlewareReconcileResume(ctx, current); err != nil {
		t.Fatalf("finalize merge resume: %v", err)
	}
	if err := cli.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, current); err != nil {
		t.Fatalf("get finalized Middleware: %v", err)
	}
	if current.Status.ReconcilePause != nil || hasReconcilePaused(current.Status.Conditions) {
		t.Fatalf("pause state remained after successful apply finalization: %#v", current.Status)
	}
	if _, exists := current.Annotations[v1.AnnotationResumePolicy]; exists {
		t.Fatalf("resume policy remained after finalization: %#v", current.Annotations)
	}
	if adoption := middlewareReconcileAdoptionCondition(current.Status.Conditions, v1.CondTypeReconcileAdoption); adoption != nil {
		t.Fatalf("one-shot adoption condition remained after finalization: %#v", adoption)
	}
}

func middlewareReconcileAdoptionCondition(conditions []metav1.Condition, conditionType string) *metav1.Condition {
	for i := range conditions {
		if conditions[i].Type == conditionType {
			return &conditions[i]
		}
	}
	return nil
}
