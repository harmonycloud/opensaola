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
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/managedfields"
	"k8s.io/kube-openapi/pkg/validation/spec"
	"sigs.k8s.io/structured-merge-diff/v6/fieldpath"
	"sigs.k8s.io/structured-merge-diff/v6/value"
)

func TestDeclaredNativeOmitEmptyProjection(t *testing.T) {
	converter := declaredNativePodConverter(t)
	for _, test := range []struct {
		name, desired, actual string
		valid                 bool
	}{
		{"readOnly false omitted", `{"containers":[{"name":"broker","volumeMounts":[{"name":"log","mountPath":"/logs","readOnly":false}]}]}`, `{"containers":[{"name":"broker","volumeMounts":[{"name":"log","mountPath":"/logs"}]}]}`, true},
		{"readOnly true is not false", `{"containers":[{"name":"broker","volumeMounts":[{"name":"log","mountPath":"/logs","readOnly":false}]}]}`, `{"containers":[{"name":"broker","volumeMounts":[{"name":"log","mountPath":"/logs","readOnly":true}]}]}`, false},
		{"empty env value omitted", `{"containers":[{"name":"broker","env":[{"name":"EMPTY","value":""}]}]}`, `{"containers":[{"name":"broker","env":[{"name":"EMPTY"}]}]}`, true},
		{"nonempty env does not satisfy empty", `{"containers":[{"name":"broker","env":[{"name":"EMPTY","value":""}]}]}`, `{"containers":[{"name":"broker","env":[{"name":"EMPTY","value":"nonempty"}]}]}`, false},
		{"pointer false remains distinct from missing", `{"containers":[{"name":"broker","env":[{"name":"A","valueFrom":{"secretKeyRef":{"name":"s","key":"k","optional":false}}}]}]}`, `{"containers":[{"name":"broker","env":[{"name":"A","valueFrom":{"secretKeyRef":{"name":"s","key":"k"}}}]}]}`, false},
		{"empty native collection may be omitted", `{"containers":[{"name":"broker","volumeMounts":[]}]}`, `{"containers":[{"name":"broker"}]}`, true},
		{"empty associative list accepts present null", `{"containers":[]}`, `{"containers":null}`, true},
		{"present null cannot supply a declared list member", `{"containers":[{"name":"broker"}]}`, `{"containers":null}`, false},
		{"required list's null receipt must be present", `{"containers":[]}`, `{}`, false},
		{"unknown native list field keeps null distinct", `{"futureField":[]}`, `{"futureField":null}`, false},
		{"null native collection may be omitted", `{"containers":[{"name":"broker","volumeMounts":null}]}`, `{"containers":[{"name":"broker"}]}`, true},
		{"future spec field not ignored", `{"containers":[{"name":"broker"}],"futureField":true}`, `{"containers":[{"name":"broker"}]}`, false},
		{"future container field not ignored", `{"containers":[{"name":"broker","futureField":true}]}`, `{"containers":[{"name":"broker"}]}`, false},
		{"future nested member field not ignored", `{"containers":[{"name":"broker","env":[{"name":"A","futureField":"wanted"}]}]}`, `{"containers":[{"name":"broker","env":[{"name":"A","futureField":"drift"}]}]}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			desired, actual := declaredPodObject(t, test.desired), declaredPodObject(t, test.actual)
			dCopy, aCopy := desired.DeepCopy(), actual.DeepCopy()
			err := ValidateDeclaredResourceIntent(converter, desired, actual)
			if (err == nil) != test.valid {
				t.Fatalf("valid=%v error=%v", test.valid, err)
			}
			if !reflect.DeepEqual(desired, dCopy) || !reflect.DeepEqual(actual, aCopy) {
				t.Fatal("native normalization changed input")
			}
		})
	}
}

func TestDeclaredNullListReceiptPreservedOnRetry(t *testing.T) {
	converter := declaredNativePodConverter(t)
	desired := declaredPodObject(t, `{"containers":[]}`)
	actual := declaredPodObject(t, `{"containers":null}`)
	plan, err := PlanDeclaredResourceIntent(converter, desired, actual, desired, desired, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(plan.Object, actual) {
		t.Fatal("reconciling an empty list rewrote a null receipt")
	}
	if err := restoreVerificationPlan(t, converter, desired, plan).Validate(converter, actual); err != nil {
		t.Fatal(err)
	}
	withdrawn := declaredPodObject(t, `{}`)
	if _, _, err := MergeDeclaredResourceIntent(converter, desired, actual, withdrawn, withdrawn, nil); err == nil {
		t.Fatal("null receipt granted authority to delete a historically different value")
	}
}

func TestDeclaredNativeZeroStillEnforced(t *testing.T) {
	converter := declaredNativePodConverter(t)
	before := declaredPodObject(t, `{"containers":[{"name":"broker","volumeMounts":[{"name":"log","mountPath":"/logs","readOnly":false}]}]}`)
	live := declaredPodObject(t, `{"containers":[{"name":"broker","volumeMounts":[{"name":"log","mountPath":"/logs","readOnly":true}]}]}`)
	result, _, err := MergeDeclaredResourceIntent(converter, before, live, before, before, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateDeclaredResourceIntent(converter, before, result); err != nil {
		t.Fatal(err)
	}
	if reflect.DeepEqual(result.Object, live.Object) {
		t.Fatal("raw false declaration was lost before enforcement")
	}
	accepted := declaredPodObject(t, `{"containers":[{"name":"broker","volumeMounts":[{"name":"log","mountPath":"/logs"}]}]}`)
	repeated, _, err := MergeDeclaredResourceIntent(converter, before, accepted, before, before, nil)
	if err != nil || !reflect.DeepEqual(repeated.Object, accepted.Object) {
		t.Fatalf("already canonical false causes another write: %v %s", err, intentTestJSON(repeated))
	}
}

func TestDeclaredNativeHistoricalMemberDeletionNormalizesZero(t *testing.T) {
	converter := declaredNativePodConverter(t)
	before := declaredPodObject(t, `{"containers":[{"name":"broker","volumeMounts":[{"name":"log","mountPath":"/logs","readOnly":false}]}]}`)
	live := declaredPodObject(t, `{"containers":[{"name":"broker","volumeMounts":[{"name":"log","mountPath":"/logs"}]}]}`)
	desired := declaredPodObject(t, `{"containers":[{"name":"broker","volumeMounts":[]}]}`)
	result, retained, err := MergeDeclaredResourceIntent(converter, before, live, desired, desired, nil)
	if err != nil || len(retained) != 0 {
		t.Fatalf("native omitempty is not external modification: %v %v", err, retained)
	}
	if !reflect.DeepEqual(result.Object, desired.Object) {
		t.Fatalf("mount not removed: %s", intentTestJSON(result.Object))
	}
}

func TestDeclaredProjectionNumericRepresentation(t *testing.T) {
	converter := declaredTestConverter(t)
	desired := intentTestObject(t, `{"replicas":2}`)
	live := desired.DeepCopy()
	if err := unstructured.SetNestedField(desired.Object, float64(2), "spec", "replicas"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateDeclaredResourceIntent(converter, desired, live); err != nil {
		t.Fatalf("JSON merge-patch float64 and API int64 must agree: %v", err)
	}
	merged, _, err := MergeDeclaredResourceIntent(converter, nil, live, desired, desired, nil)
	if err != nil || !reflect.DeepEqual(merged.Object, live.Object) {
		t.Fatalf("numerically identical declarations must not write: %v", err)
	}
}

func TestDeclaredCRDNativeOmitEmpty(t *testing.T) {
	var definition spec.Schema
	if err := json.Unmarshal([]byte(`{"type":"object","x-kubernetes-group-version-kind":[{"group":"apiextensions.k8s.io","version":"v1","kind":"CustomResourceDefinition"}],"properties":{"apiVersion":{"type":"string"},"kind":{"type":"string"},"metadata":{"type":"object","properties":{"name":{"type":"string"},"namespace":{"type":"string"}}},"spec":{"type":"object","x-kubernetes-preserve-unknown-fields":true,"properties":{"preserveUnknownFields":{"type":"boolean"}}}}}`), &definition); err != nil {
		t.Fatal(err)
	}
	converter, err := managedfields.NewTypeConverter(map[string]*spec.Schema{"crd.CRD": &definition}, false)
	if err != nil {
		t.Fatal(err)
	}
	object := func(body string) *unstructured.Unstructured {
		obj := intentTestObject(t, body)
		obj.SetAPIVersion("apiextensions.k8s.io/v1")
		obj.SetKind("CustomResourceDefinition")
		obj.SetNamespace("")
		return obj
	}
	for _, test := range []struct {
		name, desired string
		valid         bool
	}{
		{"false may be omitted", `{"preserveUnknownFields":false}`, true},
		{"true cannot be omitted", `{"preserveUnknownFields":true}`, false},
		{"future fields cannot be ignored", `{"preserveUnknownFields":false,"futureField":true}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			desired, actual := object(test.desired), object(`{}`)
			err := ValidateDeclaredResourceIntent(converter, desired, actual)
			if (err == nil) != test.valid {
				t.Fatalf("valid=%v error=%v", test.valid, err)
			}
			merged, _, err := MergeDeclaredResourceIntent(converter, desired, actual, desired, desired, nil)
			if err != nil {
				t.Fatal(err)
			}
			if reflect.DeepEqual(merged.Object, actual.Object) != test.valid {
				t.Fatal("CRD normalization incorrectly changed the no-op decision")
			}
		})
	}
}

func declaredPodObject(t *testing.T, body string) *unstructured.Unstructured {
	object := intentTestObject(t, body)
	object.SetAPIVersion("v1")
	object.SetKind("Pod")
	return object
}

func declaredNativePodConverter(t *testing.T) managedfields.TypeConverter {
	t.Helper()
	var definition spec.Schema
	if err := json.Unmarshal([]byte(`{
  "type":"object","x-kubernetes-group-version-kind":[{"group":"","version":"v1","kind":"Pod"}],
  "properties":{
    "apiVersion":{"type":"string"},"kind":{"type":"string"},
    "metadata":{"type":"object","properties":{"name":{"type":"string"},"namespace":{"type":"string"}}},
    "spec":{"type":"object","x-kubernetes-preserve-unknown-fields":true,"properties":{
      "containers":{"type":"array","x-kubernetes-list-type":"map","x-kubernetes-list-map-keys":["name"],"items":{"type":"object","properties":{
        "name":{"type":"string"},"image":{"type":"string"},"imagePullPolicy":{"type":"string"},"terminationMessagePolicy":{"type":"string"},
        "volumeMounts":{"type":"array","x-kubernetes-list-type":"map","x-kubernetes-list-map-keys":["mountPath"],"items":{"type":"object","properties":{"name":{"type":"string"},"mountPath":{"type":"string"},"readOnly":{"type":"boolean"}}}},
        "env":{"type":"array","x-kubernetes-list-type":"map","x-kubernetes-list-map-keys":["name"],"items":{"type":"object","properties":{"name":{"type":"string"},"value":{"type":"string"},"valueFrom":{"type":"object","properties":{"secretKeyRef":{"type":"object","properties":{"name":{"type":"string"},"key":{"type":"string"},"optional":{"type":"boolean"}}}}}}}}
      }}}
    }}
  }
}`), &definition); err != nil {
		t.Fatal(err)
	}
	converter, err := managedfields.NewTypeConverter(map[string]*spec.Schema{"io.k8s.api.core.v1.Pod": &definition}, false)
	if err != nil {
		t.Fatal(err)
	}
	return converter
}

func TestInitContainerOrderAppliesOnlyAtNativePodSpec(t *testing.T) {
	for _, tc := range []struct {
		gvk  schema.GroupVersionKind
		path []string
		want bool
	}{
		{schema.GroupVersionKind{Version: "v1", Kind: "Pod"}, []string{"spec", "initContainers"}, true},
		{schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "StatefulSet"}, []string{"spec", "template", "spec", "initContainers"}, true},
		{schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "CronJob"}, []string{"spec", "jobTemplate", "spec", "template", "spec", "initContainers"}, true},
		{schema.GroupVersionKind{Group: "other.example", Version: "v1", Kind: "Deployment"}, []string{"spec", "template", "spec", "initContainers"}, false},
		{schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}, []string{"spec", "initContainers"}, false},
		{schema.GroupVersionKind{Version: "v1", Kind: "Pod"}, []string{"spec", "containers"}, false},
	} {
		var path fieldpath.Path
		for i := range tc.path {
			path = append(path, fieldpath.PathElement{FieldName: &tc.path[i]})
		}
		m := intentDeclaredMerger{nativeType: declaredNativeType(tc.gvk)}
		if got := m.ordersNamedMembers(path); got != tc.want {
			t.Errorf("native init order for %v.%v = %t, want %t", tc.gvk, tc.path, got, tc.want)
		}
	}
}

func TestEnvOrderRequiresNativeContainerType(t *testing.T) {
	container := &value.FieldList{{Name: "name", Value: value.NewValueInterface("app")}}
	pod := schema.GroupVersionKind{Version: "v1", Kind: "Pod"}
	deployment := schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}
	for _, tc := range []struct {
		name    string
		gvk     schema.GroupVersionKind
		path    fieldpath.Path
		ordered bool
	}{
		{"regular", pod, fieldpath.MakePathOrDie("spec", "containers", container, "env"), true},
		{"init", deployment, fieldpath.MakePathOrDie("spec", "template", "spec", "initContainers", container, "env"), true},
		{"ephemeral", pod, fieldpath.MakePathOrDie("spec", "ephemeralContainers", container, "env"), true},
		{"cronjob", schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "CronJob"}, fieldpath.MakePathOrDie("spec", "jobTemplate", "spec", "template", "spec", "containers", container, "env"), true},
		{"other list", pod, fieldpath.MakePathOrDie("spec", "containers", container, "volumeMounts"), false},
		{"unrelated env field", deployment, fieldpath.MakePathOrDie("spec", "env"), false},
		{"similar custom GVK", schema.GroupVersionKind{Group: "example.org", Version: "v1", Kind: "Deployment"}, fieldpath.MakePathOrDie("spec", "template", "spec", "containers", container, "env"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := intentDeclaredMerger{nativeType: declaredNativeType(tc.gvk)}
			if m.ordersNamedMembers(tc.path) != tc.ordered {
				t.Fatalf("unexpected order policy for %s %s", tc.gvk, tc.path)
			}
		})
	}
}

func TestDeclaredEnvOrderAcrossRecovery(t *testing.T) {
	c := declaredNativePodConverter(t)
	pod := func(env string) *unstructured.Unstructured {
		return declaredPodObject(t, `{"containers":[{"name":"app","env":[`+env+`]}]}`)
	}
	const a = `{"name":"A","value":"one"}`
	const b = `{"name":"B","value":"two"}`
	const extra = `{"name":"INJECTED","value":"sensitive"}`
	before := pod(b + "," + a)
	live := pod(b + "," + extra + "," + a)
	wanted := pod(a + "," + b)
	submitted := pod(a + "," + extra + "," + b)
	plan, err := PlanDeclaredResourceIntent(c, before, live, wanted, wanted, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(plan.Object.Object, submitted.Object) {
		t.Fatalf("named fields or injected slot changed: %s", intentTestJSON(plan.Object))
	}
	recovered, err := NewDeclaredValidationPlan(c, wanted, plan.DeletionChecks)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []*DeclaredIntentMergeResult{plan, recovered} {
		if err := p.Validate(c, submitted); err != nil {
			t.Fatal(err)
		}
		err := p.Validate(c, live)
		if err == nil || !strings.Contains(err.Error(), `containers[name="app"].env: expected order ["A" "B"], observed ["B" "A"]`) || strings.Contains(err.Error(), "INJECTED") || strings.Contains(err.Error(), "sensitive") {
			t.Fatalf("unexpected order diagnostic: %v", err)
		}
	}
	overlay, err := OverlayActionFields(c, live, wanted)
	if err != nil || !reflect.DeepEqual(overlay.Object, submitted.Object) {
		t.Fatalf("one-off overlay did not preserve slots: %v (%v)", overlay, err)
	}
}

func TestCustomContainerEnvPreservesLiveOrder(t *testing.T) {
	c := declaredTestConverter(t)
	live := intentTestObject(t, `{"template":{"spec":{"containers":[{"name":"app","env":[{"name":"B"},{"name":"A"}]}]}}}`)
	reversed := intentTestObject(t, `{"template":{"spec":{"containers":[{"name":"app","env":[{"name":"A"},{"name":"B"}]}]}}}`)
	result, _, err := MergeDeclaredResourceIntent(c, live, live, reversed, reversed, nil)
	if err != nil || !reflect.DeepEqual(result.Object, live.Object) {
		t.Fatalf("custom object changed on reorder: %v (%v)", result, err)
	}
	if err := ValidateDeclaredResourceIntent(c, reversed, live); err != nil {
		t.Fatalf("custom field acquired Pod env semantics: %v", err)
	}
}
