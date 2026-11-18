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
	"strings"
	"testing"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestMigrateLegacyConfigurationReferences(t *testing.T) {
	owner := testMiddleware()
	owner.Labels = map[string]string{v1.LabelPackageName: "legacy-refs-package"}
	cfg := orphanTestConfig(map[string]string{v1.AnnotationConfigurationDeletePolicy: "orphan"})
	cfg.Name = "legacy-refs-test"
	cfg.Labels = owner.Labels
	// The owner never receives a spec edit and the business template cannot be replayed.
	cfg.Spec.Template += `    {{ fail "must not render body" }}`
	foreign := metav1.OwnerReference{APIVersion: "v1", Kind: "ConfigMap", Name: "external", UID: "by-another-owner"}
	build := func(name string) *corev1.ConfigMap {
		return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: owner.Namespace,
			Labels:          map[string]string{v1.LabelApp: owner.Name},
			Annotations:     map[string]string{v1.LabelConfigurations: cfg.Name},
			OwnerReferences: []metav1.OwnerReference{middlewareOwnerRef(), foreign},
		}, Data: map[string]string{"live": "unchanged"}}
	}
	type target struct {
		live   *corev1.ConfigMap
		detach bool
	}
	var tests []target
	retained := build("renamed-old-resource")
	tests = append(tests, target{retained, true})
	explicitDelete := build("delete-overrides-mcf")
	explicitDelete.Annotations[v1.AnnotationConfigurationDeletePolicy] = "delete"
	tests = append(tests, target{explicitDelete, false})
	otherConfig := build("other-config")
	otherConfig.Annotations[v1.LabelConfigurations] = "other"
	tests = append(tests, target{otherConfig, false})
	otherOwner := build("other-owner")
	otherOwner.OwnerReferences[0].UID = "other"
	tests = append(tests, target{otherOwner, false})
	otherNS := build("other-namespace")
	otherNS.Namespace = "unrelated"
	tests = append(tests, target{otherNS, false})
	objects := []client.Object{owner, cfg}
	for _, tt := range tests {
		objects = append(objects, tt.live)
	}
	patches := 0
	cli := interceptor.NewClient(orphanTestClient(t, objects...), interceptor.Funcs{
		Patch: func(ctx context.Context, next client.WithWatch, obj client.Object, p client.Patch, opts ...client.PatchOption) error {
			patches++
			return next.Patch(ctx, obj, p, opts...)
		},
	})
	refs := []v1.Configuration{{Name: cfg.Name}}
	for range 2 {
		if err := migrateLegacyConfigurationReferences(context.Background(), cli, owner, refs); err != nil {
			t.Fatal(err)
		}
	}
	if patches != 1 || len(owner.Status.RenderedConfigurationResources) != 0 {
		t.Fatalf("migration wrote %d patches and produced %d inventory entries", patches, len(owner.Status.RenderedConfigurationResources))
	}
	for _, tt := range tests {
		live := &corev1.ConfigMap{}
		if err := cli.Get(context.Background(), client.ObjectKeyFromObject(tt.live), live); err != nil {
			t.Fatal(err)
		}
		if tt.detach {
			if len(live.OwnerReferences) != 1 || live.OwnerReferences[0].UID != foreign.UID {
				t.Fatalf("%s kept owner: %v", live.Name, live.OwnerReferences)
			}
		} else if len(live.OwnerReferences) != 2 {
			t.Fatalf("%s was modified: %v", live.Name, live.OwnerReferences)
		}
		if live.Data["live"] != "unchanged" {
			t.Fatalf("%s data changed", live.Name)
		}
	}
}

func TestLegacyReferenceLookupRetriesFailedReads(t *testing.T) {
	owner := testMiddleware()
	owner.Labels = map[string]string{v1.LabelPackageName: "legacy-reference-retry"}
	cfg := orphanTestConfig(nil)
	cfg.Labels = owner.Labels
	errExpected := fmt.Errorf("temporary list error")
	base := orphanTestClient(t, owner, cfg)
	cli := interceptor.NewClient(base, interceptor.Funcs{List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error {
		return errExpected
	}})
	err := migrateLegacyConfigurationReferences(context.Background(), cli, owner, []v1.Configuration{{Name: cfg.Name}})
	if err == nil || !strings.Contains(err.Error(), errExpected.Error()) {
		t.Fatalf("lost transient failure: %v", err)
	}
}

func TestLegacyReferenceDiscoveryReadsInlineJSON(t *testing.T) {
	owner := testMiddleware()
	cfg := orphanTestConfig(map[string]string{v1.AnnotationConfigurationDeletePolicy: "orphan"})
	cfg.Spec.Template = `{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"old"}}`
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
		Name: "old", Namespace: owner.Namespace, Labels: map[string]string{v1.LabelApp: owner.Name},
		Annotations:     map[string]string{v1.LabelConfigurations: cfg.Name},
		OwnerReferences: []metav1.OwnerReference{middlewareOwnerRef()},
	}}
	cli := orphanTestClient(t, owner, cfg, cm)
	if err := detachLegacyConfigurationReferences(t.Context(), cli, owner, cfg); err != nil {
		t.Fatal(err)
	}
	if err := cli.Get(t.Context(), client.ObjectKeyFromObject(cm), cm); err != nil {
		t.Fatal(err)
	}
	if len(cm.OwnerReferences) != 0 {
		t.Fatalf("legacy JSON manifest retained instance reference: %v", cm.OwnerReferences)
	}
}
