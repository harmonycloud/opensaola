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

package synchronizer

import (
	"context"
	"fmt"
	"strings"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	"github.com/harmonycloud/opensaola/internal/k8s"
	"github.com/harmonycloud/opensaola/internal/service/statusrule"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// applyDeclaredStatusRules overlays the generic status projection with the
// first matching CEL rule declared on the active operator baseline GVK. Its
// boolean return reports whether a rule declaration exists, not whether a rule
// matched. This prevents an invalid or non-matching declaration from falling
// through to a legacy GVK-specific projector.
func applyDeclaredStatusRules(
	ctx context.Context,
	cli client.Client,
	mid *v1.Middleware,
	cr *unstructured.Unstructured,
	previous v1.CustomResources,
	target *v1.CustomResources,
) (bool, error) {
	rules, declared, err := statusRulesForCustomResource(ctx, cli, mid, cr)
	if err != nil || !declared {
		return declared, err
	}

	result, matched, err := statusrule.Evaluate(ctx, rules, cr.Object, previous)
	if err != nil || !matched {
		return true, err
	}
	result.Apply(target)
	return true, nil
}

func statusRulesForCustomResource(
	ctx context.Context,
	cli client.Client,
	mid *v1.Middleware,
	cr *unstructured.Unstructured,
) ([]string, bool, error) {
	if mid == nil || cr == nil {
		return nil, false, nil
	}
	operatorBaselineName := strings.TrimSpace(mid.Spec.OperatorBaseline.Name)
	gvkName := strings.TrimSpace(mid.Spec.OperatorBaseline.GvkName)
	if operatorBaselineName == "" || gvkName == "" {
		return nil, false, nil
	}

	operatorBaseline, err := k8s.GetMiddlewareOperatorBaseline(ctx, cli, operatorBaselineName)
	if err != nil {
		return nil, false, fmt.Errorf("get operator baseline %q: %w", operatorBaselineName, err)
	}
	for _, declaredGVK := range operatorBaseline.Spec.GVKs {
		if declaredGVK.Name != gvkName {
			continue
		}
		if len(declaredGVK.StatusRules) == 0 {
			return nil, false, nil
		}

		actualGVK := cr.GroupVersionKind()
		if declaredGVK.Group != actualGVK.Group ||
			declaredGVK.Version != actualGVK.Version ||
			declaredGVK.Kind != actualGVK.Kind {
			return nil, true, fmt.Errorf(
				"operator baseline %q gvk %q is %s/%s, Kind=%s but primary resource is %s",
				operatorBaselineName,
				gvkName,
				declaredGVK.Group,
				declaredGVK.Version,
				declaredGVK.Kind,
				actualGVK.String(),
			)
		}
		return declaredGVK.StatusRules, true, nil
	}

	return nil, false, fmt.Errorf("operator baseline %q does not define gvk %q", operatorBaselineName, gvkName)
}
