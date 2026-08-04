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

package middlewareaction

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func Test_executeCmd(t *testing.T) {
	t.Skip("executeCmd depends on cluster discovery client/template rendering and executes external shell commands, making unit tests unstable; use envtest or add testability via dependency injection for command runner/templating")
}

func TestExecutePreActionCue_ResourceMismatchIncludesExpectedAndActual(t *testing.T) {
	t.Parallel()

	obj := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "v1",
			"kind":       "ConfigMap",
			"metadata": map[string]any{
				"namespace": "default",
				"name":      "actual-config",
			},
		},
	}
	action := &v1.MiddlewareAction{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "patch-config",
			Namespace:  "default",
			Generation: 5,
		},
	}
	cueString := `
parameters: resource: {
	apiversion: "v1"
	kind:       "ConfigMap"
	namespace:  "default"
	name:       "expected-config"
}
output: {}
`

	err := executePreActionCue(context.Background(), cueString, obj, action)
	if err == nil {
		t.Fatal("expected resource mismatch error")
	}
	if strings.Contains(err.Error(), "%!w(<nil>)") {
		t.Fatalf("mismatch error must not wrap nil, got %q", err.Error())
	}
	for _, want := range []string{
		"phase=config-validation",
		"failedObject=v1/ConfigMap default/actual-config",
		"fieldPath=parameters.resource",
		"expected=apiVersion=v1 kind=ConfigMap namespace=default name=expected-config",
		"actual=apiVersion=v1 kind=ConfigMap namespace=default name=actual-config",
		"generation=5",
		"next=check the pre-action CUE parameters.resource block",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("expected error to contain %q, got %q", want, err.Error())
		}
	}
	if len(action.Status.Conditions) != 1 {
		t.Fatalf("expected one condition, got %d", len(action.Status.Conditions))
	}
	if !strings.Contains(action.Status.Conditions[0].Message, "fieldPath=parameters.resource") {
		t.Fatalf("expected condition message to include field path, got %q", action.Status.Conditions[0].Message)
	}
}

func TestRenderPreActionsMatchesNormalCUEExecution(t *testing.T) {
	ctx := context.Background()
	const (
		packageName = "render-preaction-package"
		actionName  = "render-preaction-cue"
	)
	scheme := runtime.NewScheme()
	if err := v1.AddToScheme(scheme); err != nil {
		t.Fatalf("add OpenSaola scheme: %v", err)
	}
	action := &v1.MiddlewareActionBaseline{
		ObjectMeta: metav1.ObjectMeta{
			Name:   actionName,
			Labels: map[string]string{v1.LabelPackageName: packageName},
		},
		Spec: v1.MiddlewareActionBaselineSpec{
			BaselineType: v1.WorkflowPreAction,
			Steps: []v1.Step{{
				Name: "patch-parameters",
				CUE: `
output: { spec: parameters.spec }
parameters: {
	spec: {
		parameters: {
			replicas: 3
			fromPreAction: true
		}
	}
	resource: {
		apiversion: "middleware.cn/v1"
		kind: "Middleware"
		name: "demo"
		namespace: "middleware"
	}
}`,
			}},
		},
	}
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(action).Build()
	mid := &v1.Middleware{
		TypeMeta: metav1.TypeMeta{APIVersion: "middleware.cn/v1", Kind: "Middleware"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "demo",
			Namespace: "middleware",
			Labels:    map[string]string{v1.LabelPackageName: packageName},
		},
		Spec: v1.MiddlewareSpec{
			Parameters: runtime.RawExtension{Raw: []byte(`{"replicas":1,"unchanged":true}`)},
			PreActions: []v1.PreAction{{Name: actionName}},
		},
	}

	normal := mid.DeepCopy()
	if err := HandlePreActions(ctx, cli, normal); err != nil {
		t.Fatalf("HandlePreActions() error = %v", err)
	}
	rendered := mid.DeepCopy()
	if err := RenderPreActions(ctx, cli, rendered); err != nil {
		t.Fatalf("RenderPreActions() error = %v", err)
	}

	if got, want := parametersMap(t, rendered), parametersMap(t, normal); !reflect.DeepEqual(got, want) {
		t.Fatalf("render-only parameters = %#v, want normal execution %#v", got, want)
	}
	if got, want := parametersMap(t, mid), map[string]any{"replicas": float64(1), "unchanged": true}; !reflect.DeepEqual(got, want) {
		t.Fatalf("RenderPreActions() mutated input parameters = %#v, want %#v", got, want)
	}
}

func TestRenderPreActionsRejectsNonCUEPreActionBeforeApplyingAnyPatch(t *testing.T) {
	ctx := context.Background()
	const (
		packageName      = "render-preaction-unsafe-package"
		safeActionName   = "render-preaction-cue"
		unsafeActionName = "render-preaction-unsafe"
	)
	scheme := runtime.NewScheme()
	if err := v1.AddToScheme(scheme); err != nil {
		t.Fatalf("add OpenSaola scheme: %v", err)
	}
	safeAction := &v1.MiddlewareActionBaseline{
		ObjectMeta: metav1.ObjectMeta{
			Name:   safeActionName,
			Labels: map[string]string{v1.LabelPackageName: packageName},
		},
		Spec: v1.MiddlewareActionBaselineSpec{
			BaselineType: v1.WorkflowPreAction,
			Steps: []v1.Step{{
				Name: "would-patch",
				CUE: `
output: { spec: parameters.spec }
parameters: {
	spec: { parameters: { replicas: 3 } }
	resource: {
		apiversion: "middleware.cn/v1"
		kind: "Middleware"
		name: "demo"
		namespace: "middleware"
	}
}`,
			}},
		},
	}
	unsafeAction := &v1.MiddlewareActionBaseline{
		ObjectMeta: metav1.ObjectMeta{
			Name:   unsafeActionName,
			Labels: map[string]string{v1.LabelPackageName: packageName},
		},
		Spec: v1.MiddlewareActionBaselineSpec{
			BaselineType: v1.WorkflowPreAction,
			Steps:        []v1.Step{{Name: "must-not-run", CMD: v1.CMD{Command: []string{"false"}}}},
		},
	}
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(safeAction, unsafeAction).Build()
	mid := &v1.Middleware{
		TypeMeta: metav1.TypeMeta{APIVersion: "middleware.cn/v1", Kind: "Middleware"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "demo",
			Namespace: "middleware",
			Labels:    map[string]string{v1.LabelPackageName: packageName},
		},
		Spec: v1.MiddlewareSpec{
			Parameters: runtime.RawExtension{Raw: []byte(`{"replicas":1}`)},
			PreActions: []v1.PreAction{{Name: safeActionName}, {Name: unsafeActionName}},
		},
	}

	err := RenderPreActions(ctx, cli, mid)
	if !errors.Is(err, ErrPreActionNotRenderSafe) {
		t.Fatalf("RenderPreActions() error = %v, want ErrPreActionNotRenderSafe", err)
	}
	for _, want := range []string{unsafeActionName, "must-not-run", "non-CUE"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("RenderPreActions() error = %q, want %q", err, want)
		}
	}
	if got, want := parametersMap(t, mid), map[string]any{"replicas": float64(1)}; !reflect.DeepEqual(got, want) {
		t.Fatalf("render-only preflight applied a partial patch: got %#v, want %#v", got, want)
	}
}

func parametersMap(t *testing.T, mid *v1.Middleware) map[string]any {
	t.Helper()
	result := make(map[string]any)
	if err := json.Unmarshal(mid.Spec.Parameters.Raw, &result); err != nil {
		t.Fatalf("decode middleware parameters: %v", err)
	}
	return result
}
