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

package middlewareconfiguration

import (
	"errors"
	"testing"

	"github.com/harmonycloud/opensaola/pkg/tools"
)

func TestDeleteIdentityProjection(t *testing.T) {
	values := &tools.TemplateValues{Globe: map[string]any{"Name": "broker", "Namespace": "audit"}}
	for _, tc := range []struct{ name, template, kind, target, namespace string }{
		{"bare pipeline with quotes", `apiVersion: v1
data:
  manifest: |
    kind: Fake
    metadata: { name: misleading }
  fail: {{ required "business value is absent" .Values.absent }}
metadata:
  labels: {name: another-name}
  name: {{ printf "%s-log" .Globe.Name | quote }}
  namespace: {{ .Globe.Namespace | quote }}
kind: ConfigMap`, "ConfigMap", "broker-log", "audit"},
		{"JSON", `{"data":{"kind":"Fake","body":"{{ fail "unrelated" }}"},"apiVersion":"v1","metadata":{"namespace":"audit","name":"{{ .Globe.Name }}"},"kind":"Secret"}`, "Secret", "broker", "audit"},
		{"whole optional resource", `{{- if .Capabilities.APIVersions.Has "x" }}
apiVersion: v1
kind: Service
metadata:
  name: '{{ .Globe.Name }}-svc'
  namespace: audit
spec:
  {{- with .Values.address }}
  clusterIP: {{ . }}
  {{- end }}
{{- end }}`, "Service", "broker-svc", "audit"},
		{"template helper in name", `{{define "target"}}{{printf "%s-log" .Globe.Name}}{{end}}apiVersion: v1
kind: ConfigMap
metadata: {name: '{{ template "target" . }}', namespace: audit}`, "ConfigMap", "broker-log", "audit"},
	} {
		t.Run(tc.name, func(t *testing.T) {

			id, err := parseDeleteIdentity(tc.template)
			if err != nil {
				t.Fatal(err)
			}
			for _, v := range []struct {
				field deleteIdentityField
				want  string
			}{{id.APIVersion, "v1"}, {id.Kind, tc.kind}, {id.Name, tc.target}, {id.Namespace, tc.namespace}} {
				got, err := v.field.Render(t.Context(), values)
				if got != v.want || err != nil {
					t.Fatalf("got %q want %q err=%v", got, v.want, err)
				}
			}
		})
	}
}

func TestDeleteIdentityRejectsAmbiguousStructures(t *testing.T) {
	for _, input := range []string{
		"apiVersion: v1\nkind: ConfigMap\nkind: Secret",
		"apiVersion: v1\nkind: ConfigMap\nmetadata: {name: A, name: B}",

		"apiVersion: v1\nkind: ConfigMap\nmetadata: {<<: {name: borrowed}}",
		"apiVersion: v1\nkind: ConfigMap\nmetadata:\n{{if .Values.toggle}}\n  namespace: optional\n{{end}}\n  name: target",
	} {
		if _, err := parseDeleteIdentity(input); err == nil || errors.Is(err, errOpaqueDeleteIdentity) {
			t.Errorf("ambiguous structural template accepted or misclassified: %v", err)
		}
	}
}

func TestOpaqueCleanupHintsAndRealDocuments(t *testing.T) {
	const hint = `{{/* Single-object cleanup GVK and ownership fallback.
apiVersion: v1
kind: Service
metadata:
  name: {{ "" }}
  namespace: {{ .Globe.Namespace }}
*/}}`
	for _, tc := range []struct {
		name, template, kind string
		named, opaque        bool
	}{
		{"explicit hint for generated object", hint + `{{toYaml .Values.resource}}`, "Service", false, false},
		{"real identities take precedence", hint + "apiVersion: v1\nkind: Secret\nmetadata: {name: actual}", "Secret", true, false},
		{"ordinary comment is not an identity", `{{/* apiVersion: v1; kind: Service */}}{{toYaml .Values.object}}`, "", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id, err := parseDeleteIdentity(tc.template)
			if tc.opaque {
				if !errors.Is(err, errOpaqueDeleteIdentity) {
					t.Fatalf("unexpected %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			k, err := id.Kind.Render(t.Context(), nil)
			if err != nil || k != tc.kind || id.Name.Present() != tc.named {
				t.Fatalf("kind %q named %t error %v", k, id.Name.Present(), err)
			}
		})
	}
}

func TestDeleteIdentityLiteralTpl(t *testing.T) {
	src := `{{$ctx := dict "Release" (dict "Name" .Globe.Name)}}{{ tpl ` + "\x60" + "apiVersion: v1\nkind: Service\nmetadata: {name: '{{.Release.Name}}'}\nspec: {x: '{{fail \"business\"}}'}" + "\x60" + ` $ctx }}`
	id, err := parseDeleteIdentity(src)
	if err != nil {
		t.Fatal(err)
	}
	name, err := id.Name.Render(t.Context(), &tools.TemplateValues{Globe: map[string]any{"Name": "broker"}})
	if err != nil || name != "broker" {
		t.Fatalf("name %q, err %v", name, err)
	}
}

func TestDeleteIdentityConditionalScalar(t *testing.T) {
	id, err := parseDeleteIdentity("apiVersion: v1\nkind: {{if .Values.enabled}}Secret{{else}}ConfigMap{{end}}\nmetadata: {name: target}")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		enabled bool
		want    string
	}{{true, "Secret"}, {false, "ConfigMap"}} {
		kind, err := id.Kind.Render(t.Context(), &tools.TemplateValues{Values: map[string]any{"enabled": tc.enabled}})
		if !id.Kind.Dynamic() || err != nil || kind != tc.want {
			t.Errorf("enabled %v kind %q err %v", tc.enabled, kind, err)
		}
	}
}

func TestDeleteIdentityInputMutationAndLocalBindings(t *testing.T) {
	const resource = "apiVersion: v1\nkind: Secret\nmetadata:\n  name: ok\n  namespace: "
	for _, tc := range []struct{ name, setup, field, want string }{
		{"only used declaration", `{{$irrelevant := fail "wrong dependency"}}{{$ns := .Globe.Namespace}}`, `{{$ns}}`, "team"},
		{"context map mutation", `{{$_ := set .Values "ns" "changed"}}`, `{{.Values.ns}}`, ""},
		{"conditional assignment", `{{$ns := .Globe.Namespace}}{{if .Values.change}}{{$ns = "changed"}}{{end}}`, `{{$ns}}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id, err := parseDeleteIdentity(tc.setup + resource + tc.field)
			if err != nil {
				t.Fatal(err)
			}
			v, err := id.Namespace.Render(t.Context(), &tools.TemplateValues{Globe: map[string]any{"Namespace": "team"}, Values: map[string]any{"ns": "unchanged", "change": true}})
			if tc.want == "" {
				if err == nil {
					t.Errorf("ambiguous identity rendered as %q", v)
				}
				return
			}
			if err != nil || v != tc.want {
				t.Errorf("got %q err=%v", v, err)
			}
		})
	}
}

func TestDeleteIdentityConditionalRoleScope(t *testing.T) {
	const source = `{{- $dedicated := eq .Values.strategy "dedicated" }}
{{- if $dedicated }}
kind: "Role"
{{- else }}
kind: "ClusterRole"
{{- end }}
apiVersion: rbac.authorization.k8s.io/v1
metadata:
  {{- if $dedicated }}
  name: dedicated-admin
  namespace: {{ .Globe.Namespace | quote }}
  {{- else }}
  name: {{ printf "%s-admin" .Globe.Namespace | quote }}
  {{- end }}
rules:
  - resources: ["pods"]
`
	id, err := parseDeleteIdentity(source)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ strategy, kind, name string }{{"dedicated", "Role", "dedicated-admin"}, {"all", "ClusterRole", "team-admin"}} {
		t.Run(tc.strategy, func(t *testing.T) {
			values := &tools.TemplateValues{Values: map[string]any{"strategy": tc.strategy}, Globe: map[string]any{"Namespace": "team"}}
			for _, x := range []struct {
				f    deleteIdentityField
				want string
			}{{id.Kind, tc.kind}, {id.Name, tc.name}} {
				got, err := x.f.Render(t.Context(), values)
				if err != nil || got != x.want {
					t.Fatalf("got %q, want %q, err=%v", got, x.want, err)
				}
			}
			if tc.kind == "Role" {
				got, err := id.Namespace.Render(t.Context(), values)
				if err != nil || got != "team" {
					t.Fatalf("namespace=%q err=%v", got, err)
				}
			}
		})
	}
}

func TestDeleteTypeIgnoresAmbiguousMetadata(t *testing.T) {
	const resource = "apiVersion: v1\nkind: ConfigMap\nmetadata:\n{{if .Values.enabled}}\n  namespace: team\n{{end}}\n  name: data"
	id, err := parseDeleteType(resource)
	if err != nil {
		t.Fatal(err)
	}
	k, err := id.Kind.Render(t.Context(), nil)
	if err != nil || k != "ConfigMap" {
		t.Fatalf("kind=%q err=%v", k, err)
	}
}

func TestDeleteIdentityStaticCRDBundle(t *testing.T) {
	const crd = "apiVersion: apiextensions.k8s.io/v1\nkind: CustomResourceDefinition\nmetadata: {name: one.test.invalid}"
	for _, tc := range []struct {
		name, extra string
		retain      bool
	}{
		{"all static CRDs", "---\n# empty document\n---\napiVersion: apiextensions.k8s.io/v1beta1\nkind: CustomResourceDefinition\nmetadata: {name: two.test.invalid}\n---", true},
		{"mixed with namespaced resource", "---\napiVersion: v1\nkind: Secret\nmetadata: {name: second}", false},
		{"templated kind", "---\napiVersion: apiextensions.k8s.io/v1\nkind: {{.Values.kind}}", false},
		{"same kind in another group", "---\napiVersion: custom.invalid/v1\nkind: CustomResourceDefinition", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, parse := range []func(string) (deleteIdentity, error){parseDeleteIdentity, parseDeleteType} {
				id, err := parse(crd + "\n" + tc.extra)
				if !tc.retain {
					if !errors.Is(err, errOpaqueDeleteIdentity) {
						t.Fatalf("expected inventory, got %v", err)
					}
					continue
				}
				if err != nil {
					t.Fatal(err)
				}
				k, err := id.Kind.Render(t.Context(), nil)
				if k != "CustomResourceDefinition" || err != nil || id.Name.Present() {
					t.Fatalf("bad bundle identity: %q err=%v", k, err)
				}
			}
		})
	}
}

func TestDeleteIdentityMutationExecutionOrder(t *testing.T) {
	const header = "apiVersion: v1\nkind: ConfigMap\nmetadata:\n"
	const name = "  name: {{ .Globe.Name }}\n"
	const namespace = "  namespace: {{ .Globe.Namespace }}\n"
	const mutate = "{{ $_ := set .Globe \"Namespace\" \"second\" }}\n"
	for _, test := range []struct {
		name, source              string
		blockName, blockNamespace bool
	}{
		{"Nacos business defaults after metadata", header + name + namespace + "data:\n  defaults: |\n    {{ $defaults := dict }}{{ $_ := set $defaults \"mode\" \"standalone\" }}", false, false},
		{"script argument is text", "{{ $script := printf \"set -e ; merge files\" }}\n" + header + name + namespace, false, false},
		{"comment is text", "{{/* set .Globe Namespace */}}\n" + header + name + namespace, false, false},
		{"mutation before both fields", mutate + header + name + namespace, true, true},
		{"mutation between name and namespace", header + name + mutate + namespace, false, true},
		{"nested command mutates input", "{{ $_ := get (set .Globe \"Namespace\" \"second\") \"Namespace\" }}" + header + name + namespace, true, true},
		{"unused mutating definition", `{{define "mut"}}{{$_ := set .Globe "Namespace" "changed"}}{{end}}` + header + name + namespace, false, false},
		{"helper called through include", `{{define "mut"}}{{$_ := set .Globe "Namespace" "changed"}}{{end}}{{$unused := include "mut" .}}` + header + name + namespace, true, true},
		{"transitive template after name", `{{define "inner"}}{{$_ := set .Globe "Namespace" "changed"}}{{end}}{{define "outer"}}{{template "inner" .}}{{end}}` + header + name + `{{$unused := include "outer" .}}` + "\n" + namespace, false, true},
		{"read-only include before identity", `{{define "ro"}}{{.Globe.Name}}{{end}}{{$unused := include "ro" .}}` + header + name + namespace, false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			id, err := parseDeleteIdentity(test.source)
			if err != nil {
				t.Fatal(err)
			}
			values := &tools.TemplateValues{Globe: map[string]any{"Name": "app", "Namespace": "one"}}
			for _, item := range []struct {
				field   deleteIdentityField
				want    string
				blocked bool
			}{{id.Name, "app", test.blockName}, {id.Namespace, "one", test.blockNamespace}} {
				got, err := item.field.Render(t.Context(), values)
				if item.blocked {
					if !errors.Is(err, errMutableDeleteIdentity) {
						t.Fatalf("expected inventory marker, got %q err=%v", got, err)
					}
					continue
				}
				if err != nil || got != item.want {
					t.Fatalf("got %q, want %q err=%v", got, item.want, err)
				}
			}
		})
	}
	for _, value := range []string{`{{required "namespace missing" .Values.unknown}}`, `""`} {
		id, err := parseDeleteIdentity(header + name + "  namespace: " + value)
		if err != nil {
			t.Fatal(err)
		}
		_, err = id.Namespace.Render(t.Context(), &tools.TemplateValues{})
		if err == nil || errors.Is(err, errMutableDeleteIdentity) {
			t.Fatalf("invalid explicit namespace misclassified: %v", err)
		}
	}
}
