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

package middlewarebaseline

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

// newBaseline creates a MiddlewareBaseline with the given name and labels.
func newBaseline(name string, labels map[string]string) *v1.MiddlewareBaseline {
	return &v1.MiddlewareBaseline{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: labels,
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

// TestCheck_SetsAvailable verifies that Check sets the Checked condition to True
// and the state to Available for a valid MiddlewareBaseline.
func TestCheck_SetsAvailable(t *testing.T) {
	ctx := context.Background()
	m := newBaseline("test-baseline", nil)
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
		t.Errorf("expected ConditionTrue, got %v", cond.Status)
	}
	if m.Status.State != v1.StateAvailable {
		t.Errorf("expected state Available, got %v", m.Status.State)
	}
}

// TestCheck_AlreadyChecked_Skips verifies that Check returns early without
// modifying the condition when it is already set to True with a matching generation.
func TestCheck_AlreadyChecked_Skips(t *testing.T) {
	ctx := context.Background()
	m := newBaseline("test-baseline", nil)
	// Pre-set the condition as already checked with matching generation
	m.Status.Conditions = []metav1.Condition{
		{
			Type:               v1.CondTypeChecked,
			Status:             metav1.ConditionTrue,
			ObservedGeneration: 0, // matches default Generation=0
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

	// Should not have re-checked -- condition stays as is
	cond := findCondition(m.Status.Conditions, v1.CondTypeChecked)
	if cond == nil {
		t.Fatal("expected Checked condition to still exist")
	}
	if cond.Status != metav1.ConditionTrue {
		t.Errorf("expected condition to remain ConditionTrue, got %v", cond.Status)
	}
}

// TestGet_FromCluster verifies that Get retrieves a MiddlewareBaseline from the
// cluster when it exists and has matching package labels.
func TestGet_FromCluster(t *testing.T) {
	ctx := context.Background()
	pkgName := "test-pkg"
	baselineName := "test-baseline"

	m := newBaseline(baselineName, map[string]string{
		v1.LabelPackageName: pkgName,
	})
	s := newScheme()
	cli := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(m).
		Build()

	// Clear cache to ensure cluster fetch
	BaselineCache.Delete(pkgName + "/" + baselineName)

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

// TestGet_NotFound verifies that Get returns an error when the baseline
// does not exist in the cluster or cache.
func TestGet_NotFound(t *testing.T) {
	ctx := context.Background()
	s := newScheme()
	cli := fake.NewClientBuilder().
		WithScheme(s).
		Build()

	name := "nonexistent-baseline"
	pkgName := "nonexistent-pkg"
	// Clear cache
	BaselineCache.Delete(pkgName + "/" + name)

	_, err := Get(ctx, cli, name, pkgName)
	if err == nil {
		t.Fatal("expected error for non-existent baseline, got nil")
	}
}

// TestGet_CachesResult verifies that after a successful cluster Get,
// the baseline is stored in the cache.
func TestGet_CachesResult(t *testing.T) {
	ctx := context.Background()
	pkgName := "cache-test-pkg"
	baselineName := "cache-test-baseline"

	m := newBaseline(baselineName, map[string]string{
		v1.LabelPackageName: pkgName,
	})
	s := newScheme()
	cli := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(m).
		Build()

	// Clear cache
	key := pkgName + "/" + baselineName
	BaselineCache.Delete(key)

	_, err := Get(ctx, cli, baselineName, pkgName)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify it was cached
	cached, ok := BaselineCache.Get(key)
	if !ok {
		t.Fatal("expected baseline to be cached after Get")
	}
	if cached.Name != baselineName {
		t.Errorf("expected cached baseline name %q, got %q", baselineName, cached.Name)
	}
}
