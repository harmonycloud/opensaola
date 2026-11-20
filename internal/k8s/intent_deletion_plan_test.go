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
	"strings"
	"testing"

	"sigs.k8s.io/structured-merge-diff/v6/fieldpath"
)

func TestAlreadyAbsentDeletionStillVerified(t *testing.T) {
	converter := declaredTestConverter(t)
	for _, test := range []struct {
		name, before, accepted string
		valid                  bool
	}{
		{"scalar reinsertion", `{"removed":"owned"}`, `{"removed":"again"}`, false},
		{"map own child reinsertion", `{"options":{"owned":"yes"}}`, `{"options":{"owned":"again"}}`, false},
		{"map external new child", `{"options":{"owned":"yes"}}`, `{"options":{"external":"new"}}`, true},
		{"opaque owned payload reinsertion", `{"containers":[{"name":"owned","image":"one"}]}`, `{"containers":[{"name":"owned","image":"one","external":"new"}]}`, false},
		{"typed compound owned payload reinsertion", `{"ports":[{"port":80,"protocol":"TCP","target":"owned"}]}`, `{"ports":[{"port":80,"protocol":"TCP","target":"owned","external":"new"}]}`, false},
		{"typed compound new member", `{"ports":[{"port":80,"protocol":"TCP","target":"owned"}]}`, `{"ports":[{"port":80,"protocol":"UDP","external":"new"}]}`, true},
		{"set membership reinsertion", `{"tags":["owned"]}`, `{"tags":["owned"]}`, false},
		{"set new member", `{"tags":["owned"]}`, `{"tags":["external"]}`, true},
		{"atomic reinsertion", `{"atomic":[{"name":"owned"}]}`, `{"atomic":[{"name":"external"}]}`, false},
		{"empty map reinsertion", `{"options":{}}`, `{"options":{}}`, false},
		{"opaque null reinsertion", `{"nullable":null}`, `{"nullable":null}`, false},
	} {
		for _, legacy := range []bool{false, true} {
			label := "declared/"
			if legacy {
				label = "legacy/"
			}
			t.Run(label+test.name, func(t *testing.T) {
				before, live, desired := intentTestObject(t, test.before), intentTestObject(t, `{}`), intentTestObject(t, `{}`)
				var plan *DeclaredIntentMergeResult
				var err error
				if legacy {
					plan, err = PlanLegacyResourceIntentDeletions(converter, before, live, desired, nil)
				} else {
					plan, err = PlanDeclaredResourceIntent(converter, before, live, desired, desired, nil)
				}
				if err != nil {
					t.Fatal(err)
				}
				if plan.DeletionConflictError() != nil || len(plan.DeletionChecks) == 0 {
					t.Fatalf("missing valid deletion obligations: %+v", plan)
				}
				if err := ValidateResourceIntentDeletions(converter, plan, live); err != nil {
					t.Fatalf("unchanged absence rejected: %v", err)
				}
				if err := ValidateResourceIntentDeletions(converter, plan, intentTestObject(t, test.accepted)); (err == nil) != test.valid {
					t.Fatalf("valid=%v error=%v checks=%+v", test.valid, err, plan.DeletionChecks)
				}
			})
		}
	}
}

func TestPlanDeclaredMemberDeletion(t *testing.T) {
	converter := declaredTestConverter(t)
	for _, test := range []struct {
		name, before, live, desired, want string
		protected                         *fieldpath.Set
		blocked, external                 bool
	}{
		{name: "whole broker omission cleans owned leaves only", before: `{"containers":[{"name":"broker","image":"owned","env":[{"name":"OWN","value":"owned"}],"volumeMounts":[{"name":"log","mountPath":"/logs"}]}]}`, live: `{"containers":[{"name":"broker","image":"owned","resources":{},"env":[{"name":"OWN","value":"owned"},{"name":"EXTERNAL","value":"keep"}],"volumeMounts":[{"name":"log","mountPath":"/logs"}]},{"name":"sidecar","image":"external"}]}`, desired: `{}`, want: `{"containers":[{"name":"broker","resources":{},"env":[{"name":"EXTERNAL","value":"keep"}]},{"name":"sidecar","image":"external"}]}`, external: true},
		{name: "empty declaration cleans owned member payload", before: `{"containers":[{"name":"broker","volumeMounts":[{"name":"log","mountPath":"/logs"}]}]}`, live: `{"containers":[{"name":"broker","resources":{},"volumeMounts":[{"name":"log","mountPath":"/logs"},{"name":"data","mountPath":"/data"}]}]}`, desired: `{"containers":[]}`, want: `{"containers":[{"name":"broker","resources":{},"volumeMounts":[{"name":"data","mountPath":"/data"}]}]}`, external: true},
		{name: "key-only member removed", before: `{"containers":[{"name":"broker"}]}`, live: `{"containers":[{"name":"broker"}]}`, desired: `{"containers":[]}`, want: `{"containers":[]}`},
		{name: "external empty map and empty list remain", before: `{"containers":[{"name":"broker"}]}`, live: `{"containers":[{"name":"broker","resources":{},"externalList":[]}]}`, desired: `{"containers":[]}`, want: `{"containers":[{"name":"broker","resources":{},"externalList":[]}]}`, external: true},
		{name: "compound keys remain with external payload", before: `{"ports":[{"port":80,"protocol":"TCP","target":"owned"}]}`, live: `{"ports":[{"port":80,"protocol":"TCP","target":"owned","external":{}}]}`, desired: `{"ports":[]}`, want: `{"ports":[{"port":80,"protocol":"TCP","external":{}}]}`, external: true},
		{name: "schema default identity remains valid", before: `{"ports":[{"port":80,"target":"owned"}]}`, live: `{"ports":[{"port":80,"protocol":"TCP","target":"owned","external":"keep"}]}`, desired: `{"ports":[]}`, want: `{"ports":[{"port":80,"protocol":"TCP","external":"keep"}]}`, external: true},
		{name: "reordered nested names are not drift", before: `{"containers":[{"name":"broker","env":[{"name":"A","value":"one"},{"name":"B","value":"two"}]}]}`, live: `{"containers":[{"name":"broker","env":[{"name":"B","value":"two"},{"name":"A","value":"one"}]}]}`, desired: `{"containers":[]}`, want: `{"containers":[]}`},
		{name: "already removed child does not block member deletion", before: `{"containers":[{"name":"broker","image":"owned"}]}`, live: `{"containers":[{"name":"broker"}]}`, desired: `{"containers":[]}`, want: `{"containers":[]}`},
		{name: "owned child changed means conflict", before: `{"containers":[{"name":"secret-member","image":"owned"}]}`, live: `{"containers":[{"name":"secret-member","image":"external-change","resources":{}}]}`, desired: `{"containers":[]}`, blocked: true, external: true},
		{name: "parent atomic Apply protection reaches leaves", before: `{"containers":[{"name":"broker","image":"owned"}]}`, live: `{"containers":[{"name":"broker","image":"owned","resources":{}}]}`, desired: `{"containers":[]}`, protected: fieldpath.NewSet(fieldpath.MakePathOrDie("spec", "containers")), blocked: true, external: true},
		{name: "protected key-only member cannot be deleted", before: `{"containers":[{"name":"broker"}]}`, live: `{"containers":[{"name":"broker"}]}`, desired: `{"containers":[]}`, protected: fieldpath.NewSet(fieldpath.MakePathOrDie("spec", "containers")), blocked: true},
		{name: "atomic array cannot split external member", before: `{"atomic":[{"name":"owned"}]}`, live: `{"atomic":[{"name":"owned"},{"name":"external"}]}`, desired: `{}`, blocked: true},
		{name: "atomic map cannot split external child", before: `{"atomicMap":{"owned":"one"}}`, live: `{"atomicMap":{"owned":"one","external":"two"}}`, desired: `{}`, blocked: true},
		{name: "withdrawn atomic child changed externally", before: `{"atomicMap":{"keep":"yes","drop":"old"}}`, live: `{"atomicMap":{"keep":"yes","drop":"changed"}}`, desired: `{"atomicMap":{"keep":"yes"}}`, blocked: true},
		{name: "other apply owns atomic list holding withdrawn child", before: `{"anyItems":[{"keep":1,"drop":2}]}`, live: `{"anyItems":[{"keep":1,"drop":2}]}`, desired: `{"anyItems":[{"keep":1}]}`, protected: fieldpath.NewSet(fieldpath.MakePathOrDie("spec", "anyItems")), blocked: true},
		{name: "set keeps external membership", before: `{"tags":["owned"]}`, live: `{"tags":["owned","external"]}`, desired: `{}`, want: `{"tags":["external"]}`, external: true},
		{name: "opaque null can be deleted", before: `{"nullable":null}`, live: `{"nullable":null}`, desired: `{}`, want: `{}`},
		{name: "Quantity normalization permits deletion", before: `{"resources":{"cpu":"1.0"}}`, live: `{"resources":{"cpu":"1"}}`, desired: `{}`, want: `{}`},
		{name: "malformed live name list blocks deletion", before: `{"containers":[{"name":"broker"}]}`, live: `{"containers":[{"name":"broker"},{"name":"broker"}]}`, desired: `{}`, blocked: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			before, live, desired := intentTestObject(t, test.before), intentTestObject(t, test.live), intentTestObject(t, test.desired)
			beforeCopy, liveCopy, desiredCopy := before.DeepCopy(), live.DeepCopy(), desired.DeepCopy()
			plan, err := PlanDeclaredResourceIntent(converter, before, live, desired, desired, test.protected)
			if err != nil {
				t.Fatal(err)
			}
			if (len(plan.BlockedDeletions) > 0) != test.blocked || (plan.DeletionConflictError() != nil) != test.blocked {
				t.Fatalf("unexpected conflicts: %+v", plan.BlockedDeletions)
			}
			if test.external && len(plan.PreservedExternal) == 0 {
				t.Fatal("external residual was not identified separately")
			}
			if test.blocked {
				if strings.Contains(plan.DeletionConflictError().Error(), "secret-member") || strings.Contains(plan.DeletionConflictError().Error(), "external-change") {
					t.Fatal("conflict diagnostic contains field values")
				}
				if err := ValidateResourceIntentDeletions(converter, plan, plan.Object); err == nil {
					t.Fatal("blocked plan was accepted")
				}
			} else {
				want := intentTestObject(t, test.want)
				if !reflect.DeepEqual(plan.Object.Object, want.Object) {
					t.Fatalf("got %s\nwant %s", intentTestJSON(plan.Object.Object), intentTestJSON(want.Object))
				}
				if err := ValidateResourceIntentDeletions(converter, plan, plan.Object); err != nil {
					t.Fatalf("own deletion plan did not validate: %v", err)
				}
			}
			if !reflect.DeepEqual(before, beforeCopy) || !reflect.DeepEqual(live, liveCopy) || !reflect.DeepEqual(desired, desiredCopy) {
				t.Fatal("planning mutated input")
			}
		})
	}
}

func TestDeletedGranularMemberCanCarryNewExternalPayload(t *testing.T) {
	converter := declaredTestConverter(t)
	for _, fixture := range []struct {
		name, before, external, resurrected, bare string
	}{
		{"opaque mount payload", `{"containers":[{"name":"broker","volumeMounts":[{"name":"log","mountPath":"/logs"}]}]}`, `{"containers":[{"name":"broker","resources":{}}]}`, `{"containers":[{"name":"broker","resources":{},"volumeMounts":[{"name":"log","mountPath":"/logs"}]}]}`, `{"containers":[{"name":"broker"}]}`},
		{"typed compound payload", `{"ports":[{"port":80,"protocol":"TCP","target":"owned"}]}`, `{"ports":[{"port":80,"protocol":"TCP","external":{}}]}`, `{"ports":[{"port":80,"protocol":"TCP","external":{},"target":"owned"}]}`, `{"ports":[{"port":80,"protocol":"TCP"}]}`},
		{"opaque key-only", `{"containers":[{"name":"broker"}]}`, `{"containers":[{"name":"broker","resources":{}}]}`, `{"containers":[{"name":"broker"}]}`, `{"containers":[{"name":"broker"}]}`},
	} {
		for _, absent := range []bool{false, true} {
			name := fixture.name + "/present"
			if absent {
				name = fixture.name + "/already absent"
			}
			t.Run(name, func(t *testing.T) {
				before, live, desired := intentTestObject(t, fixture.before), intentTestObject(t, fixture.before), intentTestObject(t, `{}`)
				if absent {
					live = desired.DeepCopy()
				}
				plan, err := PlanDeclaredResourceIntent(converter, before, live, desired, desired, nil)
				if err != nil || plan.DeletionConflictError() != nil {
					t.Fatalf("invalid fixture: %v %+v", err, plan)
				}
				if err := ValidateResourceIntentDeletions(converter, plan, intentTestObject(t, fixture.external)); err != nil {
					t.Fatalf("new external payload rejected: %v", err)
				}
				for _, rejected := range []string{fixture.resurrected, fixture.bare} {
					if err := ValidateResourceIntentDeletions(converter, plan, intentTestObject(t, rejected)); err == nil {
						t.Fatal("resurrected owned fields or bare identity accepted")
					}
				}
				legacy, err := PlanLegacyResourceIntentDeletions(converter, before, live, desired, nil)
				if err != nil {
					t.Fatal(err)
				}
				if err := ValidateResourceIntentDeletions(converter, legacy, intentTestObject(t, fixture.external)); err == nil {
					t.Fatal("legacy ownership was silently reinterpreted at new child granularity")
				}
			})
		}
	}
}

func TestValidatePlannedDeletions(t *testing.T) {
	converter := declaredTestConverter(t)
	for _, test := range []struct {
		name, before, live, desired, accepted string
		valid                                 bool
	}{
		{"reinserted owned leaf rejected", `{"containers":[{"name":"secret-member","image":"owned"}]}`, `{"containers":[{"name":"secret-member","image":"owned","resources":{}}]}`, `{"containers":[]}`, `{"containers":[{"name":"secret-member","image":"owned","resources":{}}]}`, false},
		{"external residual may change independently", `{"containers":[{"name":"broker","image":"owned"}]}`, `{"containers":[{"name":"broker","image":"owned","resources":{}}]}`, `{"containers":[]}`, `{"containers":[{"name":"broker","resources":{"new":"external"}},{"name":"sidecar"}]}`, true},
		{"new member does not resurrect deleted member", `{"containers":[{"name":"owned"}]}`, `{"containers":[{"name":"owned"}]}`, `{}`, `{"containers":[{"name":"new-external"}]}`, true},
		{"same identity carrying new external payload allowed", `{"containers":[{"name":"owned"}]}`, `{"containers":[{"name":"owned"}]}`, `{}`, `{"containers":[{"name":"owned","external":"new"}]}`, true},
		{"bare deleted identity reinserted rejected", `{"containers":[{"name":"owned"}]}`, `{"containers":[{"name":"owned"}]}`, `{}`, `{"containers":[{"name":"owned"}]}`, false},
		{"map external addition not whole object comparison", `{"options":{"owned":"yes"}}`, `{"options":{"owned":"yes"}}`, `{}`, `{"options":{"new":"external"},"other":"anything"}`, true},
		{"map own key reinserted rejected", `{"options":{"owned":"yes"}}`, `{"options":{"owned":"yes"}}`, `{}`, `{"options":{"owned":"yes","new":"external"}}`, false},
		{"set external addition allowed", `{"tags":["owned"]}`, `{"tags":["owned"]}`, `{}`, `{"tags":["new-external"]}`, true},
		{"set old membership reinserted rejected", `{"tags":["owned"]}`, `{"tags":["owned"]}`, `{}`, `{"tags":["owned"]}`, false},
		{"opaque false does not mean absent", `{"opaqueFalse":false}`, `{"opaqueFalse":false}`, `{}`, `{"opaqueFalse":false}`, false},
		{"opaque null does not mean absent", `{"nullable":null}`, `{"nullable":null}`, `{}`, `{"nullable":null}`, false},
		{"opaque empty map does not mean absent", `{"options":{}}`, `{"options":{}}`, `{}`, `{"options":{}}`, false},
		{"real schema default may remain", `{"defaultFalse":false}`, `{"defaultFalse":false}`, `{}`, `{"defaultFalse":false}`, true},
		{"nondefault value still rejected", `{"defaultFalse":false}`, `{"defaultFalse":false}`, `{}`, `{"defaultFalse":true}`, false},
		{"Quantity normalization does not excuse reinserted field", `{"resources":{"cpu":"1.0"}}`, `{"resources":{"cpu":"1"}}`, `{}`, `{"resources":{"cpu":"1"}}`, false},
		{"duplicate accepted identities fail closed", `{"containers":[{"name":"owned"}]}`, `{"containers":[{"name":"owned"}]}`, `{}`, `{"containers":[{"name":"external"},{"name":"external"}]}`, false},
		{"malformed accepted membership fails closed", `{"containers":[{"name":"owned"}]}`, `{"containers":[{"name":"owned"}]}`, `{}`, `{"containers":[{"image":"unnamed"}]}`, false},
		{"null list means old members are absent", `{"containers":[{"name":"owned"}]}`, `{"containers":[{"name":"owned"}]}`, `{}`, `{"containers":null}`, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			before, live, desired := intentTestObject(t, test.before), intentTestObject(t, test.live), intentTestObject(t, test.desired)
			plan, err := PlanDeclaredResourceIntent(converter, before, live, desired, desired, nil)
			if err != nil || plan.DeletionConflictError() != nil {
				t.Fatalf("invalid fixture plan: %v %v", err, plan)
			}
			err = ValidateResourceIntentDeletions(converter, plan, intentTestObject(t, test.accepted))
			if (err == nil) != test.valid {
				t.Fatalf("valid=%v error=%v checks=%v", test.valid, err, plan.DeletionChecks)
			}
			if err != nil && strings.Contains(err.Error(), "secret-member") {
				t.Fatal("deletion diagnostic exposed an identity value")
			}
		})
	}
}

func TestValidateNativePlannedDeletionNormalization(t *testing.T) {
	converter := declaredNativePodConverter(t)
	for _, test := range []struct {
		name, before, desired, accepted string
		valid                           bool
	}{
		{"native false can remain as omitempty zero", `{"containers":[{"name":"broker","volumeMounts":[{"name":"log","mountPath":"/logs","readOnly":false}]}]}`, `{"containers":[{"name":"broker","volumeMounts":[{"name":"log","mountPath":"/logs"}]}]}`, `{"containers":[{"name":"broker","volumeMounts":[{"name":"log","mountPath":"/logs","readOnly":false}]}]}`, true},
		{"native true cannot replace deleted false", `{"containers":[{"name":"broker","volumeMounts":[{"name":"log","mountPath":"/logs","readOnly":false}]}]}`, `{"containers":[{"name":"broker","volumeMounts":[{"name":"log","mountPath":"/logs"}]}]}`, `{"containers":[{"name":"broker","volumeMounts":[{"name":"log","mountPath":"/logs","readOnly":true}]}]}`, false},
		{"native resources empty struct may be reserialized", `{"containers":[{"name":"broker","resources":{}}]}`, `{"containers":[{"name":"broker"}]}`, `{"containers":[{"name":"broker","resources":{}}]}`, true},
		{"client unknown empty map is not native default", `{"containers":[{"name":"broker","futureEmpty":{}}]}`, `{"containers":[{"name":"broker"}]}`, `{"containers":[{"name":"broker","futureEmpty":{}}]}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			before, desired := declaredPodObject(t, test.before), declaredPodObject(t, test.desired)
			plan, err := PlanDeclaredResourceIntent(converter, before, before, desired, desired, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(plan.DeletionChecks) == 0 {
				t.Fatal("fixture must test an actual deletion check")
			}
			err = ValidateResourceIntentDeletions(converter, plan, declaredPodObject(t, test.accepted))
			if (err == nil) != test.valid {
				t.Fatalf("valid=%v error=%v", test.valid, err)
			}
		})
	}
}

func TestLegacyDeletionRespectsProtectedMapPaths(t *testing.T) {
	converter := declaredTestConverter(t)
	before := intentTestObject(t, `{"options":{"owned":"one"}}`)
	live := intentTestObject(t, `{"options":{"owned":"one","external":"keep"}}`)
	desired := intentTestObject(t, `{}`)
	want := intentTestObject(t, `{"options":{"external":"keep"}}`)
	for _, test := range []struct {
		name      string
		protected fieldpath.Path
		blocked   bool
	}{
		{"map itself", fieldpath.MakePathOrDie("spec", "options"), true},
		{"ancestor", fieldpath.MakePathOrDie("spec"), true},
		{"sibling", fieldpath.MakePathOrDie("spec", "anotherMap"), false},
		{"external child", fieldpath.MakePathOrDie("spec", "options", "external"), false},
		{"historical child", fieldpath.MakePathOrDie("spec", "options", "owned"), true},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan, err := PlanLegacyResourceIntentDeletions(converter, before, live, desired, fieldpath.NewSet(test.protected))
			if err != nil {
				t.Fatal(err)
			}
			conflict := plan.DeletionConflictError()
			if test.blocked {
				if conflict == nil || !strings.Contains(conflict.Error(), ".spec.options") {
					t.Fatalf("expected deletion under .spec.options to be blocked with %s protected, got %v", test.protected, conflict)
				}
				return
			}
			if conflict != nil {
				t.Fatal(conflict)
			}
			if !reflect.DeepEqual(plan.Object.Object, want.Object) {
				t.Fatalf("got %s want %s", intentTestJSON(plan.Object.Object), intentTestJSON(want.Object))
			}
		})
	}
}

func TestPlanLegacyDeletionBoundaries(t *testing.T) {
	converter := declaredTestConverter(t)
	for _, test := range []struct {
		name, before, live, desired, want string
		blocked                           bool
	}{
		{"atomic modification grants no name deletion", `{"containers":[{"name":"old"},{"name":"keep","image":"one"}]}`, `{"containers":[{"name":"old"},{"name":"keep","image":"one"},{"name":"external"}]}`, `{"containers":[{"name":"keep","image":"two"}]}`, `{"containers":[{"name":"old"},{"name":"keep","image":"one"},{"name":"external"}]}`, false},
		{"atomic omission with external member blocks", `{"containers":[{"name":"old"}]}`, `{"containers":[{"name":"old"},{"name":"external"}]}`, `{}`, ``, true},
		{"granular map prunes owned key only", `{"options":{"owned":"one"}}`, `{"options":{"owned":"one","external":"keep"}}`, `{}`, `{"options":{"external":"keep"}}`, false},
		{"changed owned scalar blocks", `{"options":{"owned":"one"}}`, `{"options":{"owned":"changed","external":"keep"}}`, `{}`, ``, true},
		{"legacy associative whole member stays conservative", `{"entries":[{"id":"A","value":"one"}]}`, `{"entries":[{"id":"A","value":"one","note":"external"}]}`, `{"entries":[]}`, ``, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan, err := PlanLegacyResourceIntentDeletions(converter, intentTestObject(t, test.before), intentTestObject(t, test.live), intentTestObject(t, test.desired), nil)
			if err != nil {
				t.Fatal(err)
			}
			if (plan.DeletionConflictError() != nil) != test.blocked {
				t.Fatalf("unexpected conflict: %v", plan.DeletionConflictError())
			}
			if !test.blocked {
				want := intentTestObject(t, test.want)
				if !reflect.DeepEqual(plan.Object.Object, want.Object) {
					t.Fatalf("got %s want %s", intentTestJSON(plan.Object.Object), intentTestJSON(want.Object))
				}
				if err := ValidateResourceIntentDeletions(converter, plan, plan.Object); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
