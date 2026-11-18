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
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/managedfields"
	"k8s.io/kube-openapi/pkg/validation/spec"
	"sigs.k8s.io/structured-merge-diff/v6/fieldpath"
)

func TestMergeDeclaredResourceIntent(t *testing.T) {
	converter := declaredTestConverter(t)
	tests := []struct {
		name, before, live, raw, effective, want string
		protected                                *fieldpath.Set
		retained                                 bool
		blocked                                  bool
	}{
		{name: "unchanged declared scalar corrects drift", before: `{"mode":"wanted"}`, live: `{"mode":"drift","external":"preserve"}`, raw: `{"mode":"wanted"}`, want: `{"mode":"wanted","external":"preserve"}`},
		{name: "missing unchanged scalar is restored", before: `{"mode":"wanted"}`, live: `{}`, raw: `{"mode":"wanted"}`, want: `{"mode":"wanted"}`},
		{name: "no history preserves unspecified values", live: `{"replicas":7,"mode":"old"}`, raw: `{"mode":"new"}`, want: `{"replicas":7,"mode":"new"}`},
		{name: "safe historical scalar deletion", before: `{"auth":"yes","keep":"yes"}`, live: `{"auth":"yes","keep":"yes","external":"yes"}`, raw: `{"keep":"yes"}`, want: `{"keep":"yes","external":"yes"}`},
		{name: "changed historical deletion blocks write", before: `{"auth":"yes"}`, live: `{"auth":"external"}`, raw: `{}`, blocked: true},
		{name: "null live containers accepts declarations", before: `{}`, live: `{"containers":null}`, raw: `{"containers":[{"name":"broker","volumeMounts":[{"name":"log","mountPath":"/root/logs"}]}],"volumes":[{"name":"log","emptyDir":{}}]}`, want: `{"containers":[{"name":"broker","volumeMounts":[{"name":"log","mountPath":"/root/logs"}]}],"volumes":[{"name":"log","emptyDir":{}}]}`},
		{name: "named nested mounts preserve sidecars and external children", before: `{"containers":[{"name":"broker","volumeMounts":[{"name":"log","mountPath":"/old"}]}]}`, live: `{"containers":[{"name":"broker","image":"operator","env":[{"name":"EXTERNAL","value":"yes"}],"volumeMounts":[{"name":"log","mountPath":"/drift"},{"name":"data","mountPath":"/data"}]},{"name":"sidecar","image":"external"}]}`, raw: `{"containers":[{"name":"broker","volumeMounts":[{"name":"log","mountPath":"/root/logs"}]}]}`, want: `{"containers":[{"name":"broker","image":"operator","env":[{"name":"EXTERNAL","value":"yes"}],"volumeMounts":[{"name":"log","mountPath":"/root/logs"},{"name":"data","mountPath":"/data"}]},{"name":"sidecar","image":"external"}]}`},
		{name: "named safe deletion retains external member", before: `{"containers":[{"name":"owned","image":"one"}]}`, live: `{"containers":[{"name":"owned","image":"one"},{"name":"external","image":"two"}]}`, raw: `{"containers":[]}`, want: `{"containers":[{"name":"external","image":"two"}]}`},
		{name: "named removed member retains only external children", before: `{"containers":[{"name":"owned","image":"one"}]}`, live: `{"containers":[{"name":"owned","image":"one","env":[{"name":"external","value":"two"}]}]}`, raw: `{"containers":[]}`, want: `{"containers":[{"name":"owned","env":[{"name":"external","value":"two"}]}]}`, retained: true},
		{name: "whole named list omission prunes owned members", before: `{"containers":[{"name":"owned","image":"one"}]}`, live: `{"containers":[{"name":"owned","image":"one"},{"name":"external","image":"two"}]}`, raw: `{}`, want: `{"containers":[{"name":"external","image":"two"}]}`, retained: true},
		{name: "whole named list omission removes final member", before: `{"containers":[{"name":"owned","image":"one"}]}`, live: `{"containers":[{"name":"owned","image":"one"}]}`, raw: `{}`, want: `{}`},
		{name: "empty named declaration without history retains external members", live: `{"containers":[{"name":"external","image":"two"}]}`, raw: `{"containers":[]}`, want: `{"containers":[{"name":"external","image":"two"}]}`},
		{name: "explicit null remains authoritative", before: `{"containers":[{"name":"owned"}]}`, live: `{"containers":[{"name":"owned"},{"name":"external"}]}`, raw: `{"containers":null}`, want: `{"containers":null}`},
		{name: "empty opaque list accepted", live: `{"containers":null}`, raw: `{"containers":[]}`, want: `{"containers":[]}`},
		{name: "schema key wins over incidental name", before: `{"entries":[{"id":"A","name":"old","value":"old"}]}`, live: `{"entries":[{"id":"A","name":"external","value":"drift","note":"keep"},{"id":"B","name":"external","value":"keep"}]}`, raw: `{"entries":[{"id":"A","name":"new","value":"new"}]}`, want: `{"entries":[{"id":"A","name":"new","value":"new","note":"keep"},{"id":"B","name":"external","value":"keep"}]}`},
		{name: "compound keys include schema default", before: `{"ports":[{"port":80,"target":"old"}]}`, live: `{"ports":[{"port":80,"protocol":"TCP","target":"drift"},{"port":80,"protocol":"UDP","target":"external"}]}`, raw: `{"ports":[{"port":80,"target":"new"}]}`, want: `{"ports":[{"port":80,"protocol":"TCP","target":"new"},{"port":80,"protocol":"UDP","target":"external"}]}`},
		{name: "typed default atomic list is whole declaration", before: `{"atomic":[{"name":"owned","value":"one"}]}`, live: `{"atomic":[{"name":"owned","value":"drift"},{"name":"external","value":"keep"}]}`, raw: `{"atomic":[{"name":"owned","value":"two"}]}`, want: `{"atomic":[{"name":"owned","value":"two"}]}`},
		{name: "typed atomic list of untyped items remains atomic", live: `{"anyItems":[{"name":"owned","extra":"external"},{"name":"sidecar"}]}`, raw: `{"anyItems":[{"name":"owned"}]}`, want: `{"anyItems":[{"name":"owned"}]}`},
		{name: "atomic map fully replaced", before: `{"atomicMap":{"a":"old"}}`, live: `{"atomicMap":{"a":"drift","external":"keep"}}`, raw: `{"atomicMap":{"a":"new"}}`, want: `{"atomicMap":{"a":"new"}}`},
		{name: "explicit atomic boundary outranks unknown descendants", live: `{"atomicOpaque":{"containers":[{"name":"broker","external":"keep"},{"name":"sidecar"}]}}`, raw: `{"atomicOpaque":{"containers":[{"name":"broker"}]}}`, want: `{"atomicOpaque":{"containers":[{"name":"broker"}]}}`},
		{name: "atomic empty list clears full field", live: `{"atomic":[{"name":"external"}]}`, raw: `{"atomic":[]}`, want: `{"atomic":[]}`},
		{name: "schema set deletes owned and keeps external membership", before: `{"tags":["a","b"]}`, live: `{"tags":["a","b","external"]}`, raw: `{"tags":["b","c"]}`, want: `{"tags":["b","external","c"]}`},
		{name: "foreign apply atomic parent protects named deletion", before: `{"containers":[{"name":"old"}]}`, live: `{"containers":[{"name":"old"}]}`, raw: `{"containers":[{"name":"new"}]}`, want: `{"containers":[{"name":"old"},{"name":"new"}]}`, protected: fieldpath.NewSet(fieldpath.MakePathOrDie("spec", "containers")), blocked: true},
		{name: "protected descendant does not protect siblings", before: `{"data":{"protected":"one","delete":"two"}}`, live: `{"data":{"protected":"one","delete":"two"}}`, raw: `{}`, want: `{"data":{"protected":"one"}}`, protected: fieldpath.NewSet(fieldpath.MakePathOrDie("spec", "data", "protected")), blocked: true},
		{name: "captured defaults do not overwrite external change", before: `{"entries":[{"id":"A","value":"old","optional":false}]}`, live: `{"entries":[{"id":"A","value":"drift","optional":true}]}`, raw: `{"entries":[{"id":"A","value":"new"}]}`, effective: `{"entries":[{"id":"A","value":"new","optional":false}]}`, want: `{"entries":[{"id":"A","value":"new","optional":true}]}`},
		{name: "captured default deleted with whole unchanged member", before: `{"entries":[{"id":"A","value":"old","optional":false}]}`, live: `{"entries":[{"id":"A","value":"old","optional":false}]}`, raw: `{"entries":[]}`, want: `{"entries":[]}`},
		{name: "reordered named children do not block member deletion", before: `{"containers":[{"name":"broker","env":[{"name":"A","value":"one"},{"name":"B","value":"two"}]}]}`, live: `{"containers":[{"name":"broker","env":[{"name":"B","value":"two"},{"name":"A","value":"one"}]}]}`, raw: `{"containers":[]}`, want: `{"containers":[]}`},
		{name: "ordered unkeyed list replaced", before: `{"args":["one"]}`, live: `{"args":["drift","external"]}`, raw: `{"args":["two","one"]}`, want: `{"args":["two","one"]}`},
		{name: "unkeyed object list is not position merged", live: `{"rules":[{"left":"old","external":"keep"}]}`, raw: `{"rules":[{"right":"new"}]}`, want: `{"rules":[{"right":"new"}]}`},
		{name: "new map replaces incompatible live scalar", live: `{"options":"old"}`, raw: `{"options":{"setting":"new"}}`, want: `{"options":{"setting":"new"}}`},
		{name: "whole granular map omission keeps external addition", before: `{"options":{"managed":"old"}}`, live: `{"options":{"managed":"old","external":"keep"}}`, raw: `{}`, want: `{"options":{"external":"keep"}}`, retained: true},
		{name: "schema Quantity formatting does not cause writes", live: `{"resources":{"cpu":"1","memory":"1Gi"},"quantityString":"1"}`, raw: `{"resources":{"cpu":"1.0","memory":"1024Mi"},"quantityString":"1.0"}`, want: `{"resources":{"cpu":"1","memory":"1Gi"},"quantityString":"1.0"}`},
		{name: "schema default omission equals declared zero", live: `{}`, raw: `{"defaultFalse":false}`, want: `{}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var before *unstructured.Unstructured
			if test.before != "" {
				before = intentTestObject(t, test.before)
			}
			live, raw := intentTestObject(t, test.live), intentTestObject(t, test.raw)
			effective := raw.DeepCopy()
			if test.effective != "" {
				effective = intentTestObject(t, test.effective)
			}
			beforeCopy, liveCopy, rawCopy, effectiveCopy := before.DeepCopy(), live.DeepCopy(), raw.DeepCopy(), effective.DeepCopy()
			plan, err := PlanDeclaredResourceIntent(converter, before, live, effective, raw, test.protected)
			if err == nil {
				err = plan.DeletionConflictError()
			}
			if test.blocked {
				if err == nil {
					t.Fatal("blocked historical deletion must fail the entire merge")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			result, retained := plan.Object, plan.PreservedExternal
			want := intentTestObject(t, test.want)
			if !reflect.DeepEqual(result.Object, want.Object) {
				t.Fatalf("got %s\nwant %s", intentTestJSON(result.Object), intentTestJSON(want.Object))
			}
			if test.retained && len(retained) == 0 {
				t.Fatalf("retained=%v expected retention=%v", retained, test.retained)
			}
			if err := plan.Validate(converter, result); err != nil {
				t.Fatalf("merged result rejected by declaration projection: %v", err)
			}
			if !reflect.DeepEqual(before, beforeCopy) || !reflect.DeepEqual(live, liveCopy) || !reflect.DeepEqual(raw, rawCopy) || !reflect.DeepEqual(effective, effectiveCopy) {
				t.Fatal("merger mutated input")
			}
			second, _, err := MergeDeclaredResourceIntent(converter, effective, result, effective, raw, test.protected)
			if err != nil || !reflect.DeepEqual(second.Object, result.Object) {
				t.Fatalf("not idempotent: %v %s", err, intentTestJSON(second))
			}
		})
	}
}

func TestMergeDeclaredResourceIntentAmbiguousLists(t *testing.T) {
	for _, body := range []string{
		`{"containers":[{"name":"secret-name"},{"name":"secret-name"}]}`,
		`{"containers":[{"name":""}]}`,
		`{"containers":[{"name":42}]}`,
		`{"containers":[{"name":"secret-name"},{"image":"unnamed"}]}`,
		`{"containers":[{"name":"secret-name"},"scalar"]}`,
	} {
		t.Run(body, func(t *testing.T) {
			desired := intentTestObject(t, body)
			_, _, err := MergeDeclaredResourceIntent(declaredTestConverter(t), nil, intentTestObject(t, `{}`), desired, desired, nil)
			if err == nil || strings.Contains(err.Error(), "secret-name") {
				t.Fatalf("expected redacted ambiguity error: %v", err)
			}
		})
	}
}

func TestDeclaredResourceIntentProjection(t *testing.T) {
	converter := declaredTestConverter(t)
	for _, test := range []struct {
		name, desired, actual string
		valid                 bool
	}{
		{"extra named items accepted", `{"containers":[{"name":"broker","image":"one"}]}`, `{"containers":[{"name":"sidecar"},{"name":"broker","image":"one","extra":"yes"}]}`, true},
		{"unchanged declaration drift detected", `{"containers":[{"name":"broker","image":"secret-value"}]}`, `{"containers":null}`, false},
		{"quantity canonical accepted", `{"resources":{"cpu":"1.0","memory":"1024Mi"}}`, `{"resources":{"cpu":"1","memory":"1Gi"}}`, true},
		{"ordinary strings remain exact", `{"quantityString":"1.0"}`, `{"quantityString":"1"}`, false},
		{"opaque false omission is not guessed", `{"opaqueFalse":false}`, `{}`, false},
		{"opaque empty string omission is not guessed", `{"opaqueEmpty":""}`, `{}`, false},
		{"real schema default accepts omitted field", `{"defaultFalse":false}`, `{}`, true},
		{"explicit null is checked", `{"containers":null}`, `{}`, false},
		{"atomic external additions are not converged", `{"atomic":[{"name":"owned"}]}`, `{"atomic":[{"name":"owned"},{"name":"external"}]}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateDeclaredResourceIntent(converter, intentTestObject(t, test.desired), intentTestObject(t, test.actual))
			if (err == nil) != test.valid || err != nil && strings.Contains(err.Error(), "secret-value") {
				t.Fatalf("valid=%v error=%v", test.valid, err)
			}
		})
	}
}

func TestLegacyIntentDeletionMigrationDoesNotInventNamedOwnership(t *testing.T) {
	converter := declaredTestConverter(t)
	before := intentTestObject(t, `{"containers":[{"name":"old","image":"one"},{"name":"keep","image":"old"}],"remove":"old"}`)
	live := intentTestObject(t, `{"containers":[{"name":"old","image":"one"},{"name":"keep","image":"drift"},{"name":"external"}],"remove":"old"}`)
	desired := intentTestObject(t, `{"containers":[{"name":"keep","image":"new"}]}`)
	legacy, err := PlanLegacyResourceIntentDeletions(converter, before, live, desired, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := legacy.DeletionConflictError(); err != nil {
		t.Fatalf("legacy phase unexpectedly blocked explicit write: %v", err)
	}
	got, _, err := MergeDeclaredResourceIntent(converter, nil, legacy.Object, desired, desired, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := intentTestObject(t, `{"containers":[{"name":"old","image":"one"},{"name":"keep","image":"new"},{"name":"external"}]}`)
	if !reflect.DeepEqual(got.Object, want.Object) {
		t.Fatalf("migration expanded legacy deletion rights: got %s want %s", intentTestJSON(got.Object), intentTestJSON(want.Object))
	}
	if err := ValidateResourceIntentDeletions(converter, legacy, got); err != nil {
		t.Fatal(err)
	}
}

func TestDeclaredMergeProtectsSystemFields(t *testing.T) {
	live := intentTestObject(t, `{"mode":"old"}`)
	live.SetUID("resource-uid")
	live.SetResourceVersion("17")
	live.Object["status"] = map[string]any{"phase": "ExternalReady"}
	desired := intentTestObject(t, `{"mode":"new"}`)
	desired.Object["status"] = map[string]any{"phase": "NotOurs"}
	result, _, err := MergeDeclaredResourceIntent(declaredTestConverter(t), nil, live, desired, desired, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.GetUID() != live.GetUID() || result.GetResourceVersion() != live.GetResourceVersion() || !reflect.DeepEqual(result.Object["status"], live.Object["status"]) {
		t.Fatal("system fields were overwritten")
	}
}

func declaredTestConverter(t testing.TB) managedfields.TypeConverter {
	t.Helper()
	var root, quantity spec.Schema
	if err := json.Unmarshal([]byte(`{
  "type":"object","x-kubernetes-group-version-kind":[{"group":"test.invalid","version":"v1","kind":"Thing"}],
  "properties":{
    "apiVersion":{"type":"string"},"kind":{"type":"string"},
    "metadata":{"type":"object","properties":{"name":{"type":"string"},"namespace":{"type":"string"},"labels":{"type":"object","additionalProperties":{"type":"string"}},"annotations":{"type":"object","additionalProperties":{"type":"string"}}}},
    "spec":{"type":"object","x-kubernetes-preserve-unknown-fields":true,"properties":{
      "entries":{"type":"array","x-kubernetes-list-type":"map","x-kubernetes-list-map-keys":["id"],"items":{"type":"object","properties":{"id":{"type":"string"},"name":{"type":"string"},"value":{"type":"string"},"note":{"type":"string"},"optional":{"type":"boolean"}}}},
      "ports":{"type":"array","x-kubernetes-list-type":"map","x-kubernetes-list-map-keys":["port","protocol"],"items":{"type":"object","properties":{"port":{"type":"integer"},"protocol":{"type":"string","default":"TCP"},"target":{"type":"string"}}}},
      "tags":{"type":"array","x-kubernetes-list-type":"set","items":{"type":"string"}},
      "atomic":{"type":"array","items":{"type":"object","properties":{"name":{"type":"string"},"value":{"type":"string"}}}},
      "anyItems":{"type":"array","items":{}},
      "atomicMap":{"type":"object","x-kubernetes-map-type":"atomic","additionalProperties":{"type":"string"}},
      "atomicOpaque":{"type":"object","x-kubernetes-map-type":"atomic","x-kubernetes-preserve-unknown-fields":true},
      "resources":{"type":"object","additionalProperties":{"$ref":"#/definitions/io.k8s.apimachinery.pkg.api.resource.Quantity"}},
      "quantityString":{"type":"string"},"defaultFalse":{"type":"boolean","default":false}
    }}
  }
}`), &root); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(`{"type":"string"}`), &quantity); err != nil {
		t.Fatal(err)
	}
	converter, err := managedfields.NewTypeConverter(map[string]*spec.Schema{"test.Thing": &root, "io.k8s.apimachinery.pkg.api.resource.Quantity": &quantity}, false)
	if err != nil {
		t.Fatal(err)
	}
	return converter
}
