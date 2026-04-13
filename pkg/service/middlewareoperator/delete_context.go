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
	"fmt"
	"strings"

	v1 "github.com/OpenSaola/opensaola/api/v1"
	"github.com/OpenSaola/opensaola/pkg/k8s"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func hardRequirementsMet(m *v1.MiddlewareOperator) (bool, []string) {
	var missing []string
	if m.Labels[v1.LabelPackageName] == "" {
		missing = append(missing, "packageName")
	}
	if m.Spec.Baseline == "" {
		missing = append(missing, "baseline")
	}
	return len(missing) == 0, missing
}

func ShouldUseLegacyDeleteFallback(m *v1.MiddlewareOperator) (bool, string) {
	ok, missing := hardRequirementsMet(m)
	if ok {
		return false, ""
	}
	return true, fmt.Sprintf("missing hard requirements: %s", strings.Join(missing, ", "))
}

func ResolveDeleteContext(_ context.Context, _ client.Client, m *v1.MiddlewareOperator) (*v1.MiddlewareOperator, bool, string, error) {
	cp := m.DeepCopy()
	if IsNoOperatorResource(cp) {
		return cp, false, "", nil
	}
	if met, _ := hardRequirementsMet(cp); met {
		return cp, false, "all hard requirements already present", nil
	}

	needFallback, reason := ShouldUseLegacyDeleteFallback(cp)
	if !needFallback {
		return cp, false, "", nil
	}

	cacheKey := types.NamespacedName{Name: cp.Name, Namespace: cp.Namespace}.String()
	raw, hit := k8s.MiddlewareOperatorCache.Load(cacheKey)
	if !hit {
		return nil, true, reason, fmt.Errorf("delete context: cache miss for %s (%s)", cacheKey, reason)
	}

	cachedM, valid := raw.(*v1.MiddlewareOperator)
	if !valid || cachedM == nil {
		return nil, true, reason, fmt.Errorf("delete context: invalid cache entry for %s", cacheKey)
	}

	if cp.Labels == nil {
		cp.Labels = make(map[string]string)
	}
	if cp.Labels[v1.LabelPackageName] == "" && cachedM.Labels[v1.LabelPackageName] != "" {
		cp.Labels[v1.LabelPackageName] = cachedM.Labels[v1.LabelPackageName]
	}
	if cp.Spec.Baseline == "" && cachedM.Spec.Baseline != "" {
		cp.Spec.Baseline = cachedM.Spec.Baseline
	}

	if met, stillMissing := hardRequirementsMet(cp); !met {
		return nil, true, reason, fmt.Errorf(
			"delete context: hard requirements still unmet after cache fallback for %s: missing %s",
			cacheKey, strings.Join(stillMissing, ", "),
		)
	}

	return cp, true, reason, nil
}
