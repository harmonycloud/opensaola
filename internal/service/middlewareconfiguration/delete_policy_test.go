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
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	"github.com/harmonycloud/opensaola/pkg/tools"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestDeleteConfigurationWithNoRecordedTarget(t *testing.T) {
	// Like Qdrant, the template always emits a header and computes whether
	// an entire resource should be emitted inside a nested context.
	const optional = `{{ printf "# optional resource\n" }}{{ with .Values }}{{ if .enabled }}{{ .document }}{{ end }}{{ end }}`
	const resource = "apiVersion: v1\nkind: ConfigMap\nmetadata: {name: shared}"
	for _, tc := range []struct {
		name, template, document string
		enabled                  bool
		wantError                string
	}{
		{name: "disabled with comment", template: optional, document: resource},
		{name: "enabled but only empty documents", template: optional, enabled: true, document: "---\n# first\n---\n# last"},
		{name: "blank source", template: "\n  \n"},
		{name: "comment source", template: "# no object"},
		{name: "only definitions", template: `{{ define "resource" }}unused{{ end }}`},
		{name: "only document separators", template: "---\n---\n"},
		{name: "enabled object", template: optional, enabled: true, document: resource, wantError: "recorded identity is required"},
		{name: "nonempty later document", template: optional, enabled: true, document: "---\n# empty\n---\n" + resource, wantError: "multiple configuration documents"},
		{name: "malformed YAML", template: optional, enabled: true, document: "metadata: [", wantError: "check rendered configuration documents"},
		{name: "empty mapping is content", template: optional, enabled: true, document: "{}", wantError: "recorded identity is required"},
		{name: "explicit null is content", template: optional, enabled: true, document: "null", wantError: "recorded identity is required"},
		{name: "template execution fails", template: `{{ fail "unavailable input" }}`, wantError: "unavailable input"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			owner := testMiddleware()
			owner.Labels = map[string]string{v1.LabelPackageName: "optional"}
			values, err := json.Marshal(map[string]any{"enabled": tc.enabled, "document": tc.document})
			if err != nil {
				t.Fatal(err)
			}
			cfg := orphanTestConfig(nil)
			cfg.Labels = owner.Labels
			cfg.Spec.Template = tc.template
			owner.Spec.Configurations = []v1.Configuration{{Name: cfg.Name, Values: runtime.RawExtension{Raw: values}}}
			cli := interceptor.NewClient(orphanTestClient(t, owner, cfg), interceptor.Funcs{
				List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error {
					t.Fatal("checking for empty output must not search the cluster for deletion targets")
					return nil
				},
				Delete: func(context.Context, client.WithWatch, client.Object, ...client.DeleteOption) error {
					t.Fatal("rendering an unrecorded dynamic object must not authorize deletion")
					return nil
				},
			})
			err = DeleteTemplateRenderedResources(t.Context(), cli, owner, owner, func(context.Context, *tools.TemplateValues, DeleteParameterProjection) error {
				t.Fatal("literal reference values must not load baseline parameters")
				return nil
			})
			if tc.wantError == "" && err != nil || tc.wantError != "" && (err == nil || !strings.Contains(err.Error(), tc.wantError)) {
				t.Fatalf("delete error = %v, want %q", err, tc.wantError)
			}
		})
	}
}

func TestDeleteRendersConfigurationReferenceBeforeOptionalResource(t *testing.T) {
	// The quoted operand is valid Go template source in the baseline YAML.
	// Rendering its JSON encoding directly instead would introduce \"true\".
	const reference = `{"metrics":{"enabled":"{{ (eq (toString .Necessary.metricsEnabled) \"true\") }}"},"settings":"{{ .Parameters.settings | toJson }}"}`
	for _, tc := range []struct {
		name, necessary, prepareError, wantError string
		enabled                                  bool
	}{
		{name: "both disabled", necessary: "false"},
		{name: "enabled via necessary", necessary: "true", wantError: "recorded identity is required"},
		{name: "enabled via parameters", necessary: "false", enabled: true, wantError: "recorded identity is required"},
		{name: "failed parameter rendering", necessary: "false", prepareError: "parameters unavailable", wantError: "parameters unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			owner := testMiddleware()
			owner.Labels = map[string]string{v1.LabelPackageName: "optional"}
			owner.Spec.Necessary.Raw = []byte(fmt.Sprintf(`{"metricsEnabled":%s}`, tc.necessary))
			cfg := orphanTestConfig(nil)
			cfg.Labels = owner.Labels
			cfg.Spec.Template = `{{ printf "# optional resource\n" }}{{ with .Values }}{{ if ne $.Parameters nil }}{{ fail "parameters leaked into the MCF" }}{{ end }}{{ if or .metrics.enabled .settings.enabled }}{{ toYaml (dict "kind" "ServiceMonitor") }}{{ end }}{{ end }}`
			owner.Spec.Configurations = []v1.Configuration{{Name: cfg.Name, Values: runtime.RawExtension{Raw: []byte(reference)}}}
			cli := orphanTestClient(t, owner, cfg)
			err := DeleteTemplateRenderedResources(t.Context(), cli, owner, owner, func(_ context.Context, values *tools.TemplateValues, _ DeleteParameterProjection) error {
				if tc.prepareError != "" {
					return fmt.Errorf("%s", tc.prepareError)
				}
				values.Parameters = map[string]any{"settings": map[string]any{"enabled": tc.enabled}}
				return nil
			})
			if tc.wantError == "" && err != nil || tc.wantError != "" && (err == nil || !strings.Contains(err.Error(), tc.wantError)) {
				t.Fatalf("delete error = %v, want %q", err, tc.wantError)
			}
		})
	}
}

func TestDeleteRecordedTargetBeforeCheckingEmptyOutput(t *testing.T) {
	for _, template := range []string{"# disabled", `{{ fail "old input is gone" }}`} {
		owner := testMiddleware()
		owner.Labels = map[string]string{v1.LabelPackageName: "optional"}
		cfg := orphanTestConfig(map[string]string{v1.AnnotationConfigurationDeletePolicy: v1.ConfigurationDeletePolicyDelete})
		cfg.Labels = owner.Labels
		cfg.Spec.Template = template
		owner.Spec.Configurations = []v1.Configuration{{Name: cfg.Name, Values: runtime.RawExtension{Raw: []byte(`{"old":"{{ fail \"obsolete reference\" }}"}`)}}}
		target := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
			Name: "old-target", Namespace: owner.Namespace, UID: "old-target-uid",
			Labels: map[string]string{v1.LabelApp: owner.Name},
			Annotations: map[string]string{
				v1.LabelConfigurations: cfg.Name, v1.AnnotationConfigurationOwnerUID: string(owner.UID), v1.AnnotationConfigurationUID: string(cfg.UID),
			},
		}}
		owner.Status.RenderedConfigurationResources = []v1.RenderedConfigurationResource{{
			Version: "v1", Kind: "ConfigMap", Namespace: target.Namespace, Name: target.Name, Namespaced: true,
			ConfigurationName: cfg.Name, ConfigurationUID: cfg.UID, ResourceUID: target.UID, OwnerUID: owner.UID,
		}}
		cli := orphanTestClient(t, owner, cfg, target)
		if err := DeleteTemplateRenderedResources(t.Context(), cli, owner, owner, func(context.Context, *tools.TemplateValues, DeleteParameterProjection) error {
			t.Fatal("recorded target must not load reference values")
			return nil
		}); err != nil {
			t.Fatalf("delete with recorded target and template %q: %v", template, err)
		}
		if err := cli.Get(t.Context(), client.ObjectKeyFromObject(target), &corev1.ConfigMap{}); !apierrors.IsNotFound(err) {
			t.Fatalf("previously published object still exists: %v", err)
		}
	}
}

func TestDeleteEmptyRenderIsolatesInputsAcrossConfigurations(t *testing.T) {
	owner := testMiddleware()
	owner.Spec.Necessary.Raw = []byte(`{"feature":{"enabled":false}}`)
	owner.Labels = map[string]string{v1.LabelPackageName: "optional"}
	first := orphanTestConfig(nil)
	first.Labels = owner.Labels
	first.Spec.Template = `{{ $_ := set .Necessary.feature "enabled" true }}# empty`
	second := first.DeepCopy()
	second.Name = "next-configuration"
	second.Spec.Template = `{{ printf "# optional\n" }}{{ with .Necessary.feature }}{{ if .enabled }}{{ fail "first template mutated the shared input" }}{{ end }}{{ end }}`
	owner.Spec.Configurations = []v1.Configuration{{Name: first.Name}, {Name: second.Name}}
	cli := orphanTestClient(t, owner, first, second)
	if err := DeleteTemplateRenderedResources(t.Context(), cli, owner, owner, nil); err != nil {
		t.Fatal(err)
	}
}

func TestOwnerDeleteDetachesOnlyKnownOrphan(t *testing.T) {
	for _, tt := range []struct {
		name                                                 string
		fallback, inventory, replaced, foreignMarker, detach bool
	}{
		{name: "legacy target without inventory", detach: true},
		{name: "legacy fallback after target renamed", fallback: true, detach: true},
		{name: "recorded target with same-name rebuilt MCF", inventory: true, detach: true},
		{name: "same-name rebuilt target", inventory: true, replaced: true},
		{name: "legacy target with conflicting owner identity", foreignMarker: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			owner := testMiddleware()
			owner.Labels = map[string]string{v1.LabelPackageName: "test-package"}
			cfg := orphanTestConfig(map[string]string{v1.AnnotationConfigurationDeletePolicy: v1.ConfigurationDeletePolicyOrphan})
			cfg.UID = "reinstalled-config"
			cfg.Labels = owner.Labels
			owner.Spec.Configurations = []v1.Configuration{{Name: cfg.Name}}
			name := "shared-sample"
			if tt.fallback {
				name = "previous-name"
			}
			foreign := metav1.OwnerReference{APIVersion: "v1", Kind: "ConfigMap", Name: "foreign", UID: "foreign"}
			live := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
				Name: name, Namespace: owner.Namespace, UID: "original",
				Labels:          map[string]string{v1.LabelApp: owner.Name},
				Annotations:     map[string]string{v1.LabelConfigurations: cfg.Name},
				OwnerReferences: []metav1.OwnerReference{middlewareOwnerRef(), foreign},
			}}
			if tt.inventory {
				owner.Status.RenderedConfigurationResources = []v1.RenderedConfigurationResource{{
					Version: "v1", Kind: "ConfigMap", Namespace: owner.Namespace, Name: name, Namespaced: true,
					OwnerUID: owner.UID, ResourceUID: live.UID, ConfigurationName: cfg.Name, ConfigurationUID: "original-config",
				}}
				live.Annotations[v1.AnnotationConfigurationOwnerUID] = string(owner.UID)
				live.Annotations[v1.AnnotationConfigurationUID] = "original-config"
			}
			if tt.foreignMarker {
				live.Annotations[v1.AnnotationConfigurationOwnerUID] = "other"
			}
			if tt.replaced {
				live.UID = "replacement"
			}
			cli := orphanTestClient(t, owner, cfg, live)
			if err := DeleteTemplateRenderedResources(context.Background(), cli, owner, owner, nil); err != nil {
				t.Fatal(err)
			}
			after := &corev1.ConfigMap{}
			if err := cli.Get(context.Background(), client.ObjectKeyFromObject(live), after); err != nil {
				t.Fatal(err)
			}
			if tt.detach {
				if len(after.OwnerReferences) != 1 || after.OwnerReferences[0].UID != foreign.UID {
					t.Fatalf("expected only foreign reference after cleanup, got %v", after.OwnerReferences)
				}
			} else if len(after.OwnerReferences) != 2 || after.OwnerReferences[0].UID != owner.UID || after.OwnerReferences[1].UID != foreign.UID {
				t.Fatalf("foreign or replaced target was changed: %v", after.OwnerReferences)
			}
		})
	}
}

func TestFallbackDeletesOnlyObjectsRenderedByItsConfiguration(t *testing.T) {
	owner := testMiddleware()
	owner.Labels = map[string]string{v1.LabelPackageName: "fixture-package"}
	cfg := orphanTestConfig(map[string]string{v1.AnnotationConfigurationDeletePolicy: v1.ConfigurationDeletePolicyDelete})
	cfg.Labels = owner.Labels
	owner.Spec.Configurations = []v1.Configuration{{Name: cfg.Name}}
	var objects = []client.Object{owner, cfg}
	for _, name := range []string{cfg.Name, "separate-orphan-config", "unmarked"} {
		item := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: owner.Namespace,
			Labels: map[string]string{v1.LabelApp: owner.Name}, Annotations: map[string]string{}}}
		if name != "unmarked" {
			item.Annotations[v1.LabelConfigurations] = name
		}
		objects = append(objects, item)
	}
	cli := orphanTestClient(t, objects...)
	if err := DeleteTemplateRenderedResources(context.Background(), cli, owner, owner, nil); err != nil {
		t.Fatal(err)
	}
	for _, obj := range objects[2:] {
		err := cli.Get(context.Background(), client.ObjectKeyFromObject(obj), &corev1.ConfigMap{})
		if obj.GetName() == cfg.Name {
			if !apierrors.IsNotFound(err) {
				t.Fatalf("matching previous name should be removed: %v", err)
			}
		} else if err != nil {
			t.Fatalf("fallback removed %s: %v", obj.GetName(), err)
		}
	}
}

func TestLegacyLabelFallbackChecksOwnerIdentity(t *testing.T) {
	for _, policy := range []string{"", v1.ConfigurationDeletePolicyDelete} {
		t.Run("deletePolicy="+policy, func(t *testing.T) {
			owner := testMiddleware()
			owner.Labels = map[string]string{v1.LabelPackageName: "shared-namespace"}
			cfg := orphanTestConfig(map[string]string{v1.AnnotationConfigurationDeletePolicy: policy})
			cfg.Spec.Template = "apiVersion: v1\nkind: ConfigMap\nmetadata: {name: current-name, namespace: shared}"
			cfg.Labels = owner.Labels
			owner.Spec.Configurations = []v1.Configuration{{Name: cfg.Name}}

			same := middlewareOwnerRef()
			other := same
			other.UID = "other-instance-in-another-namespace"
			cases := []struct {
				name, markedUID string
				controller      *metav1.OwnerReference
				wantDelete      bool
			}{
				{"old-unmarked", "", nil, true},
				{"matching-owner", string(owner.UID), nil, true},
				{"other-owner", string(other.UID), nil, false},
				{"other-controller", "", &other, false},
				{"other-owner-matching-controller", string(other.UID), &same, false},
				{"matching-owner-other-controller", string(owner.UID), &other, false},
			}
			objects := []client.Object{owner, cfg}
			for _, tc := range cases {
				obj := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: tc.name, Namespace: "shared",
					Labels: map[string]string{v1.LabelApp: owner.Name}, Annotations: map[string]string{v1.LabelConfigurations: cfg.Name}}}
				if tc.markedUID != "" {
					obj.Annotations[v1.AnnotationConfigurationOwnerUID] = tc.markedUID
				}
				if tc.controller != nil {
					obj.OwnerReferences = []metav1.OwnerReference{*tc.controller}
				}
				objects = append(objects, obj)
			}
			cli := orphanTestClient(t, objects...)
			if err := DeleteTemplateRenderedResources(t.Context(), cli, owner, owner, nil); err != nil {
				t.Fatal(err)
			}
			for _, tc := range cases {
				err := cli.Get(t.Context(), client.ObjectKey{Namespace: "shared", Name: tc.name}, &corev1.ConfigMap{})
				if tc.wantDelete && !apierrors.IsNotFound(err) || !tc.wantDelete && err != nil {
					t.Errorf("%s: wantDelete=%t, get returned %v", tc.name, tc.wantDelete, err)
				}
			}
		})
	}
}

func TestOwnerDeleteDoesNotListAfterMatchingTarget(t *testing.T) {
	for _, tt := range []struct {
		name       string
		policy     string
		failDelete bool
	}{
		{name: "retained target", policy: "orphan"},
		{name: "delete rejected after resource changed", policy: "delete", failDelete: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			owner := testMiddleware()
			cfg := orphanTestConfig(map[string]string{v1.AnnotationConfigurationDeletePolicy: tt.policy})
			owner.Labels = map[string]string{v1.LabelPackageName: "test-package"}
			cfg.Labels = owner.Labels
			owner.Spec.Configurations = []v1.Configuration{{Name: cfg.Name}}
			target := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "shared-sample", Namespace: owner.Namespace, UID: "observed"}}
			var deletes int
			cli := interceptor.NewClient(orphanTestClient(t, owner, cfg, target), interceptor.Funcs{
				List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error {
					t.Fatal("unexpected fallback after the named target was found")
					return nil
				},
				Delete: func(_ context.Context, _ client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
					deletes++
					pre := (&client.DeleteOptions{}).ApplyOptions(opts).Preconditions
					if pre == nil || pre.UID == nil || *pre.UID != target.UID || pre.ResourceVersion == nil || *pre.ResourceVersion == "" || *pre.ResourceVersion != obj.GetResourceVersion() {
						t.Fatalf("expected delete with observed UID and resourceVersion, got %#v", pre)
					}
					return fmt.Errorf("conflict while deleting")
				},
			})
			err := DeleteTemplateRenderedResources(context.Background(), cli, owner, owner, nil)
			if tt.failDelete {
				if err == nil || deletes != 1 {
					t.Fatalf("expected one retriable deletion failure, got calls=%d err=%v", deletes, err)
				}
			} else if err != nil || deletes != 0 {
				t.Fatalf("expected retain, got calls=%d err=%v", deletes, err)
			}
		})
	}
}

func TestDeleteRendersNamespaceWithPublishValues(t *testing.T) {
	owner := testMiddleware()
	owner.Labels = map[string]string{v1.LabelPackageName: "fixture"}
	cfg := orphanTestConfig(map[string]string{v1.AnnotationConfigurationDeletePolicy: v1.ConfigurationDeletePolicyDelete})
	cfg.Labels = owner.Labels
	cfg.Spec.Template = `apiVersion: v1
kind: ConfigMap
metadata:
  name: same-name
  namespace: {{ ternary "published" "different" (eq .Values nil) }}`
	owner.Spec.Configurations = []v1.Configuration{{Name: cfg.Name}}
	published := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "same-name", Namespace: "published", UID: "original"}}
	other := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "same-name", Namespace: "different", UID: "unrelated"}}
	cli := orphanTestClient(t, owner, cfg, published, other)
	if err := DeleteTemplateRenderedResources(t.Context(), cli, owner, owner, nil); err != nil {
		t.Fatal(err)
	}
	if err := cli.Get(t.Context(), client.ObjectKeyFromObject(published), &corev1.ConfigMap{}); !apierrors.IsNotFound(err) {
		t.Fatalf("expected the originally targeted object to be deleted, got %v", err)
	}
	if err := cli.Get(t.Context(), client.ObjectKeyFromObject(other), &corev1.ConfigMap{}); err != nil {
		t.Fatalf("unexpected change in the other namespace: %v", err)
	}
}

func TestDeleteConfigurationReadsOnlyIdentityInputs(t *testing.T) {
	for _, tc := range []struct {
		name, bindings, expr string
		recorded             bool
	}{
		{"recorded Redis", "", "{{ .Globe.Name }}", true},
		{"legacy Redis", "", "{{ .Globe.Name }}", false},
		{"nested reference", "", "{{ .Values.route.name }}", false},
		{"local alias", "{{ $route := .Values.route }}", "{{ $route.name }}", false},
		{"literal index", "", "{{ index .Values \"route\" \"name\" }}", false},
		{"named helper", `{{define "target"}}{{.Globe.Name}}{{end}}`, `{{include "target" .}}`, false},
		{"named helper with values", `{{define "target"}}{{.route.name}}{{end}}`, `{{include "target" .Values}}`, false},
		{"computed owner key", "", `{{ index .Values .Globe.Name }}`, false},
		{"key presence", "", `{{ ternary .Globe.Name "wrong" (hasKey .Values "password") }}`, false},
		{"published map shape", "", `{{ ternary "wrong" .Globe.Name (eq .Values nil) }}`, false},
		{"typed reference", "", `{{ ternary .Globe.Name "wrong" .Values.typed }}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			owner := testMiddleware()
			owner.Labels = map[string]string{v1.LabelPackageName: "fixture"}
			cfg := orphanTestConfig(nil)
			cfg.Labels = owner.Labels
			cfg.Spec.Template = tc.bindings + "\napiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: " + tc.expr + "\n  namespace: {{ .Globe.Namespace }}\ndata:\n  forbidden: {{ fail \"body must not run\" }}"
			raw, err := json.Marshal(map[string]any{
				"password": "{{ (index .Parameters.pod 0).requirepass }}",
				"route":    map[string]any{"name": "{{ .Globe.Name }}", "unused": `{{ fail "unused route member" }}`},
				owner.Name: "{{ .Globe.Name }}",
				"typed":    `{{ eq .Globe.Name "redis-lxt" }}`,
			})
			if err != nil {
				t.Fatal(err)
			}
			owner.Spec.Configurations = []v1.Configuration{{Name: cfg.Name, Values: runtime.RawExtension{Raw: raw}}}
			target := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
				Name: owner.Name, Namespace: owner.Namespace, UID: "target",
				Labels: map[string]string{v1.LabelApp: owner.Name},
				Annotations: map[string]string{
					v1.LabelConfigurations: cfg.Name, v1.AnnotationConfigurationOwnerUID: string(owner.UID),
					v1.AnnotationConfigurationUID: string(cfg.UID),
				},
			}}
			if tc.recorded {
				owner.Status.RenderedConfigurationResources = []v1.RenderedConfigurationResource{{
					ConfigurationName: cfg.Name, ConfigurationUID: cfg.UID, OwnerUID: owner.UID, ResourceUID: target.UID,
					Version: "v1", Kind: "ConfigMap", Namespace: owner.Namespace, Name: target.Name, Namespaced: true,
				}}
			}
			other := target.DeepCopy()
			other.Namespace = "other"
			other.UID = "other"
			cli := interceptor.NewClient(orphanTestClient(t, owner, cfg, target, other), interceptor.Funcs{List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error {
				t.Fatal("expected exact identity; unexpected label scan")
				return nil
			}})
			err = DeleteTemplateRenderedResources(t.Context(), cli, owner, owner, func(context.Context, *tools.TemplateValues, DeleteParameterProjection) error {
				t.Fatal("unrelated Parameters evaluated")
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if err = cli.Get(t.Context(), client.ObjectKeyFromObject(target), &corev1.ConfigMap{}); !apierrors.IsNotFound(err) {
				t.Fatalf("expected deletion: %v", err)
			}
			if err = cli.Get(t.Context(), client.ObjectKeyFromObject(other), &corev1.ConfigMap{}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDeleteReferenceProjectsMergedParameters(t *testing.T) {
	base := &tools.TemplateValues{Globe: map[string]any{"Name": "redis-lxt"}}
	cfg := v1.Configuration{Name: "redis-credential", Values: runtime.RawExtension{Raw: []byte(`{"name":"{{ (index .Parameters.pod 0).target }}","unused":"{{ fail \"unrelated reference\" }}"}`)}}
	calls := 0
	prepare := func(_ context.Context, values *tools.TemplateValues, project DeleteParameterProjection) error {
		calls++
		params := runtime.RawExtension{Raw: []byte(`{"pod":[{"target":"redis-lxt","other":"{{ fail \"irrelevant Pod field\" }}"},{"target":"{{ fail \"irrelevant Pod\" }}"}],"body":"{{ fail \"irrelevant body\" }}"}`)}
		if err := project(&params); err != nil {
			return err
		}
		if strings.Contains(string(params.Raw), "fail") {
			t.Fatal("unused parameter template would be executed")
		}
		return json.Unmarshal(params.Raw, &values.Parameters)
	}
	v, err := configurationIdentityValues(t.Context(), cfg, base, "{{ .Values.name }}", prepare)
	if err != nil || calls != 1 || v.Values["name"] != "redis-lxt" || v.Parameters != nil {
		t.Fatalf("reference projection: calls=%d values=%+v err=%v", calls, v, err)
	}
}

func TestDeleteUnresolvedNamespaceDoesNotUseOwnerNamespace(t *testing.T) {
	for _, source := range []string{
		`{"select":"{{ .Globe.Name }}","redis-lxt":"other"}`,
		`{"{{ .Globe.Name }}":"other","select":"redis-lxt"}`,
	} {
		owner := testMiddleware()
		owner.Labels = map[string]string{v1.LabelPackageName: "redis"}
		cfg := orphanTestConfig(nil)
		cfg.Labels = owner.Labels
		cfg.Spec.Template = "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: target\n  namespace: {{ index .Values .Values.select | default .Globe.Namespace }}"
		owner.Spec.Configurations = []v1.Configuration{{Name: cfg.Name, Values: runtime.RawExtension{Raw: []byte(source)}}}
		target := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "target", Namespace: owner.Namespace, UID: "unrelated"}}
		cfg.Annotations = map[string]string{v1.AnnotationConfigurationDeletePolicy: v1.ConfigurationDeletePolicyDelete}
		cli := orphanTestClient(t, owner, cfg, target)
		err := DeleteTemplateRenderedResources(t.Context(), cli, owner, owner, nil)
		if err == nil || !strings.Contains(err.Error(), "metadata.namespace") {
			t.Fatalf("expected unresolved namespace; got %v", err)
		}
		if err := cli.Get(t.Context(), client.ObjectKeyFromObject(target), target); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDeleteRefusesProjectionAcrossInputMutation(t *testing.T) {
	cfg := v1.Configuration{Name: "mutable", Values: runtime.RawExtension{Raw: []byte(`{
		"a":"{{ $_ := set .Necessary \"ns\" \"published\" }}",
		"ns":"{{ .Necessary.ns }}"
	}`)}}
	base := &tools.TemplateValues{Necessary: map[string]any{"ns": "other"}}
	_, err := configurationIdentityValues(t.Context(), cfg, base, "{{ .Values.ns }}", nil)
	if err == nil || !strings.Contains(err.Error(), "may be modified") || base.Necessary["ns"] != "other" {
		t.Fatalf("an omitted mutation could change the deletion namespace: %v", err)
	}
}

func TestDeleteEmptyOutputUsesOnlyReferencedParameters(t *testing.T) {
	cfg := v1.Configuration{Name: "redis-shard-sc", Values: runtime.RawExtension{Raw: []byte(`{"type":"{{ .Parameters.type }}","proxy":"{{ hasKey .Parameters \"predixy\" }}"}`)}}
	mc := orphanTestConfig(nil)
	mc.Spec.Template = `{{if eq .Values.type "cluster"}}apiVersion: v1
kind: ConfigMap
metadata: {name: redis-shard}{{end}}`
	base := &tools.TemplateValues{}
	err := verifyEmptyConfiguration(t.Context(), cfg, base, mc, errOpaqueDeleteIdentity, func(_ context.Context, v *tools.TemplateValues, project DeleteParameterProjection) error {
		if project == nil {
			t.Fatal("unrelated primary-resource parameters would be rendered")
		}
		raw := runtime.RawExtension{Raw: []byte(`{"type":"sentinel","unused":"{{ fail \"unrelated parameter\" }}"}`)}
		if err := project(&raw); err != nil {
			return err
		}
		if strings.Contains(string(raw.Raw), "fail") {
			t.Fatal("unexpected full parameter render")
		}
		return json.Unmarshal(raw.Raw, &v.Parameters)
	})
	if err != nil {
		t.Fatal(err)
	}
}
