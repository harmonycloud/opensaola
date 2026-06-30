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

package middleware

import (
	"context"
	"errors"
	"strings"
	"testing"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	"github.com/harmonycloud/opensaola/internal/service/consts"
	"github.com/harmonycloud/opensaola/pkg/tools/ctxkeys"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestTemplateParseWithBaseline_MissingNecessaryIncludesFieldPath(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := v1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}

	baseline := &v1.MiddlewareBaseline{
		ObjectMeta: metav1.ObjectMeta{
			Name: "milvus-cluster",
			Labels: map[string]string{
				v1.LabelPackageName: "milvus",
			},
		},
		Spec: v1.MiddlewareBaselineSpec{
			Necessary: runtime.RawExtension{Raw: []byte(`{"resource":{"etcd":{"volume":"10Gi"}}}`)},
		},
	}
	mid := &v1.Middleware{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "demo-milvus",
			Namespace:  "middleware",
			Generation: 7,
			Labels: map[string]string{
				v1.LabelPackageName: "milvus",
			},
		},
		Spec: v1.MiddlewareSpec{
			Baseline:  "milvus-cluster",
			Necessary: runtime.RawExtension{Raw: []byte(`{"resource":{"etcd":{}}}`)},
		},
		Status: v1.MiddlewareStatus{
			ObservedGeneration: 6,
		},
	}

	cli := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&v1.Middleware{}).
		WithObjects(baseline, mid).
		Build()

	err := TemplateParseWithBaseline(ctx, cli, mid)
	if err == nil {
		t.Fatal("expected missing required field error")
	}

	for _, want := range []string{
		"phase=config-validation",
		"resource=middleware.cn/v1/Middleware middleware/demo-milvus",
		"fieldPath=spec.necessary.resource.etcd.volume",
		"expected=present",
		"actual=missing",
		"generation=7",
		"observedGeneration=6",
		"staleStatus=true",
		"MiddlewareBaseline milvus/milvus-cluster spec.necessary",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("expected error to contain %q, got %q", want, err.Error())
		}
	}

	if len(mid.Status.Conditions) != 1 {
		t.Fatalf("expected one condition, got %d", len(mid.Status.Conditions))
	}
	condition := mid.Status.Conditions[0]
	if condition.Type != v1.CondTypeTemplateParseWithBaseline {
		t.Fatalf("expected TemplateParseWithBaseline condition, got %q", condition.Type)
	}
	if condition.Status != metav1.ConditionFalse {
		t.Fatalf("expected failed condition, got %s", condition.Status)
	}
	if !strings.Contains(condition.Message, "fieldPath=spec.necessary.resource.etcd.volume") {
		t.Fatalf("expected condition message to include field path, got %q", condition.Message)
	}
}

func TestBuildCustomResource_CreateFailureWritesApplyClusterDiagnostic(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := v1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}

	baseline := &v1.MiddlewareBaseline{
		ObjectMeta: metav1.ObjectMeta{
			Name: "redis",
			Labels: map[string]string{
				v1.LabelPackageName: "redis",
			},
		},
		Spec: v1.MiddlewareBaselineSpec{
			GVK: v1.GVK{
				Group:   "apps",
				Version: "v1",
				Kind:    "Deployment",
			},
		},
	}
	mid := &v1.Middleware{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "demo-redis",
			Namespace:  "middleware",
			Generation: 9,
			Labels: map[string]string{
				v1.LabelPackageName: "redis",
			},
		},
		Spec: v1.MiddlewareSpec{
			Baseline:   "redis",
			Parameters: runtime.RawExtension{Raw: []byte(`{"replicas":1}`)},
		},
		Status: v1.MiddlewareStatus{ObservedGeneration: 8},
	}

	cli := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&v1.Middleware{}).
		WithObjects(baseline, mid).
		WithInterceptorFuncs(interceptor.Funcs{
			Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
				if obj.GetObjectKind().GroupVersionKind().Kind == "Deployment" {
					return errors.New("admission denied: replicas exceeds namespace quota")
				}
				return c.Create(ctx, obj, opts...)
			},
		}).
		Build()

	err := buildCustomResource(ctxkeys.WithScheme(ctx, scheme), cli, consts.HandleActionPublish, mid)
	if err == nil {
		t.Fatal("expected custom resource create error")
	}

	for _, want := range []string{
		"phase=runtime-reconcile",
		"resource=middleware.cn/v1/Middleware middleware/demo-redis",
		"failedObject=apps/v1/Deployment middleware/demo-redis",
		"ownerRef=middleware.cn/v1/Middleware middleware/demo-redis",
		"generation=9",
		"observedGeneration=8",
		"admission denied: replicas exceeds namespace quota",
		"kubectl describe deployment demo-redis -n middleware",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("expected error to contain %q, got %q", want, err.Error())
		}
	}

	var applyCluster *metav1.Condition
	for i := range mid.Status.Conditions {
		if mid.Status.Conditions[i].Type == v1.CondTypeApplyCluster {
			applyCluster = &mid.Status.Conditions[i]
			break
		}
	}
	if applyCluster == nil {
		t.Fatalf("expected ApplyCluster condition, got %#v", mid.Status.Conditions)
	}
	if applyCluster.Status != metav1.ConditionFalse {
		t.Fatalf("expected failed ApplyCluster condition, got %s", applyCluster.Status)
	}
	if !strings.Contains(applyCluster.Message, "failedObject=apps/v1/Deployment middleware/demo-redis") {
		t.Fatalf("expected condition message to include failed object, got %q", applyCluster.Message)
	}
}
