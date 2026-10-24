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
	"strings"
	"testing"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/managedfields"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func intentUnitOwner() *v1.Middleware {
	return &v1.Middleware{ObjectMeta: metav1.ObjectMeta{Name: "owner", Namespace: "owners", UID: "owner-uid"}}
}
func intentUnitResource() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": "target", "namespace": "owners", "uid": "target-uid", "resourceVersion": "3"}, "data": map[string]any{"keep": "yes"}}}
}
func intentUnitClient(t *testing.T, objects ...client.Object) client.Client {
	t.Helper()
	s := runtime.NewScheme()
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := v1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	mapper := meta.NewDefaultRESTMapper([]schema.GroupVersion{corev1.SchemeGroupVersion})
	mapper.Add(corev1.SchemeGroupVersion.WithKind("ConfigMap"), meta.RESTScopeNamespace)
	return fake.NewClientBuilder().WithScheme(s).WithRESTMapper(mapper).WithObjects(objects...).Build()
}
func TestIntentSecretCanonicalization(t *testing.T) {
	obj := intentUnitResource()
	obj.SetKind("Secret")
	obj.Object["data"] = map[string]any{"keep": "b2xk"}
	obj.Object["stringData"] = map[string]any{"keep": "new-password", "token": "another-password"}
	got := intentObject(obj)
	if _, ok := got.Object["stringData"]; ok {
		t.Fatal("write-only data persisted")
	}
	gotData, ok := got.Object["data"].(map[string]any)
	if !ok || gotData["keep"] != "bmV3LXBhc3N3b3Jk" {
		t.Fatal("stringData did not override data")
	}
	if _, ok := obj.Object["stringData"]; !ok {
		t.Fatal("input mutated")
	}
}
func TestIntentConfigurationIdentity(t *testing.T) {
	for _, tc := range []struct {
		name                               string
		cross, foreign, markers, recreated bool
		wantErr                            bool
	}{
		{name: "legacy_same_namespace_controller"},
		{name: "same_name_recreated_configuration", recreated: true},
		{name: "foreign_controller", foreign: true, wantErr: true},
		{name: "cross_namespace_markers", cross: true, markers: true},
		{name: "cross_namespace_name_only_rejected", cross: true, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			owner := intentUnitOwner()
			live := intentUnitResource()
			ref, _ := intentOwnerReference(owner)
			if tc.foreign {
				ref.UID = "foreign"
			}
			if tc.cross {
				live.SetNamespace("other")
			} else {
				live.SetOwnerReferences([]metav1.OwnerReference{ref})
			}
			live.SetAnnotations(map[string]string{v1.LabelConfigurations: "config", v1.AnnotationConfigurationUID: "old-config-uid"})
			if tc.markers {
				a := live.GetAnnotations()
				a[v1.AnnotationConfigurationOwnerUID] = string(owner.UID)
				live.SetAnnotations(a)
				live.SetLabels(map[string]string{v1.LabelApp: owner.Name})
			}
			desired := live.DeepCopy()
			if tc.recreated {
				a := desired.GetAnnotations()
				a[v1.AnnotationConfigurationUID] = "new-config-uid"
				desired.SetAnnotations(a)
			}
			err := verifyIntentIdentity(owner, live, desired, "config")
			if (err != nil) != tc.wantErr {
				t.Fatalf("error=%v wantErr=%v", err, tc.wantErr)
			}
		})
	}
}
func TestIntentRecordsFailClosed(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mutate  func(*corev1.Secret, *unstructured.Unstructured)
		wantErr bool
	}{
		{name: "valid"},
		{name: "resource_recreated", mutate: func(_ *corev1.Secret, o *unstructured.Unstructured) { o.SetUID("new-uid") }, wantErr: true},
		{name: "foreign_record_owner", mutate: func(s *corev1.Secret, _ *unstructured.Unstructured) { s.OwnerReferences[0].UID = "foreign" }, wantErr: true},
		{name: "mutable_record", mutate: func(s *corev1.Secret, _ *unstructured.Unstructured) { b := false; s.Immutable = &b }, wantErr: true},
		{name: "corrupt_record", mutate: func(s *corev1.Secret, _ *unstructured.Unstructured) { s.Data["intent.json"] = []byte("not-json") }, wantErr: true},
		{name: "record_missing", mutate: func(_ *corev1.Secret, o *unstructured.Unstructured) { setIntentPointer(o, "missing") }, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			owner := intentUnitOwner()
			live := intentUnitResource()
			cli := intentUnitClient(t)
			w := &ManagedResourceWriter{Client: cli, Reader: cli}
			s, err := w.prepareResourceIntent(ctx, owner, live, live, "", "schema", nil)
			if err != nil {
				t.Fatal(err)
			}
			setIntentPointer(live, s.Name)
			if tc.mutate != nil {
				tc.mutate(s, live)
				if err := cli.Update(ctx, s); err != nil {
					t.Fatal(err)
				}
			}
			_, _, err = w.loadResourceIntent(ctx, owner, live, "")
			if (err != nil) != tc.wantErr {
				t.Fatalf("error=%v wantErr=%v", err, tc.wantErr)
			}
		})
	}
}
func TestIntentUncertainCommitKeepsPreparedRecord(t *testing.T) {
	for _, tc := range []struct {
		name                string
		writeErr            error
		committed, wantKeep bool
	}{
		{name: "connection_lost", writeErr: errors.New("connection lost"), wantKeep: true},
		{name: "server_timeout", writeErr: apierrors.NewTimeoutError("timeout", 1), wantKeep: true},
		{name: "conflict_rejected", writeErr: apierrors.NewConflict(schema.GroupResource{Resource: "configmaps"}, "target", errors.New("conflict"))},
		{name: "pointer_already_committed", writeErr: apierrors.NewConflict(schema.GroupResource{Resource: "configmaps"}, "target", errors.New("conflict")), committed: true, wantKeep: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			owner := intentUnitOwner()
			live := intentUnitResource()
			cli := intentUnitClient(t, live)
			w := &ManagedResourceWriter{Client: cli, Reader: cli}
			s, err := w.prepareResourceIntent(ctx, owner, live, live, "", "schema", nil)
			if err != nil {
				t.Fatal(err)
			}
			if tc.committed {
				if err := cli.Get(ctx, client.ObjectKeyFromObject(live), live); err != nil {
					t.Fatal(err)
				}
				setIntentPointer(live, s.Name)
				if err := cli.Update(ctx, live); err != nil {
					t.Fatal(err)
				}
			}
			w.cleanupUncommittedIntent(ctx, live, s, tc.writeErr)
			err = cli.Get(ctx, client.ObjectKeyFromObject(s), &corev1.Secret{})
			if (err == nil) != tc.wantKeep {
				t.Fatalf("record kept=%v want=%v err=%v", err == nil, tc.wantKeep, err)
			}
		})
	}
}
func TestIntentPatchHasPreconditionsAndNoOwnershipRewrite(t *testing.T) {
	live := intentUnitResource()
	live.SetManagedFields([]metav1.ManagedFieldsEntry{{Manager: "other", Operation: metav1.ManagedFieldsOperationUpdate}})
	live.Object["status"] = map[string]any{"phase": "Ready"}
	after := live.DeepCopy()
	unstructured.RemoveNestedField(after.Object, "data", "keep")
	setIntentPointer(after, "new-secret")
	patch, err := intentPatch(live, after)
	if err != nil {
		t.Fatal(err)
	}
	data, err := patch.Data(live)
	if err != nil {
		t.Fatal(err)
	}
	var ops []map[string]any
	if err := json.Unmarshal(data, &ops); err != nil {
		t.Fatal(err)
	}
	if ops[0]["path"] != "/metadata/uid" || ops[1]["path"] != "/metadata/resourceVersion" {
		t.Fatal("missing UID/RV tests")
	}
	for _, op := range ops {
		path, ok := op["path"].(string)
		if !ok {
			t.Fatal("patch operation has no string path")
		}
		if strings.HasPrefix(path, "/status") || strings.HasPrefix(path, "/metadata/managedFields") {
			t.Fatalf("forbidden patch path %s", path)
		}
	}
	if patch.Type() != types.JSONPatchType {
		t.Fatal("not JSONPatch")
	}
}

type intentUnitSchemas struct{ converter managedfields.TypeConverter }

func (s intentUnitSchemas) Resolve(context.Context, schema.GroupVersionKind) (managedfields.TypeConverter, string, error) {
	return s.converter, "unit-schema", nil
}

type intentInitConflictClient struct {
	client.Client
	injected bool
	prepared []string
}

func (c *intentInitConflictClient) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	if err := c.Client.Create(ctx, obj, opts...); err != nil {
		return err
	}
	if _, ok := obj.(*corev1.Secret); ok {
		c.prepared = append(c.prepared, obj.GetName())
	}
	return nil
}

func (c *intentInitConflictClient) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	if obj.GetObjectKind().GroupVersionKind().Kind == "Thing" && !c.injected {
		c.injected = true
		latest := &unstructured.Unstructured{}
		latest.SetGroupVersionKind(obj.GetObjectKind().GroupVersionKind())
		if err := c.Client.Get(ctx, client.ObjectKeyFromObject(obj), latest); err != nil {
			return err
		}
		latest.SetAnnotations(map[string]string{"external-change": "keep"})
		if err := c.Client.Update(ctx, latest); err != nil {
			return err
		}
		return apierrors.NewConflict(schema.GroupResource{Group: "test.invalid", Resource: "things"}, obj.GetName(), errors.New("status writer won the resourceVersion race"))
	}
	return c.Client.Patch(ctx, obj, patch, opts...)
}
func TestIntentInitializationRetainsCreateEvidenceAcrossConflict(t *testing.T) {
	ctx := context.Background()
	owner := intentUnitOwner()
	owner.Namespace = "test"
	desired := intentTestObject(t, `{"env":[{"id":"AUTH","valueFrom":{"secret":"s"}}]}`)
	created := intentTestObject(t, `{"env":[{"id":"AUTH","valueFrom":{"secret":"s","optional":false}}]}`)
	created.SetUID("created-uid")
	created.SetResourceVersion("1")
	ref, err := intentOwnerReference(owner)
	if err != nil {
		t.Fatal(err)
	}
	created.SetOwnerReferences([]metav1.OwnerReference{ref})
	cli := intentUnitClient(t, created)
	wrapped := &intentInitConflictClient{Client: cli}
	w := &ManagedResourceWriter{Client: wrapped, Reader: cli, IntentSchemas: intentUnitSchemas{converter: intentTestConverter(t)}}
	if err := w.initializeResourceIntent(ctx, owner, created, desired, ""); err != nil {
		t.Fatal(err)
	}
	if !wrapped.injected {
		t.Fatal("no conflict injected")
	}
	if len(wrapped.prepared) != 2 || wrapped.prepared[0] == wrapped.prepared[1] {
		t.Fatal("initialization reused a fenced record after resourceVersion conflict")
	}
	latest := created.DeepCopy()
	if err := cli.Get(ctx, client.ObjectKeyFromObject(created), latest); err != nil {
		t.Fatal(err)
	}
	if latest.GetAnnotations()["external-change"] != "keep" {
		t.Fatal("concurrent metadata write overwritten")
	}
	record, _, err := w.loadResourceIntent(ctx, owner, latest, "")
	if err != nil {
		t.Fatal(err)
	}
	if record == nil || record.Effective == nil {
		t.Fatal("lost first Create defaulting evidence after RV retry")
	}
	if record.PublicationProtocol != intentPublicationProtocol || record.BaseResourceVersion == created.GetResourceVersion() {
		t.Fatal("retry record is not bound to the refreshed resourceVersion")
	}
	normalized := intentTestJSON(record.Effective.Object)
	if !strings.Contains(normalized, `"optional":false`) {
		t.Fatal("API default omitted from retained effective intent")
	}
	var records corev1.SecretList
	if err := cli.List(ctx, &records); err != nil {
		t.Fatal(err)
	}
	if len(records.Items) != 1 {
		t.Fatalf("metadata retry created %d records instead of one", len(records.Items))
	}
}

type intentDefaultingPreviewClient struct {
	client.Client
	previewRecord *resourceIntent
	createErr     error
}

func (c *intentDefaultingPreviewClient) Create(_ context.Context, obj client.Object, opts ...client.CreateOption) error {
	options := &client.CreateOptions{}
	for _, option := range opts {
		option.ApplyToCreate(options)
	}
	if len(options.DryRun) != 1 || options.DryRun[0] != metav1.DryRunAll {
		return errors.New("preflight attempted a real Secret write")
	}
	secret, ok := obj.(*corev1.Secret)
	if !ok {
		return errors.New("expected intent Secret")
	}
	var err error
	c.previewRecord, err = decodeResourceIntent(secret.Data)
	if err != nil {
		return err
	}
	return c.createErr
}

func (c *intentDefaultingPreviewClient) Patch(_ context.Context, obj client.Object, _ client.Patch, opts ...client.PatchOption) error {
	options := &client.PatchOptions{}
	for _, option := range opts {
		option.ApplyToPatch(options)
	}
	if len(options.DryRun) != 1 || options.DryRun[0] != metav1.DryRunAll {
		return errors.New("preflight attempted a real write")
	}
	u, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return errors.New("expected unstructured object")
	}
	env, _, err := unstructured.NestedSlice(u.Object, "spec", "env")
	if err != nil {
		return err
	}
	item, ok := env[0].(map[string]any)
	if !ok {
		return errors.New("expected env item")
	}
	source, ok := item["valueFrom"].(map[string]any)
	if !ok {
		return errors.New("expected valueFrom")
	}
	source["optional"] = false
	return unstructured.SetNestedSlice(u.Object, env, "spec", "env")
}
func TestIntentPreflightValidatesEncodedSecretBeforeCreation(t *testing.T) {
	for _, tc := range []struct {
		name      string
		size      int
		createErr error
	}{
		{name: "small_record", size: 64 * 1024},
		{name: "two_copies_above_old_limit", size: 280 * 1024},
		{name: "record_above_one_mib", size: 600 * 1024},
		{name: "api_server_rejection_propagated", size: 64 * 1024, createErr: apierrors.NewForbidden(schema.GroupResource{Resource: "secrets"}, "record", errors.New("admission denied"))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			owner := intentUnitOwner()
			desired := intentTestObject(t, `{"env":[{"id":"AUTH","valueFrom":{"secret":"s"}}]}`)
			if err := unstructured.SetNestedField(desired.Object, strings.Repeat("x", tc.size), "spec", "data", "large"); err != nil {
				t.Fatal(err)
			}
			cli := intentUnitClient(t)
			preview := &intentDefaultingPreviewClient{Client: cli, createErr: tc.createErr}
			w := &ManagedResourceWriter{Client: preview, Reader: cli, IntentSchemas: intentUnitSchemas{converter: intentTestConverter(t)}}
			err := w.preflightResourceIntent(context.Background(), owner, desired, "")
			if !errors.Is(err, tc.createErr) {
				t.Fatalf("error=%v want=%v", err, tc.createErr)
			}
			if preview.previewRecord == nil || preview.previewRecord.Effective == nil {
				t.Fatal("Secret preview omitted the API-defaulted copy")
			}
			var records corev1.SecretList
			if err := cli.List(context.Background(), &records); err != nil || len(records.Items) != 0 {
				t.Fatalf("preflight persisted a record: count=%d err=%v", len(records.Items), err)
			}
		})
	}
}
