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

package synchronizer

import (
	"context"
	"testing"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestApplyDeclaredStatusRules_OverridesGenericProjection(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := v1.AddToScheme(scheme); err != nil {
		t.Fatalf("add OpenSaola API to scheme: %v", err)
	}
	baseline := &v1.MiddlewareOperatorBaseline{
		ObjectMeta: metav1.ObjectMeta{Name: "demo-operator"},
		Spec: v1.MiddlewareOperatorBaselineSpec{GVKs: []v1.GVK{{
			Name:    "v1",
			Group:   "example.io",
			Version: "v1",
			Kind:    "DemoCluster",
			StatusRules: []string{`
				object.status.ready
				  ? dyn({
				      "phase": "Running",
				      "reason": object.status.message,
				      "replicas": int(object.status.members)
				    })
				  : null
			`},
		}}},
	}
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(baseline).Build()
	mid := &v1.Middleware{Spec: v1.MiddlewareSpec{OperatorBaseline: v1.OperatorBaseline{
		Name:    baseline.Name,
		GvkName: "v1",
	}}}
	cr := &unstructured.Unstructured{Object: map[string]interface{}{
		"status": map[string]interface{}{
			"ready":   true,
			"message": "all members ready",
			"members": int64(3),
		},
	}}
	cr.SetGroupVersionKind(schema.GroupVersionKind{Group: "example.io", Version: "v1", Kind: "DemoCluster"})
	target := v1.CustomResources{
		Phase:    v1.PhaseCreating,
		Reason:   "generic reason",
		Replicas: 1,
	}

	declared, err := applyDeclaredStatusRules(context.Background(), cli, mid, cr, v1.CustomResources{Phase: v1.PhaseCreating}, &target)
	if err != nil {
		t.Fatalf("applyDeclaredStatusRules() error = %v", err)
	}
	if !declared {
		t.Fatal("expected declared status rule to take precedence")
	}
	if target.Phase != v1.PhaseRunning || target.Reason != "all members ready" || target.Replicas != 3 {
		t.Fatalf("target = %#v, want CEL projection", target)
	}
}

func TestApplyDeclaredStatusRules_NullKeepsGenericProjection(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := v1.AddToScheme(scheme); err != nil {
		t.Fatalf("add OpenSaola API to scheme: %v", err)
	}
	baseline := &v1.MiddlewareOperatorBaseline{
		ObjectMeta: metav1.ObjectMeta{Name: "demo-operator"},
		Spec: v1.MiddlewareOperatorBaselineSpec{GVKs: []v1.GVK{{
			Name:        "v1",
			Group:       "example.io",
			Version:     "v1",
			Kind:        "DemoCluster",
			StatusRules: []string{"null"},
		}}},
	}
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(baseline).Build()
	mid := &v1.Middleware{Spec: v1.MiddlewareSpec{OperatorBaseline: v1.OperatorBaseline{
		Name:    baseline.Name,
		GvkName: "v1",
	}}}
	cr := &unstructured.Unstructured{}
	cr.SetGroupVersionKind(schema.GroupVersionKind{Group: "example.io", Version: "v1", Kind: "DemoCluster"})
	target := v1.CustomResources{Phase: v1.PhaseCreating, Reason: "generic reason", Replicas: 1}

	declared, err := applyDeclaredStatusRules(context.Background(), cli, mid, cr, v1.CustomResources{}, &target)
	if err != nil {
		t.Fatalf("applyDeclaredStatusRules() error = %v", err)
	}
	if !declared {
		t.Fatal("expected null rule declaration to suppress legacy projectors")
	}
	if target.Phase != v1.PhaseCreating || target.Reason != "generic reason" || target.Replicas != 1 {
		t.Fatalf("target = %#v, want generic projection to remain unchanged", target)
	}
}

func TestStatusRulesForCustomResource_AbsentRulesFallsBack(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := v1.AddToScheme(scheme); err != nil {
		t.Fatalf("add OpenSaola API to scheme: %v", err)
	}
	baseline := &v1.MiddlewareOperatorBaseline{
		ObjectMeta: metav1.ObjectMeta{Name: "demo-operator"},
		Spec: v1.MiddlewareOperatorBaselineSpec{GVKs: []v1.GVK{{
			Name: "v1", Group: "example.io", Version: "v1", Kind: "DemoCluster",
		}}},
	}
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(baseline).Build()
	mid := &v1.Middleware{Spec: v1.MiddlewareSpec{OperatorBaseline: v1.OperatorBaseline{
		Name: baseline.Name, GvkName: "v1",
	}}}
	cr := &unstructured.Unstructured{}
	cr.SetGroupVersionKind(schema.GroupVersionKind{Group: "example.io", Version: "v1", Kind: "DemoCluster"})

	rules, declared, err := statusRulesForCustomResource(context.Background(), cli, mid, cr)
	if err != nil {
		t.Fatalf("statusRulesForCustomResource() error = %v", err)
	}
	if declared || len(rules) != 0 {
		t.Fatalf("rules = %#v, declared = %v, want legacy fallback", rules, declared)
	}
}
