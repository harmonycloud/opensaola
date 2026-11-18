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

package k8s

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func runDeclaredPlanAPIRegressions(t *testing.T, ctx context.Context, cli client.Client, resolver IntentSchemaResolver, namespace string) {
	newDeployment := func(t *testing.T, pod map[string]any) *deletionAPIFixture {
		f := newDeletionAPIFixture(t, ctx, cli, resolver, schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}, namespace)
		f.desired.Object["spec"] = map[string]any{"selector": map[string]any{"matchLabels": map[string]any{"app": "fixture"}}, "template": map[string]any{"metadata": map[string]any{"labels": map[string]any{"app": "fixture"}}, "spec": pod}}
		return f
	}
	for _, handler := range []string{"exec", "httpGet", "grpc", "tcpSocket"} {
		t.Run("withdraw_native_probe_"+handler, func(t *testing.T) {
			handlers := map[string]any{"exec": map[string]any{"command": []any{"true"}}, "httpGet": map[string]any{"port": int64(8080)}, "grpc": map[string]any{"port": int64(9090)}, "tcpSocket": map[string]any{"port": int64(8080)}}
			probe := map[string]any{handler: handlers[handler], "periodSeconds": int64(20)}
			container := map[string]any{"name": "worker", "image": "example:v1", "readinessProbe": probe}
			f := newDeployment(t, map[string]any{"containers": []any{container}})
			f.publish()
			delete(probe, "periodSeconds")
			f.publish()
			check := func() map[string]any {
				t.Helper()
				items, _, err := unstructured.NestedSlice(f.get().Object, "spec", "template", "spec", "containers")
				if err != nil || len(items) != 1 {
					t.Fatalf("invalid fixture containers: %v", err)
				}
				return intentSchemaTestMap(t, items[0])
			}
			got := intentSchemaTestMap(t, check(), "readinessProbe")
			if got["periodSeconds"] != int64(10) || got[handler] == nil {
				t.Fatal("defaulted probe lost handler or period")
			}
			before := f.get()
			f.publish()
			f.assertUnchanged(before)
			delete(container, "readinessProbe")
			f.publish()
			if _, found := check()["readinessProbe"]; found {
				t.Fatal("withdrawn probe survived")
			}
			before = f.get()
			f.publish()
			f.assertUnchanged(before)
		})
	}
	t.Run("external_probe_change_blocks_whole_removal", func(t *testing.T) {
		c := map[string]any{"name": "worker", "image": "example:v1", "readinessProbe": map[string]any{"exec": map[string]any{"command": []any{"true"}}}}
		f := newDeployment(t, map[string]any{"containers": []any{c}})
		f.publish()
		// Only OpenSaola originally submitted the probe; then another writer
		// overrides one of its captured defaults.
		live := f.get()
		items, _, err := unstructured.NestedSlice(live.Object, "spec", "template", "spec", "containers")
		if err != nil || len(items) != 1 {
			t.Fatal("missing test container")
		}
		intentSchemaTestMap(t, items[0], "readinessProbe")["timeoutSeconds"] = int64(7)
		f.set(live, items, "spec", "template", "spec", "containers")
		f.update(live)
		before := f.get()
		delete(c, "readinessProbe")
		c["image"] = "example:v2"
		if err := f.apply(); err == nil || !strings.Contains(err.Error(), "timeoutSeconds") {
			t.Fatalf("expected conflicted probe deletion: %v", err)
		}
		f.assertUnchanged(before)
	})
	t.Run("native_init_execution_order_survives_external_steps_and_restart", func(t *testing.T) {
		entry := func(name string) map[string]any { return map[string]any{"name": name, "image": "example:v1"} }
		init := func(names ...string) []any {
			r := []any{}
			for _, n := range names {
				r = append(r, entry(n))
			}
			return r
		}
		path := []string{"spec", "template", "spec", "initContainers"}
		pod := map[string]any{"containers": []any{entry("main")}, "initContainers": init("migrate", "prepare")}
		f := newDeployment(t, pod)
		f.publish()
		external := f.get()
		f.set(external, init("before", "migrate", "middle", "prepare", "after"), path...)
		f.update(external)
		pod["initContainers"] = init("prepare", "bootstrap", "migrate")
		f.publish()
		updated := f.get()
		items, _, err := unstructured.NestedSlice(updated.Object, path...)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, item := range items {
			name, _ := intentSchemaTestMap(t, item)["name"].(string)
			names = append(names, name)
		}
		if !reflect.DeepEqual(names, []string{"before", "prepare", "middle", "bootstrap", "after", "migrate"}) {
			t.Fatalf("unexpected merged execution order %v", names)
		}
		f.publish()
		f.assertUnchanged(updated)
		converter, _, err := resolver.Resolve(ctx, f.desired.GroupVersionKind())
		if err != nil {
			t.Fatal(err)
		}
		plan, err := NewDeclaredValidationPlan(converter, intentObject(f.desired), nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := plan.Validate(converter, intentObject(updated)); err != nil {
			t.Fatal(err)
		}
		items[1], items[3] = items[3], items[1]
		f.set(updated, items, path...)
		if err := plan.Validate(converter, intentObject(updated)); err == nil {
			t.Fatal("restored plan accepted a changed execution order")
		}
	})
	t.Run("native_replicas_default_accepted_after_removal", func(t *testing.T) {
		f := newDeletionAPIFixture(t, ctx, cli, resolver, schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}, namespace)
		f.desired.Object["spec"] = map[string]any{
			"replicas": int64(3), "selector": map[string]any{"matchLabels": map[string]any{"app": "test"}},
			"template": map[string]any{"metadata": map[string]any{"labels": map[string]any{"app": "test"}},
				"spec": map[string]any{"containers": []any{map[string]any{"name": "main", "image": "registry.k8s.io/pause:3.10"}}}},
		}
		volume := map[string]any{"name": "credential", "secret": map[string]any{"secretName": "credential", "defaultMode": int64(0400)}}
		f.set(f.desired, []any{volume}, "spec", "template", "spec", "volumes")
		f.publish()
		unstructured.RemoveNestedField(volume, "secret", "defaultMode")
		f.set(f.desired, []any{volume}, "spec", "template", "spec", "volumes")
		unstructured.RemoveNestedField(f.desired.Object, "spec", "replicas")
		f.publish()
		actual := f.get()
		replicas, found, err := unstructured.NestedInt64(actual.Object, "spec", "replicas")
		if err != nil || !found || replicas != 1 {
			t.Fatalf("expected native replicas default of 1, got %v (present=%v): %v", replicas, found, err)
		}
		volumes, _, err := unstructured.NestedSlice(actual.Object, "spec", "template", "spec", "volumes")
		expected := []any{map[string]any{"name": "credential", "secret": map[string]any{"secretName": "credential", "defaultMode": int64(0644)}}}
		if err != nil || !reflect.DeepEqual(volumes, expected) {
			t.Fatalf("expected Kubernetes volume default and original secret name, got %v: %v", volumes, err)
		}
		f.publish()
		if rv := f.get().GetResourceVersion(); rv != actual.GetResourceVersion() {
			t.Fatalf("repeated reconcile wrote native default, resourceVersion changed from %s to %s", actual.GetResourceVersion(), rv)
		}
	})
	t.Run("granular_ssa_env_preserves_container_and_allows_volume_mount_removal", func(t *testing.T) {
		f := newDeletionAPIFixture(t, ctx, cli, resolver, typedDeletionGVK, namespace)
		f.desired.Object["spec"] = map[string]any{"containers": []any{map[string]any{"name": "broker", "volumeMounts": []any{map[string]any{"name": "log", "mountPath": "/logs"}}}}}
		f.publish()
		claim := cleanApplyObject(f.desired)
		external := map[string]any{"name": "broker", "env": []any{map[string]any{"name": "external", "value": "new"}}}
		claim.Object["spec"] = map[string]any{"containers": []any{external}}
		claim.SetOwnerReferences(nil)
		if err := cli.Patch(ctx, claim, client.Apply, client.FieldOwner("external-agent")); err != nil {
			t.Fatal(err)
		}
		unstructured.RemoveNestedField(f.desired.Object, "spec", "containers")
		f.publish()
		containers, _, err := unstructured.NestedSlice(f.get().Object, "spec", "containers")
		if err != nil || !reflect.DeepEqual(containers, []any{external}) {
			t.Fatalf("expected exactly the externally managed container fields, got %v: %v", containers, err)
		}
	})
}
