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

package k8s

import (
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/applyconfigurations"
	kubescheme "k8s.io/client-go/kubernetes/scheme"
)

func TestNetworkPolicyAtomicDefaults(t *testing.T) {
	converter := applyconfigurations.NewTypeConverter(kubescheme.Scheme)
	policy := func(spec string) *unstructured.Unstructured {
		obj := intentTestObject(t, spec)
		obj.SetAPIVersion("networking.k8s.io/v1")
		obj.SetKind("NetworkPolicy")
		return obj
	}
	const raw = `{"ingress":[{"ports":[{"port":5432}]}]}`
	const defaulted = `{"ingress":[{"ports":[{"port":5432,"protocol":"TCP"}]}]}`
	t.Run("submit removal even when the native wire type omits null", func(t *testing.T) {
		before := policy(`{"ingress":[{"ports":[{"port":5432,"endPort":null}]}]}`)
		desired := policy(raw)
		plan, err := PlanDeclaredResourceIntent(converter, before, before, desired, desired, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(plan.Object.Object["spec"], desired.Object["spec"]) {
			t.Fatalf("submission kept the released endPort: %v", plan.Object.Object["spec"])
		}
		if err := plan.Validate(converter, before); err != nil {
			t.Fatalf("readmission of null should succeed: %v", err)
		}
	})
	for _, tc := range []struct {
		name, declared, observed string
		valid                    bool
	}{
		{"server adds TCP", raw, defaulted, true},
		{"port rewritten", raw, `{"ingress":[{"ports":[{"port":80,"protocol":"TCP"}]}]}`, false},
		{"port removed", raw, `{"ingress":[{"ports":[{"protocol":"TCP"}]}]}`, false},
		{"explicit protocol missing", defaulted, raw, false},
		{"omitted pointer and defaulted sibling", `{"ingress":[{"ports":[{"port":5432,"endPort":null}]}]}`, defaulted, true},
		{"explicit endPort missing", `{"ingress":[{"ports":[{"port":5432,"endPort":5433}]}]}`, defaulted, false},
		{"extra port range", raw, `{"ingress":[{"ports":[{"port":5432,"protocol":"TCP","endPort":5433}]}]}`, true},
		{"extra empty member", raw, `{"ingress":[{"ports":[{"port":5432,"protocol":"TCP"},{}]}]}`, false},
		{"ordered rule membership", `{"ingress":[{"ports":[{"port":80}]},{"ports":[{"port":81}]}]}`, `{"ingress":[{"ports":[{"port":81,"protocol":"TCP"}]},{"ports":[{"port":80,"protocol":"TCP"}]}]}`, false},
		{"parent not declared", `{"ingress":[{}]}`, `{"ingress":[{"ports":[{"protocol":"TCP"}]}]}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want, got := policy(tc.declared), policy(tc.observed)
			write, err := PlanDeclaredResourceIntent(converter, want, got, want, want, nil)
			if err != nil {
				t.Fatal(err)
			}
			if noop := reflect.DeepEqual(write.Object.Object, got.Object); noop != tc.valid {
				t.Fatalf("no-op=%t want %t", noop, tc.valid)
			}
			recovered, err := NewDeclaredValidationPlan(converter, want, nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, plan := range []*DeclaredIntentMergeResult{write, recovered} {
				if err := plan.Validate(converter, got); (err == nil) != tc.valid {
					t.Fatalf("valid=%t error=%v", tc.valid, err)
				}
			}
		})
	}
}

// Moving a complete port declaration to another slot does not withdraw the
// previous occupant's endPort from the new occupant.
func TestAtomicMemberMovementDoesNotWithdrawNewSlotFields(t *testing.T) {
	converter := applyconfigurations.NewTypeConverter(kubescheme.Scheme)
	policy := func(ports string) *unstructured.Unstructured {
		obj := intentTestObject(t, `{"ingress":[{"ports":[`+ports+`]}]}`)
		obj.SetAPIVersion("networking.k8s.io/v1")
		obj.SetKind("NetworkPolicy")
		return obj
	}
	const a = `{"port":80,"endPort":90}`
	const b = `{"port":443}`
	const enrichedB = `{"port":443,"endPort":450}`
	before := policy(a + "," + b)
	for _, tc := range []struct {
		name, want, observed string
		valid                bool
	}{
		{"swap", b + "," + a, enrichedB + "," + a, true},
		{"drop first member", b, enrichedB, true},
		{"same slot permits a defaulted range", `{"port":80},` + b, `{"port":80,"endPort":85},` + b, true},
		{"explicit bound changed", a + "," + b, `{"port":80,"endPort":85},` + b, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			desired := policy(tc.want)
			plan, err := PlanDeclaredResourceIntent(converter, before, before, desired, desired, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := plan.DeletionConflictError(); err != nil {
				t.Fatal(err)
			}
			for _, p := range []*DeclaredIntentMergeResult{plan, restoreVerificationPlan(t, converter, desired, plan)} {
				if err := p.Validate(converter, policy(tc.observed)); (err == nil) != tc.valid {
					t.Fatalf("valid=%t, observed %s, error=%v", tc.valid, tc.observed, err)
				}
				if err := p.Validate(converter, policy(`{"port":1000}`)); err == nil {
					t.Fatal("accepted a different port declaration")
				}
			}
		})
	}
}
