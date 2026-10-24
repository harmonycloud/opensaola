//go:build envtest

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

package k8s

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

func TestResourceIntentStorageAPI(t *testing.T) {
	environment := &envtest.Environment{CRDDirectoryPaths: []string{filepath.Join("..", "..", "config", "crd", "bases")}, ErrorIfCRDPathMissing: true}
	cfg, err := environment.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := environment.Stop(); err != nil {
			t.Error(err)
		}
	})
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{v1.AddToScheme, corev1.AddToScheme, appsv1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	cli, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := NewIntentSchemaResolver(cfg)
	if err != nil {
		t.Fatal(err)
	}
	writer := &ManagedResourceWriter{Client: cli, Reader: cli, IntentSchemas: resolver}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	ownerFor := func(t *testing.T) *v1.Middleware {
		t.Helper()
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "intent-storage-"}}
		if err := cli.Create(ctx, ns); err != nil {
			t.Fatal(err)
		}
		owner := &v1.Middleware{ObjectMeta: metav1.ObjectMeta{Name: "owner", Namespace: ns.Name}, Spec: v1.MiddlewareSpec{Baseline: "fixture"}}
		if err := cli.Create(ctx, owner); err != nil {
			t.Fatal(err)
		}
		return owner
	}
	deploymentFor := func(t *testing.T, owner *v1.Middleware, value string) *unstructured.Unstructured {
		t.Helper()
		desired := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"name": "target", "namespace": owner.Namespace}, "spec": map[string]any{"replicas": int64(0), "selector": map[string]any{"matchLabels": map[string]any{"app": "target"}}, "template": map[string]any{"metadata": map[string]any{"labels": map[string]any{"app": "target"}}, "spec": map[string]any{"containers": []any{map[string]any{"name": "main", "image": "registry.k8s.io/pause:3.10", "env": []any{map[string]any{"name": "KEEP", "value": "yes"}, map[string]any{"name": "LARGE", "value": value}}}}}}}}}
		ref, err := intentOwnerReference(owner)
		if err != nil {
			t.Fatal(err)
		}
		desired.SetOwnerReferences([]metav1.OwnerReference{ref})
		return desired
	}
	for _, legacy := range []bool{false, true} {
		name := "new_large_record"
		if legacy {
			name = "legacy_large_record"
		}
		t.Run(name, func(t *testing.T) {
			owner := ownerFor(t)
			value, found, err := unstructured.NestedString(intentSizeFixture(700<<10, false).Object, "data", "large")
			if err != nil || !found {
				t.Fatalf("missing large fixture value: %v", err)
			}
			desired := deploymentFor(t, owner, value)
			if legacy {
				if err := cli.Create(ctx, desired.DeepCopy(), client.FieldOwner("manager")); err != nil {
					t.Fatal(err)
				}
			}
			if err := writer.Reconcile(ctx, owner, desired, ""); err != nil {
				t.Fatal(err)
			}
			live := desired.DeepCopy()
			if err := cli.Get(ctx, client.ObjectKeyFromObject(live), live); err != nil {
				t.Fatal(err)
			}
			record, secret, err := writer.loadResourceIntent(ctx, owner, live, "")
			if err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(record)
			if err != nil {
				t.Fatal(err)
			}
			if len(raw) <= 512<<10 || len(secret.Data[intentZstdKey]) == 0 {
				t.Fatal("large record was blocked or not compressed")
			}
			if !legacy && len(raw) <= corev1.MaxSecretSize {
				t.Fatal("new record did not exercise JSON larger than the Secret payload limit")
			}
			t.Logf("raw JSON=%d stored zstd=%d", len(raw), len(secret.Data[intentZstdKey]))
			next := deploymentFor(t, owner, "temporary")
			env := []any{map[string]any{"name": "KEEP", "value": "yes"}}
			containers, _, err := unstructured.NestedSlice(next.Object, "spec", "template", "spec", "containers")
			if err != nil || len(containers) != 1 {
				t.Fatalf("expected one fixture container: %v", err)
			}
			main, ok := containers[0].(map[string]any)
			if !ok {
				t.Fatal("fixture container is not an object")
			}
			main["env"] = env
			if err := unstructured.SetNestedSlice(next.Object, containers, "spec", "template", "spec", "containers"); err != nil {
				t.Fatal(err)
			}
			if err := writer.Reconcile(ctx, owner, next, ""); err != nil {
				t.Fatal(err)
			}
			if err := cli.Get(ctx, client.ObjectKeyFromObject(live), live); err != nil {
				t.Fatal(err)
			}
			got, _, err := unstructured.NestedSlice(live.Object, "spec", "template", "spec", "containers")
			if err != nil || len(got) != 1 {
				t.Fatalf("expected one live container: %v", err)
			}
			gotMain, ok := got[0].(map[string]any)
			if !ok {
				t.Fatal("live container is not an object")
			}
			gotEnv, found, err := unstructured.NestedSlice(gotMain, "env")
			if err != nil || !found || !reflect.DeepEqual(gotEnv, env) {
				t.Fatal("large prior record did not support omitted env deletion")
			}
			if err := cli.Get(ctx, client.ObjectKeyFromObject(secret), &corev1.Secret{}); !apierrors.IsNotFound(err) {
				t.Fatalf("old record not reclaimed: %v", err)
			}
		})
	}
	t.Run("multi_mib_record_load_and_collection", func(t *testing.T) {
		owner := ownerFor(t)
		target := intentUnitResource()
		target.SetNamespace(owner.Namespace)
		target.SetUID("")
		target.SetResourceVersion("")
		ref, err := intentOwnerReference(owner)
		if err != nil {
			t.Fatal(err)
		}
		target.SetOwnerReferences([]metav1.OwnerReference{ref})
		if err := cli.Create(ctx, target); err != nil {
			t.Fatal(err)
		}
		desired := intentSizeFixture(3<<20, false)
		desired.SetNamespace(owner.Namespace)
		prepared, err := writer.prepareResourceIntent(ctx, owner, target, desired, "", "schema", nil)
		if err != nil {
			t.Fatal(err)
		}
		pointed := target.DeepCopy()
		setIntentPointer(pointed, prepared.Name)
		record, _, err := writer.loadResourceIntent(ctx, owner, pointed, "")
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(record.Desired.Object, intentObject(desired).Object) {
			t.Fatal("multi-MiB JSON record truncated")
		}
		collector := NewResourceIntentCollector(cli, cli)
		collector.MinAge = 0
		deleted, err := collector.collect(ctx, prepared, time.Now())
		if err != nil || !deleted {
			t.Fatalf("compressed record GC deleted=%v err=%v", deleted, err)
		}
		if err := cli.Get(ctx, client.ObjectKeyFromObject(prepared), &corev1.Secret{}); !apierrors.IsNotFound(err) {
			t.Fatalf("prepared record not deleted: %v", err)
		}
	})
	for _, existing := range []bool{false, true} {
		name := "server_rejects_record_before_new_resource"
		if existing {
			name = "server_rejects_record_preserves_existing_resource"
		}
		t.Run(name, func(t *testing.T) {
			owner := ownerFor(t)
			var before *unstructured.Unstructured
			if existing {
				small := deploymentFor(t, owner, "small")
				if err := writer.Reconcile(ctx, owner, small, ""); err != nil {
					t.Fatal(err)
				}
				before = small.DeepCopy()
				if err := cli.Get(ctx, client.ObjectKeyFromObject(before), before); err != nil {
					t.Fatal(err)
				}
			}
			value, found, err := unstructured.NestedString(intentSizeFixture(1280<<10, true).Object, "data", "large")
			if err != nil || !found {
				t.Fatalf("missing oversized fixture value: %v", err)
			}
			desired := deploymentFor(t, owner, value)
			// A valid business dry-run distinguishes Secret validation from a generic
			// request-size failure or resource-schema rejection.
			preview := cleanApplyObject(desired)
			if err := cli.Patch(ctx, preview, client.Apply, client.FieldOwner(v1.FieldOwner), client.ForceOwnership, client.DryRunAll); err != nil {
				t.Fatalf("business resource dry-run rejected: %v", err)
			}
			err = writer.Reconcile(ctx, owner, desired, "")
			if !apierrors.IsInvalid(err) {
				t.Fatalf("want API Secret validation error, got %v", err)
			}
			status, ok := err.(apierrors.APIStatus)
			if !ok {
				// The writer wraps errors with contextual messages.
				var apiStatus apierrors.APIStatus
				if !errors.As(err, &apiStatus) {
					t.Fatal("missing API status")
				}
				status = apiStatus
			}
			if status.Status().Details == nil || status.Status().Details.Kind != "Secret" {
				t.Fatalf("failure was not Secret validation: %v", err)
			}
			if existing {
				after := before.DeepCopy()
				if err := cli.Get(ctx, client.ObjectKeyFromObject(after), after); err != nil {
					t.Fatal(err)
				}
				// A real cluster can update Deployment status while the rejected
				// request is in flight. Compare the writable surface and record
				// pointer separately instead of treating status/RV drift as a write.
				if !reflect.DeepEqual(intentObject(before).Object, intentObject(after).Object) ||
					before.GetUID() != after.GetUID() ||
					before.GetAnnotations()[AnnotationResourceIntent] != after.GetAnnotations()[AnnotationResourceIntent] {
					t.Fatal("failed record write changed business resource or pointer")
				}
			} else {
				if err := cli.Get(ctx, client.ObjectKeyFromObject(desired), desired); !apierrors.IsNotFound(err) {
					t.Fatalf("resource created despite failed record preview: %v", err)
				}
			}
			var records corev1.SecretList
			if err := cli.List(ctx, &records, client.InNamespace(owner.Namespace)); err != nil {
				t.Fatal(err)
			}
			want := 0
			if existing {
				want = 1
			}
			if len(records.Items) != want {
				t.Fatalf("rejected write changed record count to %d", len(records.Items))
			}
		})
	}
}
