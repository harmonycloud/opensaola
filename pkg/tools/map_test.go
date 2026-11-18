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

package tools

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestStructMerge_MapOverride(t *testing.T) {
	t.Parallel()

	type testStruct struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}

	base := &testStruct{Name: "base", Version: "v1"}
	overlay := &testStruct{Name: "overlay", Version: "v2"}

	err := StructMerge(base, overlay, StructMergeMapType)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// After merge, overlay should contain overlaid values (overlay wins)
	if overlay.Name != "overlay" {
		t.Errorf("Name = %q, want %q", overlay.Name, "overlay")
	}
	if overlay.Version != "v2" {
		t.Errorf("Version = %q, want %q", overlay.Version, "v2")
	}
}

func TestStructMerge_NestedMap(t *testing.T) {
	t.Parallel()

	base := map[string]any{
		"metadata": map[string]any{
			"name":      "test",
			"namespace": "default",
		},
		"spec": map[string]any{
			"replicas": float64(3),
		},
	}
	overlay := map[string]any{
		"metadata": map[string]any{
			"namespace": "production",
			"labels": map[string]any{
				"app": "web",
			},
		},
	}

	result := MergeMap(base, overlay)

	metadata, ok := result["metadata"].(map[string]any)
	if !ok {
		t.Fatal("metadata is not a map")
	}
	if metadata["name"] != "test" {
		t.Errorf("metadata.name = %v, want %q", metadata["name"], "test")
	}
	if metadata["namespace"] != "production" {
		t.Errorf("metadata.namespace = %v, want %q", metadata["namespace"], "production")
	}
	labels, ok := metadata["labels"].(map[string]any)
	if !ok {
		t.Fatal("metadata.labels is not a map")
	}
	if labels["app"] != "web" {
		t.Errorf("metadata.labels.app = %v, want %q", labels["app"], "web")
	}
	// spec should be preserved from base
	spec, ok := result["spec"].(map[string]any)
	if !ok {
		t.Fatal("spec is not a map")
	}
	if spec["replicas"] != float64(3) {
		t.Errorf("spec.replicas = %v, want %v", spec["replicas"], float64(3))
	}
}

func TestStructMerge_NilBase(t *testing.T) {
	t.Parallel()

	type testStruct struct {
		Name string `json:"name"`
	}

	// When old is nil (marshals to "null"), and new has value,
	// StructMerge should keep new as-is (the "null" old branch returns nil).
	overlay := &testStruct{Name: "overlay"}

	err := StructMerge((*testStruct)(nil), overlay, StructMergeMapType)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if overlay.Name != "overlay" {
		t.Errorf("Name = %q, want %q", overlay.Name, "overlay")
	}
}

func TestStructMerge_NilOverlay(t *testing.T) {
	t.Parallel()

	type testStruct struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}

	base := &testStruct{Name: "base", Version: "v1"}
	overlay := &testStruct{Name: "overlay"}

	// StructMerge merges old (base) into new (overlay).
	// The overlay's non-zero values win; base fills in missing keys only
	// at nested-map level. For flat structs with zero-value strings,
	// the JSON merge sees overlay's "" as a real value so base's value
	// is overridden by overlay's.
	err := StructMerge(base, overlay, StructMergeMapType)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if overlay.Name != "overlay" {
		t.Errorf("Name = %q, want %q", overlay.Name, "overlay")
	}
	// Version: base has "v1", overlay has "" (zero value).
	// MergeMap iterates new's keys and writes them into old, so overlay's ""
	// overwrites base's "v1". The merged result has Version="".
	if overlay.Version != "" {
		t.Errorf("Version = %q, want %q", overlay.Version, "")
	}
}

// Package overlays have their own public input semantics. These examples enter
// through the public raw JSON StructMerge entry point.
func TestPackageOverlayCompatibility(t *testing.T) {
	for _, tc := range []struct{ name, baseline, mid, want string }{
		{"full width integers", `{"previous":-9223372036854775808}`, `{"id":9007199254740993,"max":9223372036854775807}`, `{"id":9007199254740993,"max":9223372036854775807,"previous":-9223372036854775808}`},
		{"opaque structured identifier", `{"rules":[{"name":{"key":"one"},"old":true}]}`, `{"rules":[{"name":{"key":"one"},"new":true}]}`, `{"rules":[{"name":{"key":"one"},"old":true,"new":true}]}`},
		{"Kafka named container additions", `{"containers":[{"name":"broker","image":"base","env":[{"name":"BASE","value":"on"}]}]}`, `{"containers":[{"name":"broker","volumeMounts":[{"name":"log","mountPath":"/root/logs"}]}]}`, `{"containers":[{"name":"broker","image":"base","env":[{"name":"BASE","value":"on"}],"volumeMounts":[{"name":"log","mountPath":"/root/logs"}]}]}`},
		{"empty arrays and maps inherit", `{"containers":[{"name":"broker"}],"config":{"x":"keep"}}`, `{"containers":[],"config":{}}`, `{"containers":[{"name":"broker"}],"config":{"x":"keep"}}`},
		{"keyed and positional lists", `{"accounts":[{"serviceAccountName":"sa","role":"old"}],"steps":[{"a":"keep"}]}`, `{"accounts":[{"serviceAccountName":"sa","scope":"ns"}],"steps":[{"b":"added"}]}`, `{"accounts":[{"role":"old","scope":"ns","serviceAccountName":"sa"}],"steps":[{"a":"keep","b":"added"}]}`},
		{"scalar arrays replace and nested null", `{"args":["first","second"],"option":{"nested":true}}`, `{"args":["new"],"option":null}`, `{"args":["new"],"option":null}`},
		{"root null inherits", `{"enabled":false}`, `null`, `{"enabled":false}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, m := json.RawMessage(tc.baseline), json.RawMessage(tc.mid)
			if err := StructMerge(&b, &m, StructMergeMapType); err != nil {
				t.Fatal(err)
			}
			var want any
			decoder := json.NewDecoder(strings.NewReader(tc.want))
			decoder.UseNumber()
			if err := decoder.Decode(&want); err != nil {
				t.Fatal(err)
			}
			expected, err := json.Marshal(want)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(m, expected) {
				t.Fatalf("rendered %s, want %s", m, expected)
			}
		})
	}
}
