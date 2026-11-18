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
	"testing"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestOwnerDeleteDetachesOnlyKnownOrphan(t *testing.T) {
	for _, tt := range []struct {
		name                                                 string
		fallback, inventory, replaced, foreignMarker, detach bool
	}{
		{name: "legacy target without inventory", detach: true},
		{name: "legacy fallback after target renamed", fallback: true, detach: true},
		{name: "recorded target with same-name rebuilt MCF", inventory: true, detach: true},
		{name: "same-name rebuilt target", inventory: true, replaced: true},
		{name: "legacy target with conflicting owner identity", foreignMarker: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			owner := testMiddleware()
			owner.Labels = map[string]string{v1.LabelPackageName: "test-package"}
			cfg := orphanTestConfig(map[string]string{v1.AnnotationConfigurationDeletePolicy: v1.ConfigurationDeletePolicyOrphan})
			cfg.UID = "reinstalled-config"
			cfg.Labels = owner.Labels
			owner.Spec.Configurations = []v1.Configuration{{Name: cfg.Name}}
			name := "shared-sample"
			if tt.fallback {
				name = "previous-name"
			}
			foreign := metav1.OwnerReference{APIVersion: "v1", Kind: "ConfigMap", Name: "foreign", UID: "foreign"}
			live := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
				Name: name, Namespace: owner.Namespace, UID: "original",
				Labels:          map[string]string{v1.LabelApp: owner.Name},
				Annotations:     map[string]string{v1.LabelConfigurations: cfg.Name},
				OwnerReferences: []metav1.OwnerReference{middlewareOwnerRef(), foreign},
			}}
			if tt.inventory {
				owner.Status.RenderedConfigurationResources = []v1.RenderedConfigurationResource{{
					Version: "v1", Kind: "ConfigMap", Namespace: owner.Namespace, Name: name, Namespaced: true,
					OwnerUID: owner.UID, ResourceUID: live.UID, ConfigurationName: cfg.Name, ConfigurationUID: "original-config",
				}}
				live.Annotations[v1.AnnotationConfigurationOwnerUID] = string(owner.UID)
				live.Annotations[v1.AnnotationConfigurationUID] = "original-config"
			}
			if tt.foreignMarker {
				live.Annotations[v1.AnnotationConfigurationOwnerUID] = "other"
			}
			if tt.replaced {
				live.UID = "replacement"
			}
			cli := orphanTestClient(t, owner, cfg, live)
			if err := DeleteTemplateRenderedResources(context.Background(), cli, owner, owner); err != nil {
				t.Fatal(err)
			}
			after := &corev1.ConfigMap{}
			if err := cli.Get(context.Background(), client.ObjectKeyFromObject(live), after); err != nil {
				t.Fatal(err)
			}
			if tt.detach {
				if len(after.OwnerReferences) != 1 || after.OwnerReferences[0].UID != foreign.UID {
					t.Fatalf("expected only foreign reference after cleanup, got %v", after.OwnerReferences)
				}
			} else if len(after.OwnerReferences) != 2 || after.OwnerReferences[0].UID != owner.UID || after.OwnerReferences[1].UID != foreign.UID {
				t.Fatalf("foreign or replaced target was changed: %v", after.OwnerReferences)
			}
		})
	}
}

func TestFallbackDeletesOnlyObjectsRenderedByItsConfiguration(t *testing.T) {
	owner := testMiddleware()
	owner.Labels = map[string]string{v1.LabelPackageName: "fixture-package"}
	cfg := orphanTestConfig(map[string]string{v1.AnnotationConfigurationDeletePolicy: v1.ConfigurationDeletePolicyDelete})
	cfg.Labels = owner.Labels
	owner.Spec.Configurations = []v1.Configuration{{Name: cfg.Name}}
	var objects = []client.Object{owner, cfg}
	for _, name := range []string{cfg.Name, "separate-orphan-config", "unmarked"} {
		item := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: owner.Namespace,
			Labels: map[string]string{v1.LabelApp: owner.Name}, Annotations: map[string]string{}}}
		if name != "unmarked" {
			item.Annotations[v1.LabelConfigurations] = name
		}
		objects = append(objects, item)
	}
	cli := orphanTestClient(t, objects...)
	if err := DeleteTemplateRenderedResources(context.Background(), cli, owner, owner); err != nil {
		t.Fatal(err)
	}
	for _, obj := range objects[2:] {
		err := cli.Get(context.Background(), client.ObjectKeyFromObject(obj), &corev1.ConfigMap{})
		if obj.GetName() == cfg.Name {
			if !apierrors.IsNotFound(err) {
				t.Fatalf("matching previous name should be removed: %v", err)
			}
		} else if err != nil {
			t.Fatalf("fallback removed %s: %v", obj.GetName(), err)
		}
	}
}

func TestOwnerDeleteDoesNotListAfterMatchingTarget(t *testing.T) {
	for _, tt := range []struct {
		name       string
		policy     string
		failDelete bool
	}{
		{name: "retained target", policy: "orphan"},
		{name: "delete rejected after resource changed", policy: "delete", failDelete: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			owner := testMiddleware()
			cfg := orphanTestConfig(map[string]string{v1.AnnotationConfigurationDeletePolicy: tt.policy})
			owner.Labels = map[string]string{v1.LabelPackageName: "test-package"}
			cfg.Labels = owner.Labels
			owner.Spec.Configurations = []v1.Configuration{{Name: cfg.Name}}
			target := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "shared-sample", Namespace: owner.Namespace, UID: "observed"}}
			var deletes int
			cli := interceptor.NewClient(orphanTestClient(t, owner, cfg, target), interceptor.Funcs{
				List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error {
					t.Fatal("unexpected fallback after the named target was found")
					return nil
				},
				Delete: func(_ context.Context, _ client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
					deletes++
					pre := (&client.DeleteOptions{}).ApplyOptions(opts).Preconditions
					if pre == nil || pre.UID == nil || *pre.UID != target.UID || pre.ResourceVersion == nil || *pre.ResourceVersion == "" || *pre.ResourceVersion != obj.GetResourceVersion() {
						t.Fatalf("expected delete with observed UID and resourceVersion, got %#v", pre)
					}
					return fmt.Errorf("conflict while deleting")
				},
			})
			err := DeleteTemplateRenderedResources(context.Background(), cli, owner, owner)
			if tt.failDelete {
				if err == nil || deletes != 1 {
					t.Fatalf("expected one retriable deletion failure, got calls=%d err=%v", deletes, err)
				}
			} else if err != nil || deletes != 0 {
				t.Fatalf("expected retain, got calls=%d err=%v", deletes, err)
			}
		})
	}
}
