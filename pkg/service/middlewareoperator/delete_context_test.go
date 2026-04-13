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

package middlewareoperator

import (
	"context"
	"testing"

	v1 "github.com/OpenSaola/opensaola/api/v1"
	"github.com/OpenSaola/opensaola/pkg/k8s"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func fullMiddlewareOperator(name, namespace string) *v1.MiddlewareOperator {
	return &v1.MiddlewareOperator{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				v1.LabelPackageName: "pkg-mysql",
			},
		},
		Spec: v1.MiddlewareOperatorSpec{
			Baseline: "baseline-mysql",
		},
	}
}

func emptyMiddlewareOperator(name, namespace string) *v1.MiddlewareOperator {
	return &v1.MiddlewareOperator{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
	}
}

func middlewareOperatorCacheKey(name, namespace string) string {
	return types.NamespacedName{Name: name, Namespace: namespace}.String()
}

func TestResolveDeleteContext_AllFieldsPresent(t *testing.T) {
	m := fullMiddlewareOperator("mo-1", "default")
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
	if resolved.Labels[v1.LabelPackageName] != "pkg-mysql" {
		t.Errorf("unexpected packageName: %s", resolved.Labels[v1.LabelPackageName])
	}
}

func TestResolveDeleteContext_MissingFields_CacheHit(t *testing.T) {
	name, ns := "mo-2", "default"
	k8s.MiddlewareOperatorCache.Store(middlewareOperatorCacheKey(name, ns), fullMiddlewareOperator(name, ns))
	defer k8s.MiddlewareOperatorCache.Delete(middlewareOperatorCacheKey(name, ns))

	m := emptyMiddlewareOperator(name, ns)
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
	if resolved.Labels[v1.LabelPackageName] != "pkg-mysql" {
		t.Errorf("packageName not filled from cache: %s", resolved.Labels[v1.LabelPackageName])
	}
	if resolved.Spec.Baseline != "baseline-mysql" {
		t.Errorf("baseline not filled from cache: %s", resolved.Spec.Baseline)
	}
	if m.Labels[v1.LabelPackageName] != "" {
		t.Error("live object must not be mutated by resolver")
	}
}

func TestResolveDeleteContext_MissingFields_CacheMiss(t *testing.T) {
	name, ns := "mo-3", "default"
	k8s.MiddlewareOperatorCache.Delete(middlewareOperatorCacheKey(name, ns))

	m := emptyMiddlewareOperator(name, ns)
	_, usedFallback, _, err := ResolveDeleteContext(context.Background(), nil, m)
	if err == nil {
		t.Fatal("expected error on cache miss")
	}
	if !usedFallback {
		t.Error("expected usedFallback=true even on cache miss")
	}
}

func TestResolveDeleteContext_MissingFields_CacheHitButStillIncomplete(t *testing.T) {
	name, ns := "mo-4", "default"
	incomplete := &v1.MiddlewareOperator{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			Labels: map[string]string{
				v1.LabelPackageName: "pkg-mysql",
			},
		},
		Spec: v1.MiddlewareOperatorSpec{},
	}
	k8s.MiddlewareOperatorCache.Store(middlewareOperatorCacheKey(name, ns), incomplete)
	defer k8s.MiddlewareOperatorCache.Delete(middlewareOperatorCacheKey(name, ns))

	m := emptyMiddlewareOperator(name, ns)
	_, usedFallback, _, err := ResolveDeleteContext(context.Background(), nil, m)
	if err == nil {
		t.Fatal("expected error when hard requirements still unmet after fallback")
	}
	if !usedFallback {
		t.Error("expected usedFallback=true")
	}
}

func TestResolveDeleteContext_NoOperatorFastPath(t *testing.T) {
	m := &v1.MiddlewareOperator{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "mo-5",
			Namespace: "default",
			Annotations: map[string]string{
				v1.LabelNoOperator: "true",
			},
		},
	}
	resolved, usedFallback, reason, err := ResolveDeleteContext(context.Background(), nil, m)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if usedFallback {
		t.Error("expected no legacy fallback for nooperator resource")
	}
	if reason != "" {
		t.Errorf("expected empty reason, got: %s", reason)
	}
	if resolved == m {
		t.Error("resolved must be a DeepCopy, not the same pointer")
	}
}

func TestShouldUseLegacyDeleteFallback_Complete(t *testing.T) {
	need, reason := ShouldUseLegacyDeleteFallback(fullMiddlewareOperator("mo-6", "default"))
	if need {
		t.Errorf("expected no fallback needed, got reason: %s", reason)
	}
	if reason != "" {
		t.Errorf("expected empty reason, got: %s", reason)
	}
}

func TestShouldUseLegacyDeleteFallback_Incomplete(t *testing.T) {
	need, reason := ShouldUseLegacyDeleteFallback(emptyMiddlewareOperator("mo-7", "default"))
	if !need {
		t.Error("expected fallback needed")
	}
	if reason == "" {
		t.Error("expected non-empty reason")
	}
}
