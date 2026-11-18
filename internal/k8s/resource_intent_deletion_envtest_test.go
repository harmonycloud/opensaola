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
	"time"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var typedDeletionGVK = schema.GroupVersionKind{Group: "intent.audit.invalid", Version: "v1", Kind: "TypedDeletionTest"}

func typedIntentDeletionCRD() *apiextensionsv1.CustomResourceDefinition {
	stringType := apiextensionsv1.JSONSchemaProps{Type: "string"}
	stringMap := apiextensionsv1.JSONSchemaProps{Type: "object", AdditionalProperties: &apiextensionsv1.JSONSchemaPropsOrBool{Allows: true, Schema: &stringType}}
	namedList := func(fields map[string]apiextensionsv1.JSONSchemaProps) apiextensionsv1.JSONSchemaProps {
		listType := "map"
		fields["name"] = stringType
		return apiextensionsv1.JSONSchemaProps{Type: "array", XListType: &listType, XListMapKeys: []string{"name"}, Items: &apiextensionsv1.JSONSchemaPropsOrArray{Schema: &apiextensionsv1.JSONSchemaProps{Type: "object", Required: []string{"name"}, Properties: fields}}}
	}
	resources := apiextensionsv1.JSONSchemaProps{Type: "object", Properties: map[string]apiextensionsv1.JSONSchemaProps{"requests": stringMap, "limits": stringMap}}
	containers := namedList(map[string]apiextensionsv1.JSONSchemaProps{
		"image": stringType, "resources": resources,
		"env":          namedList(map[string]apiextensionsv1.JSONSchemaProps{"value": stringType}),
		"volumeMounts": namedList(map[string]apiextensionsv1.JSONSchemaProps{"mountPath": stringType}),
	})
	volumes := namedList(map[string]apiextensionsv1.JSONSchemaProps{"emptyDir": {Type: "object", Properties: map[string]apiextensionsv1.JSONSchemaProps{"medium": stringType}}})
	return &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "typeddeletiontests." + typedDeletionGVK.Group},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: typedDeletionGVK.Group, Scope: apiextensionsv1.NamespaceScoped,
			Names: apiextensionsv1.CustomResourceDefinitionNames{Plural: "typeddeletiontests", Singular: "typeddeletiontest", Kind: typedDeletionGVK.Kind},
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{{Name: "v1", Served: true, Storage: true, Schema: &apiextensionsv1.CustomResourceValidation{OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{Type: "object", Properties: map[string]apiextensionsv1.JSONSchemaProps{
				"spec": {Type: "object", Properties: map[string]apiextensionsv1.JSONSchemaProps{
					"setting": stringType, "owned": stringType, "containers": containers, "volumes": volumes, "resources": resources,
					"defaulted": {Type: "string", Default: &apiextensionsv1.JSON{Raw: []byte(`"api-default"`)}},
				}},
			}}}}},
		},
	}
}

type deletionAPIFixture struct {
	t       *testing.T
	ctx     context.Context
	cli     client.Client
	writer  *ManagedResourceWriter
	owner   *v1.Middleware
	desired *unstructured.Unstructured
}

func newDeletionAPIFixture(t *testing.T, ctx context.Context, cli client.Client, resolver IntentSchemaResolver, gvk schema.GroupVersionKind, namespace string) *deletionAPIFixture {
	t.Helper()
	owner := &v1.Middleware{ObjectMeta: metav1.ObjectMeta{GenerateName: "deletion-", Namespace: namespace}, Spec: v1.MiddlewareSpec{Baseline: "fixture"}}
	if err := cli.Create(ctx, owner); err != nil {
		t.Fatal(err)
	}
	desired := &unstructured.Unstructured{Object: map[string]any{"apiVersion": gvk.GroupVersion().String(), "kind": gvk.Kind, "metadata": map[string]any{"name": owner.Name, "namespace": namespace}, "spec": map[string]any{"setting": "before"}}}
	ref, err := intentOwnerReference(owner)
	if err != nil {
		t.Fatal(err)
	}
	desired.SetOwnerReferences([]metav1.OwnerReference{ref})
	return &deletionAPIFixture{t: t, ctx: ctx, cli: cli, writer: &ManagedResourceWriter{Client: cli, Reader: cli, IntentSchemas: resolver}, owner: owner, desired: desired}
}

func (f *deletionAPIFixture) set(obj *unstructured.Unstructured, value any, path ...string) {
	f.t.Helper()
	if err := unstructured.SetNestedField(obj.Object, value, path...); err != nil {
		f.t.Fatal(err)
	}
}

func (f *deletionAPIFixture) get() *unstructured.Unstructured {
	f.t.Helper()
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(f.desired.GroupVersionKind())
	if err := f.cli.Get(f.ctx, client.ObjectKeyFromObject(f.desired), obj); err != nil {
		f.t.Fatal(err)
	}
	return obj
}

func (f *deletionAPIFixture) apply() error {
	return f.writer.Reconcile(f.ctx, f.owner, f.desired, "")
}

func (f *deletionAPIFixture) publish() {
	f.t.Helper()
	if err := f.apply(); err != nil {
		f.t.Fatal(err)
	}
}

func (f *deletionAPIFixture) update(obj *unstructured.Unstructured) {
	f.t.Helper()
	if err := f.cli.Update(f.ctx, obj); err != nil {
		f.t.Fatal(err)
	}
}

func (f *deletionAPIFixture) assertUnchanged(before *unstructured.Unstructured) {
	f.t.Helper()
	if !reflect.DeepEqual(f.get().Object, before.Object) {
		f.t.Fatal("blocked deletion changed the resource or its successful-history pointer")
	}
	if _, _, err := f.writer.loadResourceIntent(f.ctx, f.owner, before, ""); err != nil {
		f.t.Fatalf("blocked deletion lost its previous history: %v", err)
	}
}

func (f *deletionAPIFixture) assertAbsent(obj *unstructured.Unstructured, path ...string) {
	f.t.Helper()
	if value, found, err := unstructured.NestedFieldNoCopy(obj.Object, path...); err != nil || found && value != nil {
		f.t.Fatalf("owned field was not removed at %s (found=%t err=%v)", strings.Join(path, "."), found, err)
	}
}

func runIntentDeletionAPICases(t *testing.T, ctx context.Context, cli client.Client, resolver IntentSchemaResolver, opaqueGVK schema.GroupVersionKind, namespace string) {
	t.Run("stable_history_survives_lost_pointer_until_target_uid_replaced", func(t *testing.T) {
		f := newDeletionAPIFixture(t, ctx, cli, resolver, opaqueGVK, namespace)
		f.publish()
		actual := f.get()
		_, history, err := f.writer.loadResourceIntent(ctx, f.owner, actual, "")
		if err != nil {
			t.Fatal(err)
		}
		annotations := actual.GetAnnotations()
		delete(annotations, AnnotationResourceIntent)
		delete(annotations, AnnotationResourceIntentOperation)
		actual.SetAnnotations(annotations)
		f.update(actual)
		gc := &ResourceIntentCollector{Client: cli, Reader: cli}
		if deleted, err := gc.collect(ctx, history, time.Now().Add(time.Hour)); err != nil || deleted {
			t.Fatalf("collected live history with missing annotations: %v", err)
		}
		if err := cli.Delete(ctx, actual); err != nil {
			t.Fatal(err)
		}
		replacement := f.desired.DeepCopy()
		if err := cli.Create(ctx, replacement); err != nil {
			t.Fatal(err)
		}
		if replacement.GetUID() == actual.GetUID() {
			t.Fatal("replacement UID unchanged")
		}
		if deleted, err := gc.collect(ctx, history, time.Now().Add(time.Hour)); err != nil || !deleted {
			t.Fatalf("could not collect the previous target lifetime: %v", err)
		}
		if record, _, err := f.writer.loadResourceIntent(ctx, f.owner, replacement, ""); err != nil || record != nil {
			t.Fatalf("replacement inherited old deletion rights: %v", err)
		}
	})
	t.Run("audit_pending_default_refill", func(t *testing.T) {
		f := newDeletionAPIFixture(t, ctx, cli, resolver, typedDeletionGVK, namespace)
		f.set(f.desired, "manual", "spec", "defaulted")
		f.publish()
		unstructured.RemoveNestedField(f.desired.Object, "spec", "defaulted")
		f.writer.Client = &intentPolicyFaultClient{Client: cli, beforeSecretPatch: func(context.Context, *corev1.Secret) error {
			return apierrors.NewTimeoutError("injected confirmation failure", 1)
		}}
		if err := f.apply(); err == nil {
			t.Fatal("failed to inject")
		}
		actual, _, _ := unstructured.NestedString(f.get().Object, "spec", "defaulted")
		if actual != "api-default" {
			t.Fatalf("did not reach default refill: %q", actual)
		}
		f.writer = &ManagedResourceWriter{Client: cli, Reader: cli, IntentSchemas: resolver}
		if err := f.apply(); err != nil {
			t.Fatalf("same desired retry after accepted default refill & transient confirmation failure rejected: %v", err)
		}
	})
	t.Run("audit_pending_mixed_change_withdrawn", func(t *testing.T) {
		f := newDeletionAPIFixture(t, ctx, cli, resolver, opaqueGVK, namespace)
		f.set(f.desired, "old", "spec", "owned")
		f.publish()
		unstructured.RemoveNestedField(f.desired.Object, "spec", "owned")
		f.set(f.desired, "new", "spec", "replacement")
		f.writer.Client = &intentPolicyFaultClient{Client: cli, beforeSecretPatch: func(context.Context, *corev1.Secret) error {
			return apierrors.NewTimeoutError("injected confirmation failure", 1)
		}}
		if err := f.apply(); err == nil {
			t.Fatal("failed to inject")
		}
		actual, _, _ := unstructured.NestedString(f.get().Object, "spec", "replacement")
		if actual != "new" {
			t.Fatal("first update not committed")
		}
		f.writer = &ManagedResourceWriter{Client: cli, Reader: cli, IntentSchemas: resolver}
		unstructured.RemoveNestedField(f.desired.Object, "spec", "replacement")
		f.set(f.desired, "old", "spec", "owned")
		f.publish()
		f.assertAbsent(f.get(), "spec", "replacement")
	})

	t.Run("audit_pointer_dropped_with_stale_field", func(t *testing.T) {
		f := newDeletionAPIFixture(t, ctx, cli, resolver, opaqueGVK, namespace)
		f.set(f.desired, "old", "spec", "owned")
		f.publish()
		unstructured.RemoveNestedField(f.desired.Object, "spec", "owned")
		f.writer.Client = &intentPolicyFaultClient{Client: cli, committed: func(ctx context.Context, u *unstructured.Unstructured) error {
			a := u.GetAnnotations()
			delete(a, AnnotationResourceIntent)
			u.SetAnnotations(a)
			f.set(u, "old", "spec", "owned")
			return cli.Update(ctx, u)
		}}
		if err := f.apply(); err == nil {
			t.Fatal("missing pointer/stale field was accepted")
		}
		if f.get().GetAnnotations()[AnnotationResourceIntent] != "" {
			t.Fatal("injection failed")
		}
		f.writer = &ManagedResourceWriter{Client: cli, Reader: cli, IntentSchemas: resolver}
		f.publish()
		f.assertAbsent(f.get(), "spec", "owned")
	})
	for _, gvk := range []schema.GroupVersionKind{opaqueGVK, typedDeletionGVK} {
		t.Run(gvk.Kind+"_omitted_members_keep_only_external_children", func(t *testing.T) {
			f := newDeletionAPIFixture(t, ctx, cli, resolver, gvk, namespace)
			f.set(f.desired, []any{map[string]any{"name": "broker", "image": "declared-image", "env": []any{map[string]any{"name": "OWN", "value": "owned"}}, "volumeMounts": []any{map[string]any{"name": "log", "mountPath": "/logs"}}}}, "spec", "containers")
			f.set(f.desired, []any{map[string]any{"name": "log", "emptyDir": map[string]any{}}}, "spec", "volumes")
			f.set(f.desired, map[string]any{"requests": map[string]any{"cpu": "100m"}, "limits": map[string]any{"memory": "128Mi"}}, "spec", "resources")
			f.publish()
			live := f.get()
			f.set(live, []any{map[string]any{"name": "broker", "image": "declared-image", "env": []any{map[string]any{"name": "OWN", "value": "owned"}, map[string]any{"name": "EXTERNAL", "value": "external"}}, "volumeMounts": []any{map[string]any{"name": "log", "mountPath": "/logs"}, map[string]any{"name": "external", "mountPath": "/external"}}}, map[string]any{"name": "sidecar", "image": "external-image"}}, "spec", "containers")
			f.set(live, []any{map[string]any{"name": "log", "emptyDir": map[string]any{}}, map[string]any{"name": "external", "emptyDir": map[string]any{}}}, "spec", "volumes")
			f.update(live)
			unstructured.RemoveNestedField(f.desired.Object, "spec", "containers")
			unstructured.RemoveNestedField(f.desired.Object, "spec", "volumes")
			f.set(f.desired, map[string]any{}, "spec", "resources")
			f.publish()
			actual := f.get()
			containers, _, _ := unstructured.NestedSlice(actual.Object, "spec", "containers")
			wantContainers := []any{map[string]any{"name": "broker", "env": []any{map[string]any{"name": "EXTERNAL", "value": "external"}}, "volumeMounts": []any{map[string]any{"name": "external", "mountPath": "/external"}}}, map[string]any{"name": "sidecar", "image": "external-image"}}
			if !reflect.DeepEqual(containers, wantContainers) {
				t.Fatal("omitted container retained an owned child or lost an external child")
			}
			volumes, _, _ := unstructured.NestedSlice(actual.Object, "spec", "volumes")
			if !reflect.DeepEqual(volumes, []any{map[string]any{"name": "external", "emptyDir": map[string]any{}}}) {
				t.Fatal("whole volumes omission did not delete only the declared volume")
			}
			resources, _, _ := unstructured.NestedMap(actual.Object, "spec", "resources")
			if len(resources) != 0 {
				t.Fatal("resources:{} did not clear historical owned children")
			}
			record, _, err := f.writer.loadResourceIntent(ctx, f.owner, actual, "")
			if err != nil {
				t.Fatal(err)
			}
			f.assertAbsent(record.Desired, "spec", "containers")
			f.assertAbsent(record.Desired, "spec", "volumes")
			f.set(f.desired, "later", "spec", "setting")
			f.publish()
			laterContainers, _, _ := unstructured.NestedSlice(f.get().Object, "spec", "containers")
			if !reflect.DeepEqual(laterContainers, wantContainers) {
				t.Fatal("preserved external children were subsequently treated as owned")
			}
		})

		t.Run(gvk.Kind+"_modified_owned_leaf_blocks_the_whole_resource", func(t *testing.T) {
			f := newDeletionAPIFixture(t, ctx, cli, resolver, gvk, namespace)
			f.set(f.desired, []any{map[string]any{"name": "broker", "env": []any{map[string]any{"name": "OWN", "value": "sensitive-owned-value"}}}}, "spec", "containers")
			f.publish()
			live := f.get()
			f.set(live, []any{map[string]any{"name": "broker", "env": []any{map[string]any{"name": "OWN", "value": "sensitive-external-value"}}}}, "spec", "containers")
			f.update(live)
			unstructured.RemoveNestedField(f.desired.Object, "spec", "containers")
			f.set(f.desired, "unrelated-change", "spec", "setting")
			if err := f.apply(); err == nil || strings.Contains(err.Error(), "sensitive-") {
				t.Fatalf("modified owned leaf was not rejected with a redacted error: %v", err)
			}
			f.assertUnchanged(live)
			f.set(live, []any{map[string]any{"name": "broker", "env": []any{map[string]any{"name": "OWN", "value": "sensitive-owned-value"}}}}, "spec", "containers")
			f.update(live)
			f.publish()
			f.assertAbsent(f.get(), "spec", "containers")
		})

		t.Run(gvk.Kind+"_external_apply_blocks_owned_deletion", func(t *testing.T) {
			f := newDeletionAPIFixture(t, ctx, cli, resolver, gvk, namespace)
			f.set(f.desired, "sensitive-owned-value", "spec", "owned")
			f.publish()
			claim := cleanApplyObject(f.desired)
			claim.Object["spec"] = map[string]any{"owned": "sensitive-owned-value"}
			claim.SetOwnerReferences(nil)
			if err := cli.Patch(ctx, claim, client.Apply, client.FieldOwner("external-deletion-claim"), client.ForceOwnership); err != nil {
				t.Fatal(err)
			}
			before := f.get()
			unstructured.RemoveNestedField(f.desired.Object, "spec", "owned")
			f.set(f.desired, "after", "spec", "setting")
			if err := f.apply(); err == nil || strings.Contains(err.Error(), "sensitive-") {
				t.Fatalf("Apply-protected deletion was not safely rejected: %v", err)
			}
			f.assertUnchanged(before)
			claim = cleanApplyObject(claim)
			delete(claim.Object, "spec")
			if err := cli.Patch(ctx, claim, client.Apply, client.FieldOwner("external-deletion-claim")); err != nil {
				t.Fatal(err)
			}
			f.publish()
			f.assertAbsent(f.get(), "spec", "owned")
		})
	}

	t.Run("schema_default_refill_is_accepted", func(t *testing.T) {
		f := newDeletionAPIFixture(t, ctx, cli, resolver, typedDeletionGVK, namespace)
		f.set(f.desired, "explicit", "spec", "defaulted")
		f.publish()
		unstructured.RemoveNestedField(f.desired.Object, "spec", "defaulted")
		f.publish()
		value, _, _ := unstructured.NestedString(f.get().Object, "spec", "defaulted")
		if value != "api-default" {
			t.Fatal("API did not restore its declared schema default")
		}
	})

	t.Run("no_history_preserves_omitted_legacy_fields", func(t *testing.T) {
		f := newDeletionAPIFixture(t, ctx, cli, resolver, opaqueGVK, namespace)
		live := f.desired.DeepCopy()
		f.set(live, []any{map[string]any{"name": "broker", "image": "external"}}, "spec", "containers")
		f.set(live, map[string]any{"requests": map[string]any{"cpu": "100m"}}, "spec", "resources")
		if err := cli.Create(ctx, live); err != nil {
			t.Fatal(err)
		}
		f.set(f.desired, map[string]any{}, "spec", "resources")
		f.publish()
		actual := f.get()
		if !reflect.DeepEqual(actual.Object["spec"], live.Object["spec"]) {
			t.Fatal("bootstrap inferred deletion rights without recorded history")
		}
	})
	for _, external := range []bool{false, true} {
		name := "legacy_atomic_exact_snapshot_can_be_deleted"
		if external {
			name = "legacy_atomic_external_edit_blocks_migration"
		}
		t.Run(name, func(t *testing.T) {
			f := newDeletionAPIFixture(t, ctx, cli, resolver, opaqueGVK, namespace)
			original := []any{map[string]any{"name": "broker", "image": "declared"}}
			f.set(f.desired, original, "spec", "containers")
			f.publish()
			live := f.get()
			record, previous, err := f.writer.loadResourceIntent(ctx, f.owner, live, "")
			if err != nil {
				t.Fatal(err)
			}
			_, hash, err := resolver.Resolve(ctx, opaqueGVK)
			if err != nil {
				t.Fatal(err)
			}
			record.Version = 1
			record.SchemaHash = hash
			record.BaseResourceVersion = live.GetResourceVersion()
			legacy, err := buildResourceIntentSecret(f.owner, *record)
			if err != nil {
				t.Fatal(err)
			}
			if err := cli.Create(ctx, legacy); err != nil {
				t.Fatal(err)
			}
			setIntentPointer(live, legacy.Name)
			if external {
				f.set(live, []any{map[string]any{"name": "broker", "image": "declared", "env": []any{map[string]any{"name": "EXTERNAL", "value": "external"}}}}, "spec", "containers")
			}
			f.update(live)
			f.writer.discardIntentRecord(ctx, previous)
			unstructured.RemoveNestedField(f.desired.Object, "spec", "containers")
			f.set(f.desired, "after", "spec", "setting")
			if external {
				if err := f.apply(); err == nil {
					t.Fatal("v1 atomic migration inferred individual member ownership")
				}
				f.assertUnchanged(live)
				f.set(live, original, "spec", "containers")
				f.update(live)
			}
			f.publish()
			f.assertAbsent(f.get(), "spec", "containers")
			upgraded, _, err := f.writer.loadResourceIntent(ctx, f.owner, f.get(), "")
			if err != nil || upgraded.Version != currentResourceIntentVersion {
				t.Fatalf("successful legacy deletion did not finish its policy upgrade: %v", err)
			}
		})
	}

	runIntentDeletionAdmissionCases(t, ctx, cli, resolver, opaqueGVK, namespace)
	runIntentDeletionDurabilityCases(t, ctx, cli, resolver, opaqueGVK, namespace)
	runReintroducedMemberPayloadCases(t, ctx, cli, resolver, opaqueGVK, namespace)
	runNativeIntentDeletionCases(t, ctx, cli, resolver, namespace)
	runDeclaredPlanAPIRegressions(t, ctx, cli, resolver, namespace)
}

func runReintroducedMemberPayloadCases(t *testing.T, ctx context.Context, cli client.Client, resolver IntentSchemaResolver, opaqueGVK schema.GroupVersionKind, namespace string) {
	for _, gvk := range []schema.GroupVersionKind{opaqueGVK, typedDeletionGVK} {
		for _, phase := range []string{"dry_run", "commit"} {
			for _, oldPayload := range []bool{false, true} {
				shape := "external_payload_only"
				if oldPayload {
					shape = "old_mount_and_external_payload"
				}
				t.Run(gvk.Kind+"_"+phase+"_reintroduced_member_"+shape, func(t *testing.T) {
					f := newDeletionAPIFixture(t, ctx, cli, resolver, gvk, namespace)
					oldMount := []any{map[string]any{"name": "logs", "mountPath": "/logs"}}
					f.set(f.desired, []any{map[string]any{"name": "broker", "volumeMounts": oldMount}}, "spec", "containers")
					f.publish()
					before := f.get()
					unstructured.RemoveNestedField(f.desired.Object, "spec", "containers")
					member := map[string]any{"name": "broker", "resources": map[string]any{}}
					if oldPayload {
						member["volumeMounts"] = oldMount
					}
					injection := []any{member}
					fault := &intentPolicyFaultClient{Client: cli, targetKind: gvk.Kind}
					previewed, committed := false, false
					if phase == "dry_run" {
						fault.preview = func(actual *unstructured.Unstructured) {
							previewed = true
							f.set(actual, runtime.DeepCopyJSONValue(injection), "spec", "containers")
						}
					}
					fault.committed = func(ctx context.Context, actual *unstructured.Unstructured) error {
						if committed {
							return nil
						}
						committed = true
						f.set(actual, runtime.DeepCopyJSONValue(injection), "spec", "containers")
						return cli.Update(ctx, actual)
					}
					f.writer.Client = fault
					err := f.apply()
					if oldPayload {
						if err == nil {
							t.Fatal("reintroduced old mounting payload was accepted as external")
						}
						if phase == "dry_run" {
							if !previewed || committed {
								t.Fatal("invalid preview reached the real commit")
							}
							f.assertUnchanged(before)
						} else if !committed {
							t.Fatal("commit reinsertion was not exercised")
						}
						f.writer = &ManagedResourceWriter{Client: cli, Reader: cli, IntentSchemas: resolver}
						f.publish()
						if phase == "dry_run" {
							f.assertAbsent(f.get(), "spec", "containers")
							return
						}
					} else if err != nil || !committed {
						t.Fatalf("new external-only member payload was incorrectly rejected: %v", err)
					}
					actual := f.get()
					containers, _, _ := unstructured.NestedSlice(actual.Object, "spec", "containers")
					want := []any{map[string]any{"name": "broker", "resources": map[string]any{}}}
					if !reflect.DeepEqual(containers, want) {
						t.Fatal("cleanup failed to preserve only the same-name external payload")
					}
					record, _, err := f.writer.loadResourceIntent(ctx, f.owner, actual, "")
					if err != nil {
						t.Fatal(err)
					}
					f.assertAbsent(record.Desired, "spec", "containers")
					f.writer = &ManagedResourceWriter{Client: cli, Reader: cli, IntentSchemas: resolver}
					f.set(f.desired, "later", "spec", "setting")
					f.publish()
					later, _, _ := unstructured.NestedSlice(f.get().Object, "spec", "containers")
					if !reflect.DeepEqual(later, want) {
						t.Fatal("external-only reintroduced member was claimed by a later write")
					}
				})
			}
		}
	}
}

func runIntentDeletionAdmissionCases(t *testing.T, ctx context.Context, cli client.Client, resolver IntentSchemaResolver, gvk schema.GroupVersionKind, namespace string) {
	for _, phase := range []string{"dry_run", "commit"} {
		for _, shape := range []string{"leaf", "member"} {
			t.Run(phase+"_reinserts_deleted_"+shape, func(t *testing.T) {
				f := newDeletionAPIFixture(t, ctx, cli, resolver, gvk, namespace)
				path := []string{"spec", "owned"}
				var original any = "sensitive-deleted-value"
				if shape == "member" {
					path = []string{"spec", "volumes"}
					original = []any{map[string]any{"name": "logs", "emptyDir": map[string]any{}}}
				}
				f.set(f.desired, original, path...)
				f.publish()
				before := f.get()
				unstructured.RemoveNestedField(f.desired.Object, path...)
				f.set(f.desired, "after", "spec", "setting")
				fault := &intentPolicyFaultClient{Client: cli}
				injected := false
				if phase == "dry_run" {
					fault.preview = func(actual *unstructured.Unstructured) {
						injected = true
						f.set(actual, runtime.DeepCopyJSONValue(original), path...)
					}
				} else {
					fault.committed = func(ctx context.Context, actual *unstructured.Unstructured) error {
						if injected {
							return nil
						}
						injected = true
						f.set(actual, runtime.DeepCopyJSONValue(original), path...)
						return cli.Update(ctx, actual)
					}
				}
				f.writer.Client = fault
				if err := f.apply(); err == nil || strings.Contains(err.Error(), "sensitive-") {
					t.Fatalf("deleted content reintroduced by admission was not safely rejected: %v", err)
				}
				if !injected {
					t.Fatal("reinsertion fault was not reached")
				}
				if phase == "dry_run" {
					f.assertUnchanged(before)
				} else {
					record, _, err := f.writer.loadResourceIntent(ctx, f.owner, f.get(), "")
					if err != nil || record == nil {
						t.Fatalf("incomplete deletion lost its prior authority: %v", err)
					}
					if value, found, err := unstructured.NestedFieldNoCopy(record.Desired.Object, path...); err != nil || !found || !reflect.DeepEqual(value, original) {
						t.Fatal("failed commit advanced past the unfulfilled deletion")
					}
				}
				f.writer = &ManagedResourceWriter{Client: cli, Reader: cli, IntentSchemas: resolver}
				f.publish()
				f.assertAbsent(f.get(), path...)
			})
		}
	}
	for _, phase := range []string{"dry_run", "commit"} {
		t.Run(phase+"_cannot_reinsert_owned_field_already_missing_at_plan_time", func(t *testing.T) {
			f := newDeletionAPIFixture(t, ctx, cli, resolver, gvk, namespace)
			f.set(f.desired, "owned", "spec", "owned")
			f.publish()
			live := f.get()
			unstructured.RemoveNestedField(live.Object, "spec", "owned")
			f.update(live)
			unstructured.RemoveNestedField(f.desired.Object, "spec", "owned")
			f.set(f.desired, "after", "spec", "setting")
			fault := &intentPolicyFaultClient{Client: cli}
			injected := false
			if phase == "dry_run" {
				fault.preview = func(actual *unstructured.Unstructured) {
					injected = true
					f.set(actual, "owned", "spec", "owned")
				}
			} else {
				fault.committed = func(ctx context.Context, actual *unstructured.Unstructured) error {
					if injected {
						return nil
					}
					injected = true
					f.set(actual, "owned", "spec", "owned")
					return cli.Update(ctx, actual)
				}
			}
			f.writer.Client = fault
			if err := f.apply(); err == nil {
				t.Fatal("a transiently absent historical field bypassed deletion verification")
			}
			if !injected {
				t.Fatal("reinsertion fault was not reached")
			}
			if phase == "dry_run" {
				f.assertUnchanged(live)
			}
			f.writer = &ManagedResourceWriter{Client: cli, Reader: cli, IntentSchemas: resolver}
			f.publish()
			f.assertAbsent(f.get(), "spec", "owned")
		})
	}
}

func runIntentDeletionDurabilityCases(t *testing.T, ctx context.Context, cli client.Client, resolver IntentSchemaResolver, gvk schema.GroupVersionKind, namespace string) {
	for _, faultName := range []string{"commit_response_timeout", "admission_reinsertion", "admission_reinsertion_during_gc", "confirmation_failure", "confirmation_response_timeout", "withdraw_deletion"} {
		t.Run(faultName+"_preserves_deletion_across_writer_restart", func(t *testing.T) {
			f := newDeletionAPIFixture(t, ctx, cli, resolver, gvk, namespace)
			f.set(f.desired, "sensitive-deleted-value", "spec", "owned")
			f.publish()
			_, oldSecret, err := f.writer.loadResourceIntent(ctx, f.owner, f.get(), "")
			if err != nil {
				t.Fatal(err)
			}
			unstructured.RemoveNestedField(f.desired.Object, "spec", "owned")
			if faultName != "withdraw_deletion" {
				f.set(f.desired, "after", "spec", "setting")
			}
			fault := &intentPolicyFaultClient{Client: cli}
			injected := false
			if strings.HasPrefix(faultName, "confirmation") {
				fail := func(context.Context, *corev1.Secret) error {
					injected = true
					return apierrors.NewTimeoutError("injected record confirmation timeout", 1)
				}
				if faultName == "confirmation_failure" {
					fault.beforeSecretPatch = fail
				} else {
					fault.afterSecretPatch = fail
				}
			} else {
				fault.committed = func(ctx context.Context, actual *unstructured.Unstructured) error {
					if injected {
						return nil
					}
					injected = true
					f.set(actual, "sensitive-deleted-value", "spec", "owned")
					if err := cli.Update(ctx, actual); err != nil {
						return err
					}
					if faultName == "admission_reinsertion_during_gc" {
						collector := &ResourceIntentCollector{Client: cli, Reader: cli}
						deleted, err := collector.collect(ctx, oldSecret, time.Now().Add(time.Hour))
						if err != nil || deleted {
							t.Fatalf("pending transaction did not survive concurrent collection: deleted=%t err=%v", deleted, err)
						}
					}
					if faultName == "commit_response_timeout" || faultName == "withdraw_deletion" {
						return apierrors.NewTimeoutError("injected lost commit response", 1)
					}
					return nil
				}
			}
			f.writer.Client = fault
			if err := f.apply(); err == nil || strings.Contains(err.Error(), "sensitive-") {
				t.Fatalf("incomplete transaction was not reported safely: %v", err)
			}
			if !injected {
				t.Fatal("durability fault was not reached")
			}
			if faultName != "confirmation_response_timeout" {
				effective, _, err := f.writer.loadResourceIntent(ctx, f.owner, f.get(), "")
				if err != nil || effective == nil {
					t.Fatalf("pending transaction is unreadable after failure: %v", err)
				}
				if _, found, _ := unstructured.NestedFieldNoCopy(effective.Desired.Object, "spec", "owned"); !found {
					t.Fatal("pending transaction lost its previous deletion evidence")
				}
			}
			// No in-memory plan or previous Secret from the first writer survives.
			// The current resource pointer and persisted record must be sufficient.
			f.writer = &ManagedResourceWriter{Client: cli, Reader: cli, IntentSchemas: resolver}
			if faultName == "withdraw_deletion" {
				f.set(f.desired, "sensitive-deleted-value", "spec", "owned")
			}
			f.publish()
			if faultName == "withdraw_deletion" {
				_, active, err := f.writer.loadResourceIntent(ctx, f.owner, f.get(), "")
				if err != nil {
					t.Fatal(err)
				}
				recorded, err := decodeResourceIntent(active.Data)
				if err != nil {
					t.Fatal(err)
				}
				if recorded.DeletionBase != nil && active.Annotations[annotationResourceIntentVerified] == "" {
					t.Fatal("withdrawing a deletion left the previous pending transaction active")
				}
				if _, found, _ := unstructured.NestedFieldNoCopy(recorded.Desired.Object, "spec", "owned"); !found {
					t.Fatal("withdrawn deletion did not record the restored declaration")
				}
			} else {
				f.assertAbsent(f.get(), "spec", "owned")
			}
		})
	}
	for _, phase := range []string{"dry_run", "commit"} {
		t.Run("confirmation_failure_then_"+phase+"_reinsertion_remains_pending", func(t *testing.T) {
			f := newDeletionAPIFixture(t, ctx, cli, resolver, gvk, namespace)
			f.set(f.desired, "owned", "spec", "owned")
			f.publish()
			unstructured.RemoveNestedField(f.desired.Object, "spec", "owned")
			f.writer.Client = &intentPolicyFaultClient{Client: cli, beforeSecretPatch: func(context.Context, *corev1.Secret) error {
				return apierrors.NewTimeoutError("injected first confirmation failure", 1)
			}}
			if err := f.apply(); err == nil {
				t.Fatal("first confirmation failure was not reported")
			}
			f.assertAbsent(f.get(), "spec", "owned")
			reinserted := f.get()
			f.set(reinserted, "owned", "spec", "owned")
			f.update(reinserted)
			fault := &intentPolicyFaultClient{Client: cli}
			injected := false
			if phase == "dry_run" {
				fault.preview = func(actual *unstructured.Unstructured) {
					injected = true
					f.set(actual, "owned", "spec", "owned")
				}
			} else {
				fault.committed = func(ctx context.Context, actual *unstructured.Unstructured) error {
					if injected {
						return nil
					}
					injected = true
					f.set(actual, "owned", "spec", "owned")
					return cli.Update(ctx, actual)
				}
			}
			f.writer = &ManagedResourceWriter{Client: fault, Reader: cli, IntentSchemas: resolver}
			if err := f.apply(); err == nil {
				t.Fatal("pending deletion was forgotten when the first commit had already removed the field")
			}
			if !injected {
				t.Fatal("second-stage reinsertion fault was not reached")
			}
			effective, _, err := f.writer.loadResourceIntent(ctx, f.owner, f.get(), "")
			if err != nil {
				t.Fatal(err)
			}
			if _, found, _ := unstructured.NestedFieldNoCopy(effective.Desired.Object, "spec", "owned"); !found {
				t.Fatal("second-stage failure lost the original deletion evidence")
			}
			f.writer = &ManagedResourceWriter{Client: cli, Reader: cli, IntentSchemas: resolver}
			f.publish()
			f.assertAbsent(f.get(), "spec", "owned")
		})
	}
}

func runNativeIntentDeletionCases(t *testing.T, ctx context.Context, cli client.Client, resolver IntentSchemaResolver, namespace string) {
	t.Run("legacy_secret_volume_default_does_not_allow_partial_write", func(t *testing.T) {
		f := newDeletionAPIFixture(t, ctx, cli, resolver, schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}, namespace)
		f.desired.Object["spec"] = map[string]any{
			"replicas": int64(0), "selector": map[string]any{"matchLabels": map[string]any{"app": f.desired.GetName()}},
			"template": map[string]any{"metadata": map[string]any{"labels": map[string]any{"app": f.desired.GetName()}}, "spec": map[string]any{
				"containers": []any{map[string]any{"name": "main", "image": "registry.k8s.io/pause:3.10", "env": []any{map[string]any{"name": "OWN", "value": "owned"}}, "volumeMounts": []any{map[string]any{"name": "credential", "mountPath": "/credential"}}}},
				"volumes":    []any{map[string]any{"name": "credential", "secret": map[string]any{"secretName": "fixture-credential"}}},
			}},
		}
		legacy := f.desired.DeepCopy()
		if err := cli.Create(ctx, legacy); err != nil {
			t.Fatal(err)
		}
		volumeSource := func(obj *unstructured.Unstructured) map[string]any {
			t.Helper()
			volumes, found, err := unstructured.NestedSlice(obj.Object, "spec", "template", "spec", "volumes")
			if err != nil || !found || len(volumes) != 1 {
				t.Fatalf("expected one volume in fixture, got %d: %v", len(volumes), err)
			}
			return intentSchemaTestMap(t, volumes[0], "secret")
		}
		if volumeSource(legacy)["defaultMode"] != int64(420) {
			t.Fatal("legacy fixture lacks a real API-provided defaultMode")
		}
		f.publish()
		before := f.get()
		record, _, err := f.writer.loadResourceIntent(ctx, f.owner, before, "")
		if err != nil {
			t.Fatal(err)
		}
		history := record.Desired
		if record.Effective != nil {
			history = record.Effective
		}
		if _, tracked := volumeSource(history)["defaultMode"]; tracked {
			t.Fatal("bootstrap incorrectly claimed a default already present on the legacy object")
		}
		f.set(f.desired, []any{map[string]any{"name": "main", "image": "registry.k8s.io/pause:3.10"}}, "spec", "template", "spec", "containers")
		unstructured.RemoveNestedField(f.desired.Object, "spec", "template", "spec", "volumes")
		if err := f.apply(); !apierrors.IsInvalid(err) || !strings.Contains(err.Error(), "secret.secretName: Required value") {
			t.Fatalf("legacy untracked default did not fail the complete resource validation: %v", err)
		}
		f.assertUnchanged(before)
	})
	t.Run("native_deployment_cleans_optional_children_and_rejects_required_removal", func(t *testing.T) {
		f := newDeletionAPIFixture(t, ctx, cli, resolver, schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}, namespace)
		f.desired.Object["spec"] = map[string]any{
			"replicas": int64(0), "selector": map[string]any{"matchLabels": map[string]any{"app": f.desired.GetName()}},
			"template": map[string]any{"metadata": map[string]any{"labels": map[string]any{"app": f.desired.GetName()}}, "spec": map[string]any{
				"containers": []any{map[string]any{"name": "main", "image": "registry.k8s.io/pause:3.10", "resources": map[string]any{"requests": map[string]any{"cpu": "100m"}}, "volumeMounts": []any{map[string]any{"name": "logs", "mountPath": "/logs"}}}},
				"volumes":    []any{map[string]any{"name": "logs", "emptyDir": map[string]any{}}},
			}},
		}
		f.publish()
		f.set(f.desired, []any{map[string]any{"name": "main", "image": "registry.k8s.io/pause:3.10", "resources": map[string]any{}}}, "spec", "template", "spec", "containers")
		unstructured.RemoveNestedField(f.desired.Object, "spec", "template", "spec", "volumes")
		f.publish()
		actual := f.get()
		f.assertAbsent(actual, "spec", "template", "spec", "volumes")
		containers, _, _ := unstructured.NestedSlice(actual.Object, "spec", "template", "spec", "containers")
		if len(containers) == 0 {
			t.Fatal("missing native container")
		}
		main := intentSchemaTestMap(t, containers[0])
		if mounts, exists := main["volumeMounts"]; exists && mounts != nil {
			t.Fatal("native volumeMounts deletion did not converge")
		}
		if resources, ok := main["resources"].(map[string]any); ok && len(resources) > 0 {
			t.Fatal("native resources:{} retained historical limits/requests")
		}
		// Kubernetes requires at least one container and its image. The failed
		// complete deletion must not publish a new successful history record.
		unstructured.RemoveNestedField(f.desired.Object, "spec", "template", "spec", "containers")
		if err := f.apply(); err == nil {
			t.Fatal("required native container deletion was reported successful")
		}
		f.assertUnchanged(actual)
	})
}
