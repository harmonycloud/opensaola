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
