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
	"strings"
	"testing"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestConfigurationAdoptionAuthority(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		mutate  func(*unstructured.Unstructured) []client.Object
		adopt   bool
		wantErr string
	}{
		{name: "legacy source without UID", adopt: true},
		{name: "deleted owner UID", adopt: true, mutate: func(live *unstructured.Unstructured) []client.Object {
			a := live.GetAnnotations()
			a[v1.AnnotationConfigurationOwnerUID] = "old-owner-uid"
			live.SetAnnotations(a)
			return nil
		}},
		{name: "same logical configuration after recreation", mutate: func(live *unstructured.Unstructured) []client.Object {
			a := live.GetAnnotations()
			a[v1.AnnotationConfigurationOwnerUID] = "owner-uid"
			a[v1.AnnotationConfigurationUID] = "previous-configuration-uid"
			live.SetAnnotations(a)
			return nil
		}},
		{name: "external source", wantErr: "middleware.cn/configurations", mutate: func(live *unstructured.Unstructured) []client.Object {
			live.SetAnnotations(nil)
			live.SetLabels(map[string]string{"app.kubernetes.io/managed-by": "Helm"})
			return nil
		}},
		{name: "foreign controller", wantErr: "primary resource owner mismatch", mutate: func(live *unstructured.Unstructured) []client.Object {
			live.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "example.io/v1", Kind: "External", Name: "external", UID: "external-uid", Controller: new(true)}})
			return nil
		}},
		{name: "stale controller of deleted same-name owner", adopt: true, mutate: func(live *unstructured.Unstructured) []client.Object {
			live.SetNamespace("owners")
			live.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: v1.GroupVersion.String(), Kind: "Middleware", Name: "owner", UID: "previous-uid", Controller: new(true)}})
			return nil
		}},
		{name: "previous owner still exists in another namespace", wantErr: "existing owner", mutate: func(live *unstructured.Unstructured) []client.Object {
			a := live.GetAnnotations()
			a[v1.AnnotationConfigurationOwnerUID] = "previous-uid"
			live.SetAnnotations(a)
			return []client.Object{&v1.MiddlewareOperator{ObjectMeta: metav1.ObjectMeta{Name: "owner", Namespace: "another", UID: "previous-uid"}}}
		}},
		{name: "paused owner still claims source", wantErr: "existing owner", mutate: func(_ *unstructured.Unstructured) []client.Object {
			return []client.Object{&v1.MiddlewareOperator{
				ObjectMeta: metav1.ObjectMeta{Name: "owner", Namespace: "another", UID: "other-uid", Annotations: map[string]string{v1.AnnotationSuspendReconcile: "true"}},
				Spec:       v1.MiddlewareOperatorSpec{Configurations: []v1.Configuration{{Name: "config"}}},
			}}
		}},
		{name: "baseline claims source before inventory exists", wantErr: "existing owner", mutate: func(_ *unstructured.Unstructured) []client.Object {
			return []client.Object{
				&v1.MiddlewareOperator{ObjectMeta: metav1.ObjectMeta{Name: "owner", Namespace: "another", UID: "other-uid"}, Spec: v1.MiddlewareOperatorSpec{Baseline: "other-baseline"}},
				&v1.MiddlewareOperatorBaseline{ObjectMeta: metav1.ObjectMeta{Name: "other-baseline"}, Spec: v1.MiddlewareOperatorBaselineSpec{Configurations: []v1.Configuration{{Name: "config"}}}},
			}
		}},
		{name: "another instance can reuse the source template", adopt: true, mutate: func(_ *unstructured.Unstructured) []client.Object {
			return []client.Object{&v1.MiddlewareOperator{
				ObjectMeta: metav1.ObjectMeta{Name: "another-instance", Namespace: "another", UID: "other-uid"},
				Spec:       v1.MiddlewareOperatorSpec{Configurations: []v1.Configuration{{Name: "config"}}, Baseline: "unrelated-baseline"},
			}}
		}},
		{name: "inventory claims physical target under another source", wantErr: "existing owner", mutate: func(live *unstructured.Unstructured) []client.Object {
			return []client.Object{&v1.MiddlewareOperator{
				ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: "another", UID: "other-uid"},
				Status: v1.MiddlewareOperatorStatus{RenderedConfigurationResources: []v1.RenderedConfigurationResource{{
					Group: live.GroupVersionKind().Group, Kind: live.GetKind(), Name: live.GetName(), Namespace: live.GetNamespace(), ConfigurationName: "other-config",
				}}},
			}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			owner := intentUnitOwner()
			live := intentUnitResource()
			live.SetNamespace("shared")
			live.SetLabels(map[string]string{v1.LabelApp: owner.Name})
			live.SetAnnotations(map[string]string{v1.LabelConfigurations: "config"})
			objects := []client.Object{owner}
			if tc.mutate != nil {
				objects = append(objects, tc.mutate(live)...)
			}
			desired := live.DeepCopy()
			desired.SetAnnotations(map[string]string{v1.LabelConfigurations: "config", v1.AnnotationConfigurationOwnerUID: string(owner.UID), v1.AnnotationConfigurationUID: "current-config-uid"})
			cli := intentUnitClient(t, objects...)
			writer := &ManagedResourceWriter{Client: cli, Reader: cli}
			adopt, err := writer.configurationNeedsAdoption(t.Context(), owner, live, desired, "config")
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) || adopt {
					t.Fatalf("adopt=%v, err=%v; expected rejection containing %q", adopt, err, tc.wantErr)
				}
				return
			}
			if err != nil || adopt != tc.adopt {
				t.Fatalf("adopt=%v, err=%v; expected adopt=%v", adopt, err, tc.adopt)
			}
		})
	}
}
