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

package middlewareoperatorbaseline

import (
	"context"
	"strings"
	"testing"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func newScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = v1.AddToScheme(s)
	return s
}

func newBaseline(name string, gvks []v1.GVK) *v1.MiddlewareOperatorBaseline {
	return &v1.MiddlewareOperatorBaseline{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: v1.MiddlewareOperatorBaselineSpec{
			GVKs: gvks,
		},
	}
}

func TestCheck_EmptyGVKs(t *testing.T) {
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

	// With empty GVKs, the condition should be failed
	cond := findCondition(m.Status.Conditions, v1.CondTypeChecked)
	if cond == nil {
		t.Fatal("expected Checked condition to be set")
	}
	if cond.Status != metav1.ConditionFalse {
		t.Errorf("expected ConditionFalse for empty GVKs, got %v", cond.Status)
	}
	if !strings.Contains(cond.Message, "gvks must not be empty") {
		t.Errorf("expected message containing 'gvks must not be empty', got %q", cond.Message)
	}
}

func TestCheck_GVKWithEmptyName(t *testing.T) {
	ctx := context.Background()
	m := newBaseline("test-baseline", []v1.GVK{
		{Name: "", Group: "apps", Version: "v1", Kind: "Deployment"},
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
		t.Errorf("expected ConditionFalse for empty name, got %v", cond.Status)
	}
	if !strings.Contains(cond.Message, "name must not be empty") {
		t.Errorf("expected message containing 'name must not be empty', got %q", cond.Message)
	}
	if m.Status.State != v1.StateUnavailable {
		t.Errorf("expected state Unavailable, got %v", m.Status.State)
	}
}

func TestCheck_GVKWithEmptyGroupVersionKind(t *testing.T) {
	ctx := context.Background()
	m := newBaseline("test-baseline", []v1.GVK{
		{Name: "my-resource", Group: "", Version: "v1", Kind: "Deployment"},
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
		t.Errorf("expected ConditionFalse for empty GVK fields, got %v", cond.Status)
	}
	if !strings.Contains(cond.Message, "GVK must not be empty") {
		t.Errorf("expected message containing 'GVK must not be empty', got %q", cond.Message)
	}
}

func TestCheck_Valid(t *testing.T) {
	ctx := context.Background()
	m := newBaseline("test-baseline", []v1.GVK{
		{Name: "my-resource", Group: "apps", Version: "v1", Kind: "Deployment"},
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
		t.Errorf("expected ConditionTrue for valid baseline, got %v", cond.Status)
	}
	if m.Status.State != v1.StateAvailable {
		t.Errorf("expected state Available, got %v", m.Status.State)
	}
}

func TestCheck_MultipleGVKErrors(t *testing.T) {
	ctx := context.Background()
	m := newBaseline("test-baseline", []v1.GVK{
		{Name: "", Group: "apps", Version: "v1", Kind: "Deployment"},
		{Name: "valid", Group: "", Version: "v1", Kind: "Service"},
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
		t.Errorf("expected ConditionFalse for multiple errors, got %v", cond.Status)
	}
	// Both errors should be joined with ";"
	if !strings.Contains(cond.Message, "name must not be empty") {
		t.Errorf("expected message containing 'name must not be empty', got %q", cond.Message)
	}
	if !strings.Contains(cond.Message, "GVK must not be empty") {
		t.Errorf("expected message containing 'GVK must not be empty', got %q", cond.Message)
	}
}

func TestCheck_AlreadyChecked_Skips(t *testing.T) {
	ctx := context.Background()
	m := newBaseline("test-baseline", []v1.GVK{
		{Name: "my-resource", Group: "apps", Version: "v1", Kind: "Deployment"},
	})
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

	// Should not have re-checked — condition stays as is
	cond := findCondition(m.Status.Conditions, v1.CondTypeChecked)
	if cond == nil {
		t.Fatal("expected Checked condition to still exist")
	}
	if cond.Status != metav1.ConditionTrue {
		t.Errorf("expected condition to remain ConditionTrue, got %v", cond.Status)
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
