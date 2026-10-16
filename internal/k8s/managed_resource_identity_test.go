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
	"strings"
	"testing"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestManagedResourceWriterReportsHelmConflict(t *testing.T) {
	t.Parallel()
	owner := &v1.MiddlewareOperator{ObjectMeta: metav1.ObjectMeta{
		Name: "mysql-operator", Namespace: "middleware-operator", UID: "current-owner-uid",
	}}
	live := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Service",
		"metadata": map[string]any{
			"name": owner.Name, "namespace": owner.Namespace, "uid": "service-uid",
			"labels": map[string]any{"app.kubernetes.io/managed-by": "Helm"},
			"annotations": map[string]any{
				"meta.helm.sh/release-name": owner.Name, "meta.helm.sh/release-namespace": owner.Namespace,
			},
		},
	}}
	desired := live.DeepCopy()
	desired.SetAnnotations(map[string]string{
		v1.LabelConfigurations: "mysql-operator-service", v1.AnnotationConfigurationOwnerUID: string(owner.UID),
	})
	desired.SetOwnerReferences([]metav1.OwnerReference{{
		APIVersion: v1.GroupVersion.String(), Kind: "MiddlewareOperator", Name: owner.Name,
		UID: owner.UID, Controller: new(true),
	}})
	cli := intentUnitClient(t, owner, live)
	if err := cli.Get(t.Context(), client.ObjectKeyFromObject(live), live); err != nil {
		t.Fatal(err)
	}
	before := live.DeepCopy()
	writer := &ManagedResourceWriter{Client: cli, Reader: cli, IntentSchemas: intentUnitSchemas{}}
	err := writer.Reconcile(t.Context(), owner, desired, "mysql-operator-service")
	if err == nil {
		t.Fatal("expected Helm resource identity failure")
	}
	for _, want := range []string{
		`resource=Service middleware-operator/mysql-operator (apiVersion=v1)`,
		`annotation "middleware.cn/configurations" is missing or empty (expected "mysql-operator-service")`,
		`helm release="mysql-operator"`,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("writer diagnostic %q does not contain %q", err, want)
		}
	}
	if err := cli.Get(t.Context(), client.ObjectKeyFromObject(live), live); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(live.Object, before.Object) {
		t.Fatal("identity failure changed the existing Helm resource")
	}
	t.Log(err)
}

func TestManagedResourceIdentityDiagnostics(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name          string
		configuration string
		mutate        func(*unstructured.Unstructured)
		want          []string
	}{
		{
			name: "missing configuration on Helm resource", configuration: "mysql-operator-service",
			mutate: func(live *unstructured.Unstructured) {
				live.SetLabels(map[string]string{"app.kubernetes.io/managed-by": "Helm"})
				live.SetAnnotations(map[string]string{
					"meta.helm.sh/release-name":      "mysql-operator",
					"meta.helm.sh/release-namespace": "middleware-operator",
				})
			},
			want: []string{`annotation "middleware.cn/configurations" is missing or empty (expected "mysql-operator-service")`,
				`configuration="mysql-operator-service"`, `actual controller owner=<none>`, `managed-by="Helm"`,
				`helm release="mysql-operator"`, `helm release namespace="middleware-operator"`},
		},
		{
			name: "different configuration", configuration: "mysql-operator-service",
			mutate: func(live *unstructured.Unstructured) {
				live.SetAnnotations(map[string]string{v1.LabelConfigurations: "old-configuration"})
			},
			want: []string{`annotation "middleware.cn/configurations"="old-configuration" (expected "mysql-operator-service")`},
		},
		{
			name: "different controller lifecycle", configuration: "mysql-operator-service",
			mutate: func(live *unstructured.Unstructured) {
				live.SetOwnerReferences([]metav1.OwnerReference{{
					APIVersion: v1.GroupVersion.String(), Kind: "MiddlewareOperator", Name: "mysql-operator",
					UID: "old-owner-uid", Controller: new(true),
				}})
			},
			want: []string{`primary resource owner mismatch`, `configuration="mysql-operator-service"`,
				`actual controller owner=MiddlewareOperator mysql-operator (apiVersion=middleware.cn/v1, uid="old-owner-uid")`},
		},
		{
			name: "stale owner marker", configuration: "mysql-operator-service",
			mutate: func(live *unstructured.Unstructured) {
				live.SetOwnerReferences([]metav1.OwnerReference{{
					APIVersion: v1.GroupVersion.String(), Kind: "MiddlewareOperator", Name: "mysql-operator",
					UID: "current-owner-uid", Controller: new(true),
				}})
				live.SetAnnotations(map[string]string{
					v1.LabelConfigurations:             "mysql-operator-service",
					v1.AnnotationConfigurationOwnerUID: "old-owner-uid",
				})
			},
			want: []string{`annotation "middleware.cn/configurationOwnerUID"="old-owner-uid" (expected "current-owner-uid")`},
		},
		{
			name: "cluster resource without owner markers", configuration: "mysql-operator-service",
			mutate: func(live *unstructured.Unstructured) {
				live.SetAPIVersion("apiextensions.k8s.io/v1")
				live.SetKind("CustomResourceDefinition")
				live.SetNamespace("")
				live.SetName("mysqlclusters.mysql.middleware.harmonycloud.cn")
			},
			want: []string{`resource=CustomResourceDefinition mysqlclusters.mysql.middleware.harmonycloud.cn (cluster-scoped)`,
				`apiVersion=apiextensions.k8s.io/v1`, `label "middleware.cn/app" is missing or empty (expected "mysql-operator")`,
				`annotation "middleware.cn/configurationOwnerUID" is missing or empty (expected "current-owner-uid")`},
		},
		{
			name: "missing resource UID", configuration: "mysql-operator-service",
			mutate: func(live *unstructured.Unstructured) { live.SetUID("") },
			want:   []string{`invalid configuration resource identity`, `resource UID=""`, `resource deleting=false`},
		},
		{
			name: "primary resource without controller",
			want: []string{`primary resource lacks controller owner`, `actual controller owner=<none>`},
		},
		{
			name: "secret diagnostics omit payload and arbitrary metadata", configuration: "mysql-operator-service",
			mutate: func(live *unstructured.Unstructured) {
				live.SetKind("Secret")
				live.Object["data"] = map[string]any{"password": "sensitive-secret-payload"}
				live.Object["stringData"] = map[string]any{"password": "sensitive-secret-plaintext"}
				live.SetLabels(map[string]string{"private-label": "sensitive-label-value"})
				live.SetAnnotations(map[string]string{"private-annotation": "sensitive-annotation-value"})
			},
			want: []string{`resource=Secret middleware-operator/mysql-operator`, `annotation "middleware.cn/configurations" is missing or empty`},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			owner := &v1.MiddlewareOperator{ObjectMeta: metav1.ObjectMeta{
				Name: "mysql-operator", Namespace: "middleware-operator", UID: "current-owner-uid",
			}}
			live := intentUnitResource()
			delete(live.Object, "data")
			live.SetKind("Service")
			live.SetName(owner.Name)
			live.SetNamespace(owner.Namespace)
			live.SetAnnotations(map[string]string{v1.LabelConfigurations: tc.configuration})
			if tc.mutate != nil {
				tc.mutate(live)
			}
			before := live.DeepCopy()
			err := verifyIntentIdentity(owner, live, live.DeepCopy(), tc.configuration)
			if err == nil {
				t.Fatal("expected identity failure")
			}
			message := err.Error()
			for _, want := range append(tc.want,
				"managed resource write blocked:",
				`expected owner=MiddlewareOperator middleware-operator/mysql-operator (apiVersion=middleware.cn/v1, uid="current-owner-uid")`,
			) {
				if !strings.Contains(message, want) {
					t.Errorf("diagnostic %q does not contain %q", message, want)
				}
			}
			if strings.Contains(message, "sensitive-") || strings.Contains(message, "private-") {
				t.Fatalf("diagnostic exposed unrelated metadata or secret data: %s", message)
			}
			if !reflect.DeepEqual(live.Object, before.Object) {
				t.Fatal("identity diagnostics mutated the live resource")
			}
		})
	}
}
