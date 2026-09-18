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
	"testing"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func inventoryTestOwner() *v1.Middleware {
	return &v1.Middleware{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "demo",
			Namespace: "middleware",
			UID:       types.UID("owner-uid"),
		},
	}
}

func inventoryTestResource(namespaced bool, policy string) v1.RenderedConfigurationResource {
	resource := v1.RenderedConfigurationResource{
		ConfigurationName: "optional-config",
		ConfigurationUID:  types.UID("configuration-uid"),
		OwnerUID:          types.UID("owner-uid"),
		ResourceUID:       types.UID("resource-uid"),
		Namespaced:        namespaced,
		DisablePolicy:     policy,
		Name:              "optional-resource",
	}
	if namespaced {
		resource.Version = "v1"
		resource.Kind = "ConfigMap"
		resource.Namespace = "middleware"
	} else {
		resource.Group = "rbac.authorization.k8s.io"
		resource.Version = "v1"
		resource.Kind = "ClusterRole"
	}
	return resource
}

func inventoryLiveObject(owner *v1.Middleware, resource v1.RenderedConfigurationResource, uid types.UID, controllerOwned bool) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(schema.GroupVersionKind{Group: resource.Group, Version: resource.Version, Kind: resource.Kind})
	obj.SetNamespace(resource.Namespace)
	obj.SetName(resource.Name)
	obj.SetUID(uid)
	obj.SetLabels(map[string]string{v1.LabelApp: owner.Name})
	obj.SetAnnotations(map[string]string{
		v1.LabelConfigurations:             resource.ConfigurationName,
		v1.AnnotationConfigurationOwnerUID: string(resource.OwnerUID),
		v1.AnnotationConfigurationUID:      string(resource.ConfigurationUID),
	})
	if controllerOwned {
		controller := true
		obj.SetOwnerReferences([]metav1.OwnerReference{{
			APIVersion: v1.GroupVersion.String(),
			Kind:       "Middleware",
			Name:       owner.Name,
			UID:        owner.UID,
			Controller: &controller,
		}})
	}
	return obj
}

func inventoryTestClient(t *testing.T, objects ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := v1.AddToScheme(scheme); err != nil {
		t.Fatalf("add OpenSaola scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}
	if err := rbacv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add rbac scheme: %v", err)
	}
	if err := apiextensionsv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add apiextensions scheme: %v", err)
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
}

func getInventoryObject(ctx context.Context, cli client.Client, resource v1.RenderedConfigurationResource) error {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(schema.GroupVersionKind{Group: resource.Group, Version: resource.Version, Kind: resource.Kind})
	return cli.Get(ctx, client.ObjectKey{Namespace: resource.Namespace, Name: resource.Name}, obj)
}

func TestDeleteDisabledRenderedConfigurationResources(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		resource        v1.RenderedConfigurationResource
		liveUID         types.UID
		controllerOwned bool
		externalOwner   bool
		current         []v1.RenderedConfigurationResource
		wantDeleted     bool
	}{
		{
			name:            "deletes opt-in namespaced resource",
			resource:        inventoryTestResource(true, v1.ConfigurationDisablePolicyDelete),
			liveUID:         types.UID("resource-uid"),
			controllerOwned: true,
			wantDeleted:     true,
		},
		{
			name:        "orphan policy keeps disabled resource",
			resource:    inventoryTestResource(true, v1.ConfigurationDisablePolicyOrphan),
			liveUID:     types.UID("resource-uid"),
			wantDeleted: false,
		},
		{
			name:          "external controller is never deleted on disable",
			resource:      inventoryTestResource(true, v1.ConfigurationDisablePolicyDelete),
			liveUID:       types.UID("resource-uid"),
			externalOwner: true,
			wantDeleted:   false,
		},
		{
			name:        "resource uid mismatch protects replacement",
			resource:    inventoryTestResource(true, v1.ConfigurationDisablePolicyDelete),
			liveUID:     types.UID("replacement-uid"),
			wantDeleted: false,
		},
		{
			name:        "deletes opt-in cluster scoped resource using markers",
			resource:    inventoryTestResource(false, v1.ConfigurationDisablePolicyDelete),
			liveUID:     types.UID("resource-uid"),
			wantDeleted: true,
		},
		{
			name: "never deletes CustomResourceDefinition on disable",
			resource: func() v1.RenderedConfigurationResource {
				resource := inventoryTestResource(false, v1.ConfigurationDisablePolicyDelete)
				resource.Group = "apiextensions.k8s.io"
				resource.Version = "v1"
				resource.Kind = "CustomResourceDefinition"
				resource.Name = "widgets.example.io"
				return resource
			}(),
			liveUID:     types.UID("resource-uid"),
			wantDeleted: false,
		},
		{
			name:            "still rendered unmanaged identity is retained",
			resource:        inventoryTestResource(true, v1.ConfigurationDisablePolicyDelete),
			liveUID:         types.UID("resource-uid"),
			controllerOwned: true,
			current: func() []v1.RenderedConfigurationResource {
				identity := inventoryTestResource(true, v1.ConfigurationDisablePolicyOrphan)
				identity.OwnerUID = ""
				identity.ResourceUID = ""
				return []v1.RenderedConfigurationResource{identity}
			}(),
			wantDeleted: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			owner := inventoryTestOwner()
			live := inventoryLiveObject(owner, tt.resource, tt.liveUID, tt.controllerOwned)
			if tt.externalOwner {
				controller := true
				live.SetOwnerReferences([]metav1.OwnerReference{{
					APIVersion: "example.io/v1",
					Kind:       "ExternalController",
					Name:       "external",
					UID:        types.UID("external-owner-uid"),
					Controller: &controller,
				}})
			}
			cli := inventoryTestClient(t, live)

			if err := DeleteDisabledRenderedConfigurationResources(ctx, cli, owner, []v1.RenderedConfigurationResource{tt.resource}, tt.current); err != nil {
				t.Fatalf("DeleteDisabledRenderedConfigurationResources() error = %v", err)
			}
			err := getInventoryObject(ctx, cli, tt.resource)
			if tt.wantDeleted {
				if !apierrors.IsNotFound(err) {
					t.Fatalf("expected resource to be deleted, get error = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("expected resource to remain, get error = %v", err)
			}
		})
	}
}

func TestNormalizeRenderedConfigurationResourcesRejectsSharedIdentity(t *testing.T) {
	t.Parallel()
	first := inventoryTestResource(true, v1.ConfigurationDisablePolicyDelete)
	second := first
	second.ConfigurationName = "other-config"
	if _, err := normalizeRenderedConfigurationResources([]v1.RenderedConfigurationResource{first, second}); err == nil {
		t.Fatal("expected duplicate rendered resource identity to be rejected")
	}
}

func TestDeleteDisabledCrossNamespaceResources(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		mutate        func(*unstructured.Unstructured)
		sameNamespace bool
		deleted       bool
	}{
		{name: "cross namespace valid identity", deleted: true},
		{name: "same namespace missing controller", sameNamespace: true},
		{name: "replacement UID", mutate: func(o *unstructured.Unstructured) { o.SetUID("replacement") }},
		{name: "wrong owner UID", mutate: func(o *unstructured.Unstructured) {
			a := o.GetAnnotations()
			a[v1.AnnotationConfigurationOwnerUID] = "other"
			o.SetAnnotations(a)
		}},
		{name: "wrong configuration UID", mutate: func(o *unstructured.Unstructured) {
			a := o.GetAnnotations()
			a[v1.AnnotationConfigurationUID] = "other"
			o.SetAnnotations(a)
		}},
		{name: "external controller", mutate: func(o *unstructured.Unstructured) {
			yes := true
			o.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "example.io/v1", Kind: "External", Name: "other", UID: "other", Controller: &yes}})
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			owner := inventoryTestOwner()
			resource := inventoryTestResource(true, v1.ConfigurationDisablePolicyDelete)
			if !tt.sameNamespace {
				resource.Namespace = "another-namespace"
			}
			live := inventoryLiveObject(owner, resource, resource.ResourceUID, false)
			if tt.mutate != nil {
				tt.mutate(live)
			}
			cli := inventoryTestClient(t, live)
			if err := DeleteDisabledRenderedConfigurationResources(context.Background(), cli, owner, []v1.RenderedConfigurationResource{resource}, nil); err != nil {
				t.Fatal(err)
			}
			err := getInventoryObject(context.Background(), cli, resource)
			if tt.deleted {
				if !apierrors.IsNotFound(err) {
					t.Fatalf("expected deletion, got %v", err)
				}
			} else if err != nil {
				t.Fatalf("expected protected resource, got %v", err)
			}
		})
	}
}
