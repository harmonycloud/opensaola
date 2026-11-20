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

package packages

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func definitionYAML(kind, name string) string {
	return fmt.Sprintf("apiVersion: middleware.cn/v1\nkind: %q\nmetadata: {name: %q}\n", kind, name)
}

func TestDefinitionsSelectsArchiveResourcesInOrder(t *testing.T) {
	const template = "kind: MiddlewareActionBaseline\n---\nname: from-template"
	config := definitionYAML("MiddlewareConfiguration", "shared") + "spec:\n  template: |\n    " + strings.ReplaceAll(template, "\n", "\n    ")
	p := &Package{Files: map[string][]byte{
		"configurations/nested/resource.yml": []byte(config),
		"baselines/bundle.yaml":              []byte("# header\n---\n" + definitionYAML("MiddlewareBaseline", "shared") + "--- # second\n" + definitionYAML("MiddlewareOperatorBaseline", "operator")),
		"actions/run.YAML":                   []byte(definitionYAML("MiddlewareActionBaseline", "run")),
		"docs/demo.yaml":                     []byte(config), "examples/example.yaml": []byte(config),
		"metadata.yaml": []byte(config), "baselines/README.md": []byte(config),
		"baselines/empty.yaml": []byte("# no resources\n---\n"),
		"crds/bundle.yaml":     []byte("apiVersion: apiextensions.k8s.io/v1\nkind: CustomResourceDefinition\nmetadata: {name: widgets.example.com}\n---\n" + definitionYAML("MiddlewareConfiguration", "crd-widget")),
	}}
	for range 3 {
		defs, err := p.Definitions()
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, def := range defs {
			got = append(got, def.GetKind()+"/"+def.GetName())
		}
		want := []string{"MiddlewareActionBaseline/run", "MiddlewareBaseline/shared", "MiddlewareOperatorBaseline/operator", "MiddlewareConfiguration/shared", "MiddlewareConfiguration/crd-widget"}
		if !slices.Equal(got, want) {
			t.Fatalf("definitions = %v, want %v", got, want)
		}
		value, _, err := unstructured.NestedString(defs[3].Object, "spec", "template")
		if err != nil || value != template {
			t.Fatalf("template = %q, err %v", value, err)
		}
		defs[3].SetName("caller-change")
	}
}

func TestDefinitionsRejectsInvalidResourceWithLocation(t *testing.T) {
	base := definitionYAML("MiddlewareBaseline", "demo")
	for _, tc := range []struct {
		name, filename, data string
		want                 []string
	}{
		{"duplicate CRD wrapper across directories", "crds/wrapper.yaml", definitionYAML("MiddlewareConfiguration", "from-config"), []string{"duplicate", "configurations/from-config.yaml#1", "crds/wrapper.yaml#1"}},
		{"duplicate across files", "baselines/b.yaml", base, []string{"duplicate", "MiddlewareBaseline", "demo", "baselines/a.yaml#1", "baselines/b.yaml#1"}},
		{"duplicate in file", "baselines/b.yaml", definitionYAML("MiddlewareBaseline", "other") + "---\n" + definitionYAML("MiddlewareBaseline", "other"), []string{"duplicate", "baselines/b.yaml#1", "baselines/b.yaml#2"}},
		{"missing name", "baselines/b.yaml", definitionYAML("MiddlewareBaseline", ""), []string{"baselines/b.yaml#1", "metadata.name"}},
		{"wrong apiVersion", "baselines/b.yaml", strings.Replace(base, "middleware.cn/v1", "other/v1", 1), []string{"baselines/b.yaml#1", "other/v1"}},
		{"wrong kind", "baselines/b.yaml", definitionYAML("ConfigMap", "demo"), []string{"baselines/b.yaml#1", "ConfigMap"}},
		{"wrong directory", "actions/b.yaml", base, []string{"actions/b.yaml#1", "MiddlewareBaseline"}},
		{"broken second document", "baselines/b.yaml", definitionYAML("MiddlewareBaseline", "other") + "---\nspec: [", []string{"baselines/b.yaml#2"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &Package{Files: map[string][]byte{"baselines/a.yaml": []byte(base), "configurations/from-config.yaml": []byte(definitionYAML("MiddlewareConfiguration", "from-config")), tc.filename: []byte(tc.data)}}
			defs, err := p.Definitions()
			if err == nil || len(defs) != 0 {
				t.Fatalf("expected no partial results on failure, got %v, err %v", defs, err)
			}
			for _, part := range tc.want {
				if !strings.Contains(err.Error(), part) {
					t.Errorf("error %q missing %q", err, part)
				}
			}
		})
	}
}

func TestDefinitionGettersPreserveTypedContentAndMatchExactNames(t *testing.T) {
	const name = "definition-lookups"
	p := &Package{Files: map[string][]byte{
		"baselines/main.yaml":        []byte(definitionYAML("MiddlewareBaseline", "main") + "spec:\n  operatorBaseline: {name: referenced}\n  parameters: {legacy: discarded}\n  parameters: {id: 9007199254740993}\n---\n" + definitionYAML("MiddlewareBaseline", "empty")),
		"baselines/engine.yaml":      []byte(definitionYAML("MiddlewareOperatorBaseline", "engine-ha") + "spec:\n  configurations:\n  - name: referenced\n"),
		"actions/action.yaml":        []byte(definitionYAML("MiddlewareActionBaseline", "noop") + "spec: {steps: []}\n"),
		"configurations/config.yaml": []byte(definitionYAML("MiddlewareConfiguration", "service") + "spec: {template: hello}\n"),
		"crds/crd-nacos.yaml":        []byte(definitionYAML("MiddlewareConfiguration", "crd-nacos") + "spec: {template: wrapped}\n"),
	}}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: DataNamespace, ResourceVersion: "12"}}
	cli := fake.NewClientBuilder().WithObjects(secret).Build()
	packageCache.Set(name, &cacheEntry{resourceVersion: secret.ResourceVersion, pkg: p})
	t.Cleanup(func() { InvalidatePackageCache(name) })
	ctx := t.Context()
	bs, err := GetMiddlewareBaselines(ctx, cli, name)
	if err != nil || len(bs) != 2 {
		t.Fatalf("baselines = %v, err %v", bs, err)
	}
	b, err := GetMiddlewareBaseline(ctx, cli, "main", name)
	if err != nil || string(b.Spec.Parameters.Raw) != `{"id":9007199254740993}` || b.Spec.OperatorBaseline.Name != "referenced" {
		t.Fatalf("main baseline = %#v, err %v", b, err)
	}
	b, err = GetMiddlewareBaseline(ctx, cli, "empty", name)
	if err != nil || len(b.Spec.Parameters.Raw) != 0 || b.Spec.OperatorBaseline.Name != "" {
		t.Fatalf("empty baseline = %#v, err %v", b, err)
	}
	if _, err = GetMiddlewareBaseline(ctx, cli, "referenced", name); !apierrors.IsNotFound(err) {
		t.Errorf("baseline by nested name: %v", err)
	}
	ops, err := GetMiddlewareOperatorBaselines(ctx, cli, name)
	if err != nil || len(ops) != 1 || ops[0].Name != "engine-ha" {
		t.Fatalf("operator baselines = %v, err %v", ops, err)
	}
	op, err := GetMiddlewareOperatorBaseline(ctx, cli, "engine-ha", name)
	if err != nil || op.Name != "engine-ha" {
		t.Fatalf("operator baseline = %v, err %v", op, err)
	}
	for _, missing := range []string{"engine", "referenced"} {
		if _, err := GetMiddlewareOperatorBaseline(ctx, cli, missing, name); !apierrors.IsNotFound(err) {
			t.Errorf("operator baseline %s: %v", missing, err)
		}
	}
	actions, err := GetMiddlewareActionBaselines(ctx, cli, name)
	if err != nil || len(actions) != 1 || actions[0].Name != "noop" || actions[0].Spec.Steps == nil || len(actions[0].Spec.Steps) != 0 {
		t.Fatalf("actions = %v, err %v", actions, err)
	}
	configs, err := GetConfigurations(ctx, cli, name)
	if err != nil || len(configs) != 2 || configs["crd-nacos"] == nil || configs["crd-nacos"].Spec.Template != "wrapped" || configs["service"] == nil || configs["service"].Spec.Template != "hello" {
		t.Fatalf("configs = %v, err %v", configs, err)
	}
}
