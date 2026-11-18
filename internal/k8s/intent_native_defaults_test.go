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

package k8s

import (
	"reflect"
	"testing"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"sigs.k8s.io/structured-merge-diff/v6/fieldpath"
)

func TestNativeAdmissionDefaultProvenance(t *testing.T) {
	claim := reflect.TypeFor[*corev1.PersistentVolumeClaim]()
	pod := reflect.TypeFor[*corev1.Pod]()
	sts := reflect.TypeFor[*appsv1.StatefulSet]()
	rbac := reflect.TypeFor[*rbacv1.RoleBinding]()
	field := fieldpath.MakePathOrDie
	fieldRef := field("spec", "volumes", 0, "downwardAPI", "items", 0, "fieldRef", "apiVersion")
	token := field("spec", "volumes", 0, "projected", "sources", 0, "serviceAccountToken", "expirationSeconds")
	group := field("subjects", 0, "apiGroup")
	for _, tc := range []struct {
		name   string
		root   reflect.Type
		path   fieldpath.Path
		parent map[string]any
		want   any
	}{
		{"nested claim kind", sts, field("spec", "volumeClaimTemplates", 0, "kind"), nil, "PersistentVolumeClaim"},
		{"bare claim kind", claim, field("kind"), nil, nil},
		{"bare claim version", claim, field("apiVersion"), nil, nil},
		{"pending needs containing claim", reflect.TypeFor[*corev1.PersistentVolumeClaimStatus](), field("phase"), nil, nil},
		{"PVC pending phase", claim, field("status", "phase"), nil, string(corev1.ClaimPending)},
		{"explicit null stays declared", sts, field("spec", "volumeClaimTemplates", 0, "spec", "volumeMode"), map[string]any{"volumeMode": nil}, nil},
		{"explicit value stays declared", claim, field("spec", "volumeMode"), map[string]any{"volumeMode": "Block"}, nil},
		{"service account token lifetime", pod, token, nil, int64(3600)},
		{"downward API object version", pod, fieldRef, nil, "v1"},
		{"custom template with same names", nil, fieldRef, nil, nil},
		{"user subject group", rbac, group, map[string]any{"kind": "User"}, rbacv1.GroupName},
		{"group subject group", rbac, group, map[string]any{"kind": "Group"}, rbacv1.GroupName},
		{"service account subject", rbac, group, map[string]any{"kind": "ServiceAccount"}, nil},
		{"undeclared subject kind", rbac, group, nil, nil},
		{"named inline admission rule", reflect.TypeFor[*admissionv1.ValidatingAdmissionPolicy](), field("spec", "matchConstraints", "resourceRules", 0, "scope"), nil, string(admissionv1.AllScopes)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := nativeAdmissionDefault(tc.root, tc.path, tc.parent)
			if ok != (tc.want != nil) || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("default = (%#v, %t), want %#v", got, ok, tc.want)
			}
			// Defaults learned for convergence cannot grant historical cleanup rights.
			if tc.want != nil {
				lookup := declaredDeletionLookup{merger: &intentDeclaredMerger{nativeType: tc.root}}
				if lookup.nativeDefaultAt(nil, intentDeclaredType{}, tc.path, tc.want) {
					t.Fatal("newly recognized default also became a captured deletion default")
				}
			}
		})
	}
}
