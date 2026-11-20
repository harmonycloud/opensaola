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

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/applyconfigurations"
	kubescheme "k8s.io/client-go/kubernetes/scheme"
)

func TestUndeclaredAtomicFieldsDoNotGrantDeletionRights(t *testing.T) {
	converter := applyconfigurations.NewTypeConverter(kubescheme.Scheme)
	policy := func(t *testing.T, spec string) *unstructured.Unstructured {
		obj := intentTestObject(t, spec)
		obj.SetAPIVersion("networking.k8s.io/v1")
		obj.SetKind("NetworkPolicy")
		return obj
	}
	before := policy(t, `{"ingress":[{"ports":[{"port":5432}]}]}`)
	withdrawn := policy(t, `{}`)
	for _, tc := range []struct{ name, observed string }{
		{"built-in protocol default", `{"ingress":[{"ports":[{"port":5432,"protocol":"TCP"}]}]}`},
		{"external range", `{"ingress":[{"ports":[{"port":5432,"endPort":5500}]}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			live := policy(t, tc.observed)
			if err := ValidateDeclaredResourceIntent(converter, before, live); err != nil {
				t.Fatalf("external addition rejected: %v", err)
			}
			history, err := CaptureResourceIntentDefaults(converter, before, before, before, live)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before.Object, history.Object) {
				t.Fatalf("captured external content: %v", history.Object["spec"])
			}
			plan, err := PlanDeclaredResourceIntent(converter, history, live, withdrawn, withdrawn, nil)
			if err != nil {
				t.Fatal(err)
			}
			if plan.DeletionConflictError() == nil || !reflect.DeepEqual(plan.Object.Object, live.Object) {
				t.Fatal("external fields authorized deletion of the whole atomic list")
			}
		})
	}
}
