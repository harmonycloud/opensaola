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

package k8s

import (
	"context"
	"testing"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestUpdateMiddlewareStatusPreservesCustomResources(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := v1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}

	live := &v1.Middleware{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "middleware"},
		Status: v1.MiddlewareStatus{
			CustomResources: v1.CustomResources{
				Phase:    v1.PhaseRunning,
				Replicas: 3,
				Reason:   "all replicas are ready",
			},
			State:  v1.StateUnavailable,
			Reason: "old controller state",
			RenderedConfigurationResources: []v1.RenderedConfigurationResource{{
				ConfigurationName: "old-config",
				Version:           "v1",
				Kind:              "ConfigMap",
				Name:              "old-config",
			}},
			RenderedConfigurationResourcesGeneration: 2,
		},
	}
	desired := live.DeepCopy()
	desired.Status.State = v1.StateAvailable
	desired.Status.Reason = "controller reconciliation completed"
	desired.Status.CustomResources = v1.CustomResources{Phase: v1.PhaseCreating}
	desired.Status.RenderedConfigurationResources = []v1.RenderedConfigurationResource{{
		ConfigurationName: "new-config",
		Version:           "v1",
		Kind:              "ConfigMap",
		Name:              "new-config",
	}}
	desired.Status.RenderedConfigurationResourcesGeneration = 3

	cli := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&v1.Middleware{}).
		WithObjects(live).
		Build()

	if err := UpdateMiddlewareStatus(context.Background(), cli, desired); err != nil {
		t.Fatalf("UpdateMiddlewareStatus() error = %v", err)
	}

	var stored v1.Middleware
	if err := cli.Get(context.Background(), client.ObjectKey{Name: live.Name, Namespace: live.Namespace}, &stored); err != nil {
		t.Fatalf("get stored Middleware: %v", err)
	}
	if got := stored.Status.CustomResources.Phase; got != v1.PhaseRunning {
		t.Fatalf("customResources.phase = %q, want %q", got, v1.PhaseRunning)
	}
	if got := stored.Status.CustomResources.Replicas; got != 3 {
		t.Fatalf("customResources.replicas = %d, want 3", got)
	}
	if got := stored.Status.State; got != v1.StateAvailable {
		t.Fatalf("state = %q, want %q", got, v1.StateAvailable)
	}
	if got := stored.Status.Reason; got != "controller reconciliation completed" {
		t.Fatalf("reason = %q, want controller reconciliation completed", got)
	}
	if got := stored.Status.RenderedConfigurationResourcesGeneration; got != 3 {
		t.Fatalf("renderedConfigurationResourcesGeneration = %d, want 3", got)
	}
	if got := stored.Status.RenderedConfigurationResources; len(got) != 1 || got[0].ConfigurationName != "new-config" {
		t.Fatalf("renderedConfigurationResources = %#v, want new-config inventory", got)
	}
}

func TestUpdateMiddlewareStatusPreservesNewerRenderedConfigurationInventory(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := v1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}
	live := &v1.Middleware{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "middleware"},
		Status: v1.MiddlewareStatus{
			RenderedConfigurationResources: []v1.RenderedConfigurationResource{{
				ConfigurationName: "newer-config",
				Version:           "v1",
				Kind:              "ConfigMap",
				Name:              "newer-config",
			}},
			RenderedConfigurationResourcesGeneration: 8,
		},
	}
	desired := live.DeepCopy()
	desired.Status.RenderedConfigurationResources = nil
	desired.Status.RenderedConfigurationResourcesGeneration = 7

	cli := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&v1.Middleware{}).
		WithObjects(live).
		Build()
	if err := UpdateMiddlewareStatus(context.Background(), cli, desired); err != nil {
		t.Fatalf("UpdateMiddlewareStatus() error = %v", err)
	}

	var stored v1.Middleware
	if err := cli.Get(context.Background(), client.ObjectKey{Name: live.Name, Namespace: live.Namespace}, &stored); err != nil {
		t.Fatalf("get stored Middleware: %v", err)
	}
	if got := stored.Status.RenderedConfigurationResourcesGeneration; got != 8 {
		t.Fatalf("renderedConfigurationResourcesGeneration = %d, want 8", got)
	}
	if got := stored.Status.RenderedConfigurationResources; len(got) != 1 || got[0].ConfigurationName != "newer-config" {
		t.Fatalf("renderedConfigurationResources = %#v, want newer live inventory", got)
	}
}

func TestUpdateMiddlewareStatusPreservesPauseSnapshotAndDedicatedPatchCanClearIt(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := v1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}
	live := &v1.Middleware{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "middleware"},
		Status: v1.MiddlewareStatus{ReconcilePause: &v1.ReconcilePauseStatus{
			DesiredSpec: runtime.RawExtension{Raw: []byte(`{"replicas":3}`)},
			Hash:        "pause-snapshot",
		}},
	}
	stale := live.DeepCopy()
	stale.Status.ReconcilePause = nil
	stale.Status.State = v1.StateAvailable
	cli := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&v1.Middleware{}).
		WithObjects(live).
		Build()

	if err := UpdateMiddlewareStatus(context.Background(), cli, stale); err != nil {
		t.Fatalf("UpdateMiddlewareStatus() error = %v", err)
	}
	var stored v1.Middleware
	if err := cli.Get(context.Background(), client.ObjectKey{Name: live.Name, Namespace: live.Namespace}, &stored); err != nil {
		t.Fatalf("get stored Middleware: %v", err)
	}
	if stored.Status.ReconcilePause == nil || stored.Status.ReconcilePause.Hash != "pause-snapshot" {
		t.Fatalf("pause snapshot was lost: %#v", stored.Status.ReconcilePause)
	}

	if err := PatchMiddlewareStatusFields(context.Background(), cli, live.Name, live.Namespace, func(status *v1.MiddlewareStatus) {
		status.ReconcilePause = nil
	}); err != nil {
		t.Fatalf("PatchMiddlewareStatusFields() clear error = %v", err)
	}
	if err := UpdateMiddlewareStatus(context.Background(), cli, stale); err != nil {
		t.Fatalf("stale UpdateMiddlewareStatus() error = %v", err)
	}
	if err := cli.Get(context.Background(), client.ObjectKey{Name: live.Name, Namespace: live.Namespace}, &stored); err != nil {
		t.Fatalf("get cleared Middleware: %v", err)
	}
	if stored.Status.ReconcilePause != nil {
		t.Fatalf("stale status write restored cleared pause snapshot: %#v", stored.Status.ReconcilePause)
	}
}

func TestCompleteAndFinalizeMiddlewareReconcileResume(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := v1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}
	live := &v1.Middleware{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "demo",
			Namespace: "middleware",
			Annotations: map[string]string{
				v1.AnnotationSuspendReconcile: "true",
				v1.AnnotationResumePolicy:     string(v1.ReconcileResumePolicyMerge),
				"example.com/keep":            "value",
			},
		},
		Status: v1.MiddlewareStatus{ReconcilePause: &v1.ReconcilePauseStatus{Hash: "pause-snapshot"}},
	}
	cli := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&v1.Middleware{}).WithObjects(live).Build()
	overrides := &v1.ReconcileOverrides{SpecPatch: runtime.RawExtension{Raw: []byte(`{"replicas":5}`)}}
	if err := CompleteMiddlewareReconcileResume(context.Background(), cli, live.Name, live.Namespace, live.Generation, "pause-snapshot", v1.ReconcileResumePolicyMerge, overrides); err != nil {
		t.Fatalf("CompleteMiddlewareReconcileResume() error = %v", err)
	}
	var stored v1.Middleware
	if err := cli.Get(context.Background(), client.ObjectKey{Name: live.Name, Namespace: live.Namespace}, &stored); err != nil {
		t.Fatalf("get stored Middleware: %v", err)
	}
	if _, exists := stored.Annotations[v1.AnnotationSuspendReconcile]; exists {
		t.Fatalf("suspend annotation was not removed: %#v", stored.Annotations)
	}
	if got := stored.Annotations[v1.AnnotationResumePolicy]; got != string(v1.ReconcileResumePolicyMerge) {
		t.Fatalf("resume policy = %q, want merge", got)
	}
	if got := stored.Annotations["example.com/keep"]; got != "value" {
		t.Fatalf("unrelated annotation changed: %#v", stored.Annotations)
	}
	if stored.Spec.ReconcileOverrides == nil || string(stored.Spec.ReconcileOverrides.SpecPatch.Raw) != `{"replicas":5}` {
		t.Fatalf("reconcile overrides = %#v", stored.Spec.ReconcileOverrides)
	}

	if err := FinalizeMiddlewareReconcileResume(context.Background(), cli, live.Name, live.Namespace, "pause-snapshot", v1.ReconcileResumePolicyMerge); err != nil {
		t.Fatalf("FinalizeMiddlewareReconcileResume() error = %v", err)
	}
	if err := cli.Get(context.Background(), client.ObjectKey{Name: live.Name, Namespace: live.Namespace}, &stored); err != nil {
		t.Fatalf("get finalized Middleware: %v", err)
	}
	if _, exists := stored.Annotations[v1.AnnotationResumePolicy]; exists {
		t.Fatalf("resume policy was not removed: %#v", stored.Annotations)
	}
}

func TestCompleteMiddlewareReconcileResumeRejectsPolicyChange(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := v1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}
	live := &v1.Middleware{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "demo-policy-change",
			Namespace: "middleware",
			Annotations: map[string]string{
				v1.AnnotationSuspendReconcile: "true",
				v1.AnnotationResumePolicy:     string(v1.ReconcileResumePolicyApply),
			},
		},
		Status: v1.MiddlewareStatus{ReconcilePause: &v1.ReconcilePauseStatus{Hash: "pause-snapshot"}},
	}
	cli := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&v1.Middleware{}).WithObjects(live).Build()
	err := CompleteMiddlewareReconcileResume(
		context.Background(),
		cli,
		live.Name,
		live.Namespace,
		live.Generation,
		"pause-snapshot",
		v1.ReconcileResumePolicyMerge,
		&v1.ReconcileOverrides{SpecPatch: runtime.RawExtension{Raw: []byte(`{"replicas":5}`)}},
	)
	if err == nil {
		t.Fatal("CompleteMiddlewareReconcileResume() succeeded after policy changed")
	}
	var stored v1.Middleware
	if getErr := cli.Get(context.Background(), client.ObjectKey{Name: live.Name, Namespace: live.Namespace}, &stored); getErr != nil {
		t.Fatalf("get stored Middleware: %v", getErr)
	}
	if _, exists := stored.Annotations[v1.AnnotationSuspendReconcile]; !exists {
		t.Fatalf("suspend annotation was consumed despite policy mismatch: %#v", stored.Annotations)
	}
	if stored.Spec.ReconcileOverrides != nil {
		t.Fatalf("override was persisted despite policy mismatch: %#v", stored.Spec.ReconcileOverrides)
	}
}
