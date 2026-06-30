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
	"strings"
	"testing"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
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
