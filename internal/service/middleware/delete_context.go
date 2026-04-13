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
	"fmt"
	"strings"

	v1 "github.com/opensaola/opensaola/api/v1"
	"github.com/opensaola/opensaola/internal/k8s"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// hardRequirementsMet reports whether the middleware contains enough information to
// resolve delete semantics.
//
// Base hard requirements are always: packageName label and baseline.
// Delete can then be resolved in one of two legal modes:
//  1. operator mode: operatorBaseline.Name and operatorBaseline.GvkName are both present
//  2. no-operator mode: operatorBaseline.Name and operatorBaseline.GvkName are both empty,
//     and the later delete flow relies on MiddlewareBaseline.spec.gvk.
func hardRequirementsMet(m *v1.Middleware) (bool, []string) {
	var missing []string
	if m.Labels[v1.LabelPackageName] == "" {
		missing = append(missing, "packageName")
	}
	if m.Spec.Baseline == "" {
		missing = append(missing, "baseline")
	}

	operatorName := m.Spec.OperatorBaseline.Name
	operatorGVKName := m.Spec.OperatorBaseline.GvkName
	switch {
	case operatorName == "" && operatorGVKName == "":
		// Valid no-operator delete path.
	case operatorName != "" && operatorGVKName != "":
		// Valid operator delete path.
	default:
		if operatorName == "" {
			missing = append(missing, "operatorBaseline.Name")
		}
		if operatorGVKName == "" {
			missing = append(missing, "operatorBaseline.GvkName")
		}
	}
	return len(missing) == 0, missing
}

// ShouldUseLegacyDeleteFallback reports whether the middleware is missing any hard
// requirement for delete and therefore needs the cache-based legacy fallback.
// Returns (true, reason) when fallback is needed, (false, "") otherwise.
func ShouldUseLegacyDeleteFallback(m *v1.Middleware) (bool, string) {
	ok, missing := hardRequirementsMet(m)
	if ok {
		return false, ""
	}
	return true, fmt.Sprintf("missing hard requirements: %s", strings.Join(missing, ", "))
}

// ResolveDeleteContext returns a DeepCopy of m with any missing hard-requirement
// fields back-filled exclusively from k8s.MiddlewareCache (legacy fallback).
// It never mutates the live object and never executes the actual deletion.
//
// Return values:
//   - *v1.Middleware - resolved copy (always a DeepCopy, never the live object)
//   - bool           - true when the legacy cache fallback was attempted
//   - string         - human-readable reason / description
//   - error          - non-nil on cache miss or when hard requirements are still unmet after fallback
func ResolveDeleteContext(_ context.Context, _ client.Client, m *v1.Middleware) (*v1.Middleware, bool, string, error) {
	cp := m.DeepCopy()

	// Fast path: all hard requirements already present on the live object.
	if met, _ := hardRequirementsMet(cp); met {
		return cp, false, "all hard requirements already present", nil
	}

	// Slow path: attempt legacy fallback from MiddlewareCache.
	needFallback, reason := ShouldUseLegacyDeleteFallback(cp)
	if !needFallback {
		// Should not happen given the fast-path check above, but be defensive.
		return cp, false, "", nil
	}

	cacheKey := types.NamespacedName{Name: cp.Name, Namespace: cp.Namespace}.String()
	cachedVal, hit := k8s.MiddlewareCache.Get(cacheKey)
	if !hit {
		return nil, true, reason, fmt.Errorf("delete context: cache miss for %s (%s)", cacheKey, reason)
	}
	cachedM := &cachedVal

	// Fill only the missing fields from cache; never overwrite fields already set on the copy.
	// This stays compatible with both delete modes:
	//   - operator mode: cache may supply the missing operatorBaseline fields
	//   - no-operator mode: operatorBaseline may remain empty as long as baseline/packageName exist
	if cp.Labels == nil {
		cp.Labels = make(map[string]string)
	}
	if cp.Labels[v1.LabelPackageName] == "" && cachedM.Labels[v1.LabelPackageName] != "" {
		cp.Labels[v1.LabelPackageName] = cachedM.Labels[v1.LabelPackageName]
	}
	if cp.Spec.Baseline == "" && cachedM.Spec.Baseline != "" {
		cp.Spec.Baseline = cachedM.Spec.Baseline
	}
	if cp.Spec.OperatorBaseline.Name == "" && cachedM.Spec.OperatorBaseline.Name != "" {
		cp.Spec.OperatorBaseline.Name = cachedM.Spec.OperatorBaseline.Name
	}
	if cp.Spec.OperatorBaseline.GvkName == "" && cachedM.Spec.OperatorBaseline.GvkName != "" {
		cp.Spec.OperatorBaseline.GvkName = cachedM.Spec.OperatorBaseline.GvkName
	}

	// Re-validate: hard requirements must all be satisfied after fallback.
	if met, stillMissing := hardRequirementsMet(cp); !met {
		return nil, true, reason, fmt.Errorf(
			"delete context: hard requirements still unmet after cache fallback for %s: missing %s",
			cacheKey, strings.Join(stillMissing, ", "),
		)
	}

	return cp, true, reason, nil
}
