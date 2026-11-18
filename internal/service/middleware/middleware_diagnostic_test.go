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

	"encoding/json"
	v1 "github.com/harmonycloud/opensaola/api/v1"
	"github.com/harmonycloud/opensaola/internal/service/consts"
	"github.com/harmonycloud/opensaola/internal/service/customresource"
	"github.com/harmonycloud/opensaola/internal/service/middlewareconfiguration"
	"github.com/harmonycloud/opensaola/pkg/tools/ctxkeys"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
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
	configurePauseTestSchemas(t)

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
	if got := mid.Status.CustomResources.Phase; got != v1.PhaseUnknown {
		t.Fatalf("customResources.phase = %q after failed apply, want %q", got, v1.PhaseUnknown)
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

func TestRenderedMiddlewarePreservesInt64InResourceAndConfiguration(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := v1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	labels := map[string]string{v1.LabelPackageName: "integers"}
	baseline := &v1.MiddlewareBaseline{
		ObjectMeta: metav1.ObjectMeta{Name: "numbers", Labels: labels},
		Spec: v1.MiddlewareBaselineSpec{
			GVK:        v1.GVK{Group: "example.test", Version: "v1", Kind: "Numbers"},
			Parameters: runtime.RawExtension{Raw: []byte(`{"inherited":9007199254740993,"direct":1,"templated":"{{ .Necessary.exact }}"}`)},
			Configurations: []v1.Configuration{
				{Name: "copy", Values: runtime.RawExtension{Raw: []byte(`{"copy":"{{ .Parameters.direct }}"}`)}},
				{Name: "independent"},
			},
		},
	}
	mid := &v1.Middleware{ObjectMeta: metav1.ObjectMeta{Name: "exact", Namespace: "default", Labels: labels}, Spec: v1.MiddlewareSpec{
		Baseline:   "numbers",
		Necessary:  runtime.RawExtension{Raw: []byte(`{"exact":9223372036854775807}`)},
		Parameters: runtime.RawExtension{Raw: []byte(`{"direct":9007199254740995}`)},
	}}
	copyConfig := &v1.MiddlewareConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: "copy", Labels: labels},
		Spec:       v1.MiddlewareConfigurationSpec{Template: `{"spec":{"copy":{{ .Values.copy }}}}`},
	}
	independent := &v1.MiddlewareConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: "independent", Labels: labels},
		Spec:       v1.MiddlewareConfigurationSpec{Template: `{{ if .Values.copy }}unexpected value from previous configuration{{ else }}empty{{ end }}`},
	}
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(baseline, copyConfig, independent).Build()
	ctx := context.Background()
	if err := renderMiddlewareWithBaseline(ctx, cli, mid); err != nil {
		t.Fatal(err)
	}
	target, err := customresource.GetNeedPublishCustomResource(ctx, cli, mid)
	if err != nil {
		t.Fatal(err)
	}
	for field, want := range map[string]int64{"inherited": 9007199254740993, "direct": 9007199254740995, "templated": 9223372036854775807} {
		if got, found, err := unstructured.NestedInt64(target.Object, "spec", field); err != nil || !found || got != want {
			t.Fatalf("target spec.%s = %v, found %t, err %v; want %d", field, got, found, err, want)
		}
	}
	configs, err := middlewareconfiguration.GetTemplateParsedMiddlewareConfigurations(ctx, cli, consts.HandleActionPublish, mid)
	if err != nil || len(configs) != 2 {
		t.Fatalf("rendered %d configurations, err %v", len(configs), err)
	}
	var config struct{ Spec struct{ Copy int64 } }
	if err := json.Unmarshal([]byte(configs[0].Spec.Template), &config); err != nil || config.Spec.Copy != 9007199254740995 {
		t.Fatalf("rendered configuration = %s, err %v", configs[0].Spec.Template, err)
	}
	if got := configs[1].Spec.Template; got != "empty" {
		t.Fatalf("second configuration: %s", got)
	}
}
