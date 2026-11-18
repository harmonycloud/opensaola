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
	"strings"
	"sync/atomic"
	"testing"
	"time"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	"github.com/harmonycloud/opensaola/internal/k8s"
	"github.com/harmonycloud/opensaola/internal/service/consts"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
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
	for _, tc := range []struct {
		name     string
		template string
		field    string
		patch    string
	}{
		{"networkpolicy_port_default_tcp", networkPolicy, "ingress", ""},
		{"networkpolicy_port_admission_udp", networkPolicy, "ingress", `[{"op":"add","path":"/spec/ingress/0/ports/0/protocol","value":"UDP"}]`},
		{"native_projected_downwardAPI", podTemplate, "projected", ""},
		{"native_projected_undeclared_mode", podTemplate, "projected", `[{"op":"add","path":"/spec/template/spec/volumes/0/projected/sources/0/downwardAPI/items/0/mode","value":256}]`},
		{"native_projected_token_expiration", tokenTemplate, "projected", ""},
		{"native_projected_token_wrong_default", tokenTemplate, "projected", `[{"op":"add","path":"/spec/template/spec/volumes/0/projected/sources/0/serviceAccountToken/expirationSeconds","value":7200}]`},
		{"native_projected_token_explicit", explicitToken, "projected", ""},
		{"native_projected_token_explicit_overwritten_by_default", explicitToken, "projected", `[{"op":"replace","path":"/spec/template/spec/volumes/0/projected/sources/0/serviceAccountToken/expirationSeconds","value":3600}]`},
		{"native_downward_api_version", downwardTemplate, "downwardAPI", ""},
		{"native_changed_declared_path", podTemplate, "projected", `[{"op":"replace","path":"/spec/template/spec/volumes/0/projected/sources/0/downwardAPI/items/0/path","value":"other"}]`},
		{"crd_atomic_map", backend, "backend", ""},
		{"crd_atomic_map_injected_optional_parent", backend, "backend", `[{"op":"add","path":"/spec/backend/policy","value":{"mode":"safe"}}]`},
		{"crd_atomic_map_default_changed", backend, "backend", `[{"op":"replace","path":"/spec/backend/retries","value":4}]`},
		{"crd_atomic_map_explicit_changed", backend, "backend", `[{"op":"replace","path":"/spec/backend/endpoint","value":"secondary"}]`},
		{"crd_atomic_map_unknown_injection", backend, "backend", `[{"op":"add","path":"/spec/backend/vendorOption","value":true}]`},
		{"crd_atomic_list", stages, "stages", ""},
		{"crd_atomic_list_extra_item", stages, "stages", `[{"op":"add","path":"/spec/stages/-","value":{"name":"second"}}]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "atomic-publish-"}}
			if err := cli.Create(ctx, ns); err != nil {
				t.Fatal(err)
			}
			owner := &v1.MiddlewareOperator{ObjectMeta: metav1.ObjectMeta{Name: "instance", Namespace: ns.Name}}
			if err := cli.Create(ctx, owner); err != nil {
				t.Fatal(err)
			}
			cfg := &v1.MiddlewareConfiguration{ObjectMeta: metav1.ObjectMeta{Name: ns.Name}}
			cfg.Spec.Template = tc.template
			if err := cli.Create(ctx, cfg); err != nil {
				t.Fatal(err)
			}
			var target unstructured.Unstructured
			if err := yaml.Unmarshal([]byte(tc.template), &target); err != nil {
				t.Fatal(err)
			}
			target.SetNamespace(ns.Name)
			if tc.patch != "" {
				installConfigurationMutationWebhook(t, ctx, cli, ns.Name, tc.template, tc.patch)
			}
			preview := target.DeepCopy()
			if err := cli.Create(ctx, preview, client.DryRunAll); err != nil {
				t.Fatal(err)
			}
			key := client.ObjectKeyFromObject(&target)
			live := &unstructured.Unstructured{}
			live.SetGroupVersionKind(target.GroupVersionKind())
			result, err := Handle(ctx, cli, owner, consts.HandleActionPublish, cfg)
			if tc.patch != "" {
				if err == nil || !strings.Contains(err.Error(), tc.field) || result != nil {
					t.Fatalf("expected rejection at %s, result=%v err=%v; preview spec=%v", tc.field, result, err, preview.Object["spec"])
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
			switch tc.field {
			case "ingress":
				var policy networkingv1.NetworkPolicy
				if err := runtime.DefaultUnstructuredConverter.FromUnstructured(live.Object, &policy); err != nil {
					t.Fatal(err)
				}
				if len(policy.Spec.Ingress) != 1 || len(policy.Spec.Ingress[0].Ports) != 1 {
					t.Fatalf("expected one ingress rule with one port, got %+v", policy.Spec.Ingress)
				}
				port := policy.Spec.Ingress[0].Ports[0]
				if port.Port == nil || port.Protocol == nil || *port.Protocol != corev1.ProtocolTCP || port.Port.IntValue() != 5432 {
					t.Fatalf("unexpected ingress port: %v", port)
				}
			case "backend":
				n, found, err := unstructured.NestedInt64(live.Object, "spec", "backend", "retries")
				if err != nil || !found || n != 3 {
					t.Fatalf("missing CRD default: %v", live.Object["spec"])
				}
				mode, found, err := unstructured.NestedString(live.Object, "spec", "backend", "defaultPolicy", "mode")
				if err != nil || !found || mode != "safe" {
					t.Fatalf("expected the parent default and nested default: %v", live.Object["spec"])
				}
				if _, found, err := unstructured.NestedMap(live.Object, "spec", "backend", "policy"); err != nil || found {
					t.Fatalf("undeclared optional parent should remain absent: %v", live.Object["spec"])
				}
				endpoint, _, err := unstructured.NestedString(live.Object, "spec", "backend", "endpoint")
				if err != nil || endpoint != "primary" {
					t.Fatalf("explicit endpoint changed: %v", live.Object["spec"])
				}
			case "stages":
				items, _, err := unstructured.NestedSlice(live.Object, "spec", "stages")
				if err != nil || len(items) != 1 {
					t.Fatalf("expected exactly one item in spec.stages, got %v (error %v)", items, err)
				}
				stage, ok := items[0].(map[string]any)
				if !ok || stage["name"] != "first" || stage["enabled"] != true {
					t.Fatalf("expected spec.stages[0] to contain name=first and enabled=true, got %v", items[0])
				}
			case "downwardAPI", "projected":
				var workload appsv1.Deployment
				if err := runtime.DefaultUnstructuredConverter.FromUnstructured(live.Object, &workload); err != nil {
					t.Fatal(err)
				}
				volumes := workload.Spec.Template.Spec.Volumes
				if len(volumes) != 1 || volumes[0].Name != "podinfo" {
					t.Fatalf("expected one podinfo volume, got %+v", volumes)
				}
				volume := volumes[0]
				if tc.field == "projected" && (volume.Projected == nil || len(volume.Projected.Sources) != 1) {
					t.Fatalf("expected one podinfo projected source, got %+v", volume.Projected)
				}
				if tc.template == tokenTemplate || tc.template == explicitToken {
					expected := int64(3600)
					if tc.template == explicitToken {
						expected = 7200
					}
					token := volume.Projected.Sources[0].ServiceAccountToken
					if token == nil || token.ExpirationSeconds == nil || *token.ExpirationSeconds != expected {
						t.Fatalf("expected token expiration of %d seconds, got %+v", expected, token)
					}
					break
				}
				var items []corev1.DownwardAPIVolumeFile
				switch {
				case tc.field == "downwardAPI" && volume.DownwardAPI != nil:
					items = volume.DownwardAPI.Items
				case tc.field == "projected" && volume.Projected.Sources[0].DownwardAPI != nil:
					items = volume.Projected.Sources[0].DownwardAPI.Items
				}
				if len(items) != 1 || items[0].FieldRef == nil {
					t.Fatalf("expected one downwardAPI item with fieldRef in %s, got %+v", tc.field, items)
				}
				ref := items[0].FieldRef
				if ref.APIVersion != "v1" || ref.FieldPath != "metadata.name" {
					t.Fatalf("unexpected field selector: %v", ref)
				}
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
}
