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
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestBaselineAndMIDVolumeMountIdentity(t *testing.T) {
	native := v1.GVK{Group: "apps", Version: "v1", Kind: "Deployment"}
	kafka := v1.GVK{Group: "kafka.middleware.cn", Version: "v1", Kind: "KafkaCluster"}
	const a = `{"name":"log","mountPath":"/a","subPath":"one"}`
	const b = `{"name":"log","mountPath":"/b","subPath":"two"}`
	for _, tc := range []struct {
		name             string
		target           v1.GVK
		base, edit, want string
		rejected         bool
	}{
		{"add-second-path", native, "[" + a + "]", "[" + b + "]", "[" + a + "," + b + "]", false},
		{"edit-one-path", native, "[" + a + "," + b + "]", `[{"mountPath":"/b","readOnly":true}]`, `[{"name":"log","mountPath":"/a","subPath":"one"},{"name":"log","mountPath":"/b","subPath":"two","readOnly":true}]`, false},
		{"replace-volume-at-same-path", native, "[" + a + "," + b + "]", `[{"name":"audit","mountPath":"/b"}]`, `[{"name":"log","mountPath":"/a","subPath":"one"},{"name":"audit","mountPath":"/b","subPath":"two"}]`, false},
		{"legacy-unambiguous-name", native, "[" + a + "]", `[{"name":"log","readOnly":true}]`, `[{"name":"log","mountPath":"/a","subPath":"one","readOnly":true}]`, false},
		{"legacy-ambiguous-name", native, "[" + a + "," + b + "]", `[{"name":"log","readOnly":true}]`, "", true},
		{"unknown-volume-only", native, "[" + a + "]", `[{"name":"other","readOnly":true}]`, "", true},
		{"duplicate-paths", native, "[]", "[" + b + "," + b + "]", "", true},
		{"empty-base-add-both", native, "[]", "[" + a + "," + b + "]", "[" + a + "," + b + "]", false},
		{"empty-overlay-inherits", native, "[" + a + "," + b + "]", "[]", "[" + a + "," + b + "]", false},
		{"opaque-keeps-package-name-contract", kafka, "[" + a + "]", "[" + b + "]", "[" + b + "]", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const prefix = `{"template":{"spec":{"containers":[{"name":"broker","volumeMounts":`
			const suffix = `}]}}}`
			base := &v1.MiddlewareBaseline{Spec: v1.MiddlewareBaselineSpec{GVK: tc.target, Parameters: runtime.RawExtension{Raw: []byte(prefix + tc.base + suffix)}}}
			mid := &v1.Middleware{Spec: v1.MiddlewareSpec{Parameters: runtime.RawExtension{Raw: []byte(prefix + tc.edit + suffix)}}}
			err := mergeMiddlewareParameters(t.Context(), nil, base, mid)
			if tc.rejected {
				if err == nil || !strings.Contains(err.Error(), "mountPath") {
					t.Fatalf("ambiguous mount accepted or unrelated failure: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var actual, want any
			if err := json.Unmarshal(mid.Spec.Parameters.Raw, &actual); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(prefix+tc.want+suffix), &want); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(actual, want) {
				t.Fatalf("got %s, want %s", mid.Spec.Parameters.Raw, prefix+tc.want+suffix)
			}
		})
	}
}

func TestBaselineAndMIDContainerEnvOrder(t *testing.T) {
	deployment := v1.GVK{Group: "apps", Version: "v1", Kind: "Deployment"}
	pod := v1.GVK{Version: "v1", Kind: "Pod"}
	cron := v1.GVK{Group: "batch", Version: "v1", Kind: "CronJob"}
	opaque := v1.GVK{Group: "example.test", Version: "v1", Kind: "Deployment"}
	const endpoint = `{"name":"ENDPOINT","value":"$(DB_HOST):5432"}`
	const trace = `{"name":"TRACE","valueFrom":{"fieldRef":{"fieldPath":"metadata.name"}}}`
	const host = `{"name":"DB_HOST","value":"db"}`
	const first = `[` + endpoint + `,` + trace + `,` + host + `]`
	const edit = `[{"name":"DB_HOST"},{"name":"ENDPOINT"}]`
	const ordered = `[` + host + `,` + trace + `,` + endpoint + `]`
	for _, tc := range []struct {
		name                      string
		target                    v1.GVK
		prefix, suffix            string
		group, override, expected string
	}{
		{"containers env only", deployment, `{"template":{"spec":`, `}}`, "containers", edit, ordered},
		{"init container env only", pod, "", "", "initContainers", edit, ordered},
		{"ephemeral container env only", pod, "", "", "ephemeralContainers", edit, ordered},
		{"CronJob template", cron, `{"jobTemplate":{"spec":{"template":{"spec":`, `}}}}`, "containers", edit, ordered},
		{"single value edit", deployment, `{"template":{"spec":`, `}}`, "containers", `[{"name":"DB_HOST","value":"new"}]`, `[` + endpoint + `,` + trace + `,{"name":"DB_HOST","value":"new"}]`},
		{"new variable before inherited member", deployment, `{"template":{"spec":`, `}}`, "containers", `[{"name":"NEW","value":"x"},{"name":"DB_HOST"}]`, `[` + endpoint + `,` + trace + `,{"name":"NEW","value":"x"},` + host + `]`},
		{"custom resource keeps package order", opaque, `{"template":{"spec":`, `}}`, "containers", edit, first},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Include two containers with identical variable names. The sidecar is
			// first, so an accidental positional lookup modifies the wrong one.
			spec := func(containers string) []byte {
				return []byte(tc.prefix + `{"` + tc.group + `":[` + containers + `]}` + tc.suffix)
			}
			sidecar := `{"name":"sidecar","env":` + first + `}`
			main := func(env string) string { return `{"name":"main","env":` + env + `}` }
			base := &v1.MiddlewareBaseline{Spec: v1.MiddlewareBaselineSpec{GVK: tc.target, Parameters: runtime.RawExtension{Raw: spec(sidecar + "," + main(first))}}}
			mid := &v1.Middleware{Spec: v1.MiddlewareSpec{Parameters: runtime.RawExtension{Raw: spec(main(tc.override))}}}
			if err := mergeMiddlewareParameters(t.Context(), nil, base, mid); err != nil {
				t.Fatal(err)
			}
			var actual, want any
			if err := json.Unmarshal(mid.Spec.Parameters.Raw, &actual); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(spec(sidecar+","+main(tc.expected)), &want); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(actual, want) {
				t.Fatalf("got %s, want %s", mid.Spec.Parameters.Raw, spec(sidecar+","+main(tc.expected)))
			}
		})
	}
}

func TestMIDRejectsAmbiguousEnvOrder(t *testing.T) {
	base := &v1.MiddlewareBaseline{Spec: v1.MiddlewareBaselineSpec{GVK: v1.GVK{Version: "v1", Kind: "Pod"}, Parameters: runtime.RawExtension{Raw: []byte(`{"containers":[{"name":"main","env":[{"name":"DB_HOST","value":"db"}]}]}`)}}}
	mid := &v1.Middleware{Spec: v1.MiddlewareSpec{Parameters: runtime.RawExtension{Raw: []byte(`{"containers":[{"name":"main","env":[{"name":"DB_HOST"},{"name":"DB_HOST"}]}]}`)}}}
	if err := mergeMiddlewareParameters(t.Context(), nil, base, mid); err == nil || !strings.Contains(err.Error(), `containers["main"].env`) {
		t.Fatalf("expected duplicate env error for main, got %v", err)
	}
}
