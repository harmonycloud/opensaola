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
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	"github.com/harmonycloud/opensaola/internal/k8s"
	"github.com/harmonycloud/opensaola/internal/service/consts"
	admissionv1 "k8s.io/api/admission/v1"
	registrationv1 "k8s.io/api/admissionregistration/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/yaml"
)

type adoptionAPIClient struct {
	client.Client
	key          client.ObjectKey
	onSecret     func(context.Context) error
	beforeCommit func(context.Context) error
	afterConfirm func(context.Context) error
}

func (c *adoptionAPIClient) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	if _, ok := obj.(*corev1.Secret); ok && c.onSecret != nil {
		callback := c.onSecret
		c.onSecret = nil
		if err := callback(ctx); err != nil {
			return err
		}
	}
	return c.Client.Create(ctx, obj, opts...)
}

func (c *adoptionAPIClient) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	options := &client.PatchOptions{}
	for _, opt := range opts {
		opt.ApplyToPatch(options)
	}
	if patch.Type() == types.JSONPatchType && len(options.DryRun) == 0 && client.ObjectKeyFromObject(obj) == c.key && c.beforeCommit != nil {
		callback := c.beforeCommit
		c.beforeCommit = nil
		if err := callback(ctx); err != nil {
			return err
		}
	}
	if err := c.Client.Patch(ctx, obj, patch, opts...); err != nil {
		return err
	}
	if _, history := obj.(*corev1.Secret); history && len(options.DryRun) == 0 && c.afterConfirm != nil {
		callback := c.afterConfirm
		c.afterConfirm = nil
		return callback(ctx)
	}
	return nil
}

func TestConfigurationAdoptionAPI(t *testing.T) {
	environment := &envtest.Environment{CRDDirectoryPaths: []string{filepath.Join("..", "..", "..", "config", "crd", "bases")}, ErrorIfCRDPathMissing: true}
	restConfig, err := environment.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := environment.Stop(); err != nil {
			t.Error(err)
		}
	})
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{v1.AddToScheme, corev1.AddToScheme, appsv1.AddToScheme, registrationv1.AddToScheme, rbacv1.AddToScheme, apiextensionsv1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	var writes atomic.Int64
	restConfig.Wrap(func(next http.RoundTripper) http.RoundTripper {
		return configurationWriteCounter{next, &writes}
	})
	cli, err := client.New(restConfig, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := k8s.NewIntentSchemaResolver(restConfig)
	if err != nil {
		t.Fatal(err)
	}
	k8s.ConfigureManagedResources(cli)
	k8s.ConfigureResourceIntents(resolver)
	t.Cleanup(func() { k8s.ConfigureManagedResources(nil); k8s.ConfigureResourceIntents(nil) })
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	type fixture struct {
		owner  *v1.MiddlewareOperator
		config *v1.MiddlewareConfiguration
		live   *unstructured.Unstructured
	}
	newFixture := func(t *testing.T, crd bool) *fixture {
		t.Helper()
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "configuration-adoption-"}}
		if err := cli.Create(ctx, ns); err != nil {
			t.Fatal(err)
		}
		owner := &v1.MiddlewareOperator{ObjectMeta: metav1.ObjectMeta{Name: "operator", Namespace: ns.Name}}
		if err := cli.Create(ctx, owner); err != nil {
			t.Fatal(err)
		}
		configuration := &v1.MiddlewareConfiguration{ObjectMeta: metav1.ObjectMeta{Name: "source-" + ns.Name}}
		configuration.Spec.Template = fmt.Sprintf("apiVersion: rbac.authorization.k8s.io/v1\nkind: ClusterRole\nmetadata:\n  name: %s\nrules:\n- apiGroups: ['']\n  resources: [configmaps]\n  verbs: [get]\n", ns.Name)
		if crd {
			group := ns.Name + ".example.com"
			configuration.Spec.Template = fmt.Sprintf(`apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: samples.%s
spec:
  group: %s
  names: {kind: Sample, plural: samples, singular: sample}
  scope: Namespaced
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
            x-kubernetes-preserve-unknown-fields: true
`, group, group)
		}
		if err := cli.Create(ctx, configuration); err != nil {
			t.Fatal(err)
		}
		live := &unstructured.Unstructured{}
		if err := yaml.Unmarshal([]byte(configuration.Spec.Template), live); err != nil {
			t.Fatal(err)
		}
		live.SetLabels(map[string]string{v1.LabelApp: owner.Name, "legacy-only": "preserve", k8s.LabelResourceIntentGCFence: "prior-fence"})
		live.SetAnnotations(map[string]string{v1.LabelConfigurations: configuration.Name})
		if crd {
			if err := unstructured.SetNestedStringSlice(live.Object, []string{"legacy-sample"}, "spec", "names", "shortNames"); err != nil {
				t.Fatal(err)
			}
		}
		if err := cli.Create(ctx, live, client.FieldOwner(v1.FieldOwner)); err != nil {
			t.Fatal(err)
		}
		return &fixture{owner, configuration, live}
	}
	get := func(t *testing.T, f *fixture) *unstructured.Unstructured {
		t.Helper()
		live := f.live.DeepCopy()
		if err := cli.Get(ctx, client.ObjectKeyFromObject(live), live); err != nil {
			t.Fatal(err)
		}
		return live
	}
	checkIdentity := func(t *testing.T, f *fixture, result *renderedConfigurationApplyResult) *unstructured.Unstructured {
		t.Helper()
		live := get(t, f)
		if live.GetUID() != f.live.GetUID() || live.GetLabels()["legacy-only"] != "preserve" || live.GetLabels()[k8s.LabelResourceIntentGCFence] != "prior-fence" {
			t.Fatal("adoption replaced the resource or removed an omitted legacy field")
		}
		if live.GetKind() == "CustomResourceDefinition" {
			shortNames, _, err := unstructured.NestedStringSlice(live.Object, "spec", "names", "shortNames")
			if err != nil || !reflect.DeepEqual(shortNames, []string{"legacy-sample"}) {
				t.Fatalf("adoption removed an omitted legacy spec field: %v", err)
			}
		}
		if live.GetAnnotations()[v1.AnnotationConfigurationOwnerUID] != string(f.owner.UID) || live.GetAnnotations()[v1.AnnotationConfigurationUID] != string(f.config.UID) {
			t.Fatal("adoption did not publish the current owner and Configuration identity")
		}
		if result == nil || result.inventory == nil || result.inventory.OwnerUID != f.owner.UID || result.inventory.ResourceUID != live.GetUID() || result.inventory.ConfigurationUID != f.config.UID {
			t.Fatal("successful adoption did not establish current cleanup inventory")
		}
		secret := &corev1.Secret{}
		if err := cli.Get(ctx, client.ObjectKey{Namespace: f.owner.Namespace, Name: live.GetAnnotations()[k8s.AnnotationResourceIntent]}, secret); err != nil {
			t.Fatal(err)
		}
		if ref := metav1.GetControllerOf(secret); ref == nil || ref.UID != f.owner.UID || (secret.Immutable != nil && *secret.Immutable) || secret.Labels["middleware.cn/field-history"] != "v1" || secret.Annotations[k8s.AnnotationResourceIntentOperation] != "" || live.GetAnnotations()[k8s.AnnotationResourceIntentOperation] == "" {
			t.Fatal("adoption did not confirm private field history for the current owner")
		}
		return live
	}
	t.Run("AtomicDefaults", func(t *testing.T) {
		testAtomicDefaults(t, ctx, cli, resolver, &writes)
	})
	t.Run("StatefulSetFirstPublish", func(t *testing.T) {
		testStatefulSetFirstPublish(t, ctx, cli, &writes)
	})
	for _, format := range []string{"current", "legacy_immutable", "externally_modified_rules"} {
		t.Run("same_owner_history_survives_missing_owner_marker_"+format, func(t *testing.T) {
			f := newFixture(t, false)
			if _, err := Handle(ctx, cli, f.owner, consts.HandleActionPublish, f.config); err != nil {
				t.Fatal(err)
			}
			live := get(t, f)
			ledger := live.GetAnnotations()[k8s.AnnotationResourceIntent]
			annotations := live.GetAnnotations()
			if format == "legacy_immutable" {
				var old corev1.Secret
				if err := cli.Get(ctx, client.ObjectKey{Namespace: f.owner.Namespace, Name: ledger}, &old); err != nil {
					t.Fatal(err)
				}
				legacy := old.DeepCopy()
				legacy.ObjectMeta = metav1.ObjectMeta{Namespace: old.Namespace, GenerateName: "old-intent-", Labels: map[string]string{"middleware.cn/resource-intent-record": string(f.owner.UID)}, OwnerReferences: old.OwnerReferences}
				legacy.Immutable = new(true)
				if err := cli.Create(ctx, legacy); err != nil {
					t.Fatal(err)
				}
				annotations[k8s.AnnotationResourceIntent] = legacy.Name
				if err := cli.Delete(ctx, &old); err != nil {
					t.Fatal(err)
				}
			}
			if format == "externally_modified_rules" {
				live.Object["rules"] = []any{map[string]any{"apiGroups": []any{""}, "resources": []any{"configmaps"}, "verbs": []any{"list"}}}
			}
			delete(annotations, v1.AnnotationConfigurationOwnerUID)
			annotations["external-note"] = "preserve"
			live.SetAnnotations(annotations)
			if err := cli.Update(ctx, live); err != nil {
				t.Fatal(err)
			}
			f.config.Spec.Template = fmt.Sprintf("apiVersion: rbac.authorization.k8s.io/v1\nkind: ClusterRole\nmetadata:\n  name: %s\n", f.live.GetName())
			if err := cli.Update(ctx, f.config); err != nil {
				t.Fatal(err)
			}
			result, err := Handle(ctx, cli, f.owner, consts.HandleActionUpdate, f.config)
			actual := get(t, f)
			if format == "externally_modified_rules" {
				if err == nil || result != nil || !reflect.DeepEqual(live.Object, actual.Object) {
					t.Fatalf("changed rule did not block the whole adoption: result=%v err=%v", result, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			rules, found := actual.Object["rules"]
			if found && rules != nil && !reflect.DeepEqual(rules, []any{}) {
				t.Fatal("previously declared RBAC rule survived adoption")
			}
			if ledger == "" || actual.GetAnnotations()[k8s.AnnotationResourceIntent] != ledger || actual.GetAnnotations()["external-note"] != "preserve" || actual.GetAnnotations()[v1.AnnotationConfigurationOwnerUID] != string(f.owner.UID) || result.inventory == nil || result.inventory.ResourceUID != live.GetUID() {
				t.Fatal("adoption did not preserve history and restore identity")
			}
		})
	}
	t.Run("same_name_replacement_after_publication_cannot_enter_inventory", func(t *testing.T) {
		f := newFixture(t, false)
		replaced := false
		wrapped := &adoptionAPIClient{Client: cli, key: client.ObjectKeyFromObject(f.live), afterConfirm: func(ctx context.Context) error {
			live := get(t, f)
			if err := cli.Delete(ctx, live); err != nil {
				return err
			}
			duplicate := live.DeepCopy()
			duplicate.SetResourceVersion("")
			duplicate.SetUID("")
			duplicate.SetManagedFields(nil)
			if err := cli.Create(ctx, duplicate); err != nil {
				return err
			}
			replaced = duplicate.GetUID() != live.GetUID()
			return nil
		}}
		result, err := Handle(ctx, wrapped, f.owner, consts.HandleActionPublish, f.config)
		if !replaced || err == nil || !strings.Contains(err.Error(), "target changed after publication") || result != nil {
			t.Fatalf("replacement promoted to cleanup inventory: replaced=%v result=%v err=%v", replaced, result, err)
		}
	})
	t.Run("shared_deployment_releases_mounts_and_restores_replica_default", func(t *testing.T) {
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "shared-deployment-"}}
		if err := cli.Create(ctx, ns); err != nil {
			t.Fatal(err)
		}
		owner := &v1.Middleware{ObjectMeta: metav1.ObjectMeta{Name: "owner", Namespace: ns.Name}, Spec: v1.MiddlewareSpec{Baseline: "example"}}
		if err := cli.Create(ctx, owner); err != nil {
			t.Fatal(err)
		}
		conf := &v1.MiddlewareConfiguration{ObjectMeta: metav1.ObjectMeta{Name: "shared-" + ns.Name}}
		const template = `apiVersion: apps/v1
kind: Deployment
metadata: {name: shared}
spec:
  selector: {matchLabels: {app: shared}}
  template:
    metadata: {labels: {app: shared}}
    spec:
      containers:
      - name: broker
        image: example.invalid/broker:1
`
		const mounts = `        volumeMounts:
        - {name: log, mountPath: /one}
        - {name: log, mountPath: /two}
      volumes:
      - {name: log, emptyDir: {}}
`
		conf.Spec.Template = template + mounts + "  replicas: 3\n"
		if err := cli.Create(ctx, conf); err != nil {
			t.Fatal(err)
		}
		result, err := Handle(ctx, cli, owner, consts.HandleActionPublish, conf)
		if err != nil || result == nil || result.inventory == nil {
			t.Fatalf("publish: %v / %v", result, err)
		}
		uid := result.inventory.ResourceUID
		target := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"name": "shared", "namespace": ns.Name}}}
		read := func() *unstructured.Unstructured {
			t.Helper()
			actual := target.DeepCopy()
			if err := cli.Get(ctx, client.ObjectKeyFromObject(target), actual); err != nil {
				t.Fatal(err)
			}
			return actual
		}
		current := read()
		containers, _, _ := unstructured.NestedSlice(current.Object, "spec", "template", "spec", "containers")
		first, ok := containers[0].(map[string]any)
		if !ok {
			t.Fatal("invalid container")
		}
		mounted, _, _ := unstructured.NestedSlice(first, "volumeMounts")
		if len(mounted) != 2 {
			t.Fatalf("mountPath identity lost: %v", mounted)
		}
		ext := target.DeepCopy()
		ext.Object["spec"] = map[string]any{"template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"name": "broker", "env": []any{map[string]any{"name": "AUDIT_KEEP", "value": "external"}}}}}}}
		if err := cli.Patch(ctx, ext, client.Apply, client.FieldOwner("auditor")); err != nil {
			t.Fatal(err)
		}
		conf.Spec.Template = template
		result, err = Handle(ctx, cli, owner, consts.HandleActionUpdate, conf)
		if err != nil || result == nil || result.inventory == nil || result.inventory.ResourceUID != uid {
			t.Fatalf("update: %v / %v", result, err)
		}
		current = read()
		replicas, _, _ := unstructured.NestedInt64(current.Object, "spec", "replicas")
		containers, _, _ = unstructured.NestedSlice(current.Object, "spec", "template", "spec", "containers")
		first, ok = containers[0].(map[string]any)
		if !ok {
			t.Fatal("invalid container")
		}
		kept, _, _ := unstructured.NestedSlice(first, "env")
		mounted, _, _ = unstructured.NestedSlice(first, "volumeMounts")
		volumes, _, _ := unstructured.NestedSlice(current.Object, "spec", "template", "spec", "volumes")
		if replicas != 1 || len(mounted) != 0 || len(volumes) != 0 || !reflect.DeepEqual(kept, []any{map[string]any{"name": "AUDIT_KEEP", "value": "external"}}) {
			t.Fatalf("replicas=%d mounts=%v volumes=%v env=%v", replicas, mounted, volumes, kept)
		}
	})
	t.Run("retained_CRD_existing_CR_and_recreated_sources", func(t *testing.T) {
		f := newFixture(t, true)
		group, _, err := unstructured.NestedString(f.live.Object, "spec", "group")
		if err != nil {
			t.Fatal(err)
		}
		if err := wait.PollUntilContextTimeout(ctx, 100*time.Millisecond, 20*time.Second, true, func(ctx context.Context) (bool, error) {
			var crd apiextensionsv1.CustomResourceDefinition
			if err := cli.Get(ctx, client.ObjectKeyFromObject(f.live), &crd); err != nil {
				return false, err
			}
			for _, condition := range crd.Status.Conditions {
				if condition.Type == apiextensionsv1.Established && condition.Status == apiextensionsv1.ConditionTrue {
					return true, nil
				}
			}
			return false, nil
		}); err != nil {
			t.Fatal(err)
		}
		dynamicClient, err := dynamic.NewForConfig(restConfig)
		if err != nil {
			t.Fatal(err)
		}
		resources := dynamicClient.Resource(schema.GroupVersionResource{Group: group, Version: "v1", Resource: "samples"}).Namespace(f.owner.Namespace)
		sample, err := resources.Create(ctx, &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": group + "/v1", "kind": "Sample", "metadata": map[string]any{"name": "existing"}, "spec": map[string]any{"value": "preserved"},
		}}, metav1.CreateOptions{})
		if err != nil {
			t.Fatal(err)
		}
		result, err := Handle(ctx, cli, f.owner, consts.HandleActionPublish, f.config)
		if err != nil {
			t.Fatal(err)
		}
		checkIdentity(t, f, result)
		oldOwnerUID := f.owner.UID
		if err := cli.Delete(ctx, f.owner); err != nil {
			t.Fatal(err)
		}
		f.owner = &v1.MiddlewareOperator{ObjectMeta: metav1.ObjectMeta{Name: f.owner.Name, Namespace: f.owner.Namespace}}
		if err := cli.Create(ctx, f.owner); err != nil {
			t.Fatal(err)
		}
		if f.owner.UID == oldOwnerUID {
			t.Fatal("test did not recreate the owner")
		}
		result, err = Handle(ctx, cli, f.owner, consts.HandleActionPublish, f.config)
		if err != nil {
			t.Fatal(err)
		}
		checkIdentity(t, f, result)
		oldConfigurationUID := f.config.UID
		if err := cli.Delete(ctx, f.config); err != nil {
			t.Fatal(err)
		}
		f.config = &v1.MiddlewareConfiguration{ObjectMeta: metav1.ObjectMeta{Name: f.config.Name}, Spec: f.config.Spec}
		if err := cli.Create(ctx, f.config); err != nil {
			t.Fatal(err)
		}
		if f.config.UID == oldConfigurationUID {
			t.Fatal("test did not recreate the Configuration")
		}
		result, err = Handle(ctx, cli, f.owner, consts.HandleActionUpdate, f.config)
		if err != nil {
			t.Fatal(err)
		}
		live := checkIdentity(t, f, result)
		if metav1.GetControllerOf(live) != nil || result.inventory.DisablePolicy != v1.ConfigurationDisablePolicyOrphan {
			t.Fatal("retained CRD acquired controller ownership or delete policy")
		}
		if err := DeleteDisabledRenderedConfigurationResources(ctx, cli, f.owner, []v1.RenderedConfigurationResource{*result.inventory}, nil); err != nil {
			t.Fatal(err)
		}
		if _, err := Handle(ctx, cli, f.owner, consts.HandleActionDelete, f.config); err != nil {
			t.Fatal(err)
		}
		get(t, f)
		retained, err := resources.Get(ctx, sample.GetName(), metav1.GetOptions{})
		if err != nil || retained.GetUID() != sample.GetUID() || !reflect.DeepEqual(retained.Object["spec"], sample.Object["spec"]) {
			t.Fatalf("existing CR changed during adoption or cleanup: %v", err)
		}
	})
	t.Run("namespaced_stale_controller_and_unrelated_reference", func(t *testing.T) {
		f := newFixture(t, false)
		f.config.Spec.Template = fmt.Sprintf("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: adopted\n  namespace: %s\ndata:\n  setting: new\n", f.owner.Namespace)
		if err := cli.Update(ctx, f.config); err != nil {
			t.Fatal(err)
		}
		other := &v1.MiddlewareOperator{ObjectMeta: metav1.ObjectMeta{Name: "reference-only", Namespace: f.owner.Namespace}}
		if err := cli.Create(ctx, other); err != nil {
			t.Fatal(err)
		}
		f.live = &unstructured.Unstructured{}
		if err := yaml.Unmarshal([]byte(f.config.Spec.Template), f.live); err != nil {
			t.Fatal(err)
		}
		f.live.Object["data"] = map[string]any{"setting": "old", "legacy": "preserve"}
		f.live.SetLabels(map[string]string{v1.LabelApp: f.owner.Name, "legacy-only": "preserve", k8s.LabelResourceIntentGCFence: "prior-fence"})
		f.live.SetAnnotations(map[string]string{v1.LabelConfigurations: f.config.Name, v1.AnnotationConfigurationOwnerUID: "deleted-owner-uid"})
		f.live.SetOwnerReferences([]metav1.OwnerReference{
			{APIVersion: v1.GroupVersion.String(), Kind: "MiddlewareOperator", Name: f.owner.Name, UID: "deleted-owner-uid", Controller: new(true)},
			{APIVersion: v1.GroupVersion.String(), Kind: "MiddlewareOperator", Name: other.Name, UID: other.UID, Controller: new(false)},
		})
		if err := cli.Create(ctx, f.live, client.FieldOwner(v1.FieldOwner)); err != nil {
			t.Fatal(err)
		}
		result, err := Handle(ctx, cli, f.owner, consts.HandleActionPublish, f.config)
		if err != nil {
			t.Fatal(err)
		}
		live := checkIdentity(t, f, result)
		data, _, err := unstructured.NestedStringMap(live.Object, "data")
		if err != nil || !reflect.DeepEqual(data, map[string]string{"setting": "new", "legacy": "preserve"}) {
			t.Fatalf("adoption lost omitted values or failed to apply desired values: %v", err)
		}
		ref := metav1.GetControllerOf(live)
		if ref == nil || ref.UID != f.owner.UID || len(live.GetOwnerReferences()) != 2 {
			t.Fatal("adoption failed to replace the stale controller while preserving unrelated references")
		}
	})
	for _, scenario := range []string{"record_failure", "owner_paused", "claimant_appears", "active_owner_appears", "resource_replaced"} {
		t.Run(scenario, func(t *testing.T) {
			f := newFixture(t, false)
			before := get(t, f)
			wrapped := &adoptionAPIClient{Client: cli, key: client.ObjectKeyFromObject(f.live)}
			switch scenario {
			case "record_failure":
				wrapped.onSecret = func(context.Context) error { return fmt.Errorf("injected intent Secret failure") }
			case "owner_paused":
				wrapped.onSecret = func(ctx context.Context) error {
					f.owner.Annotations = map[string]string{v1.AnnotationSuspendReconcile: "true"}
					return cli.Update(ctx, f.owner)
				}
			case "claimant_appears":
				wrapped.onSecret = func(ctx context.Context) error {
					other := &v1.Middleware{ObjectMeta: metav1.ObjectMeta{Name: f.owner.Name, Namespace: f.owner.Namespace}, Spec: v1.MiddlewareSpec{Configurations: []v1.Configuration{{Name: f.config.Name}}}}
					return cli.Create(ctx, other)
				}
			case "active_owner_appears":
				wrapped.beforeCommit = func(ctx context.Context) error {
					other := &v1.MiddlewareOperator{ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: f.owner.Namespace}}
					if err := cli.Create(ctx, other); err != nil {
						return err
					}
					current := get(t, f)
					a := current.GetAnnotations()
					a[v1.AnnotationConfigurationOwnerUID] = string(other.UID)
					current.SetAnnotations(a)
					return cli.Update(ctx, current)
				}
			case "resource_replaced":
				wrapped.beforeCommit = func(ctx context.Context) error {
					if err := cli.Delete(ctx, f.live); err != nil {
						return err
					}
					replacement := f.live.DeepCopy()
					replacement.SetUID("")
					replacement.SetResourceVersion("")
					replacement.SetManagedFields(nil)
					replacement.SetAnnotations(nil)
					replacement.SetLabels(map[string]string{"external": "replacement"})
					return cli.Create(ctx, replacement)
				}
			}
			if _, err := Handle(ctx, wrapped, f.owner, consts.HandleActionPublish, f.config); err == nil {
				t.Fatal("unsafe or incomplete adoption unexpectedly succeeded")
			}
			after := get(t, f)
			if after.GetAnnotations()[k8s.AnnotationResourceIntent] != "" || after.GetAnnotations()[v1.AnnotationConfigurationOwnerUID] == string(f.owner.UID) {
				t.Fatal("failed adoption published a record pointer or owner markers")
			}
			if scenario == "record_failure" || scenario == "owner_paused" || scenario == "claimant_appears" {
				if !reflect.DeepEqual(before.Object, after.Object) {
					t.Fatal("failed adoption changed the business resource")
				}
			}
			var records corev1.SecretList
			if err := cli.List(ctx, &records, client.InNamespace(f.owner.Namespace), client.MatchingLabels{"middleware.cn/resource-intent-record": string(f.owner.UID)}); err != nil {
				t.Fatal(err)
			}
			if scenario == "record_failure" {
				if len(records.Items) != 0 {
					t.Fatal("failed secret creation left field history")
				}
				return
			}
			if len(records.Items) != 1 || records.Items[0].Annotations[k8s.AnnotationResourceIntentOperation] == "" || !metav1.IsControlledBy(&records.Items[0], f.owner) {
				t.Fatal("failed adoption did not retain exactly one identifiable, unconfirmed attempt")
			}
			if scenario == "owner_paused" {
				f.owner.Annotations = nil
				if err := cli.Update(ctx, f.owner); err != nil {
					t.Fatal(err)
				}
				result, err := Handle(ctx, cli, f.owner, consts.HandleActionPublish, f.config)
				if err != nil || result == nil || result.inventory == nil {
					t.Fatalf("eligible adoption could not resume the pending operation: %v", err)
				}
				current := get(t, f)
				if current.GetAnnotations()[k8s.AnnotationResourceIntent] != records.Items[0].Name || current.GetAnnotations()[v1.AnnotationConfigurationOwnerUID] != string(f.owner.UID) {
					t.Fatal("resumed adoption did not publish the same ledger and new owner")
				}
			}
		})
	}
	t.Run("external_Helm_source_is_not_adopted", func(t *testing.T) {
		f := newFixture(t, false)
		live := get(t, f)
		live.SetLabels(map[string]string{"app.kubernetes.io/managed-by": "Helm"})
		live.SetAnnotations(map[string]string{"meta.helm.sh/release-name": "external-release"})
		if err := cli.Update(ctx, live); err != nil {
			t.Fatal(err)
		}
		if _, err := Handle(ctx, cli, f.owner, consts.HandleActionPublish, f.config); err == nil || !strings.Contains(err.Error(), "external-release") {
			t.Fatalf("expected diagnostic identifying the external Helm release, got %v", err)
		}
		if after := get(t, f); after.GetResourceVersion() != live.GetResourceVersion() {
			t.Fatal("external Helm resource was modified")
		}
	})
}

func testStatefulSetFirstPublish(t *testing.T, ctx context.Context, cli client.Client, writes *atomic.Int64) {
	const template = `apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: database
spec:
  serviceName: database
  selector:
    matchLabels: {app: database}
  template:
    metadata:
      labels: {app: database}
    spec:
      containers:
      - name: database
        image: registry.k8s.io/pause:3.10
        volumeMounts:
        - name: data
          mountPath: /var/lib/data
  volumeClaimTemplates:
  - metadata:
      name: data
    spec:
      accessModes: [ReadWriteOnce]
      resources:
        requests:
          storage: 5Gi
`
	for _, tc := range []struct {
		name   string
		mutate bool
	}{
		{"apiserver_defaults_do_not_change_declared_storage", false},
		{"admission_rewrites_declared_storage", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "stateful-publish-"}}
			if err := cli.Create(ctx, ns); err != nil {
				t.Fatal(err)
			}
			owner := &v1.MiddlewareOperator{ObjectMeta: metav1.ObjectMeta{Name: "database", Namespace: ns.Name}}
			if err := cli.Create(ctx, owner); err != nil {
				t.Fatal(err)
			}
			cfg := &v1.MiddlewareConfiguration{ObjectMeta: metav1.ObjectMeta{Name: ns.Name}}
			cfg.Spec.Template = template
			if err := cli.Create(ctx, cfg); err != nil {
				t.Fatal(err)
			}
			target := client.ObjectKey{Namespace: ns.Name, Name: "database"}
			var dryRuns *atomic.Int32
			if tc.mutate {
				dryRuns = installConfigurationMutationWebhook(t, ctx, cli, ns.Name, template, `[{"op":"replace","path":"/spec/volumeClaimTemplates/0/spec/resources/requests/storage","value":"1Gi"}]`)
			}
			result, err := Handle(ctx, cli, owner, consts.HandleActionPublish, cfg)
			if tc.mutate {
				if err == nil || !strings.Contains(err.Error(), "volumeClaimTemplates") || result != nil {
					t.Fatalf("wanted an error identifying the changed declaration and no inventory, got result %v error %v", result, err)
				}
				if dryRuns.Load() == 0 {
					t.Fatal("the API server did not send the writer's dry-run to admission")
				}
				if err := cli.Get(ctx, target, &appsv1.StatefulSet{}); !apierrors.IsNotFound(err) {
					t.Fatalf("expected no persisted StatefulSet: %v", err)
				}
				var secrets corev1.SecretList
				if err := cli.List(ctx, &secrets, client.InNamespace(ns.Name)); err != nil {
					t.Fatal(err)
				}
				if len(secrets.Items) != 0 {
					t.Fatalf("rejected first publish created %d history Secrets", len(secrets.Items))
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var live appsv1.StatefulSet
			if err := cli.Get(ctx, target, &live); err != nil {
				t.Fatal(err)
			}
			if result == nil || result.inventory == nil || result.inventory.ResourceUID != live.UID || len(live.Spec.VolumeClaimTemplates) != 1 {
				t.Fatalf("missing applied claim template or inventory: %+v", result)
			}
			claim := live.Spec.VolumeClaimTemplates[0]
			if claim.Name != "data" || claim.Spec.Resources.Requests.Storage().String() != "5Gi" || claim.Spec.VolumeMode == nil || *claim.Spec.VolumeMode != corev1.PersistentVolumeFilesystem {
				t.Fatalf("expected data/5Gi with the server-defaulted Filesystem mode, got %+v", claim)
			}
			if live.Annotations[k8s.AnnotationResourceIntent] == "" {
				t.Fatal("no persisted field history")
			}
			key := client.ObjectKey{Namespace: ns.Name, Name: live.Annotations[k8s.AnnotationResourceIntent]}
			var history corev1.Secret
			if err := cli.Get(ctx, key, &history); err != nil {
				t.Fatal(err)
			}
			beforeReplay := writes.Load()
			if _, err := Handle(ctx, cli, owner, consts.HandleActionUpdate, cfg); err != nil {
				t.Fatal(err)
			}
			if delta := writes.Load() - beforeReplay; delta != 0 {
				t.Fatalf("unchanged configuration sent %d non-dry-run writes", delta)
			}
			var repeated appsv1.StatefulSet
			var sameHistory corev1.Secret
			if err := cli.Get(ctx, target, &repeated); err != nil {
				t.Fatal(err)
			}
			if err := cli.Get(ctx, key, &sameHistory); err != nil {
				t.Fatal(err)
			}
			if repeated.UID != live.UID || repeated.ResourceVersion != live.ResourceVersion || sameHistory.ResourceVersion != history.ResourceVersion {
				t.Fatal("unchanged configuration rewrote the StatefulSet or its field history")
			}
		})
	}
}

// Use the API server's mutating admission chain, with a dry-run probe to wait
// for its dynamic webhook cache before testing the actual publication path.
func installConfigurationMutationWebhook(t *testing.T, ctx context.Context, cli client.Client, namespace, template, patch string) *atomic.Int32 {
	t.Helper()
	var dryRuns atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var review admissionv1.AdmissionReview
		if err := json.NewDecoder(r.Body).Decode(&review); err != nil || review.Request == nil {
			http.Error(w, "invalid admission review", http.StatusBadRequest)
			return
		}
		if review.Request.DryRun != nil && *review.Request.DryRun {
			dryRuns.Add(1)
		}
		patchType := admissionv1.PatchTypeJSONPatch
		response := admissionv1.AdmissionReview{TypeMeta: review.TypeMeta, Response: &admissionv1.AdmissionResponse{
			UID: review.Request.UID, Allowed: true, PatchType: &patchType, Patch: []byte(patch),
		}}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(response); err != nil {
			t.Errorf("write admission response: %v", err)
		}
	}))
	t.Cleanup(server.Close)
	var probe unstructured.Unstructured
	if err := yaml.Unmarshal([]byte(template), &probe); err != nil {
		t.Fatal(err)
	}
	probe.SetNamespace(namespace)
	gvk := probe.GroupVersionKind()
	resource, err := cli.RESTMapper().RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		t.Fatal(err)
	}
	sideEffects := registrationv1.SideEffectClassNone
	config := &registrationv1.MutatingWebhookConfiguration{ObjectMeta: metav1.ObjectMeta{Name: namespace}, Webhooks: []registrationv1.MutatingWebhook{{
		Name: "configuration.test.opensaola.io", SideEffects: &sideEffects, AdmissionReviewVersions: []string{"v1"},
		NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{corev1.LabelMetadataName: namespace}},
		ClientConfig:      registrationv1.WebhookClientConfig{URL: &server.URL, CABundle: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})},
		Rules: []registrationv1.RuleWithOperations{{Operations: []registrationv1.OperationType{registrationv1.Create}, Rule: registrationv1.Rule{
			APIGroups: []string{gvk.Group}, APIVersions: []string{gvk.Version}, Resources: []string{resource.Resource.Resource},
		}}},
	}}}
	if err := cli.Create(ctx, config); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := cli.Delete(ctx, config); err != nil {
			t.Error(err)
		}
	})
	if err := wait.PollUntilContextTimeout(ctx, 100*time.Millisecond, 20*time.Second, true, func(ctx context.Context) (bool, error) {
		preview := probe.DeepCopy()
		if err := cli.Create(ctx, preview, client.DryRunAll); err != nil {
			return false, err
		}
		return dryRuns.Load() > 0, nil
	}); err != nil {
		t.Fatalf("webhook never admitted the dry-run probe: %v", err)
	}
	dryRuns.Store(0)
	return &dryRuns
}

// Observe writes at the HTTP boundary so even a no-op PATCH (unchanged RV) is
// counted and server-side dry-run requests are distinguishable from mutations.
type configurationWriteCounter struct {
	next   http.RoundTripper
	writes *atomic.Int64
}

func (c configurationWriteCounter) RoundTrip(r *http.Request) (*http.Response, error) {
	switch r.Method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		if r.URL.Query().Get("dryRun") == "" {
			c.writes.Add(1)
		}
	}
	return c.next.RoundTrip(r)
}
