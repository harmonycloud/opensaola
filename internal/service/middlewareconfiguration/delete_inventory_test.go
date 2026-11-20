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
	"errors"
	"testing"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

type testRefreshableMapper struct {
	meta.RESTMapper
	refresh func() (meta.RESTMapper, error)
}

func (m testRefreshableMapper) FreshRESTMapper() (meta.RESTMapper, error) {
	return m.refresh()
}

func TestRecordedDiscoveryFailures(t *testing.T) {
	entry := inventoryTestResource(true, v1.ConfigurationDisablePolicyDelete)
	gvk := schema.GroupVersionKind{Group: entry.Group, Version: entry.Version, Kind: entry.Kind}
	stale := meta.NewDefaultRESTMapper([]schema.GroupVersion{gvk.GroupVersion()})
	stale.Add(gvk, meta.RESTScopeNamespace)
	kindGone := meta.NewDefaultRESTMapper(nil)
	absent := apierrors.NewNotFound(schema.GroupResource{Group: entry.Group, Resource: "configmaps"}, entry.Name)
	endpointGone := &meta.NoKindMatchError{GroupKind: gvk.GroupKind(), SearchedVersions: []string{gvk.Version}}
	unavailable := errors.New("temporary discovery outage")
	for _, tc := range []struct {
		name       string
		getErr     error
		refreshed  meta.RESTMapper
		refreshErr error
		wantError  bool
	}{
		{name: "ordinary object 404 never calls discovery", getErr: absent, refreshErr: unavailable},
		{name: "discovery outage is retried", getErr: endpointGone, refreshErr: unavailable, wantError: true},
		{name: "fresh discovery confirms kind removed", getErr: endpointGone, refreshed: kindGone},
		{name: "stale non-refreshable mapper cannot confirm cleanup", getErr: endpointGone, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mapper meta.RESTMapper = stale
			lookups := 0
			if tc.refreshed != nil || tc.refreshErr != nil {
				mapper = testRefreshableMapper{stale, func() (meta.RESTMapper, error) {
					lookups++
					return tc.refreshed, tc.refreshErr
				}}
			}
			base := fake.NewClientBuilder().WithRESTMapper(mapper).Build()
			cli := interceptor.NewClient(base, interceptor.Funcs{Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
				return tc.getErr
			}})
			live, err := getRecordedConfigurationResource(t.Context(), cli, entry, gvk)
			if live != nil || (err != nil) != tc.wantError || (tc.wantError && tc.refreshErr != nil && !errors.Is(err, tc.refreshErr)) {
				t.Fatalf("live=%v error=%v, expected error=%t", live, err, tc.wantError)
			}
			if tc.getErr == absent && lookups != 0 {
				t.Fatalf("discovery called %d times for an ordinary 404", lookups)
			}
		})
	}
}

func TestRetireRecordedConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*unstructured.Unstructured)
		delete bool
	}{
		{"same published UID", nil, true},
		{"same name rebuilt", func(o *unstructured.Unstructured) { o.SetUID("new-resource") }, false},
		{"source transferred", func(o *unstructured.Unstructured) {
			a := o.GetAnnotations()
			a[v1.LabelConfigurations] = "other"
			o.SetAnnotations(a)
		}, false},
		{"owner transferred", func(o *unstructured.Unstructured) {
			a := o.GetAnnotations()
			a[v1.AnnotationConfigurationOwnerUID] = "other"
			o.SetAnnotations(a)
		}, false},
		{"configuration marker changed", func(o *unstructured.Unstructured) {
			a := o.GetAnnotations()
			a[v1.AnnotationConfigurationUID] = "other"
			o.SetAnnotations(a)
		}, false},
		{"controller transferred", func(o *unstructured.Unstructured) {
			refs := o.GetOwnerReferences()
			refs[0].UID = "other"
			o.SetOwnerReferences(refs)
		}, false},
		{"resource overrides delete with orphan", func(o *unstructured.Unstructured) {
			a := o.GetAnnotations()
			a[v1.AnnotationConfigurationDeletePolicy] = v1.ConfigurationDeletePolicyOrphan
			o.SetAnnotations(a)
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			owner := inventoryTestOwner()
			entry := inventoryTestResource(true, v1.ConfigurationDisablePolicyOrphan)
			owner.Status.RenderedConfigurationResources = []v1.RenderedConfigurationResource{entry}
			cfg := &v1.MiddlewareConfiguration{ObjectMeta: metav1.ObjectMeta{Name: entry.ConfigurationName, UID: "rebuilt-configuration", Annotations: map[string]string{v1.AnnotationConfigurationDeletePolicy: v1.ConfigurationDeletePolicyDelete}}}
			live := inventoryLiveObject(owner, entry, entry.ResourceUID, true)
			if tc.change != nil {
				tc.change(live)
			}
			cli := inventoryTestClient(t, live)
			handled, err := retireRecordedConfiguration(t.Context(), cli, owner, cfg)
			if err != nil || !handled {
				t.Fatalf("handled=%v err=%v", handled, err)
			}
			got := &unstructured.Unstructured{}
			got.SetGroupVersionKind(live.GroupVersionKind())
			err = cli.Get(t.Context(), client.ObjectKeyFromObject(live), got)
			if tc.delete {
				if !apierrors.IsNotFound(err) {
					t.Fatalf("expected target deletion, got %v", err)
				}
			} else if err != nil {
				t.Fatalf("expected retained target: %v", err)
			} else if live.GetAnnotations()[v1.AnnotationConfigurationDeletePolicy] == v1.ConfigurationDeletePolicyOrphan && len(got.GetOwnerReferences()) > 0 {
				t.Fatalf("retained object still belongs to the owner: %v", got.GetOwnerReferences())
			}
		})
	}
}

func TestOrphanDetachmentChecksAllRecordedIncarnations(t *testing.T) {
	owner := inventoryTestOwner()
	current := inventoryTestResource(true, v1.ConfigurationDisablePolicyOrphan)
	previous := current
	previous.ResourceUID = "prior-resource"
	live := inventoryLiveObject(owner, current, current.ResourceUID, true)
	for _, tc := range []struct {
		name    string
		entries []v1.RenderedConfigurationResource
		detach  bool
	}{
		{"old record first", []v1.RenderedConfigurationResource{previous, current}, true},
		{"current record first", []v1.RenderedConfigurationResource{current, previous}, true},
		{"no matching resource UID", []v1.RenderedConfigurationResource{previous, previous}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			owner.Status.RenderedConfigurationResources = tc.entries
			if got := canDetachConfigurationOwner(owner, live, current.ConfigurationName); got != tc.detach {
				t.Fatalf("can detach = %v, want %v", got, tc.detach)
			}
		})
	}
}

func TestRetireRecordedConfigurationSelectsPublishedTargets(t *testing.T) {
	owner := inventoryTestOwner()
	mo := &v1.MiddlewareOperator{ObjectMeta: owner.ObjectMeta}
	first := inventoryTestResource(true, v1.ConfigurationDisablePolicyDelete)
	first.Namespace = "data"
	last := inventoryTestResource(false, v1.ConfigurationDisablePolicyDelete)
	last.ResourceUID = "cluster-resource"
	missing := first
	missing.Name = "already-gone"
	missing.ResourceUID = "missing"
	other := first
	other.Name = "other-target"
	other.ConfigurationName = "other-source"
	other.ResourceUID = "other-uid"
	foreign := first
	foreign.Name = "foreign-target"
	foreign.OwnerUID = "other-owner"
	foreign.ResourceUID = "foreign-uid"
	list := []v1.RenderedConfigurationResource{missing, first, first, last, other, foreign}
	mo.Status.RenderedConfigurationResources = list
	objects := []client.Object{}
	for _, entry := range []v1.RenderedConfigurationResource{first, last, other, foreign} {
		objects = append(objects, inventoryLiveObject(owner, entry, entry.ResourceUID, false))
	}
	cli := inventoryTestClient(t, objects...)
	cfg := &v1.MiddlewareConfiguration{ObjectMeta: metav1.ObjectMeta{Name: first.ConfigurationName}}
	handled, err := retireRecordedConfiguration(t.Context(), cli, mo, cfg)
	if err != nil || !handled {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
	for _, entry := range []v1.RenderedConfigurationResource{first, last} {
		if err := getInventoryObject(t.Context(), cli, entry); !apierrors.IsNotFound(err) {
			t.Fatalf("expected %s deleted: %v", entry.Name, err)
		}
	}
	for _, entry := range []v1.RenderedConfigurationResource{other, foreign} {
		if err := getInventoryObject(t.Context(), cli, entry); err != nil {
			t.Fatalf("unrelated %s was lost: %v", entry.Name, err)
		}
	}
	if handled, err := retireRecordedConfiguration(t.Context(), cli, mo, cfg); err != nil || !handled {
		t.Fatalf("retry broadened cleanup: handled=%v err=%v", handled, err)
	}
}

func TestRetireRecordedConfigurationLegacyFallback(t *testing.T) {
	owner := inventoryTestOwner()
	entry := inventoryTestResource(true, v1.ConfigurationDisablePolicyDelete)
	cfg := &v1.MiddlewareConfiguration{ObjectMeta: metav1.ObjectMeta{Name: entry.ConfigurationName}}
	cli := inventoryTestClient(t, inventoryLiveObject(owner, entry, entry.ResourceUID, true))
	for _, tt := range []struct {
		name   string
		change func(*v1.RenderedConfigurationResource)
	}{
		{"old owner", func(e *v1.RenderedConfigurationResource) { e.OwnerUID = "old" }},
		{"other configuration", func(e *v1.RenderedConfigurationResource) { e.ConfigurationName = "other" }},
		{"unknown target UID", func(e *v1.RenderedConfigurationResource) { e.ResourceUID = "" }},
		{"missing GVK", func(e *v1.RenderedConfigurationResource) { e.Version = "" }},
		{"missing namespace", func(e *v1.RenderedConfigurationResource) { e.Namespace = "" }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			bad := entry
			tt.change(&bad)
			owner.Status.RenderedConfigurationResources = []v1.RenderedConfigurationResource{bad}
			if handled, err := retireRecordedConfiguration(t.Context(), cli, owner, cfg); err != nil || handled {
				t.Fatalf("expected legacy path, handled=%v err=%v", handled, err)
			}
			if err := getInventoryObject(t.Context(), cli, entry); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLegacyForceDeleteHonorsRecordedTargets(t *testing.T) {
	for _, tc := range []struct {
		name     string
		fallback bool
		known    bool
		rebuilt  bool
	}{
		{"direct lookup finds another configuration", false, true, false},
		{"direct lookup finds a recreated target", false, true, true},
		{"label lookup finds an object claimed by another configuration", true, true, false},
		{"force delete without history", false, false, false},
		{"label cleanup without history", true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			owner := testMiddleware()
			cfg := orphanTestConfig(map[string]string{v1.AnnotationConfigurationDeletePolicy: v1.ConfigurationDeletePolicyDelete})
			owner.Labels = map[string]string{v1.LabelPackageName: "force-policy-test"}
			cfg.Labels = owner.Labels
			owner.Spec.Configurations = []v1.Configuration{{Name: cfg.Name}}
			name := "shared-sample"
			if tc.fallback {
				name = "previous-name"
			}
			other := v1.RenderedConfigurationResource{ConfigurationName: "another-configuration", ConfigurationUID: "another-source", Group: "", Version: "v1", Kind: "ConfigMap", Namespace: owner.Namespace, Name: name, Namespaced: true, OwnerUID: owner.UID, ResourceUID: "published"}
			if tc.known {
				owner.Status.RenderedConfigurationResources = []v1.RenderedConfigurationResource{other}
			}
			live := inventoryLiveObject(owner, other, other.ResourceUID, false)
			// Stale labels can qualify the object for the fallback list, but cannot override its inventory.
			annotations := live.GetAnnotations()
			annotations[v1.LabelConfigurations] = cfg.Name
			live.SetAnnotations(annotations)
			if tc.rebuilt {
				live.SetUID("replacement")
			}
			cli := orphanTestClient(t, owner, cfg, live)
			if err := DeleteTemplateRenderedResources(t.Context(), cli, owner, owner, nil); err != nil {
				t.Fatal(err)
			}
			err := getInventoryObject(t.Context(), cli, other)
			if tc.known && err != nil {
				t.Fatalf("object from another configuration was lost: %v", err)
			}
			if !tc.known && !apierrors.IsNotFound(err) {
				t.Fatalf("expected legacy force delete, got %v", err)
			}
		})
	}
}

func TestRetireRecordedConfigurationWhenVersionIsUnserved(t *testing.T) {
	for _, tc := range []struct {
		name     string
		replaced bool
	}{
		{"current version serves the same resource", false},
		{"current version serves a new resource with the same name", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			owner := inventoryTestOwner()
			served := inventoryTestResource(true, v1.ConfigurationDisablePolicyDelete)
			record := served
			record.Version = "v1alpha1"
			owner.Status.RenderedConfigurationResources = []v1.RenderedConfigurationResource{record}
			live := inventoryLiveObject(owner, served, served.ResourceUID, true)
			if tc.replaced {
				live.SetUID("replacement")
			}
			cfg := &v1.MiddlewareConfiguration{ObjectMeta: metav1.ObjectMeta{Name: record.ConfigurationName}}
			// A cached mapper can still prefer the removed version.
			gvk := live.GroupVersionKind()
			retired := gvk
			retired.Version = record.Version
			mapper := meta.NewDefaultRESTMapper([]schema.GroupVersion{retired.GroupVersion(), gvk.GroupVersion()})
			mapper.Add(retired, meta.RESTScopeNamespace)
			mapper.Add(gvk, meta.RESTScopeNamespace)
			base := fake.NewClientBuilder().WithScheme(inventoryTestClient(t).Scheme()).WithRESTMapper(mapper).WithObjects(live).Build()
			lookedUp := false
			cli := interceptor.NewClient(base, interceptor.Funcs{
				Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					gvk := obj.GetObjectKind().GroupVersionKind()
					if gvk.Version == record.Version {
						return &meta.NoKindMatchError{GroupKind: gvk.GroupKind(), SearchedVersions: []string{record.Version}}
					}
					if gvk == live.GroupVersionKind() && key == client.ObjectKeyFromObject(live) {
						lookedUp = true
					}
					return c.Get(ctx, key, obj, opts...)
				},
			})
			handled, err := retireRecordedConfiguration(t.Context(), cli, owner, cfg)
			if err != nil || !handled || !lookedUp {
				t.Fatalf("handled=%v read served version=%v err=%v", handled, lookedUp, err)
			}
			err = getInventoryObject(t.Context(), base, served)
			if tc.replaced && err != nil {
				t.Fatalf("replacement was lost: %v", err)
			}
			if !tc.replaced && !apierrors.IsNotFound(err) {
				t.Fatalf("original resource still exists: %v", err)
			}
		})
	}
}

func TestDeleteOwnerAfterConfigurationWasWithdrawn(t *testing.T) {
	for _, test := range []struct {
		name        string
		policy      string
		replacement bool
		deleted     bool
	}{
		{"delete published target", v1.ConfigurationDisablePolicyDelete, false, true},
		{"retain orphan", v1.ConfigurationDisablePolicyOrphan, false, false},
		{"retain replacement", v1.ConfigurationDisablePolicyDelete, true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			owner := inventoryTestOwner()
			entry := inventoryTestResource(false, test.policy)
			live := inventoryLiveObject(owner, entry, entry.ResourceUID, false)
			if test.replacement {
				live.SetUID("replacement")
			}
			owner.Status.RenderedConfigurationResources = []v1.RenderedConfigurationResource{entry}
			cli := inventoryTestClient(t, owner, live)
			if err := DeleteTemplateRenderedResources(t.Context(), cli, owner, owner, nil); err != nil {
				t.Fatal(err)
			}
			err := getInventoryObject(t.Context(), cli, entry)
			if test.deleted && !apierrors.IsNotFound(err) || !test.deleted && err != nil {
				t.Fatalf("after owner deletion got %v, expected deleted=%t", err, test.deleted)
			}
		})
	}
}
