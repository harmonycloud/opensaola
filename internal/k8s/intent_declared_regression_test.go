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
	"encoding/json"
	"reflect"
	"testing"
	"unicode/utf8"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/managedfields"
)

func restoreVerificationPlan(t *testing.T, converter managedfields.TypeConverter, desired *unstructured.Unstructured, plan *DeclaredIntentMergeResult) *DeclaredIntentMergeResult {
	t.Helper()
	stored, err := StoreDeletionChecks(plan.DeletionChecks)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(stored)
	if err != nil {
		t.Fatal(err)
	}
	var read []StoredDeletionCheck
	if err := json.Unmarshal(payload, &read); err != nil {
		t.Fatal(err)
	}
	checks, err := RestoreDeletionChecks(read)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := NewDeclaredValidationPlan(converter, desired, checks, plan.NamedEmptyLists()...)
	if err != nil {
		t.Fatal(err)
	}
	return restored
}

func TestAlreadyAbsentMemberReinsertedInResponse(t *testing.T) {
	converter := declaredTestConverter(t)
	for _, tc := range []struct{ name, before, live, accepted string }{
		{"opaque", `{"containers":[{"name":"broker","image":"own"}]}`, `{"containers":[{"name":"other"}]}`, `{"containers":[{"name":"other"},{"name":"broker","image":"own"}]}`},
		{"compound map list", `{"ports":[{"port":80,"target":"own"}]}`, `{"ports":[{"port":443,"protocol":"TCP"}]}`, `{"ports":[{"port":443,"protocol":"TCP"},{"port":80,"protocol":"TCP","target":"own"}]}`},
		{"set", `{"tags":["old"]}`, `{"tags":["external"]}`, `{"tags":["external","old"]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, l, d, a := intentTestObject(t, tc.before), intentTestObject(t, tc.live), intentTestObject(t, `{}`), intentTestObject(t, tc.accepted)
			plan, err := PlanDeclaredResourceIntent(converter, b, l, d, d, nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, p := range []*DeclaredIntentMergeResult{plan, restoreVerificationPlan(t, converter, d, plan)} {
				if err := p.Validate(converter, l); err != nil {
					t.Fatalf("actual deletion should be confirmable: %v", err)
				}
				if err := p.Validate(converter, a); err == nil {
					t.Fatal("confirmed reinserted historical member")
				}
			}
		})
	}
}

func TestStoredMembershipKeepsFullIntegerPrecision(t *testing.T) {
	for _, segment := range []string{`k:{"id":9007199254740993}`, `k:{"id":-9223372036854775808,"mode":"A"}`, `v:9223372036854775807`, `v:{"id":9007199254740993}`} {
		stored := []StoredDeletionCheck{{Segments: []string{"f:items", segment}}}
		checks, err := RestoreDeletionChecks(stored)
		if err != nil {
			t.Fatal(err)
		}
		after, err := StoreDeletionChecks(checks)
		if err != nil || !reflect.DeepEqual(stored, after) {
			t.Fatalf("membership identity changed: %v => %v: %v", stored, after, err)
		}
	}
}

func TestNamedListRemovalAfterExternalTypeChange(t *testing.T) {
	converter := declaredTestConverter(t)
	before := intentTestObject(t, `{"containers":[{"name":"broker","image":"own"}]}`)
	desired := intentTestObject(t, `{}`)
	for _, body := range []string{`{"containers":{"external":"keep"}}`, `{"containers":"external"}`, `{"containers":null}`} {
		live := intentTestObject(t, body)
		plan, err := PlanDeclaredResourceIntent(converter, before, live, desired, desired, nil)
		if err != nil {
			t.Fatal(err)
		}
		if plan.DeletionConflictError() == nil || !reflect.DeepEqual(plan.Object.Object, live.Object) {
			t.Fatalf("deletion lost an external replacement: %s", body)
		}
	}
}

func TestLegacyDeletionUsesDistinctFieldSegments(t *testing.T) {
	converter := declaredTestConverter(t)
	before := intentTestObject(t, `{"group":{"cfg":"old"},"group.cfg":"own"}`)
	desired := intentTestObject(t, `{}`)
	for _, live := range []string{`{"group":{"cfg":"external"},"group.cfg":"own"}`, `{"group":{"cfg":"old"},"group.cfg":"external"}`} {
		plan, err := PlanLegacyResourceIntentDeletions(converter, before, intentTestObject(t, live), desired, nil)
		if err != nil {
			t.Fatal(err)
		}
		if plan.DeletionConflictError() == nil {
			t.Fatalf("legacy plan ignored an external change, live %s", live)
		}
	}
}

func TestEmptyListRecoveryUsesOriginalTopology(t *testing.T) {
	converter := declaredTestConverter(t)
	desired := intentTestObject(t, `{"list":[]}`)
	actual := intentTestObject(t, `{"list":[{"name":"external"}]}`)
	for _, tc := range []struct {
		name, live  string
		associative bool
	}{
		{"opaque named list", `{"list":[{"name":"existing"}]}`, true},
		{"opaque ordered list", `{"list":["existing"]}`, false},
		{"no topology evidence", `{}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := PlanDeclaredResourceIntent(converter, nil, intentTestObject(t, tc.live), desired, desired, nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, p := range []*DeclaredIntentMergeResult{plan, restoreVerificationPlan(t, converter, desired, plan)} {
				if got := p.Validate(converter, actual) == nil; got != tc.associative {
					t.Fatalf("accepted new named member = %v, want %v", got, tc.associative)
				}
				for _, body := range []string{`{"list":[{"name":"x"},{"name":"x"}]}`, `{"list":[{"name":"x"},{"no-name":0}]}`, `{"list":[{"name":42}]}`, `{"list":[{"name":""}]}`} {
					if err := p.Validate(converter, intentTestObject(t, body)); err == nil {
						t.Fatalf("malformed list verified: %s", body)
					}
				}
			}
		})
	}
	for _, bad := range []StoredDeletionCheck{{}, {Segments: []string{"unknown"}}, {Segments: []string{"i:2"}}, {Segments: []string{"f:items", "i:-1"}}, {Segments: []string{"f:items", "i:+1"}}, {Segments: []string{"f:items", "i:18446744073709551616"}}, {Segments: []string{"k:{}"}}, {Segments: []string{"f:map"}, AllowExternalPayload: true}} {
		if _, err := RestoreDeletionChecks([]StoredDeletionCheck{bad}); err == nil {
			t.Fatal("invalid stored check accepted")
		}
	}
	if _, err := NewDeclaredValidationPlan(converter, desired, nil, []string{"f:spec", "f:other"}); err == nil {
		t.Fatal("unconsumed topology hint accepted")
	}
}

// The literal key a.b and the traversal a -> b must have distinct caches,
// deletion checks and persisted identities for any valid JSON key or member.
func FuzzDeclaredPlanFieldIdentity(f *testing.F) {
	f.Add("group", "cfg", "A")
	f.Add("f:a", "k:/~1", `[name="A"]`)
	f.Add("控制", "./零", "\u0000")
	f.Add(`\`, ":", ".")
	converter := declaredTestConverter(f)
	f.Fuzz(func(t *testing.T, a, b, member string) {
		if len(a)+len(b)+len(member) > 300 || !utf8.ValidString(a) || !utf8.ValidString(b) || !utf8.ValidString(member) || member == "" {
			t.Skip()
		}
		before := intentTestObject(t, `{}`)
		owned := []any{map[string]any{"name": member, "owned": "old"}}
		before.Object["spec"] = map[string]any{"tree": map[string]any{a: map[string]any{b: owned}, a + "." + b: owned}}
		desired := intentTestObject(t, `{}`)
		plan, err := PlanDeclaredResourceIntent(converter, before, before, desired, desired, nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range []*DeclaredIntentMergeResult{plan, restoreVerificationPlan(t, converter, desired, plan)} {
			if err := p.Validate(converter, desired); err != nil {
				t.Fatal(err)
			}
			for _, nested := range []bool{false, true} {
				actual := before.DeepCopy()
				external := []any{map[string]any{"name": member + "!", "foreign": true}}
				path := []string{"spec", "tree", a, b}
				if nested {
					path = []string{"spec", "tree", a + "." + b}
				}
				if err := unstructured.SetNestedSlice(actual.Object, external, path...); err != nil {
					t.Fatal(err)
				}
				if err := p.Validate(converter, actual); err == nil {
					t.Fatal("a remaining owned member passed verification")
				}
			}
		}
	})
}

// Exercise the same declaration through drift correction, replay and withdrawal.
// The external field is a sibling within a named member, so its preservation
// can be checked independently of the shape selected for the managed field.
func FuzzDeclaredEditLifecycle(f *testing.F) {
	f.Add([]byte{0}, int64(9007199254740993))
	f.Add([]byte{1, 2, 3, 4, 5}, int64(-9223372036854775808))
	f.Add([]byte{5, 4, 3, 2, 1, 0}, int64(9223372036854775807))
	converter := declaredTestConverter(f)
	f.Fuzz(func(t *testing.T, shape []byte, number int64) {
		var payload any = number
		for _, op := range shape[:min(6, len(shape))] {
			switch op % 6 {
			case 0:
				payload = map[string]any{"nested": payload}
			case 1:
				payload = []any{map[string]any{"name": "config", "value": payload}}
			case 2:
				payload = []any{number, number ^ 1}
			case 3:
				payload = map[string]any{"a.b": payload, "a": map[string]any{"b": number}}
			case 4:
				payload = nil
			case 5:
				payload = []any{}
			}
		}
		live := intentTestObject(t, `{"containers":[{"name":"broker","owned":"drift","external":"keep"}]}`)
		desired := intentTestObject(t, `{}`)
		desired.Object["spec"] = map[string]any{"containers": []any{map[string]any{"name": "broker", "owned": payload}}}
		plan, err := PlanDeclaredResourceIntent(converter, nil, live, desired, desired, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := plan.Validate(converter, plan.Object); err != nil {
			t.Fatal(err)
		}
		want := intentTestObject(t, `{}`)
		want.Object["spec"] = map[string]any{"containers": []any{map[string]any{"name": "broker", "owned": payload, "external": "keep"}}}
		if !reflect.DeepEqual(plan.Object.Object, want.Object) {
			t.Fatalf("apply: got %v, want %v", plan.Object.Object, want.Object)
		}
		replay, err := PlanDeclaredResourceIntent(converter, desired, plan.Object, desired, desired, nil)
		if err != nil || !reflect.DeepEqual(replay.Object.Object, want.Object) {
			t.Fatalf("replay: %v, error %v", replay, err)
		}
		empty := intentTestObject(t, `{}`)
		withdraw, err := PlanDeclaredResourceIntent(converter, desired, replay.Object, empty, empty, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := withdraw.DeletionConflictError(); err != nil {
			t.Fatal(err)
		}
		externalOnly := intentTestObject(t, `{"containers":[{"name":"broker","external":"keep"}]}`)
		if !reflect.DeepEqual(withdraw.Object.Object, externalOnly.Object) {
			t.Fatalf("withdraw: got %v, want %v", withdraw.Object.Object, externalOnly.Object)
		}
		if err := restoreVerificationPlan(t, converter, empty, withdraw).Validate(converter, externalOnly); err != nil {
			t.Fatal(err)
		}
	})
}

// Replacing a retained atomic value must submit the new declaration. Once
// submitted, admission may default any key now omitted from that value.
func TestAtomicWithdrawalSurvivesRecovery(t *testing.T) {
	converter := declaredTestConverter(t)
	for _, tc := range []struct{ name, before, raw, withExternal, stale string }{
		{"atomic map", `{"atomicMap":{"keep":"yes","remove":"old"}}`, `{"atomicMap":{"keep":"yes"}}`, `{"atomicMap":{"keep":"yes","added":"external"}}`, `{"atomicMap":{"keep":"yes","remove":"reinserted","added":"external"}}`},
		{"atomic list", `{"anyItems":[{"nested":{"keep":1,"remove":false}}]}`, `{"anyItems":[{"nested":{"keep":1}}]}`, `{"anyItems":[{"nested":{"keep":1,"added":"external"}}]}`, `{"anyItems":[{"nested":{"keep":1,"remove":true,"added":"external"}}]}`},
		{"keyed parent", `{"entries":[{"id":"a","options":{"keep":"yes","remove":"old"}},{"id":"b","value":"same"}]}`, `{"entries":[{"id":"a","options":{"keep":"yes"}},{"id":"b","value":"same"}]}`, `{"entries":[{"id":"b","value":"same"},{"id":"a","options":{"keep":"yes","added":"external"}}]}`, `{"entries":[{"id":"b","value":"same"},{"id":"a","options":{"keep":"yes","remove":"again"}}]}`},
		{"untyped positional list", `{"rules":[{"keep":1,"remove":2}]}`, `{"rules":[{"keep":1}]}`, `{"rules":[{"keep":1,"added":3}]}`, `{"rules":[{"keep":1,"remove":4}]}`},
		{"nested duplicate names", `{"atomicOpaque":{"rules":[{"name":"a","slot":1,"remove":2},{"name":"a","slot":2}]}}`, `{"atomicOpaque":{"rules":[{"name":"a","slot":1},{"name":"a","slot":2}]}}`, `{"atomicOpaque":{"rules":[{"name":"a","slot":1,"extra":3},{"name":"a","slot":2,"remove":"on other slot"}]}}`, `{"atomicOpaque":{"rules":[{"name":"a","slot":1,"remove":4},{"name":"a","slot":2}]}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before, raw := intentTestObject(t, tc.before), intentTestObject(t, tc.raw)
			fresh, stale := intentTestObject(t, tc.withExternal), intentTestObject(t, tc.stale)
			write, err := PlanDeclaredResourceIntent(converter, before, before, raw, raw, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := write.DeletionConflictError(); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(write.Object.Object, raw.Object) {
				t.Fatalf("withdrawal was not applied: %v", write.Object.Object["spec"])
			}
			for _, plan := range []*DeclaredIntentMergeResult{write, restoreVerificationPlan(t, converter, raw, write)} {
				if err := plan.Validate(converter, fresh); err != nil {
					t.Fatalf("unrelated added field rejected: %v", err)
				}
				for _, defaulted := range []*unstructured.Unstructured{before, stale} {
					if err := plan.Validate(converter, defaulted); err != nil {
						t.Fatalf("defaulting an omitted atomic key was rejected: %v", err)
					}
				}
			}

			legacy, err := PlanLegacyResourceIntentDeletions(converter, before, before, raw, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := legacy.DeletionConflictError(); err != nil {
				t.Fatal(err)
			}
			upgraded, err := PlanDeclaredResourceIntent(converter, nil, legacy.Object, raw, raw, nil)
			if err != nil {
				t.Fatal(err)
			}
			upgraded.DeletionChecks = append(upgraded.DeletionChecks, legacy.DeletionChecks...)
			if !reflect.DeepEqual(upgraded.Object.Object, raw.Object) {
				t.Fatalf("v1 migration kept a withdrawn field: %v", upgraded.Object.Object["spec"])
			}
			if err := upgraded.Validate(converter, fresh); err != nil {
				t.Fatal(err)
			}
			if err := upgraded.Validate(converter, stale); err != nil {
				t.Fatalf("defaulting after v1 migration was rejected: %v", err)
			}
			next, err := PlanDeclaredResourceIntent(converter, raw, stale, raw, raw, nil)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(next.Object.Object, stale.Object) {
				t.Fatalf("repeated declaration rewrote API defaults: %v", next.Object.Object["spec"])
			}
			conflict, err := PlanLegacyResourceIntentDeletions(converter, before, stale, raw, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := conflict.DeletionConflictError(); err == nil {
				t.Fatal("v1 migration overwrote a withdrawn field changed by another writer")
			}

			oldTyped, err := converter.ObjectToTyped(before)
			if err != nil {
				t.Fatal(err)
			}
			newTyped, err := converter.ObjectToTyped(raw)
			if err != nil {
				t.Fatal(err)
			}
			change, err := oldTyped.Compare(newTyped)
			if err != nil {
				t.Fatal(err)
			}
			protected := change.Modified // SSA ownership of the whole atomic value
			conflict, err = PlanLegacyResourceIntentDeletions(converter, before, before, raw, protected)
			if err != nil {
				t.Fatal(err)
			}
			if err := conflict.DeletionConflictError(); err == nil {
				t.Fatal("v1 migration ignored another manager's ownership of a withdrawn field")
			}

			// Legacy v2 pending Secrets contain a history and no serialized
			// deletion checks. The restart path reconstructs those checks.
			const hash = "atomic-schema"
			owner := intentUnitOwner()
			for _, version := range []int{1, currentResourceIntentVersion} {
				history := newIntentCandidate(owner, before, before, nil, "", hash, nil)
				if version == 1 {
					history.Version = 1
					history.SchemaHash = hash
				}
				candidate := newIntentCandidate(owner, before, raw, nil, "", hash, nil)
				candidate.DeletionBase = history
				secret, err := buildResourceIntentSecret(owner, *candidate)
				if err != nil {
					t.Fatal(err)
				}
				base, err := effectiveResourceIntent(candidate, secret)
				if err != nil || !base.pendingDeletion {
					t.Fatalf("expected unverified historical base: %v", err)
				}
				for _, test := range []struct {
					name string
					live *unstructured.Unstructured
					want *unstructured.Unstructured
				}{
					{"confirmed", fresh, raw}, {"unverified", stale, before},
				} {
					recovered, _, err := resolveLegacyPending(converter, hash, test.live, raw, base, secret)
					if err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(recovered.Desired.Object, intentObject(test.want).Object) {
						t.Fatalf("v%d %s: incorrect historical baseline: %v", version, test.name, recovered.Desired.Object["spec"])
					}
				}
			}
		})
	}
}

func TestOpaqueListIdentityInsideAndOutsideAtomicObject(t *testing.T) {
	converter := declaredTestConverter(t)
	for _, tc := range []struct {
		name                             string
		desired, reordered, supplemented string
		ordered                          bool
	}{
		{"ordinary opaque list", `{"rules":[{"name":"a","v":1},{"name":"b","v":2}]}`, `{"rules":[{"name":"b","v":2},{"name":"a","v":1}]}`, `{"rules":[{"name":"a","v":1,"extra":true},{"name":"b","v":2}]}`, false},
		{"atomic parent", `{"atomicOpaque":{"rules":[{"name":"a","v":1},{"name":"b","v":2}]}}`, `{"atomicOpaque":{"rules":[{"name":"b","v":2},{"name":"a","v":1}]}}`, `{"atomicOpaque":{"rules":[{"name":"a","v":1,"extra":true},{"name":"b","v":2}]}}`, true},
		{"atomic parent and duplicate names", `{"atomicOpaque":{"rules":[{"name":"a","v":1},{"name":"a","v":2}]}}`, `{"atomicOpaque":{"rules":[{"name":"a","v":2},{"name":"a","v":1}]}}`, `{"atomicOpaque":{"rules":[{"name":"a","v":1,"extra":true},{"name":"a","v":2}]}}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			desired := intentTestObject(t, tc.desired)
			err := ValidateDeclaredResourceIntent(converter, desired, intentTestObject(t, tc.reordered))
			if (err != nil) != tc.ordered {
				t.Errorf("reordered receipt error=%v, want ordered=%t", err, tc.ordered)
			}
			if err := ValidateDeclaredResourceIntent(converter, desired, intentTestObject(t, tc.supplemented)); err != nil {
				t.Errorf("supplemented receipt: %v", err)
			}
		})
	}
}
