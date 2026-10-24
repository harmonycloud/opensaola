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
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	"github.com/harmonycloud/opensaola/internal/k8s"
	"github.com/harmonycloud/opensaola/internal/service/consts"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
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
	return c.Client.Patch(ctx, obj, patch, opts...)
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
	for _, add := range []func(*runtime.Scheme) error{v1.AddToScheme, corev1.AddToScheme, rbacv1.AddToScheme, apiextensionsv1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
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
		if ref := metav1.GetControllerOf(secret); ref == nil || ref.UID != f.owner.UID || secret.Immutable == nil || !*secret.Immutable {
			t.Fatal("adoption did not establish a private immutable record for the current owner")
		}
		return live
	}
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
			if len(records.Items) != 0 {
				t.Fatal("failed adoption leaked uncommitted records")
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
