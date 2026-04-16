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

package middlewareactionbaseline

import (
	"context"
	"testing"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// newScheme creates a runtime.Scheme with the opensaola API types registered.
func newScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = v1.AddToScheme(s)
	return s
}

// newActionBaseline creates a MiddlewareActionBaseline with the given name, labels, and steps.
func newActionBaseline(name string, labels map[string]string, steps []v1.Step) *v1.MiddlewareActionBaseline {
	return &v1.MiddlewareActionBaseline{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: labels,
		},
		Spec: v1.MiddlewareActionBaselineSpec{
			Steps: steps,
		},
	}
}

// findCondition is a test helper to locate a condition by type.
func findCondition(conditions []metav1.Condition, condType string) *metav1.Condition {
	for i := range conditions {
		if conditions[i].Type == condType {
			return &conditions[i]
		}
	}
	return nil
}

// TestActionBaselineCheck_ValidSteps verifies that Check sets the condition to True
// and state to Available when the baseline has valid steps.
func TestActionBaselineCheck_ValidSteps(t *testing.T) {
	ctx := context.Background()
	m := newActionBaseline("test-action", nil, []v1.Step{
		{Name: "step-1"},
		{Name: "step-2"},
	})
	s := newScheme()
	cli := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(m).
		WithStatusSubresource(m).
		Build()

	err := Check(ctx, cli, m)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cond := findCondition(m.Status.Conditions, v1.CondTypeChecked)
	if cond == nil {
		t.Fatal("expected Checked condition to be set")
	}
	if cond.Status != metav1.ConditionTrue {
		t.Errorf("expected ConditionTrue for valid steps, got %v", cond.Status)
	}
	if m.Status.State != v1.StateAvailable {
		t.Errorf("expected state Available, got %v", m.Status.State)
	}
}

// TestActionBaselineCheck_EmptySteps verifies that Check sets the condition to False
// when the baseline has no steps.
func TestActionBaselineCheck_EmptySteps(t *testing.T) {
	ctx := context.Background()
	m := newActionBaseline("test-action-empty", nil, nil)
	s := newScheme()
	cli := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(m).
		WithStatusSubresource(m).
		Build()

	err := Check(ctx, cli, m)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cond := findCondition(m.Status.Conditions, v1.CondTypeChecked)
	if cond == nil {
		t.Fatal("expected Checked condition to be set")
	}
	if cond.Status != metav1.ConditionFalse {
		t.Errorf("expected ConditionFalse for empty steps, got %v", cond.Status)
	}
}

// TestActionBaselineCheck_StepWithEmptyName verifies that Check sets the condition
// to False when a step has an empty name.
func TestActionBaselineCheck_StepWithEmptyName(t *testing.T) {
	ctx := context.Background()
	m := newActionBaseline("test-action-badstep", nil, []v1.Step{
		{Name: ""},
	})
	s := newScheme()
	cli := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(m).
		WithStatusSubresource(m).
		Build()

	err := Check(ctx, cli, m)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cond := findCondition(m.Status.Conditions, v1.CondTypeChecked)
	if cond == nil {
		t.Fatal("expected Checked condition to be set")
	}
	if cond.Status != metav1.ConditionFalse {
		t.Errorf("expected ConditionFalse for step with empty name, got %v", cond.Status)
	}
	if m.Status.State != v1.StateUnavailable {
		t.Errorf("expected state Unavailable, got %v", m.Status.State)
	}
}

// TestActionBaselineCheck_AlreadyChecked_Skips verifies that Check returns early
// when the condition is already True with a matching generation.
func TestActionBaselineCheck_AlreadyChecked_Skips(t *testing.T) {
	ctx := context.Background()
	m := newActionBaseline("test-action-skip", nil, []v1.Step{
		{Name: "step-1"},
	})
	m.Status.Conditions = []metav1.Condition{
		{
			Type:               v1.CondTypeChecked,
			Status:             metav1.ConditionTrue,
			ObservedGeneration: 0,
			Reason:             "CheckedSuccess",
			LastTransitionTime: metav1.Now(),
		},
	}
	s := newScheme()
	cli := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(m).
		WithStatusSubresource(m).
		Build()

	err := Check(ctx, cli, m)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cond := findCondition(m.Status.Conditions, v1.CondTypeChecked)
	if cond == nil {
		t.Fatal("expected Checked condition to still exist")
	}
	if cond.Status != metav1.ConditionTrue {
		t.Errorf("expected condition to remain ConditionTrue, got %v", cond.Status)
	}
}

// TestActionBaselineGet_FromCluster verifies that Get retrieves an action baseline
// from the cluster when it exists and has matching package labels.
func TestActionBaselineGet_FromCluster(t *testing.T) {
	ctx := context.Background()
	pkgName := "test-pkg"
	baselineName := "test-action-baseline"

	m := newActionBaseline(baselineName, map[string]string{
		v1.LabelPackageName: pkgName,
	}, []v1.Step{{Name: "step-1"}})

	s := newScheme()
	cli := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(m).
		Build()

	// Clear cache to ensure cluster fetch
	ActionBaselineCache.Delete(pkgName + "/" + baselineName)

	result, err := Get(ctx, cli, baselineName, pkgName)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Name != baselineName {
		t.Errorf("expected baseline name %q, got %q", baselineName, result.Name)
	}
	if result.Labels[v1.LabelPackageName] != pkgName {
		t.Errorf("expected label %q=%q, got %q", v1.LabelPackageName, pkgName, result.Labels[v1.LabelPackageName])
	}
}

// TestActionBaselineGet_FromCache verifies that Get returns a baseline from the
// cache when it is pre-populated and not in the cluster.
func TestActionBaselineGet_FromCache(t *testing.T) {
	ctx := context.Background()
	pkgName := "cache-pkg"
	baselineName := "cache-action-baseline"
	key := pkgName + "/" + baselineName

	// Pre-populate cache
	cached := v1.MiddlewareActionBaseline{
		ObjectMeta: metav1.ObjectMeta{
			Name: baselineName,
			Labels: map[string]string{
				v1.LabelPackageName: pkgName,
			},
		},
		Spec: v1.MiddlewareActionBaselineSpec{
			Steps: []v1.Step{{Name: "cached-step"}},
		},
	}
	ActionBaselineCache.Set(key, cached)

	// Empty cluster -- baseline not found via k8s.Get
	s := newScheme()
	cli := fake.NewClientBuilder().
		WithScheme(s).
		Build()

	result, err := Get(ctx, cli, baselineName, pkgName)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Name != baselineName {
		t.Errorf("expected cached baseline name %q, got %q", baselineName, result.Name)
	}
	if len(result.Spec.Steps) != 1 || result.Spec.Steps[0].Name != "cached-step" {
		t.Errorf("expected cached step 'cached-step', got %v", result.Spec.Steps)
	}

	// Clean up
	ActionBaselineCache.Delete(key)
}

// TestActionBaselineGet_NotFound verifies that Get returns an error when the
// baseline does not exist in the cluster or cache.
func TestActionBaselineGet_NotFound(t *testing.T) {
	ctx := context.Background()
	s := newScheme()
	cli := fake.NewClientBuilder().
		WithScheme(s).
		Build()

	name := "nonexistent-action"
	pkgName := "nonexistent-pkg"
	ActionBaselineCache.Delete(pkgName + "/" + name)

	_, err := Get(ctx, cli, name, pkgName)
	if err == nil {
		t.Fatal("expected error for non-existent action baseline, got nil")
	}
}
