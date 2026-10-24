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

package k8s

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/managedfields"
	"k8s.io/kube-openapi/pkg/validation/spec"
	kjson "sigs.k8s.io/json"
	"sigs.k8s.io/structured-merge-diff/v6/fieldpath"
	"sigs.k8s.io/structured-merge-diff/v6/value"
)

func TestMergeResourceIntent(t *testing.T) {
	t.Parallel()
	converter := intentTestConverter(t)
	tests := []struct {
		name      string
		before    string
		live      string
		desired   string
		want      string
		protected *fieldpath.Set
		preserved bool
	}{
		{
			name:    "bootstrap overlays without guessing omitted fields",
			live:    `{"data":{"old":"legacy","keep":"old","external":"x"}}`,
			desired: `{"data":{"keep":"new"}}`, want: `{"data":{"old":"legacy","keep":"new","external":"x"}}`,
		},
		{
			name:   "map key removed while external addition retained",
			before: `{"data":{"auth":"yes","keep":"yes"}}`, live: `{"data":{"auth":"yes","keep":"yes","external":"x"}}`,
			desired: `{"data":{"keep":"yes"}}`, want: `{"data":{"keep":"yes","external":"x"}}`,
		},
		{
			name:   "whole map omission prunes only unchanged historical keys",
			before: `{"data":{"auth":"yes"}}`, live: `{"data":{"auth":"yes","external":"x"}}`,
			desired: `{}`, want: `{"data":{"external":"x"}}`, preserved: true,
		},
		{
			name:   "omitted externally changed key is preserved",
			before: `{"data":{"auth":"yes","keep":"yes"}}`, live: `{"data":{"auth":"external","keep":"yes"}}`,
			desired: `{"data":{"keep":"yes"}}`, want: `{"data":{"auth":"external","keep":"yes"}}`, preserved: true,
		},
		{
			name:   "unchanged desired value does not override external edits",
			before: `{"data":{"keep":"before","change":"before"}}`, live: `{"data":{"keep":"external","change":"external"}}`,
			desired: `{"data":{"keep":"before","change":"after"}}`, want: `{"data":{"keep":"external","change":"after"}}`,
		},
		{
			name:   "externally removed unchanged value is not recreated",
			before: `{"data":{"gone":"before","change":"before"}}`, live: `{"data":{"change":"before"}}`,
			desired: `{"data":{"gone":"before","change":"after"}}`, want: `{"data":{"change":"after"}}`,
		},
		{
			name:    "list map uses schema key instead of name",
			before:  `{"env":[{"id":"AUTH","value":"yes"},{"id":"KEEP","value":"yes"}]}`,
			live:    `{"env":[{"id":"AUTH","value":"yes"},{"id":"KEEP","value":"yes"},{"id":"EXTERNAL","value":"x"}]}`,
			desired: `{"env":[{"id":"KEEP","value":"new"}]}`, want: `{"env":[{"id":"KEEP","value":"new"},{"id":"EXTERNAL","value":"x"}]}`,
		},
		{
			name:    "removed list item with externally changed leaf is kept whole",
			before:  `{"env":[{"id":"AUTH","value":"yes"},{"id":"KEEP","value":"yes"}]}`,
			live:    `{"env":[{"id":"AUTH","value":"external"},{"id":"KEEP","value":"yes"}]}`,
			desired: `{"env":[{"id":"KEEP","value":"yes"}]}`, want: `{"env":[{"id":"AUTH","value":"external"},{"id":"KEEP","value":"yes"}]}`, preserved: true,
		},
		{
			name:    "removed list item with externally added child is kept whole",
			before:  `{"env":[{"id":"AUTH","value":"yes"},{"id":"KEEP","value":"yes"}]}`,
			live:    `{"env":[{"id":"AUTH","value":"yes","extra":"external"},{"id":"KEEP","value":"yes"}]}`,
			desired: `{"env":[{"id":"KEEP","value":"yes"}]}`, want: `{"env":[{"id":"AUTH","value":"yes","extra":"external"},{"id":"KEEP","value":"yes"}]}`, preserved: true,
		},
		{
			name:    "removed list item with externally removed child is kept whole",
			before:  `{"env":[{"id":"AUTH","value":"yes"},{"id":"KEEP","value":"yes"}]}`,
			live:    `{"env":[{"id":"AUTH"},{"id":"KEEP","value":"yes"}]}`,
			desired: `{"env":[{"id":"KEEP","value":"yes"}]}`, want: `{"env":[{"id":"AUTH"},{"id":"KEEP","value":"yes"}]}`, preserved: true,
		},
		{
			name:    "list omission removes managed items and preserves external item",
			before:  `{"env":[{"id":"AUTH","value":"yes"}]}`,
			live:    `{"env":[{"id":"AUTH","value":"yes"},{"id":"EXTERNAL","value":"x"}]}`,
			desired: `{}`, want: `{"env":[{"id":"EXTERNAL","value":"x"}]}`, preserved: true,
		},
		{
			name:    "multi key list identifies exact member",
			before:  `{"ports":[{"port":80,"protocol":"TCP","target":"old"},{"port":80,"protocol":"UDP","target":"old"}]}`,
			live:    `{"ports":[{"port":80,"protocol":"TCP","target":"old"},{"port":80,"protocol":"UDP","target":"external"}]}`,
			desired: `{"ports":[{"port":80,"protocol":"UDP","target":"old"}]}`, want: `{"ports":[{"port":80,"protocol":"UDP","target":"external"}]}`,
		},
		{
			name:   "set removes managed membership but keeps added value",
			before: `{"tags":["a","b"]}`, live: `{"tags":["a","b","external"]}`,
			desired: `{"tags":["b"]}`, want: `{"tags":["b","external"]}`,
		},
		{
			name:   "atomic desired change replaces unchanged whole value",
			before: `{"atomicMap":{"a":"one"}}`, live: `{"atomicMap":{"a":"one"}}`,
			desired: `{"atomicMap":{"a":"two"}}`, want: `{"atomicMap":{"a":"two"}}`,
		},
		{
			name:   "atomic map double changes preserve live",
			before: `{"atomicMap":{"a":"one"}}`, live: `{"atomicMap":{"a":"external","b":"external"}}`,
			desired: `{"atomicMap":{"a":"two"}}`, want: `{"atomicMap":{"a":"external","b":"external"}}`, preserved: true,
		},
		{
			name:   "atomic list double changes preserve external elements",
			before: `{"atomicList":["a"]}`, live: `{"atomicList":["a","external"]}`,
			desired: `{"atomicList":[]}`, want: `{"atomicList":["a","external"]}`, preserved: true,
		},
		{
			name:   "atomic already converged has no conflict",
			before: `{"atomicList":["a"]}`, live: `{"atomicList":["b"]}`,
			desired: `{"atomicList":["b"]}`, want: `{"atomicList":["b"]}`,
		},
		{
			name:   "atomic omission preserves externally changed value",
			before: `{"atomicMap":{"a":"one"}}`, live: `{"atomicMap":{"a":"one","b":"external"}}`,
			desired: `{}`, want: `{"atomicMap":{"a":"one","b":"external"}}`, preserved: true,
		},
		{
			name:   "atomic list explicitly empty is distinct from omission",
			before: `{"atomicList":["a"]}`, live: `{"atomicList":["a"]}`,
			desired: `{"atomicList":[]}`, want: `{"atomicList":[]}`,
		},
		{
			name:    "valueFrom removed before value added in same result",
			before:  `{"env":[{"id":"AUTH","valueFrom":{"secret":"secret"}}]}`,
			live:    `{"env":[{"id":"AUTH","valueFrom":{"secret":"secret"}}]}`,
			desired: `{"env":[{"id":"AUTH","value":"literal"}]}`, want: `{"env":[{"id":"AUTH","value":"literal"}]}`,
		},
		{
			name:   "new granular subtree added to existing spec",
			before: `{}`, live: `{}`,
			desired: `{"data":{"a":"one","b":"two"}}`, want: `{"data":{"a":"one","b":"two"}}`,
		},
		{
			name:   "new list subtree added to existing spec",
			before: `{}`, live: `{}`,
			desired: `{"env":[{"id":"AUTH","value":"yes"}]}`, want: `{"env":[{"id":"AUTH","value":"yes"}]}`,
		},
		{
			name:   "new nested valueFrom added",
			before: `{"env":[{"id":"AUTH","value":"yes"}]}`, live: `{"env":[{"id":"AUTH","value":"yes"}]}`,
			desired: `{"env":[{"id":"AUTH","valueFrom":{"secret":"sec"}}]}`, want: `{"env":[{"id":"AUTH","valueFrom":{"secret":"sec"}}]}`,
		},
		{
			name:   "empty granular map clears managed keys",
			before: `{"data":{"auth":"yes"}}`, live: `{"data":{"auth":"yes"}}`,
			desired: `{"data":{}}`, want: `{"data":{}}`,
		},
		{
			name:   "empty list map clears managed members",
			before: `{"env":[{"id":"AUTH","value":"yes"}]}`, live: `{"env":[{"id":"AUTH","value":"yes"}]}`,
			desired: `{"env":[]}`, want: `{"env":[]}`,
		},
		{
			name:   "empty list map retains external members",
			before: `{"env":[{"id":"AUTH","value":"yes"}]}`, live: `{"env":[{"id":"AUTH","value":"yes"},{"id":"EXTERNAL","value":"x"}]}`,
			desired: `{"env":[]}`, want: `{"env":[{"id":"EXTERNAL","value":"x"}]}`,
		},
		{
			name:   "empty set clears managed members",
			before: `{"tags":["a"]}`, live: `{"tags":["a"]}`,
			desired: `{"tags":[]}`, want: `{"tags":[]}`,
		},
		{
			name:   "whole unchanged list omitted",
			before: `{"env":[{"id":"AUTH","value":"yes"}]}`, live: `{"env":[{"id":"AUTH","value":"yes"}]}`,
			desired: `{}`, want: `{}`,
		},
		{
			name:   "same value protected key is retained on omission",
			before: `{"data":{"auth":"yes","keep":"yes"}}`, live: `{"data":{"auth":"yes","keep":"yes"}}`,
			desired: `{"data":{"keep":"yes"}}`, want: `{"data":{"auth":"yes","keep":"yes"}}`, preserved: true,
			protected: fieldpath.NewSet(fieldpath.MakePathOrDie("spec", "data", "auth")),
		},
		{
			name:   "protected list child retains entire omitted member",
			before: `{"env":[{"id":"AUTH","value":"yes"}]}`, live: `{"env":[{"id":"AUTH","value":"yes"}]}`,
			desired: `{}`, want: `{"env":[{"id":"AUTH","value":"yes"}]}`, preserved: true,
			protected: fieldpath.NewSet(fieldpath.MakePathOrDie("spec", "env", &value.FieldList{{Name: "id", Value: value.NewValueInterface("AUTH")}}, "value")),
		},
		{
			name:   "already removed field is not conflict",
			before: `{"data":{"auth":"yes","keep":"yes"}}`, live: `{"data":{"keep":"yes"}}`,
			desired: `{"data":{"keep":"yes"}}`, want: `{"data":{"keep":"yes"}}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var b *unstructured.Unstructured
			if test.before != "" {
				b = intentTestObject(t, test.before)
			}
			l, d := intentTestObject(t, test.live), intentTestObject(t, test.desired)
			bCopy, lCopy, dCopy := b.DeepCopy(), l.DeepCopy(), d.DeepCopy()
			got, retained, err := MergeResourceIntent(converter, b, l, d, test.protected)
			if err != nil {
				t.Fatal(err)
			}
			want := intentTestObject(t, test.want)
			if !reflect.DeepEqual(got.Object, want.Object) {
				t.Fatalf("merged object:\n got %s\nwant %s", intentTestJSON(got.Object), intentTestJSON(want.Object))
			}
			if (len(retained) > 0) != test.preserved {
				t.Fatalf("preserved paths = %v, want conflict %v", retained, test.preserved)
			}
			if !reflect.DeepEqual(b, bCopy) || !reflect.DeepEqual(l, lCopy) || !reflect.DeepEqual(d, dCopy) {
				t.Fatal("merge mutated an input")
			}
		})
	}
}

func TestMergeResourceIntentPreservesSystemFields(t *testing.T) {
	t.Parallel()
	b := intentTestObject(t, `{"data":{"remove":"yes"}}`)
	b.SetLabels(map[string]string{"remove": "yes", "keep": "old"})
	b.SetAnnotations(map[string]string{"remove": "yes"})
	l := b.DeepCopy()
	l.SetResourceVersion("42")
	l.SetUID("live-uid")
	l.SetFinalizers([]string{"external/finalizer"})
	l.Object["status"] = map[string]interface{}{"ready": true}
	d := intentTestObject(t, `{}`)
	d.SetLabels(map[string]string{"keep": "new"})
	d.SetResourceVersion("wrong")
	d.SetUID("wrong")
	d.SetFinalizers([]string{"wrong"})
	d.Object["status"] = map[string]interface{}{"ready": false}
	got, _, err := MergeResourceIntent(intentTestConverter(t), b, l, d, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.GetResourceVersion() != "42" || got.GetUID() != "live-uid" || !reflect.DeepEqual(got.GetFinalizers(), l.GetFinalizers()) || !reflect.DeepEqual(got.Object["status"], l.Object["status"]) {
		t.Fatalf("system fields changed: %s", intentTestJSON(got.Object))
	}
	if !reflect.DeepEqual(got.GetLabels(), map[string]string{"keep": "new"}) || len(got.GetAnnotations()) != 0 {
		t.Fatalf("labels/annotations not merged: %s", intentTestJSON(got.Object))
	}
}

func TestMergeResourceIntentChangedThenDeleted(t *testing.T) {
	t.Parallel()
	converter := intentTestConverter(t)
	b := intentTestObject(t, `{"env":[{"id":"AUTH","value":"one"},{"id":"KEEP","value":"yes"}]}`)
	l := intentTestObject(t, `{"env":[{"id":"AUTH","value":"one"},{"id":"KEEP","value":"yes"},{"id":"EXTERNAL","value":"x"}]}`)
	d := intentTestObject(t, `{"env":[{"id":"AUTH","value":"two"},{"id":"KEEP","value":"yes"}]}`)
	updated, conflicts, err := MergeResourceIntent(converter, b, l, d, nil)
	if err != nil || len(conflicts) != 0 {
		t.Fatalf("update failed: %v, %v", err, conflicts)
	}
	next := intentTestObject(t, `{"env":[{"id":"KEEP","value":"yes"}]}`)
	deleted, conflicts, err := MergeResourceIntent(converter, d, updated, next, nil)
	if err != nil || len(conflicts) != 0 {
		t.Fatalf("delete failed: %v, %v", err, conflicts)
	}
	want := intentTestObject(t, `{"env":[{"id":"KEEP","value":"yes"},{"id":"EXTERNAL","value":"x"}]}`)
	if !reflect.DeepEqual(deleted.Object, want.Object) {
		t.Fatalf("got %s, want %s", intentTestJSON(deleted.Object), intentTestJSON(want.Object))
	}
	repeated, conflicts, err := MergeResourceIntent(converter, next, deleted, next, nil)
	if err != nil || len(conflicts) != 0 || !reflect.DeepEqual(repeated.Object, deleted.Object) {
		t.Fatalf("repeat was not idempotent: %v, %v, %s", err, conflicts, intentTestJSON(repeated))
	}
}

func TestMergeResourceIntentRedactsCollectionValues(t *testing.T) {
	t.Parallel()
	secret := "do-not-log-this-token"
	b := intentTestObject(t, `{"tags":["do-not-log-this-token","keep"]}`)
	d := intentTestObject(t, `{"tags":["keep"]}`)
	protected := fieldpath.NewSet(fieldpath.MakePathOrDie("spec", "tags", value.NewValueInterface(secret)))
	_, retained, err := MergeResourceIntent(intentTestConverter(t), b, b, d, protected)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(retained, []string{".spec.tags[set-item]"}) {
		t.Fatalf("retained path did not redact set member: %v", retained)
	}
	keyPath := fieldpath.MakePathOrDie("spec", "env", &value.FieldList{{Name: "id", Value: value.NewValueInterface(secret)}})
	if got := intentSafePath(keyPath); got != ".spec.env[id=<redacted>]" {
		t.Fatalf("key path not redacted: %s", got)
	}
	invalid := intentTestObject(t, `{"env":[{"id":"do-not-log-this-token"},{"id":"do-not-log-this-token"}]}`)
	_, _, err = MergeResourceIntent(intentTestConverter(t), b, b, invalid, nil)
	if err == nil || strings.Contains(err.Error(), secret) || !strings.Contains(err.Error(), "desired resource schema validation failed") {
		t.Fatalf("validation error was not safe: %v", err)
	}
}

func TestCarryResourceIntentDefaults(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, desired, want string }{
		{"same source parent remains", `{"env":[{"id":"AUTH","valueFrom":{"secret":"new"}}]}`, `{"env":[{"id":"AUTH","valueFrom":{"secret":"new","optional":false}}]}`},
		{"removed source parent is not resurrected", `{"env":[{"id":"AUTH","value":"literal"}]}`, `{"env":[{"id":"AUTH","value":"literal"}]}`},
		{"null source parent is not resurrected", `{"env":[{"id":"AUTH","valueFrom":null}]}`, `{"env":[{"id":"AUTH","valueFrom":null}]}`},
		{"empty source parent can receive defaults", `{"env":[{"id":"AUTH","valueFrom":{}}]}`, `{"env":[{"id":"AUTH","valueFrom":{"optional":false}}]}`},
		{"removed member is not resurrected", `{}`, `{}`},
		{"explicit desired default wins", `{"env":[{"id":"AUTH","valueFrom":{"secret":"new","optional":true}}]}`, `{"env":[{"id":"AUTH","valueFrom":{"secret":"new","optional":true}}]}`},
		{"different member does not inherit defaults", `{"env":[{"id":"OTHER","valueFrom":{"secret":"new"}}]}`, `{"env":[{"id":"OTHER","valueFrom":{"secret":"new"}}]}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			b := intentTestObject(t, `{"env":[{"id":"AUTH","valueFrom":{"secret":"old"}}]}`)
			e := intentTestObject(t, `{"env":[{"id":"AUTH","valueFrom":{"secret":"old","optional":false}}]}`)
			d := intentTestObject(t, test.desired)
			got, err := CarryResourceIntentDefaults(intentTestConverter(t), b, e, d)
			if err != nil {
				t.Fatal(err)
			}
			want := intentTestObject(t, test.want)
			if !reflect.DeepEqual(got.Object, want.Object) {
				t.Fatalf("carried defaults:\n got %s\nwant %s", intentTestJSON(got.Object), intentTestJSON(want.Object))
			}
		})
	}
}

func TestCaptureResourceIntentDefaults(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, desired, submitted, accepted, want string }{
		{
			name:      "capture only newly added default under existing item parent",
			desired:   `{"env":[{"id":"AUTH","valueFrom":{"secret":"s"}}]}`,
			submitted: `{"env":[{"id":"AUTH","valueFrom":{"secret":"s"}}]}`,
			accepted:  `{"env":[{"id":"AUTH","valueFrom":{"secret":"s","optional":false}}]}`,
			want:      `{"env":[{"id":"AUTH","valueFrom":{"secret":"s","optional":false}}]}`,
		},
		{
			name:      "previous live addition is not captured",
			desired:   `{"env":[{"id":"AUTH","valueFrom":{"secret":"s"}}]}`,
			submitted: `{"env":[{"id":"AUTH","extra":"external","valueFrom":{"secret":"s"}}]}`,
			accepted:  `{"env":[{"id":"AUTH","extra":"external","valueFrom":{"secret":"s","optional":false}}]}`,
			want:      `{"env":[{"id":"AUTH","valueFrom":{"secret":"s","optional":false}}]}`,
		},
		{
			name:      "legacy existing default is not retroactively adopted",
			desired:   `{"env":[{"id":"AUTH","valueFrom":{"secret":"s"}}]}`,
			submitted: `{"env":[{"id":"AUTH","valueFrom":{"secret":"s","optional":false}}]}`,
			accepted:  `{"env":[{"id":"AUTH","valueFrom":{"secret":"s","optional":false}}]}`,
			want:      `{"env":[{"id":"AUTH","valueFrom":{"secret":"s"}}]}`,
		},
		{
			name:      "modified declared value is not a default",
			desired:   `{"env":[{"id":"AUTH","value":"original"}]}`,
			submitted: `{"env":[{"id":"AUTH","value":"original"}]}`,
			accepted:  `{"env":[{"id":"AUTH","value":"mutated"}]}`,
			want:      `{"env":[{"id":"AUTH","value":"original"}]}`,
		},
		{
			name:      "new external list member is not captured",
			desired:   `{"env":[{"id":"AUTH","value":"original"}]}`,
			submitted: `{"env":[{"id":"AUTH","value":"original"}]}`,
			accepted:  `{"env":[{"id":"AUTH","value":"original"},{"id":"SIDECAR","value":"external"}]}`,
			want:      `{"env":[{"id":"AUTH","value":"original"}]}`,
		},
		{
			name:      "new nested external list member is not captured",
			desired:   `{"env":[{"id":"PARENT","children":[]}]}`,
			submitted: `{"env":[{"id":"PARENT","children":[]}]}`,
			accepted:  `{"env":[{"id":"PARENT","children":[{"id":"EXTERNAL","value":"injected"}]}]}`,
			want:      `{"env":[{"id":"PARENT","children":[]}]}`,
		},
		{
			name:    "schema defaulted compound key locates the intended parent",
			desired: `{"ports":[{"port":80}]}`, submitted: `{"ports":[{"port":80}]}`,
			accepted: `{"ports":[{"port":80,"protocol":"TCP","target":"default"}]}`, want: `{"ports":[{"port":80,"protocol":"TCP","target":"default"}]}`,
		},
		{
			name:    "added field outside list members is not captured",
			desired: `{"data":{"original":"yes"}}`, submitted: `{"data":{"original":"yes"}}`,
			accepted: `{"data":{"original":"yes","external":"x"}}`, want: `{"data":{"original":"yes"}}`,
		},
		{
			name:    "unmentioned immediate parent is not captured",
			desired: `{"env":[{"id":"AUTH"}]}`, submitted: `{"env":[{"id":"AUTH"}]}`,
			accepted: `{"env":[{"id":"AUTH","valueFrom":{"secret":"injected"}}]}`, want: `{"env":[{"id":"AUTH"}]}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			d, submitted, accepted := intentTestObject(t, test.desired), intentTestObject(t, test.submitted), intentTestObject(t, test.accepted)
			submitted.SetUID("same")
			submitted.SetResourceVersion("10")
			accepted.SetUID("same")
			accepted.SetResourceVersion("11")
			got, err := CaptureResourceIntentDefaults(intentTestConverter(t), d, d, submitted, accepted)
			if err != nil {
				t.Fatal(err)
			}
			want := intentTestObject(t, test.want)
			if !reflect.DeepEqual(got.Object, want.Object) {
				t.Fatalf("captured defaults:\n got %s\nwant %s", intentTestJSON(got.Object), intentTestJSON(want.Object))
			}
		})
	}
}

func TestResourceIntentDefaultsSupportRepeatedRemoval(t *testing.T) {
	t.Parallel()
	converter := intentTestConverter(t)
	rawBefore := intentTestObject(t, `{}`)
	effectiveBefore := rawBefore.DeepCopy()
	live := rawBefore.DeepCopy()
	for cycle := 0; cycle < 3; cycle++ {
		for _, enabled := range []bool{true, false} {
			rawDesired := intentTestObject(t, `{}`)
			if enabled {
				rawDesired = intentTestObject(t, `{"env":[{"id":"AUTH","valueFrom":{"secret":"s"}}]}`)
			}
			effectiveDesired, err := CarryResourceIntentDefaults(converter, rawBefore, effectiveBefore, rawDesired)
			if err != nil {
				t.Fatal(err)
			}
			submitted, retained, err := MergeResourceIntent(converter, effectiveBefore, live, effectiveDesired, nil)
			if err != nil || len(retained) != 0 {
				t.Fatalf("cycle %d enabled %v merge: %v, retained %v", cycle, enabled, err, retained)
			}
			accepted := submitted.DeepCopy()
			if enabled {
				// Simulate a request-scoped API default on every newly created
				// member, including a member added later by JSON Patch.
				accepted = intentTestObject(t, `{"env":[{"id":"AUTH","valueFrom":{"secret":"s","optional":false}}]}`)
			}
			nextEffective, err := CaptureResourceIntentDefaults(converter, rawDesired, effectiveDesired, submitted, accepted)
			if err != nil {
				t.Fatal(err)
			}
			if !enabled && !reflect.DeepEqual(accepted.Object, intentTestObject(t, `{}`).Object) {
				t.Fatalf("cycle %d left an API defaulted member: %s", cycle, intentTestJSON(accepted.Object))
			}
			rawBefore, effectiveBefore, live = rawDesired, nextEffective, accepted
		}
	}
}

func TestCapturedDefaultsStillProtectExternalChanges(t *testing.T) {
	t.Parallel()
	converter := intentTestConverter(t)
	raw := intentTestObject(t, `{"env":[{"id":"AUTH","valueFrom":{"secret":"s"}}]}`)
	accepted := intentTestObject(t, `{"env":[{"id":"AUTH","valueFrom":{"secret":"s","optional":false}}]}`)
	effective, err := CaptureResourceIntentDefaults(converter, raw, raw, raw, accepted)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct{ name, live string }{
		{"changed declared field", `{"env":[{"id":"AUTH","valueFrom":{"secret":"external","optional":false}}]}`},
		{"changed captured default", `{"env":[{"id":"AUTH","valueFrom":{"secret":"s","optional":true}}]}`},
		{"removed declared field", `{"env":[{"id":"AUTH","valueFrom":{"optional":false}}]}`},
		{"new external field", `{"env":[{"id":"AUTH","extra":"external","valueFrom":{"secret":"s","optional":false}}]}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			live := intentTestObject(t, test.live)
			got, retained, err := MergeResourceIntent(converter, effective, live, intentTestObject(t, `{}`), nil)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got.Object, live.Object) || len(retained) == 0 {
				t.Fatalf("external changes not preserved: %s, retained %v", intentTestJSON(got.Object), retained)
			}
		})
	}
}

func TestResourceIntentDefaultsRejectIdentityChanges(t *testing.T) {
	t.Parallel()
	b := intentTestObject(t, `{"env":[{"id":"AUTH","value":"yes"}]}`)
	d := b.DeepCopy()
	d.SetNamespace("other")
	if _, err := CarryResourceIntentDefaults(intentTestConverter(t), b, b, d); err == nil {
		t.Fatal("different desired identity must be rejected")
	}
	submitted := b.DeepCopy()
	submitted.SetUID("original")
	accepted := b.DeepCopy()
	accepted.SetUID("recreated")
	if _, err := CaptureResourceIntentDefaults(intentTestConverter(t), b, b, submitted, accepted); err == nil {
		t.Fatal("different accepted UID must be rejected")
	}
}

func TestMergeResourceIntentRejectsInvalidInput(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		edit func(*unstructured.Unstructured)
	}{
		{"different api version", func(d *unstructured.Unstructured) { d.SetAPIVersion("test.invalid/v2") }},
		{"different kind", func(d *unstructured.Unstructured) { d.SetKind("Other") }},
		{"different name", func(d *unstructured.Unstructured) { d.SetName("other") }},
		{"different namespace", func(d *unstructured.Unstructured) { d.SetNamespace("other") }},
		{"unknown field", func(d *unstructured.Unstructured) { d.Object["spec"] = map[string]interface{}{"unknown": "x"} }},
		{"duplicate list keys", func(d *unstructured.Unstructured) {
			d.Object["spec"] = map[string]interface{}{"env": []interface{}{map[string]interface{}{"id": "A"}, map[string]interface{}{"id": "A"}}}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			b := intentTestObject(t, `{}`)
			d := b.DeepCopy()
			test.edit(d)
			if _, _, err := MergeResourceIntent(intentTestConverter(t), b, b, d, nil); err == nil {
				t.Fatal("expected invalid input to fail closed")
			}
		})
	}
}

func intentTestObject(t *testing.T, fields string) *unstructured.Unstructured {
	t.Helper()
	var object map[string]interface{}
	if err := kjson.UnmarshalCaseSensitivePreserveInts([]byte(fields), &object); err != nil {
		t.Fatal(err)
	}
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "test.invalid/v1", "kind": "Thing",
		"metadata": map[string]interface{}{"name": "test", "namespace": "test"}, "spec": object,
	}}
}

func intentTestJSON(obj interface{}) string {
	data, _ := json.Marshal(obj)
	return string(data)
}

func intentTestConverter(t *testing.T) managedfields.TypeConverter {
	t.Helper()
	var definition spec.Schema
	if err := json.Unmarshal([]byte(`{
  "type":"object",
  "x-kubernetes-group-version-kind":[{"group":"test.invalid","version":"v1","kind":"Thing"}],
  "properties":{
    "apiVersion":{"type":"string"}, "kind":{"type":"string"},
    "metadata":{"type":"object","properties":{
      "name":{"type":"string"}, "namespace":{"type":"string"},
      "labels":{"type":"object","additionalProperties":{"type":"string"}},
      "annotations":{"type":"object","additionalProperties":{"type":"string"}}
    }},
    "spec":{"type":"object","properties":{
      "data":{"type":"object","additionalProperties":{"type":"string"}},
      "env":{"type":"array","x-kubernetes-list-type":"map","x-kubernetes-list-map-keys":["id"],"items":{
        "type":"object","properties":{
          "id":{"type":"string"},"value":{"type":"string"},"extra":{"type":"string"},
          "children":{"type":"array","x-kubernetes-list-type":"map","x-kubernetes-list-map-keys":["id"],"items":{
            "type":"object","properties":{"id":{"type":"string"},"value":{"type":"string"},"extra":{"type":"string"}}
          }},
          "valueFrom":{"type":"object","properties":{"secret":{"type":"string"},"optional":{"type":"boolean"}}}
        }
      }},
      "ports":{"type":"array","x-kubernetes-list-type":"map","x-kubernetes-list-map-keys":["port","protocol"],"items":{
        "type":"object","properties":{"port":{"type":"integer"},"protocol":{"type":"string","default":"TCP"},"target":{"type":"string"}}
      }},
      "tags":{"type":"array","x-kubernetes-list-type":"set","items":{"type":"string"}},
      "atomicList":{"type":"array","x-kubernetes-list-type":"atomic","items":{"type":"string"}},
      "atomicMap":{"type":"object","x-kubernetes-map-type":"atomic","additionalProperties":{"type":"string"}}
    }}
  }
}`), &definition); err != nil {
		t.Fatal(err)
	}
	converter, err := managedfields.NewTypeConverter(map[string]*spec.Schema{"test.Thing": &definition}, false)
	if err != nil {
		t.Fatal(err)
	}
	return converter
}
