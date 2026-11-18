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
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

// Faults wrap a real API client. Dry-run transformations model admission, while
// commit transformations are persisted to exercise response validation and
// guarded history compensation against real resourceVersions.
type intentPolicyFaultClient struct {
	client.Client
	targetKind        string
	afterTargetCreate func(context.Context, *unstructured.Unstructured) error
	preview           func(*unstructured.Unstructured)
	committed         func(context.Context, *unstructured.Unstructured) error
	beforeCommit      func(context.Context) error
	prepared          func(context.Context) error
	preparedRecord    func(context.Context, *corev1.Secret) error
	beforeSecretPatch func(context.Context, *corev1.Secret) error
	afterSecretPatch  func(context.Context, *corev1.Secret) error
}

func (c *intentPolicyFaultClient) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	options := &client.PatchOptions{}
	for _, option := range opts {
		option.ApplyToPatch(options)
	}
	u, isTarget := obj.(*unstructured.Unstructured)
	targetKind := c.targetKind
	if targetKind == "" {
		targetKind = "DeclaredTest"
	}
	isTarget = isTarget && u.GetKind() == targetKind
	secret, isRecord := obj.(*corev1.Secret)
	isRecord = isRecord && secret.Labels[intentRecordLabel] != "" && len(options.DryRun) == 0
	if isRecord && c.beforeSecretPatch != nil {
		if err := c.beforeSecretPatch(ctx, secret); err != nil {
			return err
		}
	}
	if isTarget && len(options.DryRun) == 0 && c.beforeCommit != nil {
		if err := c.beforeCommit(ctx); err != nil {
			return err
		}
	}
	if err := c.Client.Patch(ctx, obj, patch, opts...); err != nil {
		return err
	}
	if isRecord && c.afterSecretPatch != nil {
		return c.afterSecretPatch(ctx, secret)
	}
	if !isTarget {
		return nil
	}
	if len(options.DryRun) > 0 && c.preview != nil {
		c.preview(u)
	}
	if len(options.DryRun) == 0 && c.committed != nil {
		return c.committed(ctx, u)
	}
	return nil
}

func (c *intentPolicyFaultClient) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	if err := c.Client.Create(ctx, obj, opts...); err != nil {
		return err
	}
	options := &client.CreateOptions{}
	for _, option := range opts {
		option.ApplyToCreate(options)
	}
	if target, ok := obj.(*unstructured.Unstructured); ok && len(options.DryRun) == 0 && c.afterTargetCreate != nil {
		return c.afterTargetCreate(ctx, target)
	}
	if secret, ok := obj.(*corev1.Secret); ok && secret.Labels[intentRecordLabel] != "" && len(options.DryRun) == 0 {
		if c.prepared != nil {
			if err := c.prepared(ctx); err != nil {
				return err
			}
		}
		if c.preparedRecord != nil {
			return c.preparedRecord(ctx, secret)
		}
	}
	return nil
}

func (c *intentPolicyFaultClient) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	if err := c.Client.Update(ctx, obj, opts...); err != nil {
		return err
	}
	if secret, ok := obj.(*corev1.Secret); ok && ledgerPending(secret) {
		if c.prepared != nil {
			if err := c.prepared(ctx); err != nil {
				return err
			}
		}
		if c.preparedRecord != nil {
			return c.preparedRecord(ctx, secret)
		}
	}
	return nil
}

func TestDeclaredIntentWriterAPI(t *testing.T) {
	unknown := true
	gvk := schema.GroupVersionKind{Group: "intent.audit.invalid", Version: "v1", Kind: "DeclaredTest"}
	crd := &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "declaredtests." + gvk.Group},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: gvk.Group, Scope: apiextensionsv1.NamespaceScoped,
			Names:    apiextensionsv1.CustomResourceDefinitionNames{Plural: "declaredtests", Singular: "declaredtest", Kind: gvk.Kind},
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{{Name: gvk.Version, Served: true, Storage: true, Schema: &apiextensionsv1.CustomResourceValidation{OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{Type: "object", Properties: map[string]apiextensionsv1.JSONSchemaProps{"spec": {Type: "object", XPreserveUnknownFields: &unknown}}}}}},
		},
	}
	environment := &envtest.Environment{CRDDirectoryPaths: []string{filepath.Join("..", "..", "config", "crd", "bases")}, CRDs: []*apiextensionsv1.CustomResourceDefinition{crd, typedIntentDeletionCRD()}, ErrorIfCRDPathMissing: true}
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
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := wait.PollUntilContextTimeout(ctx, 100*time.Millisecond, 15*time.Second, true, func(ctx context.Context) (bool, error) {
		_, _, err := resolver.Resolve(ctx, gvk)
		return err == nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "declared-policy-"}}
	if err := cli.Create(ctx, ns); err != nil {
		t.Fatal(err)
	}
	t.Run("deletion_contract", func(t *testing.T) {
		runIntentDeletionAPICases(t, ctx, cli, resolver, gvk, ns.Name)
	})
	newFixture := func(t *testing.T) (*v1.Middleware, *unstructured.Unstructured, *ManagedResourceWriter) {
		t.Helper()
		owner := &v1.Middleware{ObjectMeta: metav1.ObjectMeta{GenerateName: "owner-", Namespace: ns.Name}, Spec: v1.MiddlewareSpec{Baseline: "fixture"}}
		if err := cli.Create(ctx, owner); err != nil {
			t.Fatal(err)
		}
		desired := &unstructured.Unstructured{Object: map[string]any{"apiVersion": gvk.GroupVersion().String(), "kind": gvk.Kind, "metadata": map[string]any{"name": owner.Name, "namespace": ns.Name}, "spec": map[string]any{"setting": "before"}}}
		ref, err := intentOwnerReference(owner)
		if err != nil {
			t.Fatal(err)
		}
		desired.SetOwnerReferences([]metav1.OwnerReference{ref})
		return owner, desired, &ManagedResourceWriter{Client: cli, Reader: cli, IntentSchemas: resolver}
	}
	get := func(t *testing.T, obj *unstructured.Unstructured) *unstructured.Unstructured {
		t.Helper()
		result := &unstructured.Unstructured{}
		result.SetGroupVersionKind(obj.GroupVersionKind())
		if err := cli.Get(ctx, client.ObjectKeyFromObject(obj), result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	set := func(t *testing.T, obj *unstructured.Unstructured, value any, fields ...string) {
		t.Helper()
		if err := unstructured.SetNestedField(obj.Object, value, fields...); err != nil {
			t.Fatal(err)
		}
	}
	assertSetting := func(t *testing.T, obj *unstructured.Unstructured, expected string) {
		t.Helper()
		actual, _, err := unstructured.NestedString(obj.Object, "spec", "setting")
		if err != nil || actual != expected {
			t.Fatalf("setting did not converge: value=%q err=%v", actual, err)
		}
	}
	for _, keepReceipt := range []bool{true, false} {
		name := "first_post_timeout_verified"
		if !keepReceipt {
			name = "first_post_timeout_unverifiable"
		}
		t.Run(name, func(t *testing.T) {
			owner, desired, writer := newFixture(t)
			set(t, desired, "remove-after-restart", "spec", "temporary")
			writer.Client = &intentPolicyFaultClient{Client: cli, afterTargetCreate: func(ctx context.Context, actual *unstructured.Unstructured) error {
				set(t, actual, "outside", "spec", "external")
				if !keepReceipt {
					a := actual.GetAnnotations()
					delete(a, AnnotationResourceIntentOperation)
					actual.SetAnnotations(a)
				}
				if err := cli.Update(ctx, actual); err != nil {
					return err
				}
				return apierrors.NewTimeoutError("lost POST response", 1)
			}}
			err := writer.Reconcile(ctx, owner, desired, "")
			live := get(t, desired)
			if !keepReceipt {
				if !apierrors.IsTimeout(err) || live.GetAnnotations()[AnnotationResourceIntent] != "" {
					t.Fatalf("unverified creation accepted: %v", err)
				}
				return
			}
			if err != nil || live.GetAnnotations()[AnnotationResourceIntent] == "" {
				t.Fatalf("verified POST did not establish history: %v", err)
			}
			writer = &ManagedResourceWriter{Client: cli, Reader: cli, IntentSchemas: resolver}
			unstructured.RemoveNestedField(desired.Object, "spec", "temporary")
			if err := writer.Reconcile(ctx, owner, desired, ""); err != nil {
				t.Fatal(err)
			}
			live = get(t, desired)
			_, found, err := unstructured.NestedFieldNoCopy(live.Object, "spec", "temporary")
			external, _, externalErr := unstructured.NestedString(live.Object, "spec", "external")
			if err != nil || found || externalErr != nil || external != "outside" {
				t.Fatal("withdrawal lost external data or retained known first declaration")
			}
		})
	}
	t.Run("legacy_atomic_record_upgrades_without_losing_external_members", func(t *testing.T) {
		owner, before, writer := newFixture(t)
		set(t, before, []any{map[string]any{"name": "init", "env": []any{map[string]any{"name": "EnableExternal", "value": "false"}}}}, "spec", "initContainers")
		live := before.DeepCopy()
		set(t, live, nil, "spec", "containers")
		set(t, live, "external", "spec", "operatorOnly")
		set(t, live, []any{map[string]any{"name": "init", "env": []any{map[string]any{"name": "EnableExternal", "value": "false"}, map[string]any{"name": "INJECTED", "value": "keep"}}, "command": []any{"/init.sh"}}}, "spec", "initContainers")
		if err := cli.Create(ctx, live); err != nil {
			t.Fatal(err)
		}
		_, hash, err := resolver.Resolve(ctx, gvk)
		if err != nil {
			t.Fatal(err)
		}
		record := resourceIntent{Version: 1, PublicationProtocol: intentPublicationProtocol, BaseResourceVersion: live.GetResourceVersion(), OwnerUID: owner.UID, ResourceUID: live.GetUID(), SchemaHash: hash, Desired: intentObject(before), Preserved: []string{".spec.containers"}}
		oldSecret, err := buildResourceIntentSecret(owner, record)
		if err != nil {
			t.Fatal(err)
		}
		if err := cli.Create(ctx, oldSecret); err != nil {
			t.Fatal(err)
		}
		setIntentPointer(live, oldSecret.Name)
		if err := cli.Update(ctx, live); err != nil {
			t.Fatal(err)
		}
		desired := before.DeepCopy()
		set(t, desired, []any{map[string]any{"name": "broker", "volumeMounts": []any{map[string]any{"name": "log", "mountPath": "/root/logs"}}}}, "spec", "containers")
		set(t, desired, []any{map[string]any{"name": "log", "emptyDir": map[string]any{}}}, "spec", "volumes")
		set(t, desired, []any{map[string]any{"name": "init", "env": []any{map[string]any{"name": "EnableExternal", "value": "true"}}}}, "spec", "initContainers")
		if err := writer.Reconcile(ctx, owner, desired, ""); err != nil {
			t.Fatal(err)
		}
		current := get(t, desired)
		converter, _, _ := resolver.Resolve(ctx, gvk)
		if err := ValidateDeclaredResourceIntent(converter, intentObject(desired), intentObject(current)); err != nil {
			t.Fatal(err)
		}
		init, _, _ := unstructured.NestedSlice(current.Object, "spec", "initContainers")
		if len(init) == 0 {
			t.Fatal("missing init container")
		}
		item := intentSchemaTestMap(t, init[0])
		env, _, envErr := unstructured.NestedSlice(item, "env")
		command, _, cmdErr := unstructured.NestedSlice(item, "command")
		if envErr != nil || cmdErr != nil || len(env) != 2 || len(command) != 1 {
			t.Fatal("upgrade removed external init fields")
		}
		upgraded, _, err := writer.loadResourceIntent(ctx, owner, current, "")
		if err != nil || upgraded.Version != currentResourceIntentVersion || upgraded.SchemaHash != DeclaredResourceIntentSchemaHash(hash) {
			t.Fatalf("legacy record not upgraded: %v", err)
		}
		if _, found, _ := unstructured.NestedString(upgraded.Desired.Object, "spec", "operatorOnly"); found {
			t.Fatal("live field promoted into owned history")
		}
		if err := cli.Get(ctx, client.ObjectKeyFromObject(oldSecret), &corev1.Secret{}); !apierrors.IsNotFound(err) {
			t.Fatal("old record not reclaimed")
		}
		// The declaration is unchanged in the next call, including an already
		// recorded value: a later outside edit must not disable its retry.
		set(t, current, nil, "spec", "containers")
		if err := cli.Update(ctx, current); err != nil {
			t.Fatal(err)
		}
		if err := writer.Reconcile(ctx, owner, desired, ""); err != nil {
			t.Fatal(err)
		}
		current = get(t, desired)
		if err := ValidateDeclaredResourceIntent(converter, intentObject(desired), intentObject(current)); err != nil {
			t.Fatal(err)
		}
		// Delete only our named volume/mount; keep separately injected members.
		set(t, current, []any{map[string]any{"name": "log", "emptyDir": map[string]any{}}, map[string]any{"name": "monitor", "emptyDir": map[string]any{}}}, "spec", "volumes")
		if err := cli.Update(ctx, current); err != nil {
			t.Fatal(err)
		}
		set(t, desired, []any{map[string]any{"name": "broker"}}, "spec", "containers")
		unstructured.RemoveNestedField(desired.Object, "spec", "volumes")
		if err := writer.Reconcile(ctx, owner, desired, ""); err != nil {
			t.Fatal(err)
		}
		current = get(t, desired)
		volumes, _, _ := unstructured.NestedSlice(current.Object, "spec", "volumes")
		containers, _, _ := unstructured.NestedSlice(current.Object, "spec", "containers")
		if len(volumes) != 1 || len(containers) == 0 || intentSchemaTestMap(t, volumes[0])["name"] != "monitor" || intentSchemaTestMap(t, containers[0])["volumeMounts"] != nil {
			t.Fatal("named deletion did not preserve only the external volume")
		}
	})

	t.Run("legacy_without_record_preserves_opaque_members", func(t *testing.T) {
		owner, desired, writer := newFixture(t)
		live := desired.DeepCopy()
		set(t, live, []any{map[string]any{"name": "broker", "env": []any{map[string]any{"name": "EXTERNAL", "value": "keep"}}}, map[string]any{"name": "sidecar", "image": "external"}}, "spec", "containers")
		if err := cli.Create(ctx, live); err != nil {
			t.Fatal(err)
		}
		set(t, desired, []any{map[string]any{"name": "broker", "image": "declared"}}, "spec", "containers")
		if err := writer.Reconcile(ctx, owner, desired, ""); err != nil {
			t.Fatal(err)
		}
		containers, _, _ := unstructured.NestedSlice(get(t, desired).Object, "spec", "containers")
		if len(containers) != 2 || intentSchemaTestMap(t, containers[0])["env"] == nil || intentSchemaTestMap(t, containers[0])["image"] != "declared" {
			t.Fatal("bootstrap replaced external named members")
		}
	})

	for _, failure := range []string{"dry_run_mutation", "commit_mutation", "pause_after_record_preparation", "resource_version_conflict"} {
		t.Run(failure, func(t *testing.T) {
			owner, desired, writer := newFixture(t)
			if err := writer.Reconcile(ctx, owner, desired, ""); err != nil {
				t.Fatal(err)
			}
			before := get(t, desired)
			oldPointer := before.GetAnnotations()[AnnotationResourceIntent]
			set(t, desired, "after", "spec", "setting")
			fault := &intentPolicyFaultClient{Client: cli}
			injected := false
			switch failure {
			case "dry_run_mutation":
				fault.preview = func(actual *unstructured.Unstructured) {
					injected = true
					set(t, actual, "sensitive-mutated-value", "spec", "setting")
				}
			case "commit_mutation":
				fault.committed = func(ctx context.Context, actual *unstructured.Unstructured) error {
					if injected {
						return nil
					}
					injected = true
					set(t, actual, "sensitive-mutated-value", "spec", "setting")
					return cli.Update(ctx, actual)
				}
			case "pause_after_record_preparation":
				fault.prepared = func(ctx context.Context) error {
					injected = true
					owner.Annotations = map[string]string{v1.AnnotationSuspendReconcile: "true"}
					return cli.Update(ctx, owner)
				}
			case "resource_version_conflict":
				fault.beforeCommit = func(ctx context.Context) error {
					if injected {
						return nil
					}
					injected = true
					actual := get(t, desired)
					set(t, actual, "keep", "spec", "concurrent")
					if err := cli.Update(ctx, actual); err != nil {
						return err
					}
					return apierrors.NewConflict(schema.GroupResource{Group: gvk.Group, Resource: "declaredtests"}, actual.GetName(), errors.New("injected concurrent writer"))
				}
			}
			writer.Client = fault
			err := writer.Reconcile(ctx, owner, desired, "")
			if !injected {
				t.Fatal("fault was not exercised")
			}
			actual := get(t, desired)
			if failure == "resource_version_conflict" {
				if err != nil {
					t.Fatal(err)
				}
				assertSetting(t, actual, "after")
				if concurrent, _, _ := unstructured.NestedString(actual.Object, "spec", "concurrent"); concurrent != "keep" {
					t.Fatal("retry overwrote the concurrent field")
				}
				return
			}
			if err == nil || strings.Contains(err.Error(), "sensitive-mutated-value") {
				t.Fatalf("incomplete write did not produce a safe error: %v", err)
			}
			if failure == "commit_mutation" {
				restored, _, err := writer.loadResourceIntent(ctx, owner, actual, "")
				if err != nil || restored == nil {
					t.Fatalf("incomplete write lost the last successful history: %v", err)
				}
				assertSetting(t, restored.Desired, "before")
			} else {
				if actual.GetAnnotations()[AnnotationResourceIntent] != oldPointer {
					t.Fatal("incomplete write advanced successful history")
				}
				assertSetting(t, actual, "before")
				if err := cli.Get(ctx, client.ObjectKey{Namespace: owner.Namespace, Name: oldPointer}, &corev1.Secret{}); err != nil {
					t.Fatal("last valid record was removed")
				}
			}
			writer.Client = cli
			if failure == "pause_after_record_preparation" {
				owner.Annotations = nil
				if err := cli.Update(ctx, owner); err != nil {
					t.Fatal(err)
				}
			}
			// Same owner generation and exactly the same desired object retry.
			if err := writer.Reconcile(ctx, owner, desired, ""); err != nil {
				t.Fatal(err)
			}
			actual = get(t, desired)
			assertSetting(t, actual, "after")
			if actual.GetAnnotations()[AnnotationResourceIntent] != oldPointer || actual.GetAnnotations()[AnnotationResourceIntentOperation] == before.GetAnnotations()[AnnotationResourceIntentOperation] {
				t.Fatal("successful retry did not replace history")
			}
		})
	}

	t.Run("pending_ledger_preserves_collected_v1_history", func(t *testing.T) {
		owner, desired, writer := newFixture(t)
		if err := writer.Reconcile(ctx, owner, desired, ""); err != nil {
			t.Fatal(err)
		}
		before := get(t, desired)
		oldRecord, oldSecret, err := writer.loadResourceIntent(ctx, owner, before, "")
		if err != nil {
			t.Fatal(err)
		}
		// An interrupted v1 upgrade must recover v1 deletion semantics as well
		// as its desired values; it cannot stamp the old history as v2.
		_, schemaHash, err := resolver.Resolve(ctx, gvk)
		if err != nil {
			t.Fatal(err)
		}
		oldRecord.Version = 1
		oldRecord.SchemaHash = schemaHash
		legacySecret, err := buildResourceIntentSecret(owner, *oldRecord)
		if err != nil {
			t.Fatal(err)
		}
		if err := cli.Create(ctx, legacySecret); err != nil {
			t.Fatal(err)
		}
		setIntentPointer(before, legacySecret.Name)
		if err := cli.Update(ctx, before); err != nil {
			t.Fatal(err)
		}
		writer.discardIntentRecord(ctx, oldSecret)
		oldSecret = legacySecret
		set(t, desired, "after", "spec", "setting")
		injected := false
		collector := &ResourceIntentCollector{Client: cli, Reader: cli}
		writer.Client = &intentPolicyFaultClient{Client: cli, committed: func(ctx context.Context, actual *unstructured.Unstructured) error {
			if injected {
				return nil
			}
			injected = true
			set(t, actual, "admission-change", "spec", "setting")
			if err := cli.Update(ctx, actual); err != nil {
				return err
			}
			deleted, err := collector.collect(ctx, oldSecret, time.Now().Add(time.Hour))
			if err != nil || !deleted {
				t.Fatalf("previous history was not collected in the commit window: deleted=%t err=%v", deleted, err)
			}
			return nil
		}}
		if err := writer.Reconcile(ctx, owner, desired, ""); err == nil {
			t.Fatal("changed commit reported success")
		}
		actual := get(t, desired)
		restored, restoredSecret, err := writer.loadResourceIntent(ctx, owner, actual, "")
		if err != nil || restored == nil {
			t.Fatalf("compensation published missing history: %v", err)
		}
		if restoredSecret.Name == oldSecret.Name || !ledgerPending(restoredSecret) || restored.BaseResourceVersion != oldRecord.BaseResourceVersion || restored.Version != oldRecord.Version || restored.SchemaHash != oldRecord.SchemaHash || !reflect.DeepEqual(restored.Desired, oldRecord.Desired) {
			t.Fatal("pending record did not preserve the exact v1 deletion baseline")
		}
		if err := cli.Get(ctx, client.ObjectKeyFromObject(oldSecret), &corev1.Secret{}); !apierrors.IsNotFound(err) {
			t.Fatal("test did not exercise disappearance of the original history")
		}
		writer.Client = cli
		if err := writer.Reconcile(ctx, owner, desired, ""); err != nil {
			t.Fatal(err)
		}
		assertSetting(t, get(t, desired), "after")
	})

	t.Run("retry_fences_delayed_business_write_before_withdrawing_change", func(t *testing.T) {
		owner, desired, writer := newFixture(t)
		if err := writer.Reconcile(ctx, owner, desired, ""); err != nil {
			t.Fatal(err)
		}
		before := get(t, desired)
		withdrawn := desired.DeepCopy()
		set(t, desired, "after", "spec", "setting")
		var delayed client.Patch
		writer.Client = &intentPolicyFaultClient{Client: cli, preparedRecord: func(ctx context.Context, record *corev1.Secret) error {
			after := before.DeepCopy()
			set(t, after, "after", "spec", "setting")
			var err error
			delayed, err = intentPatch(before, after)
			if err != nil {
				return err
			}
			return apierrors.NewTimeoutError("injected delayed patch", 1)
		}}
		if err := writer.Reconcile(ctx, owner, desired, ""); err == nil || delayed == nil {
			t.Fatal("did not leave a delayed field transaction")
		}
		writer.Client = cli
		if err := writer.Reconcile(ctx, owner, withdrawn, ""); err != nil {
			t.Fatal(err)
		}
		actual := get(t, withdrawn)
		if err := cli.Patch(ctx, actual, delayed); err == nil {
			t.Fatal("late request overwrote the withdrawn change")
		}
		assertSetting(t, get(t, withdrawn), "before")
	})
	t.Run("failed_initialization_keeps_pending_record_for_retry", func(t *testing.T) {
		owner, desired, writer := newFixture(t)
		live := desired.DeepCopy()
		if err := cli.Create(ctx, live); err != nil {
			t.Fatal(err)
		}
		commits := 0
		writer.Client = &intentPolicyFaultClient{
			Client: cli,
			beforeCommit: func(context.Context) error {
				commits++
				if commits == 2 {
					return apierrors.NewConflict(schema.GroupResource{Group: gvk.Group, Resource: "declaredtests"}, live.GetName(), errors.New("compensation conflict"))
				}
				return nil
			},
			committed: func(ctx context.Context, actual *unstructured.Unstructured) error {
				set(t, actual, "admission-change", "spec", "setting")
				return cli.Update(ctx, actual)
			},
		}
		if err := writer.initializeResourceIntent(ctx, owner, live, desired, "", false); err == nil {
			t.Fatal("initialization with failed compensation reported success")
		}
		actual := get(t, desired)
		if commits != 1 || actual.GetAnnotations()[AnnotationResourceIntent] == "" {
			t.Fatal("initialization issued an unexpected business write or failed to publish the receipt")
		}
		if _, _, err := writer.loadResourceIntent(ctx, owner, actual, ""); err != nil {
			t.Fatalf("initialization cleanup deleted the active record: %v", err)
		}
		writer.Client = cli
		if err := writer.Reconcile(ctx, owner, desired, ""); err != nil {
			t.Fatal(err)
		}
		assertSetting(t, get(t, desired), "before")
	})

	t.Run("native_normalization_noop_still_records_new_declarations", func(t *testing.T) {
		owner, desired, writer := newFixture(t)
		desired.SetAPIVersion("apps/v1")
		desired.SetKind("Deployment")
		desired.Object["spec"] = map[string]any{
			"replicas": int64(0),
			"selector": map[string]any{"matchLabels": map[string]any{"app": desired.GetName()}},
			"template": map[string]any{
				"metadata": map[string]any{"labels": map[string]any{"app": desired.GetName()}},
				"spec": map[string]any{
					"volumes":    []any{map[string]any{"name": "logs", "emptyDir": map[string]any{}}},
					"containers": []any{map[string]any{"name": "main", "image": "registry.k8s.io/pause:3.10", "volumeMounts": []any{map[string]any{"name": "logs", "mountPath": "/logs", "readOnly": false}}}},
				},
			},
		}
		if err := writer.Reconcile(ctx, owner, desired, ""); err != nil {
			t.Fatal(err)
		}
		before := get(t, desired)
		if err := writer.Reconcile(ctx, owner, desired, ""); err != nil {
			t.Fatal(err)
		}
		if actual := get(t, desired); actual.GetResourceVersion() != before.GetResourceVersion() || actual.GetAnnotations()[AnnotationResourceIntent] != before.GetAnnotations()[AnnotationResourceIntent] {
			t.Fatal("native omission of false repeatedly published unchanged declarations")
		}
		// Taking responsibility for a value already defaulted by the API is a
		// new declaration even when no business value needs to change.
		set(t, desired, int64(30), "spec", "template", "spec", "terminationGracePeriodSeconds")
		if err := writer.Reconcile(ctx, owner, desired, ""); err != nil {
			t.Fatal(err)
		}
		actual := get(t, desired)
		if actual.GetAnnotations()[AnnotationResourceIntentOperation] == before.GetAnnotations()[AnnotationResourceIntentOperation] {
			t.Fatal("new declaration was incorrectly skipped as a no-op")
		}
		recorded, _, err := writer.loadResourceIntent(ctx, owner, actual, "")
		if err != nil {
			t.Fatal(err)
		}
		if value, found, err := unstructured.NestedInt64(recorded.Desired.Object, "spec", "template", "spec", "terminationGracePeriodSeconds"); err != nil || !found || value != 30 {
			t.Fatalf("explicit responsibility was not recorded: %v", err)
		}
	})

	t.Run("rebuild_retry_preserves_external_fields", func(t *testing.T) { testRecreateDoesNotClaimExternalFields(t, ctx, cli, resolver, ns.Name) })
	t.Run("missing_recreation_never_rewrites_existing_business_fields", func(t *testing.T) {
		owner, desired, writer := newFixture(t)
		if err := writer.RecreateMissing(ctx, owner, desired, ""); err != nil {
			t.Fatal(err)
		}
		actual := get(t, desired)
		if actual.GetAnnotations()[AnnotationResourceIntent] == "" {
			t.Fatal("recreated primary has no history")
		}
		set(t, actual, "external", "spec", "setting")
		if err := cli.Update(ctx, actual); err != nil {
			t.Fatal(err)
		}
		before := actual.DeepCopy()
		if err := writer.RecreateMissing(ctx, owner, desired, ""); err == nil {
			t.Fatal("existing nonconverged primary reported recovered")
		}
		if !reflect.DeepEqual(get(t, desired).Object, before.Object) {
			t.Fatal("missing-only recovery changed an existing resource")
		}
	})
}
