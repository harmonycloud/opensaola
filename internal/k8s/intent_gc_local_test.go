//go:build integration

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

package k8s_test

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	"github.com/harmonycloud/opensaola/internal/k8s"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Test scans are limited to the namespace created by this subtest; the production
// collector uses the direct APIReader and scans the dedicated label cluster-wide.
type localGCReader struct {
	client.Reader
	namespace string
}

func (r localGCReader) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	return r.Reader.List(ctx, list, append(opts, client.InNamespace(r.namespace))...)
}
func localGC(cli client.Client, namespace string) *k8s.ResourceIntentCollector {
	c := k8s.NewResourceIntentCollector(cli, localGCReader{Reader: cli, namespace: namespace})
	c.MinAge = 0
	return c
}

func localGCPrepared(t *testing.T, ctx context.Context, cli client.Client, owner *v1.Middleware, live *unstructured.Unstructured) *corev1.Secret {
	t.Helper()
	current := &corev1.Secret{}
	if err := cli.Get(ctx, client.ObjectKey{Namespace: owner.Namespace, Name: live.GetAnnotations()[k8s.AnnotationResourceIntent]}, current); err != nil {
		t.Fatal(err)
	}
	var record map[string]any
	raw := current.Data["intent.json"]
	if packed, ok := current.Data["intent.json.zst"]; ok {
		decoder, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1))
		if err != nil {
			t.Fatal(err)
		}
		defer decoder.Close()
		raw, err = decoder.DecodeAll(packed, nil)
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	record["baseResourceVersion"] = live.GetResourceVersion()
	record["publicationProtocol"] = 1
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	prepared := current.DeepCopy()
	prepared.ObjectMeta = metav1.ObjectMeta{GenerateName: "gc-prepared-", Namespace: owner.Namespace, Labels: current.Labels, OwnerReferences: current.OwnerReferences}
	prepared.Data = map[string][]byte{"intent.json": raw}
	if err := cli.Create(ctx, prepared, client.FieldOwner(v1.FieldOwner)); err != nil {
		t.Fatal(err)
	}
	return prepared
}

func localGCPointerPatch(t *testing.T, live *unstructured.Unstructured, name string) client.Patch {
	t.Helper()
	operations := []map[string]any{
		{"op": "test", "path": "/metadata/uid", "value": string(live.GetUID())},
		{"op": "test", "path": "/metadata/resourceVersion", "value": live.GetResourceVersion()},
		{"op": "add", "path": "/metadata/annotations/middleware.cn~1resource-intent", "value": name},
	}
	raw, err := json.Marshal(operations)
	if err != nil {
		t.Fatal(err)
	}
	return client.RawPatch(types.JSONPatchType, raw)
}

func localGCCheckBusiness(t *testing.T, before, after *unstructured.Unstructured) {
	t.Helper()
	beforeLabels, afterLabels := before.GetLabels(), after.GetLabels()
	delete(beforeLabels, k8s.LabelResourceIntentGCFence)
	delete(afterLabels, k8s.LabelResourceIntentGCFence)
	if before.GetUID() != after.GetUID() || before.GetGeneration() != after.GetGeneration() || !maps.Equal(beforeLabels, afterLabels) || !reflect.DeepEqual(before.GetOwnerReferences(), after.GetOwnerReferences()) {
		t.Fatalf("collector changed business identity or generation: generation %d -> %d", before.GetGeneration(), after.GetGeneration())
	}
	for key, value := range before.Object {
		if key != "metadata" && key != "status" && !reflect.DeepEqual(value, after.Object[key]) {
			t.Fatalf("collector changed field %s", key)
		}
	}
	for key, value := range before.GetAnnotations() {
		if after.GetAnnotations()[key] != value {
			t.Fatalf("collector changed annotation %s", key)
		}
	}
}

func runLocalIntentGCTests(t *testing.T, cli client.Client, w *k8s.ManagedResourceWriter) {
	t.Helper()
	for _, tc := range []struct {
		name  string
		apply func(context.Context, client.Client, *unstructured.Unstructured) error
	}{
		{name: "update", apply: k8s.UpdateCustomResource}, {name: "patch", apply: k8s.PatchCustomResource},
	} {
		t.Run("stale_action_"+tc.name+"_cannot_restore_deleted_record", func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			owner := localIntentOwner(t, ctx, cli)
			desired := localIntentObject(owner, "v1", "ConfigMap", "action-target")
			desired.Object["data"] = map[string]any{"KEEP": "before"}
			if err := w.Reconcile(ctx, owner, desired, ""); err != nil {
				t.Fatal(err)
			}
			stale := localIntentGet(t, ctx, cli, desired)
			oldRecord := stale.GetAnnotations()[k8s.AnnotationResourceIntent]
			desired.Object["data"] = map[string]any{"KEEP": "after"}
			if err := w.Reconcile(ctx, owner, desired, ""); err != nil {
				t.Fatal(err)
			}
			if err := cli.Get(ctx, client.ObjectKey{Namespace: owner.Namespace, Name: oldRecord}, &corev1.Secret{}); !apierrors.IsNotFound(err) {
				t.Fatalf("old record remained: %v", err)
			}
			if err := tc.apply(ctx, cli, stale); !apierrors.IsConflict(err) {
				t.Fatalf("stale action was not rejected: %v", err)
			}
			latest := localIntentGet(t, ctx, cli, desired)
			if latest.GetAnnotations()[k8s.AnnotationResourceIntent] == oldRecord {
				t.Fatal("stale action restored deleted record")
			}
			data, _, _ := unstructured.NestedStringMap(latest.Object, "data")
			if data["KEEP"] != "after" {
				t.Fatal("stale action reverted business data")
			}
			if err := tc.apply(ctx, cli, latest); err != nil {
				t.Fatal("fresh Action Apply failed", err)
			}
		})
	}
	for _, mode := range []string{"fence_delayed_json_patch", "fence_delayed_ssa", "changed_rv_no_fence", "target_absent", "target_replaced", "owner_gc", "repeat_updates_keep_one_record"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			owner := localIntentOwner(t, ctx, cli)
			desired := localIntentObject(owner, "v1", "ConfigMap", "gc-target")
			desired.Object["data"] = map[string]any{"KEEP": "stable", "AUTH": "on"}
			if err := w.Reconcile(ctx, owner, desired, ""); err != nil {
				t.Fatal(err)
			}
			live := localIntentGet(t, ctx, cli, desired)
			collector := localGC(cli, owner.Namespace)
			// Even a zero-grace scan must never delete the current baseline.
			if result, err := collector.Sweep(ctx); err != nil || result.Deleted != 0 || result.Kept != 1 {
				t.Fatalf("current scan: %+v %v", result, err)
			}
			if mode == "repeat_updates_keep_one_record" {
				for i := 0; i < 4; i++ {
					desired.Object["data"] = map[string]any{"KEEP": fmt.Sprint(i)}
					if err := w.Reconcile(ctx, owner, desired, ""); err != nil {
						t.Fatal(err)
					}
					var records corev1.SecretList
					if err := cli.List(ctx, &records, client.InNamespace(owner.Namespace), client.HasLabels{"middleware.cn/resource-intent-record"}); err != nil {
						t.Fatal(err)
					}
					if len(records.Items) != 1 {
						t.Fatalf("update retained %d records", len(records.Items))
					}
				}
				return
			}
			prepared := localGCPrepared(t, ctx, cli, owner, live)
			switch mode {
			case "changed_rv_no_fence":
				localIntentPatch(t, ctx, cli, desired, "external", `[{"op":"add","path":"/data/EXTERNAL","value":"keep"}]`)
				live = localIntentGet(t, ctx, cli, desired)
			case "target_absent", "target_replaced":
				if err := cli.Delete(ctx, live); err != nil {
					t.Fatal(err)
				}
				if mode == "target_replaced" {
					replacement := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: owner.Namespace, Name: desired.GetName()}, Data: map[string]string{"EXTERNAL": "replacement"}}
					if err := cli.Create(ctx, replacement, client.FieldOwner("external")); err != nil {
						t.Fatal(err)
					}
					live = localIntentGet(t, ctx, cli, desired)
				}
			case "owner_gc":
				if err := cli.Delete(ctx, owner); err != nil {
					t.Fatal(err)
				}
				if err := wait.PollUntilContextTimeout(ctx, 200*time.Millisecond, 30*time.Second, true, func(ctx context.Context) (bool, error) {
					var records corev1.SecretList
					if err := cli.List(ctx, &records, client.InNamespace(owner.Namespace), client.HasLabels{"middleware.cn/resource-intent-record"}); err != nil {
						return false, err
					}
					return len(records.Items) == 0, nil
				}); err != nil {
					t.Fatal("owner GC did not remove records", err)
				}
				return
			}
			result, err := collector.Sweep(ctx)
			if err != nil || result.Deleted < 1 {
				t.Fatalf("unreferenced scan: %+v %v", result, err)
			}
			if err := cli.Get(ctx, client.ObjectKeyFromObject(prepared), &corev1.Secret{}); !apierrors.IsNotFound(err) {
				t.Fatalf("prepared record still exists: %v", err)
			}
			if mode == "target_absent" {
				if result.Deleted != 2 {
					t.Fatalf("target's two records not collected: %+v", result)
				}
				return
			}
			latest := localIntentGet(t, ctx, cli, desired)
			localGCCheckBusiness(t, live, latest)
			if mode == "changed_rv_no_fence" || mode == "target_replaced" {
				if latest.GetResourceVersion() != live.GetResourceVersion() {
					t.Fatal("collector unnecessarily wrote target")
				}
				return
			}
			if latest.GetLabels()[k8s.LabelResourceIntentGCFence] == "" {
				t.Fatal("no publication fence")
			}
			// The resource write was prepared before collection but arrives afterward.
			if mode == "fence_delayed_json_patch" {
				if err := cli.Patch(ctx, live.DeepCopy(), localGCPointerPatch(t, live, prepared.Name), client.FieldOwner(v1.FieldOwner)); err == nil {
					t.Fatal("delayed JSON Patch published deleted record")
				}
			} else {
				apply := desired.DeepCopy()
				apply.SetUID(live.GetUID())
				apply.SetResourceVersion(live.GetResourceVersion())
				a := apply.GetAnnotations()
				if a == nil {
					a = map[string]string{}
				}
				a[k8s.AnnotationResourceIntent] = prepared.Name
				apply.SetAnnotations(a)
				if err := cli.Patch(ctx, apply, client.Apply, client.FieldOwner(v1.FieldOwner), client.ForceOwnership); err == nil {
					t.Fatal("delayed SSA published deleted record")
				}
			}
			latest = localIntentGet(t, ctx, cli, desired)
			if latest.GetAnnotations()[k8s.AnnotationResourceIntent] != live.GetAnnotations()[k8s.AnnotationResourceIntent] {
				t.Fatal("current pointer lost")
			}
			// Future legitimate changes continue using the retained current baseline.
			desired.Object["data"] = map[string]any{"KEEP": "after"}
			if err := w.Reconcile(ctx, owner, desired, ""); err != nil {
				t.Fatal(err)
			}
			latest = localIntentGet(t, ctx, cli, desired)
			data, _, _ := unstructured.NestedStringMap(latest.Object, "data")
			if data["KEEP"] != "after" || data["AUTH"] != "" {
				t.Fatalf("update after fence failed: %v", data)
			}
		})
	}

	t.Run("initialization_retries_with_fresh_record_after_gc", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		owner := localIntentOwner(t, ctx, cli)
		desired := localIntentObject(owner, "v1", "ConfigMap", "gc-init")
		desired.Object["data"] = map[string]any{"KEEP": "stable"}
		wrapped := &localGCInitClient{Client: cli, collector: localGC(cli, owner.Namespace)}
		writer := &k8s.ManagedResourceWriter{Client: wrapped, Reader: cli, IntentSchemas: w.IntentSchemas}
		if err := writer.Reconcile(ctx, owner, desired, ""); err != nil {
			t.Fatal(err)
		}
		if len(wrapped.prepared) != 2 || wrapped.prepared[0] == wrapped.prepared[1] {
			t.Fatalf("expected fresh publication after fence: %v", wrapped.prepared)
		}
		live := localIntentGet(t, ctx, cli, desired)
		if live.GetAnnotations()[k8s.AnnotationResourceIntent] != wrapped.prepared[1] {
			t.Fatal("writer referenced deleted first record")
		}
		if err := cli.Get(ctx, client.ObjectKey{Namespace: owner.Namespace, Name: wrapped.prepared[0]}, &corev1.Secret{}); !apierrors.IsNotFound(err) {
			t.Fatalf("first record not collected: %v", err)
		}
	})
	for _, clusterScoped := range []bool{false, true} {
		name := "cross_namespace"
		if clusterScoped {
			name = "cluster_scoped"
		}
		t.Run(name+"_record_collection", func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			owner := localIntentOwner(t, ctx, cli)
			obj := localIntentObject(owner, "v1", "ConfigMap", "gc-cross-target")
			if clusterScoped {
				obj.SetKind("Namespace")
				obj.SetNamespace("")
				obj.SetName("gc-target-" + owner.Namespace)
				t.Cleanup(func() {
					cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					defer cancel()
					if err := cli.Delete(cleanupCtx, obj); client.IgnoreNotFound(err) != nil {
						t.Error(err)
					}
				})
			} else {
				targetOwner := localIntentOwner(t, ctx, cli)
				obj.SetNamespace(targetOwner.Namespace)
				obj.Object["data"] = map[string]any{"KEEP": "stable"}
			}
			obj.SetOwnerReferences(nil)
			obj.SetLabels(map[string]string{v1.LabelApp: owner.Name})
			obj.SetAnnotations(map[string]string{v1.LabelConfigurations: "gc-cross", v1.AnnotationConfigurationOwnerUID: string(owner.UID), v1.AnnotationConfigurationUID: "gc-config-uid"})
			if err := w.Reconcile(ctx, owner, obj, "gc-cross"); err != nil {
				t.Fatal(err)
			}
			live := localIntentGet(t, ctx, cli, obj)
			prepared := localGCPrepared(t, ctx, cli, owner, live)
			if result, err := localGC(cli, owner.Namespace).Sweep(ctx); err != nil || result.Deleted != 1 || result.Kept != 1 {
				t.Fatalf("cross-scope scan: %+v %v", result, err)
			}
			if err := cli.Get(ctx, client.ObjectKeyFromObject(prepared), &corev1.Secret{}); !apierrors.IsNotFound(err) {
				t.Fatalf("unreferenced record remained: %v", err)
			}
			localGCCheckBusiness(t, live, localIntentGet(t, ctx, cli, obj))
		})
	}
	t.Run("publication_wins_collection_race", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		owner := localIntentOwner(t, ctx, cli)
		desired := localIntentObject(owner, "v1", "ConfigMap", "race-target")
		desired.Object["data"] = map[string]any{"KEEP": "stable"}
		if err := w.Reconcile(ctx, owner, desired, ""); err != nil {
			t.Fatal(err)
		}
		live := localIntentGet(t, ctx, cli, desired)
		prepared := localGCPrepared(t, ctx, cli, owner, live)
		wrapped := &localGCRaceClient{Client: cli, beforeFence: func(ctx context.Context) error {
			return cli.Patch(ctx, live.DeepCopy(), localGCPointerPatch(t, live, prepared.Name), client.FieldOwner(v1.FieldOwner))
		}}
		collector := localGC(wrapped, owner.Namespace)
		first, err := collector.Sweep(ctx)
		if err == nil || first.Failed != 1 {
			t.Fatalf("race scan: %+v %v", first, err)
		}
		if err := cli.Get(ctx, client.ObjectKeyFromObject(prepared), prepared); err != nil {
			t.Fatal("winning publication lost record", err)
		}
		if localIntentGet(t, ctx, cli, desired).GetAnnotations()[k8s.AnnotationResourceIntent] != prepared.Name {
			t.Fatal("winning pointer lost")
		}
		if result, err := collector.Sweep(ctx); err != nil || result.Deleted+first.Deleted != 1 || result.Kept != 1 {
			t.Fatalf("retry scan: %+v %v", result, err)
		}
	})
	t.Run("secret_metadata_race_retained_then_retried", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		owner := localIntentOwner(t, ctx, cli)
		desired := localIntentObject(owner, "v1", "ConfigMap", "secret-race-target")
		desired.Object["data"] = map[string]any{"KEEP": "stable"}
		if err := w.Reconcile(ctx, owner, desired, ""); err != nil {
			t.Fatal(err)
		}
		live := localIntentGet(t, ctx, cli, desired)
		prepared := localGCPrepared(t, ctx, cli, owner, live)
		wrapped := &localGCRaceClient{Client: cli, recordName: prepared.Name, beforeDelete: func(ctx context.Context) error {
			latest := &corev1.Secret{}
			if err := cli.Get(ctx, client.ObjectKeyFromObject(prepared), latest); err != nil {
				return err
			}
			return cli.Patch(ctx, latest, client.RawPatch(types.JSONPatchType, []byte(`[{"op":"add","path":"/metadata/annotations","value":{"audit":"concurrent-metadata-write"}}]`)), client.FieldOwner("audit-controller"))
		}}
		collector := localGC(wrapped, owner.Namespace)
		if result, err := collector.Sweep(ctx); err == nil || result.Failed != 1 || result.Deleted != 0 {
			t.Fatalf("delete race scan: %+v %v", result, err)
		}
		if err := cli.Get(ctx, client.ObjectKeyFromObject(prepared), prepared); err != nil || prepared.Annotations["audit"] != "concurrent-metadata-write" {
			t.Fatalf("concurrent record change lost: %v", err)
		}
		if result, err := collector.Sweep(ctx); err != nil || result.Deleted != 1 || result.Kept != 1 {
			t.Fatalf("delete retry scan: %+v %v", result, err)
		}
		localGCCheckBusiness(t, live, localIntentGet(t, ctx, cli, desired))
	})
}

type localGCRaceClient struct {
	client.Client
	beforeFence  func(context.Context) error
	beforeDelete func(context.Context) error
	recordName   string
}

func (c *localGCRaceClient) Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
	if c.beforeDelete != nil && obj.GetName() == c.recordName {
		fn := c.beforeDelete
		c.beforeDelete = nil
		if err := fn(ctx); err != nil {
			return err
		}
	}
	return c.Client.Delete(ctx, obj, opts...)
}

func (c *localGCRaceClient) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	options := &client.PatchOptions{}
	for _, opt := range opts {
		opt.ApplyToPatch(options)
	}
	if c.beforeFence != nil && len(options.DryRun) == 0 {
		fn := c.beforeFence
		c.beforeFence = nil
		if err := fn(ctx); err != nil {
			return err
		}
	}
	return c.Client.Patch(ctx, obj, patch, opts...)
}

type localGCInitClient struct {
	client.Client
	collector *k8s.ResourceIntentCollector
	prepared  []string
}

func (c *localGCInitClient) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	if err := c.Client.Create(ctx, obj, opts...); err != nil {
		return err
	}
	options := &client.CreateOptions{}
	for _, opt := range opts {
		opt.ApplyToCreate(options)
	}
	// Only persisted preparations can race collection; a dry-run Secret has
	// not been published and must not consume the first-preparation hook.
	if len(options.DryRun) != 0 {
		return nil
	}
	if _, ok := obj.(*corev1.Secret); ok {
		c.prepared = append(c.prepared, obj.GetName())
		if len(c.prepared) == 1 {
			result, err := c.collector.Sweep(ctx)
			if err != nil {
				return err
			}
			if result.Deleted != 1 {
				return fmt.Errorf("expected collector to fence first preparation: %+v", result)
			}
		}
	}
	return nil
}
