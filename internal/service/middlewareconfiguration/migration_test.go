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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestMigrateConfigurationOwnerReferences(t *testing.T) {
	for _, tt := range []struct {
		name           string
		resourcePolicy string
		configPolicy   string
		required       bool
		replace        bool
		foreign        bool
	}{
		{name: "inherited orphan", configPolicy: "orphan"},
		{name: "manifest orphan overrides MCF", resourcePolicy: "orphan", configPolicy: "delete"},
		{name: "manifest delete overrides MCF", resourcePolicy: "delete", configPolicy: "orphan", required: true},
		{name: "ordinary managed resource", required: true},
		{name: "replacement with copied markers", configPolicy: "orphan", replace: true},
		{name: "other controller", configPolicy: "orphan", foreign: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			owner := inventoryTestOwner()
			entry := inventoryTestResource(true, v1.ConfigurationDisablePolicyDelete)
			owner.Status.RenderedConfigurationResources = []v1.RenderedConfigurationResource{entry}
			live := inventoryLiveObject(owner, entry, entry.ResourceUID, true)
			if tt.replace {
				live.SetUID("new-resource")
			}
			refs := live.GetOwnerReferences()
			if tt.foreign {
				refs[0].UID = "other-controller"
			}
			refs = append(refs, metav1.OwnerReference{APIVersion: "v1", Kind: "Secret", Name: "shared", UID: "other-ref"})
			live.SetOwnerReferences(refs)
			a := live.GetAnnotations()
			if tt.resourcePolicy != "" {
				a[v1.AnnotationConfigurationDeletePolicy] = tt.resourcePolicy
			}
			live.SetAnnotations(a)
			live.Object["data"] = map[string]interface{}{"kept": "live"}
			mcf := &v1.MiddlewareConfiguration{ObjectMeta: metav1.ObjectMeta{
				Name: entry.ConfigurationName, UID: "recreated-source", Annotations: map[string]string{v1.AnnotationConfigurationDeletePolicy: tt.configPolicy},
			}}
			builder := fake.NewClientBuilder().WithScheme(inventoryTestClient(t).Scheme()).WithStatusSubresource(owner)
			cli := builder.WithObjects(owner, mcf, live).Build()
			for range 2 {
				if err := cli.Get(ctx, client.ObjectKeyFromObject(owner), owner); err != nil {
					t.Fatal(err)
				}
				if err := MigrateConfigurationOwnerReferences(ctx, cli, owner); err != nil {
					t.Fatal(err)
				}
			}
			got := &corev1.ConfigMap{}
			if err := cli.Get(ctx, client.ObjectKeyFromObject(live), got); err != nil {
				t.Fatal(err)
			}
			if got.UID != live.GetUID() || got.Data["kept"] != "live" || got.OwnerReferences[len(got.OwnerReferences)-1].UID != "other-ref" {
				t.Fatalf("migration changed unrelated metadata or data: %#v", got)
			}
			if err := cli.Get(ctx, client.ObjectKeyFromObject(owner), owner); err != nil {
				t.Fatal(err)
			}
			flag := owner.Status.RenderedConfigurationResources[0].ControllerReferenceRequired
			if tt.replace || tt.foreign {
				if len(got.OwnerReferences) != 2 || flag != nil {
					t.Fatalf("granted authority for a different resource or controller: refs=%v, required=%v", got.OwnerReferences, flag)
				}
				return
			}
			if flag == nil || *flag != tt.required || (metav1.GetControllerOf(got) != nil) != tt.required {
				t.Fatalf("got owner refs %v and persisted flag %v, want required=%t", got.OwnerReferences, flag, tt.required)
			}
		})
	}
}

func TestReferenceMigrationRetriesAfterStatusWriteFailure(t *testing.T) {
	ctx := context.Background()
	owner := inventoryTestOwner()
	entry := inventoryTestResource(true, v1.ConfigurationDisablePolicyDelete)
	owner.Status.RenderedConfigurationResources = []v1.RenderedConfigurationResource{entry}
	live := inventoryLiveObject(owner, entry, entry.ResourceUID, true)
	annotations := live.GetAnnotations()
	annotations[v1.AnnotationConfigurationDeletePolicy] = "orphan"
	live.SetAnnotations(annotations)
	builder := fake.NewClientBuilder().WithScheme(inventoryTestClient(t).Scheme()).WithStatusSubresource(owner)
	base := builder.WithObjects(owner, live).Build()
	injected := errors.New("status write interrupted")
	cli := interceptor.NewClient(base, interceptor.Funcs{SubResourcePatch: func(context.Context, client.Client, string, client.Object, client.Patch, ...client.SubResourcePatchOption) error {
		return injected
	}})
	if err := base.Get(ctx, client.ObjectKeyFromObject(owner), owner); err != nil {
		t.Fatal(err)
	}
	if err := MigrateConfigurationOwnerReferences(ctx, cli, owner); !errors.Is(err, injected) {
		t.Fatalf("wanted interrupted status write, got %v", err)
	}
	if err := base.Get(ctx, client.ObjectKeyFromObject(owner), owner); err != nil {
		t.Fatal(err)
	}
	if owner.Status.RenderedConfigurationResources[0].ControllerReferenceRequired != nil {
		t.Fatal("migration recorded before successful status write")
	}
	actual := &unstructured.Unstructured{}
	actual.SetGroupVersionKind(live.GroupVersionKind())
	if err := base.Get(ctx, client.ObjectKeyFromObject(live), actual); err != nil || metav1.GetControllerOf(actual) != nil {
		t.Fatalf("owner reference removal failed: %v, %v", actual.GetOwnerReferences(), err)
	}
	if err := MigrateConfigurationOwnerReferences(ctx, base, owner); err != nil {
		t.Fatal(err)
	}
	if err := base.Get(ctx, client.ObjectKeyFromObject(owner), owner); err != nil {
		t.Fatal(err)
	}
	flag := owner.Status.RenderedConfigurationResources[0].ControllerReferenceRequired
	if flag == nil || *flag {
		t.Fatal("retry did not record the intentional omission")
	}
}

func TestMigrationLeavesPendingPublicationForRetry(t *testing.T) {
	mid := inventoryTestOwner()
	mid.Generation, mid.Status.ObservedGeneration = 4, 4
	mid.Status.State = v1.StateUnavailable
	mid.Status.PendingConfigurationWrite = &v1.PendingConfigurationWrite{ID: "in-flight"}
	mo := &v1.MiddlewareOperator{ObjectMeta: mid.ObjectMeta, Status: v1.MiddlewareOperatorStatus{PendingConfigurationWrite: mid.Status.PendingConfigurationWrite, ObservedGeneration: 4, State: v1.StateUnavailable}}
	for _, owner := range []client.Object{mid, mo} {
		base := fake.NewClientBuilder().WithScheme(inventoryTestClient(t).Scheme()).WithStatusSubresource(owner).WithObjects(owner).Build()
		if err := MigrateConfigurationOwnerReferences(t.Context(), base, owner); err != nil {
			t.Fatal(err)
		}
		saved, ok := owner.DeepCopyObject().(client.Object)
		if !ok {
			t.Fatalf("unexpected owner type %T", owner)
		}
		if err := base.Get(t.Context(), client.ObjectKeyFromObject(owner), saved); err != nil {
			t.Fatal(err)
		}
		entries, generation := configurationInventory(saved)
		if len(*entries) != 0 || *generation != 0 || (*pendingConfigurationWrite(saved)).ID != "in-flight" {
			t.Fatalf("%T classified interrupted publication as completed migration: %#v", saved, saved)
		}
	}
}
