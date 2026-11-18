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
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/managedfields"
	"k8s.io/client-go/applyconfigurations"
	kubescheme "k8s.io/client-go/kubernetes/scheme"
)

func TestStatefulSetClaimTemplateDeclarations(t *testing.T) {
	converter := applyconfigurations.NewTypeConverter(kubescheme.Scheme)
	claim := map[string]any{"metadata": map[string]any{"name": "logs"}, "spec": map[string]any{
		"accessModes": []any{"ReadWriteOnce"}, "storageClassName": "standard",
		"resources": map[string]any{"requests": map[string]any{"storage": "5Gi"}},
	}}
	// The response to the first POST includes these four server additions.
	defaults := []struct {
		path  []string
		value string
	}{
		{[]string{"apiVersion"}, "v1"}, {[]string{"kind"}, "PersistentVolumeClaim"},
		{[]string{"spec", "volumeMode"}, "Filesystem"}, {[]string{"status", "phase"}, "Pending"},
	}
	makeObject := func(claims ...map[string]any) *unstructured.Unstructured {
		items := make([]any, len(claims))
		for i := range claims {
			items[i] = runtime.DeepCopyJSON(claims[i])
		}
		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "apps/v1", "kind": "StatefulSet", "metadata": map[string]any{"name": "demo", "namespace": "default"},
			"spec": map[string]any{"volumeClaimTemplates": items},
		}}
	}
	set := func(m map[string]any, v any, path ...string) {
		t.Helper()
		if err := unstructured.SetNestedField(m, v, path...); err != nil {
			t.Fatal(err)
		}
	}
	check := func(t *testing.T, desired, actual *unstructured.Unstructured, valid bool) {
		t.Helper()
		d, a := desired.DeepCopy(), actual.DeepCopy()
		if err := ValidateDeclaredResourceIntent(converter, desired, actual); (err == nil) != valid {
			t.Fatalf("valid=%v, error=%v", valid, err)
		}
		// The same declarations drive an ordinary update and receipt validation.
		planned, _, err := MergeDeclaredResourceIntent(converter, desired, actual, desired, desired, nil)
		if err != nil || reflect.DeepEqual(planned.Object, actual.Object) != valid {
			t.Fatalf("write plan disagrees with receipt validation: %v", err)
		}
		if !reflect.DeepEqual(desired, d) || !reflect.DeepEqual(actual, a) {
			t.Fatal("comparison changed its inputs")
		}
	}
	for _, def := range defaults {
		t.Run("API adds "+def.path[len(def.path)-1], func(t *testing.T) {
			got := runtime.DeepCopyJSON(claim)
			set(got, def.value, def.path...)
			check(t, makeObject(claim), makeObject(got), true)
			check(t, makeObject(got), makeObject(claim), false)
		})
	}
	full := runtime.DeepCopyJSON(claim)
	for _, def := range defaults {
		set(full, def.value, def.path...)
	}
	t.Run("all defaults", func(t *testing.T) { check(t, makeObject(claim), makeObject(full), true) })
	for _, tc := range []struct {
		name  string
		path  []string
		value any
		valid bool
	}{
		{"capacity changed", []string{"spec", "resources", "requests", "storage"}, "1Gi", false},
		{"equivalent capacity", []string{"spec", "resources", "requests", "storage"}, "5120Mi", true},
		{"class changed", []string{"spec", "storageClassName"}, "other", false},
		{"access mode changed", []string{"spec", "accessModes"}, []any{"ReadWriteMany"}, false},
		{"foreign annotation", []string{"metadata", "annotations"}, map[string]any{"external": "true"}, false},
		{"unexpected volume mode", []string{"spec", "volumeMode"}, "Block", false},
		{"unexpected phase", []string{"status", "phase"}, "Bound", false},
		{"unexpected status field", []string{"status", "accessModes"}, []any{"ReadWriteOnce"}, false},
		{"unexpected kind", []string{"kind"}, "Pod", false},
		{"unexpected version", []string{"apiVersion"}, "v2", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := runtime.DeepCopyJSON(full)
			set(got, tc.value, tc.path...)
			check(t, makeObject(claim), makeObject(got), tc.valid)
		})
	}
	t.Run("missing declared storage", func(t *testing.T) {
		observed := runtime.DeepCopyJSON(full)
		unstructured.RemoveNestedField(observed, "spec", "resources", "requests", "storage")
		check(t, makeObject(claim), makeObject(observed), false)
	})
	t.Run("custom StatefulSet retains its own atomic semantics", func(t *testing.T) {
		desired, observed := makeObject(claim), makeObject(full)
		desired.SetAPIVersion("example.test/v1")
		observed.SetAPIVersion("example.test/v1")
		if err := ValidateDeclaredResourceIntent(managedfields.NewDeducedTypeConverter(), desired, observed); err == nil {
			t.Fatal("PVC defaults applied to an unrelated custom resource")
		}
	})
	t.Run("explicit block cannot become filesystem", func(t *testing.T) {
		want := runtime.DeepCopyJSON(claim)
		set(want, "Block", "spec", "volumeMode")
		check(t, makeObject(want), makeObject(full), false)
	})
	t.Run("atomic membership and order", func(t *testing.T) {
		second := runtime.DeepCopyJSON(claim)
		set(second, "data", "metadata", "name")
		check(t, makeObject(claim), makeObject(full, second), false)
		check(t, makeObject(claim, second), makeObject(full), false)
		check(t, makeObject(claim, second), makeObject(second, full), false)
	})
}
