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

package middlewareconfiguration

import (
	"context"
	"testing"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	"github.com/harmonycloud/opensaola/internal/service/consts"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// orphanTestClient equips the fake client with an explicit RESTMapper. The
// default fake RESTMapper cannot resolve core types, which would silently
// skip the namespace-scoped ownerReference branch under test.
func orphanTestClient(t *testing.T, objects ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := v1.AddToScheme(scheme); err != nil {
		t.Fatalf("add OpenSaola scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}
	mapper := meta.NewDefaultRESTMapper([]schema.GroupVersion{{Group: "", Version: "v1"}, v1.GroupVersion})
	mapper.Add(schema.GroupVersionKind{Group: "", Version: "v1", Kind: "ConfigMap"}, meta.RESTScopeNamespace)
	mapper.Add(v1.GroupVersion.WithKind("Middleware"), meta.RESTScopeNamespace)
	return fake.NewClientBuilder().WithScheme(scheme).WithRESTMapper(mapper).WithObjects(objects...).Build()
}

const orphanTestConfigMapTemplate = `
apiVersion: v1
kind: ConfigMap
metadata:
  name: shared-sample
  namespace: mv1
data:
  sample.conf: |
    key=value
`

func orphanTestConfig(annotations map[string]string) *v1.MiddlewareConfiguration {
	cfg := testConfig(annotations)
	cfg.Spec.Template = orphanTestConfigMapTemplate
	return cfg
}

func fetchAppliedConfigMap(t *testing.T, cli client.Client) *corev1.ConfigMap {
	t.Helper()
	cm := &corev1.ConfigMap{}
	if err := cli.Get(context.Background(), client.ObjectKey{Namespace: "mv1", Name: "shared-sample"}, cm); err != nil {
		t.Fatalf("get applied configmap: %v", err)
	}
	return cm
}

func TestHandleOrphanPolicySkipsControllerOwnership(t *testing.T) {
	t.Parallel()
	cli := orphanTestClient(t, testMiddleware())
	cfg := orphanTestConfig(map[string]string{
		v1.AnnotationConfigurationDisablePolicy: v1.ConfigurationDisablePolicyOrphan,
	})
	if _, err := Handle(context.Background(), cli, testMiddleware(), consts.HandleActionPublish, cfg); err != nil {
		t.Fatalf("handle orphan configuration: %v", err)
	}
	cm := fetchAppliedConfigMap(t, cli)
	if refs := cm.GetOwnerReferences(); len(refs) != 0 {
		t.Fatalf("orphan resource must stay out of the owner GC domain, got ownerReferences %v", refs)
	}
}

func TestHandleDefaultPolicyTakesControllerOwnership(t *testing.T) {
	t.Parallel()
	cli := orphanTestClient(t, testMiddleware())
	if _, err := Handle(context.Background(), cli, testMiddleware(), consts.HandleActionPublish, orphanTestConfig(nil)); err != nil {
		t.Fatalf("handle default configuration: %v", err)
	}
	cm := fetchAppliedConfigMap(t, cli)
	controller := metav1.GetControllerOf(cm)
	if controller == nil || controller.UID != testMiddleware().UID {
		t.Fatalf("default policy resource must carry the owner controller reference, got %v", cm.GetOwnerReferences())
	}
}

func TestHandleOrphanPolicyStripsPriorControllerOwnership(t *testing.T) {
	t.Parallel()
	existing := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "shared-sample",
			Namespace:       "mv1",
			OwnerReferences: []metav1.OwnerReference{middlewareOwnerRef()},
		},
	}
	cli := orphanTestClient(t, testMiddleware(), existing)
	cfg := orphanTestConfig(map[string]string{
		v1.AnnotationConfigurationDisablePolicy: v1.ConfigurationDisablePolicyOrphan,
	})
	if _, err := Handle(context.Background(), cli, testMiddleware(), consts.HandleActionUpdate, cfg); err != nil {
		t.Fatalf("handle orphan migration: %v", err)
	}
	cm := fetchAppliedConfigMap(t, cli)
	if refs := cm.GetOwnerReferences(); len(refs) != 0 {
		t.Fatalf("orphan migration must strip the prior controller reference, got %v", refs)
	}
}

func TestRemoveControllerOwnerReference(t *testing.T) {
	t.Parallel()
	foreign := redisClusterOwnerRef()
	tests := []struct {
		name  string
		refs  []metav1.OwnerReference
		owner metav1.Object
		want  int
	}{
		{name: "nil refs", refs: nil, owner: testMiddleware(), want: 0},
		{name: "nil owner keeps refs", refs: []metav1.OwnerReference{middlewareOwnerRef()}, owner: nil, want: 1},
		{name: "drops only the owner controller reference", refs: []metav1.OwnerReference{middlewareOwnerRef(), foreign}, owner: testMiddleware(), want: 1},
		{name: "keeps foreign controller reference", refs: []metav1.OwnerReference{foreign}, owner: testMiddleware(), want: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := removeControllerOwnerReference(tt.refs, tt.owner)
			if len(got) != tt.want {
				t.Fatalf("expected %d refs, got %d: %v", tt.want, len(got), got)
			}
			for _, ref := range got {
				if ref.Controller != nil && *ref.Controller && tt.owner != nil && ref.UID == tt.owner.GetUID() {
					t.Fatalf("owner controller reference survived: %v", ref)
				}
			}
		})
	}
}

func TestHandleDeleteKeepsOrphanResource(t *testing.T) {
	t.Parallel()
	existing := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "shared-sample",
			Namespace:       "mv1",
			OwnerReferences: []metav1.OwnerReference{middlewareOwnerRef()},
		},
	}
	cli := orphanTestClient(t, testMiddleware(), existing)
	cfg := orphanTestConfig(map[string]string{
		v1.AnnotationConfigurationDisablePolicy: v1.ConfigurationDisablePolicyOrphan,
	})
	if _, err := Handle(context.Background(), cli, testMiddleware(), consts.HandleActionDelete, cfg); err != nil {
		t.Fatalf("handle orphan delete: %v", err)
	}
	cm := &corev1.ConfigMap{}
	if err := cli.Get(context.Background(), client.ObjectKey{Namespace: "mv1", Name: "shared-sample"}, cm); err != nil {
		t.Fatalf("orphan resource must survive owner deletion: %v", err)
	}
}

func TestHandleDeleteRemovesDefaultResource(t *testing.T) {
	t.Parallel()
	existing := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "shared-sample",
			Namespace:       "mv1",
			OwnerReferences: []metav1.OwnerReference{middlewareOwnerRef()},
			Labels:          map[string]string{v1.LabelApp: testMiddleware().Name},
			Annotations:     map[string]string{v1.LabelConfigurations: "redis-sentinel-configmap"},
		},
	}
	cli := orphanTestClient(t, testMiddleware(), existing)
	if _, err := Handle(context.Background(), cli, testMiddleware(), consts.HandleActionDelete, orphanTestConfig(nil)); err != nil {
		t.Fatalf("handle default delete: %v", err)
	}
	cm := &corev1.ConfigMap{}
	err := cli.Get(context.Background(), client.ObjectKey{Namespace: "mv1", Name: "shared-sample"}, cm)
	if err == nil {
		t.Fatal("default policy resource must be deleted with its owner")
	}
}

func TestDeleteTemplateRenderedResourcesKeepsOrphanResource(t *testing.T) {
	t.Parallel()
	owner := testMiddleware()
	owner.Labels = map[string]string{v1.LabelPackageName: "redis"}
	owner.Spec.Configurations = []v1.Configuration{{Name: "redis-sentinel-configmap"}}
	cfg := orphanTestConfig(map[string]string{
		v1.AnnotationConfigurationDisablePolicy: v1.ConfigurationDisablePolicyOrphan,
	})
	cfg.Labels = map[string]string{v1.LabelPackageName: "redis"}
	existing := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "shared-sample",
			Namespace:   "mv1",
			Labels:      map[string]string{v1.LabelApp: owner.Name},
			Annotations: map[string]string{v1.LabelConfigurations: cfg.Name},
		},
	}
	cli := orphanTestClient(t, owner, cfg, existing)
	if err := DeleteTemplateRenderedResources(context.Background(), cli, owner, owner); err != nil {
		t.Fatalf("delete template rendered resources: %v", err)
	}
	cm := &corev1.ConfigMap{}
	if err := cli.Get(context.Background(), client.ObjectKey{Namespace: "mv1", Name: "shared-sample"}, cm); err != nil {
		t.Fatalf("orphan resource must survive the owner delete cleanup: %v", err)
	}
}

// Exercise the owner deletion entry point, including its list fallback, with
// policies on the actual rendered object rather than only the MCF metadata.
func TestDeleteTemplateRenderedResourcePolicies(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		top     map[string]string
		live    map[string]string
		deleted bool
	}{
		{name: "resource disable orphan", live: map[string]string{v1.AnnotationConfigurationDisablePolicy: "orphan"}},
		{name: "resource delete orphan", live: map[string]string{v1.AnnotationConfigurationDeletePolicy: "orphan"}},
		{name: "resource delete overrides top orphan", top: map[string]string{v1.AnnotationConfigurationDeletePolicy: "orphan"}, live: map[string]string{v1.AnnotationConfigurationDeletePolicy: "delete"}, deleted: true},
		{name: "resource orphan overrides top delete", top: map[string]string{v1.AnnotationConfigurationDeletePolicy: "delete"}, live: map[string]string{v1.AnnotationConfigurationDeletePolicy: "orphan"}},
		{name: "explicit top delete overrides disable orphan", top: map[string]string{v1.AnnotationConfigurationDeletePolicy: "delete"}, live: map[string]string{v1.AnnotationConfigurationDisablePolicy: "orphan"}, deleted: true},
		{name: "default deletes", deleted: true},
	}
	for _, tt := range tests {
		for _, path := range []string{"by-name", "fallback"} {
			t.Run(tt.name+"/"+path, func(t *testing.T) {
				t.Parallel()
				owner := testMiddleware()
				owner.Labels = map[string]string{v1.LabelPackageName: "redis"}
				owner.Spec.Configurations = []v1.Configuration{{Name: "redis-sentinel-configmap"}}
				cfg := orphanTestConfig(tt.top)
				cfg.Labels = map[string]string{v1.LabelPackageName: "redis"}
				name := "shared-sample"
				if path == "fallback" {
					name = "previous-name"
				}
				annotations := map[string]string{v1.LabelConfigurations: cfg.Name}
				for k, v := range tt.live {
					annotations[k] = v
				}
				existing := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
					Name: name, Namespace: "mv1", Labels: map[string]string{v1.LabelApp: owner.Name}, Annotations: annotations,
				}}
				cli := orphanTestClient(t, owner, cfg, existing)
				if err := DeleteTemplateRenderedResources(context.Background(), cli, owner, owner); err != nil {
					t.Fatal(err)
				}
				err := cli.Get(context.Background(), client.ObjectKeyFromObject(existing), &corev1.ConfigMap{})
				if tt.deleted {
					if !apierrors.IsNotFound(err) {
						t.Fatalf("expected deletion, got %v", err)
					}
				} else if err != nil {
					t.Fatalf("expected retained resource, got %v", err)
				}
			})
		}
	}
}
