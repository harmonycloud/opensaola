//go:build envtest

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
	"strings"
	"sync/atomic"
	"testing"
	"time"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	"github.com/harmonycloud/opensaola/internal/k8s"
	"github.com/harmonycloud/opensaola/internal/service/consts"
	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/jsonpath"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

func testAtomicDefaults(t *testing.T, ctx context.Context, cli client.Client, resolver k8s.IntentSchemaResolver, writes *atomic.Int64) {
	const crd = `apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: profiles.atomic.opensaola.test
spec:
  group: atomic.opensaola.test
  scope: Namespaced
  names: {kind: Profile, plural: profiles}
  versions:
  - name: v1
    served: true
    storage: true
    schema:
      openAPIV3Schema:
        type: object
        properties:
          spec:
            type: object
            properties:
              backend:
                type: object
                x-kubernetes-map-type: atomic
                x-kubernetes-preserve-unknown-fields: true
                properties:
                  endpoint: {type: string}
                  retries: {type: integer, default: 3}
                  policy:
                    type: object
                    properties:
                      mode: {type: string, default: safe}
                  defaultPolicy:
                    type: object
                    default: {}
                    properties:
                      mode: {type: string, default: safe}
              stages:
                type: array
                x-kubernetes-list-type: atomic
                items:
                  type: object
                  properties:
                    name: {type: string}
                    enabled: {type: boolean, default: true}
`
	var definition apiextensionsv1.CustomResourceDefinition
	if err := yaml.Unmarshal([]byte(crd), &definition); err != nil {
		t.Fatal(err)
	}
	if err := cli.Create(ctx, &definition); err != nil {
		t.Fatal(err)
	}
	profileGVK := schema.GroupVersionKind{Group: "atomic.opensaola.test", Version: "v1", Kind: "Profile"}
	var resolveErr error
	if err := wait.PollUntilContextTimeout(ctx, 100*time.Millisecond, 20*time.Second, true, func(ctx context.Context) (bool, error) {
		_, _, resolveErr = resolver.Resolve(ctx, profileGVK)
		return resolveErr == nil, nil
	}); err != nil {
		t.Fatalf("wait for CRD schema publication: %v: %v", err, resolveErr)
	}
	const profile = `apiVersion: atomic.opensaola.test/v1
kind: Profile
metadata: {name: profile}
spec:
`
	const backend = profile + "  backend: {endpoint: primary}\n"
	const stages = profile + "  stages: [{name: first}]\n"
	const podTemplate = `apiVersion: apps/v1
kind: Deployment
metadata: {name: application}
spec:
  selector: {matchLabels: {app: application}}
  template:
    metadata: {labels: {app: application}}
    spec:
      volumes:
      - name: podinfo
        projected:
          sources:
          - downwardAPI:
              items:
              - path: name
                fieldRef: {fieldPath: metadata.name}
      containers:
      - name: application
        image: registry.k8s.io/pause:3.10
        volumeMounts:
        - {name: podinfo, mountPath: /etc/podinfo}
`
	const networkPolicy = `apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata: {name: database-ingress}
spec:
  podSelector: {matchLabels: {app: database}}
  ingress:
  - ports:
    - {port: 5432}
`
	const fieldProjection = `        projected:
          sources:
          - downwardAPI:
              items:
              - path: name
                fieldRef: {fieldPath: metadata.name}`
	tokenTemplate := strings.Replace(podTemplate, fieldProjection, `        projected:
          sources:
          - serviceAccountToken: {path: token}`, 1)
	explicitToken := strings.Replace(tokenTemplate, "{path: token}", "{path: token, expirationSeconds: 7200}", 1)
	downwardTemplate := strings.Replace(podTemplate, fieldProjection, `        downwardAPI:
          items:
          - path: name
            fieldRef: {fieldPath: metadata.name}`, 1)
	const role = `apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata: {name: read-config}
rules:
- {apiGroups: [""], resources: [configmaps], verbs: [get]}
`
	const limits = `apiVersion: v1
kind: LimitRange
metadata: {name: workloads}
spec:
  limits:
  - {type: Container, max: {cpu: "2"}}
`
	const ingressPort = "{.spec.ingress[0].ports[0].port}/{.spec.ingress[0].ports[0].protocol}"
	const projected = ".spec.template.spec.volumes[0].projected.sources[0]."
	const fieldRef = "downwardAPI.items[0].fieldRef."
	const projectedField = "{" + projected + fieldRef + "apiVersion}/{" + projected + fieldRef + "fieldPath}"
	const token = "{" + projected + "serviceAccountToken.path}/{" + projected + "serviceAccountToken.expirationSeconds}"
	const backendFields = "{.spec.backend.endpoint}/{.spec.backend.retries}/{.spec.backend.defaultPolicy.mode}"
	const limitFields = "{.spec.limits[0].type}/{.spec.limits[0].max.cpu}/{.spec.limits[0].default.cpu}/{.spec.limits[0].defaultRequest.cpu}"
	explicitTCP := strings.Replace(networkPolicy, "{port: 5432}", "{port: 5432, protocol: TCP}", 1)
	explicitDefault := strings.Replace(limits, `max: {cpu: "2"}`, `max: {cpu: "2"}, default: {cpu: "1"}, defaultRequest: {cpu: "500m"}`, 1)
	const setUDP = `[{"op":"add","path":"/spec/ingress/0/ports/0/protocol","value":"UDP"}]`
	for _, tc := range []struct {
		name, template, patch string
		// Accepted cases read and compare these fields on the stored object.
		// Rejections identify the affected field and must leave no target.
		fields, want, rejectedAt string
	}{
		{"networkpolicy_default_tcp", networkPolicy, "", ingressPort, "5432/TCP", ""},
		{"networkpolicy_omitted_protocol_injected_udp", networkPolicy, setUDP, ingressPort, "5432/UDP", ""},
		{"networkpolicy_explicit_tcp_rewritten", explicitTCP, setUDP, "", "", "ingress"},
		{"native_projected_downwardAPI", podTemplate, "", projectedField, "v1/metadata.name", ""},
		{"native_projected_undeclared_mode", podTemplate, `[{"op":"add","path":"/spec/template/spec/volumes/0/projected/sources/0/downwardAPI/items/0/mode","value":256}]`, projectedField + "/{" + projected + "downwardAPI.items[0].mode}", "v1/metadata.name/256", ""},
		{"native_token_default_expiration", tokenTemplate, "", token, "token/3600", ""},
		{"native_token_injected_expiration", tokenTemplate, `[{"op":"add","path":"/spec/template/spec/volumes/0/projected/sources/0/serviceAccountToken/expirationSeconds","value":7200}]`, token, "token/7200", ""},
		{"native_token_explicit_expiration", explicitToken, "", token, "token/7200", ""},
		{"native_token_explicit_rewritten", explicitToken, `[{"op":"replace","path":"/spec/template/spec/volumes/0/projected/sources/0/serviceAccountToken/expirationSeconds","value":3600}]`, "", "", "projected"},
		{"native_downward_api_version", downwardTemplate, "", "{.spec.template.spec.volumes[0].downwardAPI.items[0].fieldRef.apiVersion}/{.spec.template.spec.volumes[0].downwardAPI.items[0].fieldRef.fieldPath}", "v1/metadata.name", ""},
		{"native_changed_declared_path", podTemplate, `[{"op":"replace","path":"/spec/template/spec/volumes/0/projected/sources/0/downwardAPI/items/0/path","value":"other"}]`, "", "", "projected"},
		{"crd_atomic_map", backend, "", backendFields, "primary/3/safe", ""},
		{"crd_atomic_map_injected_optional_parent", backend, `[{"op":"add","path":"/spec/backend/policy","value":{"mode":"safe"}}]`, backendFields + "/{.spec.backend.policy.mode}", "primary/3/safe/safe", ""},
		{"crd_atomic_map_default_changed", backend, `[{"op":"replace","path":"/spec/backend/retries","value":4}]`, backendFields, "primary/4/safe", ""},
		{"crd_atomic_map_explicit_changed", backend, `[{"op":"replace","path":"/spec/backend/endpoint","value":"secondary"}]`, "", "", "backend"},
		{"crd_atomic_map_explicit_field_removed", backend, `[{"op":"remove","path":"/spec/backend/endpoint"}]`, "", "", "backend"},
		{"crd_atomic_map_unknown_injection", backend, `[{"op":"add","path":"/spec/backend/vendorOption","value":true}]`, backendFields + "/{.spec.backend.vendorOption}", "primary/3/safe/true", ""},
		{"crd_atomic_list", stages, "", "{.spec.stages[0].name}/{.spec.stages[0].enabled}", "first/true", ""},
		{"crd_atomic_list_extra_item", stages, `[{"op":"add","path":"/spec/stages/-","value":{"name":"second"}}]`, "", "", "stages"},
		{"crd_atomic_list_missing_item", stages, `[{"op":"remove","path":"/spec/stages/0"}]`, "", "", "stages"},
		{"crd_atomic_list_reordered", profile + "  stages: [{name: first}, {name: second}]\n", `[{"op":"move","from":"/spec/stages/0","path":"/spec/stages/1"}]`, "", "", "stages"},
		{"rbac_rule_injected_resource_names", role, `[{"op":"add","path":"/rules/0/resourceNames","value":["settings"]}]`, "{.rules[0].resources[0]}/{.rules[0].verbs[0]}/{.rules[0].resourceNames[0]}", "configmaps/get/settings", ""},
		{"rbac_explicit_verb_list_grew", role, `[{"op":"add","path":"/rules/0/verbs/-","value":"list"}]`, "", "", "rules"},
		{"limitrange_derived_default_and_request", limits, "", limitFields, "Container/2/2/2", ""},
		{"limitrange_explicit_defaults", explicitDefault, "", limitFields, "Container/2/1/500m", ""},
		{"limitrange_explicit_request_rewritten", explicitDefault, `[{"op":"replace","path":"/spec/limits/0/defaultRequest/cpu","value":"750m"}]`, "", "", "limits"},
		{"limitrange_max_rewritten", limits, `[{"op":"replace","path":"/spec/limits/0/max/cpu","value":"4"}]`, "", "", "limits"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			owner, cfg := newAtomicFixture(t, ctx, cli, tc.template)
			var target unstructured.Unstructured
			if err := yaml.Unmarshal([]byte(tc.template), &target); err != nil {
				t.Fatal(err)
			}
			target.SetNamespace(owner.Namespace)
			if tc.patch != "" {
				installConfigurationMutationWebhook(t, ctx, cli, owner.Namespace, tc.template, tc.patch)
			}
			preview := target.DeepCopy()
			if err := cli.Create(ctx, preview, client.DryRunAll); err != nil {
				t.Fatal(err)
			}
			key := client.ObjectKeyFromObject(&target)
			live := &unstructured.Unstructured{}
			live.SetGroupVersionKind(target.GroupVersionKind())
			result, err := Handle(ctx, cli, owner, consts.HandleActionPublish, cfg)
			if tc.rejectedAt != "" {
				if err == nil || !strings.Contains(err.Error(), tc.rejectedAt) || result != nil {
					t.Fatalf("expected rejection at %s, result=%v err=%v; preview spec=%v", tc.rejectedAt, result, err, preview.Object["spec"])
				}
				if err := cli.Get(ctx, key, live); !apierrors.IsNotFound(err) {
					t.Fatalf("rejected target was created: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("publish failed: %v; API dry-run spec=%v", err, preview.Object["spec"])
			}
			if err := cli.Get(ctx, key, live); err != nil {
				t.Fatal(err)
			}
			fields := jsonpath.New(tc.name)
			if err := fields.Parse(tc.fields); err != nil {
				t.Fatal(err)
			}
			var got strings.Builder
			if err := fields.Execute(&got, live.Object); err != nil {
				t.Fatal(err)
			}
			if got.String() != tc.want {
				t.Fatalf("stored %s = %q, want %q", tc.fields, got.String(), tc.want)
			}
			if result == nil || result.inventory == nil || result.inventory.ResourceUID != live.GetUID() {
				t.Fatal("no live inventory")
			}
			rv := live.GetResourceVersion()
			beforeReplay := writes.Load()
			if _, err := Handle(ctx, cli, owner, consts.HandleActionUpdate, cfg); err != nil {
				t.Fatal(err)
			}
			if delta := writes.Load() - beforeReplay; delta != 0 {
				t.Fatalf("unchanged configuration sent %d non-dry-run writes", delta)
			}
			if err := cli.Get(ctx, key, live); err != nil || live.GetResourceVersion() != rv {
				t.Fatalf("unchanged configuration rewrote its target: %v", err)
			}
		})
	}
	const restorePrevious = `[{"op":"replace","path":"/spec/limits/0/default/cpu","value":"1"},{"op":"replace","path":"/spec/limits/0/defaultRequest/cpu","value":"500m"}]`
	for _, tc := range []struct {
		name, dryRunPatch, commitPatch string
		wantDefault, wantRequest       string
	}{
		{"release_limits_to_apiserver_defaults", "", "", "2", "2"},
		{"release_limits_webhook_restores_previous_values", restorePrevious, restorePrevious, "1", "500m"},
		{"release_limits_webhook_changes_only_real_write", "", restorePrevious, "1", "500m"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			owner, cfg := newAtomicFixture(t, ctx, cli, explicitDefault)
			var observeRelease atomic.Bool
			var previews, commits atomic.Int32
			installConfigurationWebhook(t, ctx, cli, owner.Namespace, explicitDefault, func(req *admissionv1.AdmissionRequest) string {
				if !observeRelease.Load() || req.Operation != admissionv1.Update {
					return ""
				}
				// The API server has applied its native defaults before invoking
				// admission. Comparing old and submitted objects proves the writer
				// actually changed spec, even if admission restores the old values.
				var old, next corev1.LimitRange
				if json.Unmarshal(req.OldObject.Raw, &old) != nil || json.Unmarshal(req.Object.Raw, &next) != nil {
					t.Error("invalid LimitRange admission payload")
					return ""
				}
				if !limitCPUsMatch(&old, "2", "1", "500m") || !limitCPUsMatch(&next, "2", "2", "2") {
					t.Errorf("atomic limits did not change before admission: old=%+v new=%+v", old.Spec.Limits, next.Spec.Limits)
				}
				if req.DryRun != nil && *req.DryRun {
					previews.Add(1)
					return tc.dryRunPatch
				}
				commits.Add(1)
				return tc.commitPatch
			})
			if _, err := Handle(ctx, cli, owner, consts.HandleActionPublish, cfg); err != nil {
				t.Fatal(err)
			}
			read := func(max, def, request string) corev1.LimitRange {
				t.Helper()
				var live corev1.LimitRange
				if err := cli.Get(ctx, client.ObjectKey{Namespace: owner.Namespace, Name: "workloads"}, &live); err != nil {
					t.Fatal(err)
				}
				if !limitCPUsMatch(&live, max, def, request) {
					t.Fatalf("stored limits=%+v, want max/default/defaultRequest=%s/%s/%s", live.Spec.Limits, max, def, request)
				}
				return live
			}
			first := read("2", "1", "500m")
			cfg.Spec.Template = limits
			observeRelease.Store(true)
			result, err := Handle(ctx, cli, owner, consts.HandleActionUpdate, cfg)
			observeRelease.Store(false)
			if err != nil || result == nil || result.inventory == nil || result.inventory.ResourceUID != first.UID {
				t.Fatalf("release was not confirmed for original LimitRange: result=%v err=%v", result, err)
			}
			if previews.Load() == 0 || commits.Load() != 1 {
				t.Fatalf("expected a release preview and a single target write: previews=%d commits=%d", previews.Load(), commits.Load())
			}
			released := read("2", tc.wantDefault, tc.wantRequest)
			beforeReplay := writes.Load()
			if _, err := Handle(ctx, cli, owner, consts.HandleActionUpdate, cfg); err != nil {
				t.Fatal(err)
			}
			replayed := read("2", tc.wantDefault, tc.wantRequest)
			if released.ResourceVersion != replayed.ResourceVersion || writes.Load() != beforeReplay {
				t.Fatal("replaying the released declaration caused another write")
			}
			cfg.Spec.Template = strings.Replace(limits, `max: {cpu: "2"}`, `max: {cpu: "3"}`, 1)
			if _, err := Handle(ctx, cli, owner, consts.HandleActionUpdate, cfg); err != nil {
				t.Fatal(err)
			}
			read("3", "3", "3")
			t.Logf("observed max/default/defaultRequest 2/1/500m -> 2/%s/%s; repeat wrote nothing; new max produced 3/3/3", tc.wantDefault, tc.wantRequest)
		})
	}
	t.Run("release_request_but_keep_explicit_default", func(t *testing.T) {
		owner, cfg := newAtomicFixture(t, ctx, cli, explicitDefault)
		var rewrite atomic.Bool
		installConfigurationWebhook(t, ctx, cli, owner.Namespace, explicitDefault, func(req *admissionv1.AdmissionRequest) string {
			if rewrite.Load() && req.Operation == admissionv1.Update {
				return `[{"op":"replace","path":"/spec/limits/0/default/cpu","value":"1500m"}]`
			}
			return ""
		})
		if _, err := Handle(ctx, cli, owner, consts.HandleActionPublish, cfg); err != nil {
			t.Fatal(err)
		}
		cfg.Spec.Template = strings.Replace(explicitDefault, `, defaultRequest: {cpu: "500m"}`, "", 1)
		rewrite.Store(true)
		before := writes.Load()
		if result, err := Handle(ctx, cli, owner, consts.HandleActionUpdate, cfg); result != nil || err == nil || !strings.Contains(err.Error(), "limits") {
			t.Fatalf("expected rejection for still-declared default.cpu=1: result=%v err=%v", result, err)
		}
		var live corev1.LimitRange
		key := client.ObjectKey{Namespace: owner.Namespace, Name: "workloads"}
		if err := cli.Get(ctx, key, &live); err != nil {
			t.Fatal(err)
		}
		if writes.Load() != before || !limitCPUsMatch(&live, "2", "1", "500m") {
			t.Fatalf("failed preflight altered stored limits: %+v", live.Spec.Limits)
		}
		rewrite.Store(false)
		if _, err := Handle(ctx, cli, owner, consts.HandleActionUpdate, cfg); err != nil {
			t.Fatal(err)
		}
		if err := cli.Get(ctx, key, &live); err != nil {
			t.Fatal(err)
		}
		if !limitCPUsMatch(&live, "2", "1", "1") {
			t.Fatalf("expected request to default to the still-declared cpu limit: %+v", live.Spec.Limits)
		}
	})
	t.Run("server_supplement_does_not_authorize_atomic_deletion", func(t *testing.T) {
		owner, cfg := newAtomicFixture(t, ctx, cli, backend)
		installConfigurationMutationWebhook(t, ctx, cli, owner.Namespace, backend,
			`[{"op":"add","path":"/spec/backend/vendorOption","value":"retain"}]`)
		if _, err := Handle(ctx, cli, owner, consts.HandleActionPublish, cfg); err != nil {
			t.Fatal(err)
		}
		cfg.Spec.Template = profile + "  stages: [{name: first}]\n"
		before := writes.Load()
		if _, err := Handle(ctx, cli, owner, consts.HandleActionUpdate, cfg); err == nil || !strings.Contains(err.Error(), "backend") {
			t.Fatalf("expected conflict withdrawing the atomic map containing external fields, got %v", err)
		}
		if before != writes.Load() {
			t.Fatal("blocked deletion caused a write")
		}
		live := &unstructured.Unstructured{}
		live.SetGroupVersionKind(profileGVK)
		if err := cli.Get(ctx, client.ObjectKey{Namespace: owner.Namespace, Name: "profile"}, live); err != nil {
			t.Fatal(err)
		}
		vendor, _, err := unstructured.NestedString(live.Object, "spec", "backend", "vendorOption")
		if err != nil || vendor != "retain" {
			t.Fatalf("lost field injected at first publish: %q, %v", vendor, err)
		}
	})

	for _, onDryRun := range []bool{true, false} {
		name := "limitrange_update_preflight"
		if !onDryRun {
			name = "limitrange_update_commit"
		}
		t.Run(name, func(t *testing.T) {
			owner, cfg := newAtomicFixture(t, ctx, cli, explicitDefault)
			var mutate atomic.Bool
			var changes atomic.Int32
			installConfigurationWebhook(t, ctx, cli, owner.Namespace, limits, func(req *admissionv1.AdmissionRequest) string {
				dryRun := req.DryRun != nil && *req.DryRun
				if mutate.Load() && req.Operation == admissionv1.Update && dryRun == onDryRun {
					changes.Add(1)
					return `[{"op":"replace","path":"/spec/limits/0/max/cpu","value":"4"}]`
				}
				return ""
			})
			if _, err := Handle(ctx, cli, owner, consts.HandleActionPublish, cfg); err != nil {
				t.Fatal(err)
			}
			cfg.Spec.Template = strings.Replace(limits, `max: {cpu: "2"}`, `max: {cpu: "3"}`, 1)
			mutate.Store(true)
			result, err := Handle(ctx, cli, owner, consts.HandleActionUpdate, cfg)
			if changes.Load() == 0 || err == nil || !strings.Contains(err.Error(), "limits") || result != nil {
				t.Fatalf("expected rejection of changed explicit max, changes=%d, result=%v, err=%v", changes.Load(), result, err)
			}
			key := client.ObjectKey{Namespace: owner.Namespace, Name: "workloads"}
			readCPU := func(t *testing.T) string {
				t.Helper()
				var lr corev1.LimitRange
				if err := cli.Get(ctx, key, &lr); err != nil {
					t.Fatal(err)
				}
				if len(lr.Spec.Limits) != 1 {
					t.Fatalf("limits=%+v", lr.Spec.Limits)
				}
				return lr.Spec.Limits[0].Max.Cpu().String()
			}
			want := "2"
			if !onDryRun {
				want = "4"
			}
			if got := readCPU(t); got != want {
				t.Fatalf("stored max.cpu = %s, want %s", got, want)
			}
			mutate.Store(false)
			if _, err := Handle(ctx, cli, owner, consts.HandleActionUpdate, cfg); err != nil {
				t.Fatal(err)
			}
			if got := readCPU(t); got != "3" {
				t.Fatalf("retry left max.cpu = %s", got)
			}
		})
	}
}

func limitCPUsMatch(lr *corev1.LimitRange, max, def, request string) bool {
	if len(lr.Spec.Limits) != 1 {
		return false
	}
	item := lr.Spec.Limits[0]
	return item.Type == corev1.LimitTypeContainer && item.Max.Cpu().String() == max &&
		item.Default.Cpu().String() == def && item.DefaultRequest.Cpu().String() == request
}

func newAtomicFixture(t *testing.T, ctx context.Context, cli client.Client, template string) (*v1.MiddlewareOperator, *v1.MiddlewareConfiguration) {
	t.Helper()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "atomic-publish-"}}
	if err := cli.Create(ctx, ns); err != nil {
		t.Fatal(err)
	}
	owner := &v1.MiddlewareOperator{ObjectMeta: metav1.ObjectMeta{Name: "instance", Namespace: ns.Name}}
	if err := cli.Create(ctx, owner); err != nil {
		t.Fatal(err)
	}
	cfg := &v1.MiddlewareConfiguration{ObjectMeta: metav1.ObjectMeta{Name: ns.Name}}
	cfg.Spec.Template = template
	if err := cli.Create(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	return owner, cfg
}
