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

package middleware

import (
	"context"
	"testing"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	"github.com/harmonycloud/opensaola/internal/k8s"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// fullMiddleware returns a Middleware using the operator delete path.
func fullMiddleware(name, namespace string) *v1.Middleware {
	return &v1.Middleware{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				v1.LabelPackageName: "pkg-redis",
			},
		},
		Spec: v1.MiddlewareSpec{
			Baseline: "baseline-redis",
			OperatorBaseline: v1.OperatorBaseline{
				Name:    "ob-redis",
				GvkName: "Redis",
			},
		},
	}
}

// noOperatorMiddleware returns a Middleware using the no-operator delete path.
func noOperatorMiddleware(name, namespace string) *v1.Middleware {
	return &v1.Middleware{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				v1.LabelPackageName: "pkg-redis",
			},
		},
		Spec: v1.MiddlewareSpec{
			Baseline: "baseline-redis",
		},
	}
}

// partialOperatorMiddleware returns a Middleware that cannot resolve delete semantics.
func partialOperatorMiddleware(name, namespace string) *v1.Middleware {
	return &v1.Middleware{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				v1.LabelPackageName: "pkg-redis",
			},
		},
		Spec: v1.MiddlewareSpec{
			Baseline: "baseline-redis",
			OperatorBaseline: v1.OperatorBaseline{
				Name: "ob-redis",
			},
		},
	}
}

// emptyMiddleware returns a Middleware with no hard-requirement fields set.
func emptyMiddleware(name, namespace string) *v1.Middleware {
	return &v1.Middleware{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
	}
}

func middlewareCacheKey(name, namespace string) string {
	return types.NamespacedName{Name: name, Namespace: namespace}.String()
}

// TestResolveDeleteContext_AllFieldsPresent: fast path, no fallback needed.
func TestResolveDeleteContext_AllFieldsPresent(t *testing.T) {
	m := fullMiddleware("mid-1", "default")
	resolved, usedFallback, _, err := ResolveDeleteContext(context.Background(), nil, m)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if usedFallback {
		t.Error("expected no legacy fallback when all fields are present")
	}
	if resolved == m {
		t.Error("resolved must be a DeepCopy, not the same pointer")
	}
	if resolved.Labels[v1.LabelPackageName] != "pkg-redis" {
		t.Errorf("unexpected packageName: %s", resolved.Labels[v1.LabelPackageName])
	}
}

func TestResolveDeleteContext_NoOperatorFastPath(t *testing.T) {
	m := noOperatorMiddleware("mid-noop-1", "default")
	resolved, usedFallback, reason, err := ResolveDeleteContext(context.Background(), nil, m)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if usedFallback {
		t.Error("expected no legacy fallback when no-operator delete semantics are already resolvable")
	}
	if reason != "all hard requirements already present" {
		t.Errorf("unexpected reason: %s", reason)
	}
	if resolved.Spec.OperatorBaseline.Name != "" || resolved.Spec.OperatorBaseline.GvkName != "" {
		t.Errorf("expected empty operatorBaseline for no-operator path, got: %+v", resolved.Spec.OperatorBaseline)
	}
}

// TestResolveDeleteContext_MissingFields_CacheHit: slow path, cache provides missing fields.
func TestResolveDeleteContext_MissingFields_CacheHit(t *testing.T) {
	name, ns := "mid-2", "default"
	k8s.MiddlewareCache.Set(middlewareCacheKey(name, ns), *fullMiddleware(name, ns))
	defer k8s.MiddlewareCache.Delete(middlewareCacheKey(name, ns))

	m := emptyMiddleware(name, ns)
	resolved, usedFallback, reason, err := ResolveDeleteContext(context.Background(), nil, m)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !usedFallback {
		t.Error("expected legacy fallback to be used")
	}
	if reason == "" {
		t.Error("expected non-empty reason")
	}
	if resolved.Labels[v1.LabelPackageName] != "pkg-redis" {
		t.Errorf("packageName not filled from cache: %s", resolved.Labels[v1.LabelPackageName])
	}
	if resolved.Spec.Baseline != "baseline-redis" {
		t.Errorf("baseline not filled from cache: %s", resolved.Spec.Baseline)
	}
	if resolved.Spec.OperatorBaseline.Name != "ob-redis" {
		t.Errorf("operatorBaseline.Name not filled from cache: %s", resolved.Spec.OperatorBaseline.Name)
	}
	if resolved.Spec.OperatorBaseline.GvkName != "Redis" {
		t.Errorf("operatorBaseline.GvkName not filled from cache: %s", resolved.Spec.OperatorBaseline.GvkName)
	}
	// live object must not be mutated
	if m.Labels[v1.LabelPackageName] != "" {
		t.Error("live object must not be mutated by resolver")
	}
}

func TestResolveDeleteContext_NoOperatorCacheHit(t *testing.T) {
	name, ns := "mid-noop-2", "default"
	k8s.MiddlewareCache.Set(middlewareCacheKey(name, ns), *noOperatorMiddleware(name, ns))
	defer k8s.MiddlewareCache.Delete(middlewareCacheKey(name, ns))

	m := emptyMiddleware(name, ns)
	resolved, usedFallback, reason, err := ResolveDeleteContext(context.Background(), nil, m)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !usedFallback {
		t.Error("expected legacy fallback to be used")
	}
	if reason == "" {
		t.Error("expected non-empty reason")
	}
	if resolved.Labels[v1.LabelPackageName] != "pkg-redis" {
		t.Errorf("packageName not filled from cache: %s", resolved.Labels[v1.LabelPackageName])
	}
	if resolved.Spec.Baseline != "baseline-redis" {
		t.Errorf("baseline not filled from cache: %s", resolved.Spec.Baseline)
	}
	if resolved.Spec.OperatorBaseline.Name != "" || resolved.Spec.OperatorBaseline.GvkName != "" {
		t.Errorf("expected no-operator cache fallback to keep operatorBaseline empty, got: %+v", resolved.Spec.OperatorBaseline)
	}
}

// TestResolveDeleteContext_MissingFields_CacheMiss: slow path, cache has no entry.
func TestResolveDeleteContext_MissingFields_CacheMiss(t *testing.T) {
	name, ns := "mid-3", "default"
	k8s.MiddlewareCache.Delete(middlewareCacheKey(name, ns))

	m := emptyMiddleware(name, ns)
	_, usedFallback, _, err := ResolveDeleteContext(context.Background(), nil, m)
	if err == nil {
		t.Fatal("expected error on cache miss")
	}
	if !usedFallback {
		t.Error("expected usedFallback=true even on cache miss (fallback was attempted)")
	}
}

// TestResolveDeleteContext_MissingFields_CacheHitButStillIncomplete: cache entry still cannot resolve delete semantics.
func TestResolveDeleteContext_MissingFields_CacheHitButStillIncomplete(t *testing.T) {
	name, ns := "mid-4", "default"
	k8s.MiddlewareCache.Set(middlewareCacheKey(name, ns), *partialOperatorMiddleware(name, ns))
	defer k8s.MiddlewareCache.Delete(middlewareCacheKey(name, ns))

	m := emptyMiddleware(name, ns)
	_, usedFallback, _, err := ResolveDeleteContext(context.Background(), nil, m)
	if err == nil {
		t.Fatal("expected error when hard requirements still unmet after fallback")
	}
	if !usedFallback {
		t.Error("expected usedFallback=true")
	}
}

func TestResolveDeleteContext_PartialOperatorLiveObjectStillFails(t *testing.T) {
	m := partialOperatorMiddleware("mid-partial-1", "default")
	_, usedFallback, _, err := ResolveDeleteContext(context.Background(), nil, m)
	if err == nil {
		t.Fatal("expected error when operator delete semantics remain incomplete")
	}
	if !usedFallback {
		t.Error("expected usedFallback=true when attempting to repair incomplete operator semantics")
	}
}

func TestCanSkipDeleteCleanup_NoCreatedResources(t *testing.T) {
	m := emptyMiddleware("mid-skip-1", "default")
	if !CanSkipDeleteCleanup(m) {
		t.Fatal("expected cleanup to be skippable when no resource conditions are true")
	}

	m.Status.Conditions = []metav1.Condition{{
		Type:   v1.CondTypeTemplateParseWithBaseline,
		Status: metav1.ConditionFalse,
	}}
	if !CanSkipDeleteCleanup(m) {
		t.Fatal("expected cleanup to remain skippable after template parsing failed")
	}
}

func TestCanSkipDeleteCleanup_CreatedResources(t *testing.T) {
	tests := []struct {
		name          string
		conditionType string
	}{
		{name: "extra resources created", conditionType: v1.CondTypeBuildExtraResource},
		{name: "custom resource applied", conditionType: v1.CondTypeApplyCluster},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := emptyMiddleware("mid-skip-2", "default")
			m.Status.Conditions = []metav1.Condition{{
				Type:   tt.conditionType,
				Status: metav1.ConditionTrue,
			}}
			if CanSkipDeleteCleanup(m) {
				t.Fatalf("expected cleanup to be required when %s is true", tt.conditionType)
			}
		})
	}
}

// TestShouldUseLegacyDeleteFallback_Complete: no fallback needed.
func TestShouldUseLegacyDeleteFallback_Complete(t *testing.T) {
	need, reason := ShouldUseLegacyDeleteFallback(fullMiddleware("mid-5", "default"))
	if need {
		t.Errorf("expected no fallback needed, got reason: %s", reason)
	}
	if reason != "" {
		t.Errorf("expected empty reason, got: %s", reason)
	}
}

func TestShouldUseLegacyDeleteFallback_NoOperatorComplete(t *testing.T) {
	need, reason := ShouldUseLegacyDeleteFallback(noOperatorMiddleware("mid-7", "default"))
	if need {
		t.Errorf("expected no fallback needed for no-operator path, got reason: %s", reason)
	}
	if reason != "" {
		t.Errorf("expected empty reason, got: %s", reason)
	}
}

// TestShouldUseLegacyDeleteFallback_Incomplete: fallback needed, reason lists missing fields.
func TestShouldUseLegacyDeleteFallback_Incomplete(t *testing.T) {
	need, reason := ShouldUseLegacyDeleteFallback(emptyMiddleware("mid-6", "default"))
	if !need {
		t.Error("expected fallback needed")
	}
	if reason == "" {
		t.Error("expected non-empty reason")
	}
}

func TestShouldUseLegacyDeleteFallback_PartialOperatorIncomplete(t *testing.T) {
	need, reason := ShouldUseLegacyDeleteFallback(partialOperatorMiddleware("mid-8", "default"))
	if !need {
		t.Error("expected fallback needed for partial operator path")
	}
	if reason == "" {
		t.Error("expected non-empty reason")
	}
}
