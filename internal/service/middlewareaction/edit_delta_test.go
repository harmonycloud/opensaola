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

package middlewareaction

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/harmonycloud/opensaola/internal/k8s"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/managedfields"
	"k8s.io/kube-openapi/pkg/validation/spec"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

type actionTestSchema struct{ converter managedfields.TypeConverter }

func (s actionTestSchema) Resolve(context.Context, schema.GroupVersionKind) (managedfields.TypeConverter, string, error) {
	return s.converter, "fixture", nil
}

func TestActionEditWritesOnlyExplicitDelta(t *testing.T) {
	var def spec.Schema
	if err := json.Unmarshal([]byte(`{
        "type":"object",
        "x-kubernetes-group-version-kind":[{"group":"example.org","version":"v1","kind":"Fixture"}],
        "x-kubernetes-preserve-unknown-fields":true,
        "properties":{"spec":{"type":"object","x-kubernetes-preserve-unknown-fields":true,"properties":{
            "tags":{"type":"array","x-kubernetes-list-type":"set","items":{"type":"string"}},
            "config":{"type":"object","x-kubernetes-map-type":"atomic","additionalProperties":{"type":"string"}}
        }}}
    }`), &def); err != nil {
		t.Fatal(err)
	}
	converter, err := managedfields.NewTypeConverter(map[string]*spec.Schema{"example.Fixture": &def}, false)
	if err != nil {
		t.Fatal(err)
	}
	k8s.ConfigureResourceIntents(actionTestSchema{converter})
	t.Cleanup(func() { k8s.ConfigureResourceIntents(nil) })
	target := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "example.org/v1", "kind": "Fixture", "metadata": map[string]any{"name": "test", "namespace": "default"}}}
	live := target.DeepCopy()
	live.SetUID("uid")
	live.SetResourceVersion("7")
	live.SetAnnotations(map[string]string{k8s.AnnotationResourceIntent: "current"})
	live.Object["status"] = map[string]any{"observed": "keep"}
	live.Object["spec"] = map[string]any{"counter": int64(9007199254740993), "containers": []any{map[string]any{"name": "broker", "image": "old", "external": "keep"}}}
	var got map[string]string
	cli := fake.NewClientBuilder().WithObjects(live).WithInterceptorFuncs(interceptor.Funcs{Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
		t.Helper()
		if patch.Type() != types.JSONPatchType {
			t.Fatalf("unexpected patch type %s", patch.Type())
		}
		options := (&client.PatchOptions{}).ApplyOptions(opts)
		if options.FieldManager != "opensaola-action" || options.Force != nil {
			t.Fatalf("unexpected field manager or force option")
		}
		b, err := patch.Data(obj)
		if err != nil {
			return err
		}
		var ops []struct {
			Op, Path string
			Value    json.RawMessage
		}
		if err = json.Unmarshal(b, &ops); err != nil {
			return err
		}
		got = map[string]string{}
		for _, op := range ops {
			got[op.Op+" "+op.Path] = string(op.Value)
		}
		return nil
	}}).Build()
	out := map[string]any{
		"metadata": map[string]any{"annotations": map[string]any{"ticket": "review"}},
		"status":   map[string]any{"step": "ready"},
		"spec":     map[string]any{"containers": []any{map[string]any{"name": "broker", "image": "new"}}, "counter": int64(9007199254740995)},
	}
	if err := editActionResource(context.Background(), cli, target, map[string]string{"action": "done"}, nil, out); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"test /metadata/uid": `"uid"`, "test /metadata/resourceVersion": `"7"`,
		"add /metadata/labels": `{"action":"done"}`, "add /metadata/annotations/ticket": `"review"`,
		"add /status/step": `"ready"`, "replace /spec/counter": "9007199254740995", "replace /spec/containers/0/image": `"new"`,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("action patch = %v, want %v", got, want)
	}
	t.Run("schema set and atomic map", func(t *testing.T) {
		obj := target.DeepCopy()
		obj.Object["spec"] = map[string]any{"tags": []any{"keep", "old"}, "config": map[string]any{"external": "keep", "setting": "old"}}
		edit := map[string]any{"spec": map[string]any{"tags": []any{"added", "old"}, "config": map[string]any{"setting": "new"}}}
		merged, err := mergeActionOutput(context.Background(), actionTestSchema{converter}, obj, edit)
		if err != nil {
			t.Fatal(err)
		}
		tags, _, err := unstructured.NestedStringSlice(merged.Object, "spec", "tags")
		if err != nil || !reflect.DeepEqual(tags, []string{"keep", "old", "added"}) {
			t.Fatalf("tags = %v, %v", tags, err)
		}
		config, _, err := unstructured.NestedStringMap(merged.Object, "spec", "config")
		if err != nil || config["setting"] != "new" || config["external"] != "keep" {
			t.Fatalf("config = %v, %v", config, err)
		}
	})

	got = nil
	if err := editActionResource(context.Background(), cli, target, nil, nil, map[string]any{"spec": map[string]any{"containers": []any{}}}); err != nil || got != nil {
		t.Fatalf("empty overlay unexpectedly wrote: %v, %v", got, err)
	}
	for _, bad := range []map[string]any{{"metadata": map[string]any{"annotations": map[string]any{k8s.AnnotationResourceIntent: "other"}}}, {"metadata": map[string]any{"annotations": map[string]any{k8s.AnnotationResourceIntentOperation: "forged"}}}, {"metadata": map[string]any{"resourceVersion": "9"}}, {"kind": "Service"}} {
		if err := editActionResource(context.Background(), cli, target, nil, nil, bad); err == nil || got != nil {
			t.Fatalf("unsafe edit reached API: %v", bad)
		}
	}
}

func TestActionProtectsHistorySecretAndAllowsBusinessSecret(t *testing.T) {
	for _, labels := range []map[string]string{nil, {"middleware.cn/resource-intent-record": "owner"}, {"middleware.cn/field-history": "v1"}} {
		secret := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]any{"name": "fixture", "namespace": "default"}}}
		secret.SetLabels(labels)
		secret.SetUID("uid")
		secret.SetResourceVersion("42")
		changed := secret.DeepCopy()
		changed.Object["data"] = map[string]any{"setting": "Yg=="}
		var wrote bool
		cli := fake.NewClientBuilder().WithInterceptorFuncs(interceptor.Funcs{Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, p client.Patch, opts ...client.PatchOption) error {
			wrote = true
			return nil
		}}).Build()
		err := k8s.PatchActionResource(context.Background(), cli, secret, changed)
		if (err == nil) != (labels == nil) || wrote != (labels == nil) {
			t.Fatalf("action edit of Secret with labels %v: wrote=%t, err=%v", labels, wrote, err)
		}
		if err := k8s.ValidateActionMetadata(nil, changed); (err == nil) != (labels == nil) {
			t.Fatalf("reserved labels on create: %v, err=%v", labels, err)
		}
		if labels != nil {
			changed.SetLabels(nil)
			if err := k8s.ValidateActionMetadata(secret, changed); err == nil {
				t.Fatal("action stripped history labels")
			}
		}
	}
}
