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
	"fmt"
	"reflect"
	"strings"
	"sync"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextensionsv1beta1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1beta1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	kubescheme "k8s.io/client-go/kubernetes/scheme"
	kjson "sigs.k8s.io/json"
	"sigs.k8s.io/structured-merge-diff/v6/fieldpath"
)

// Native APIs serialize optional zero values with omitempty even when their
// OpenAPI document has no explicit default. Compare the typed JSON meaning only
// after applying declarations; removing these fields from rawDesired beforehand
// would lose instructions such as readOnly:true -> false.
//
// The client may know fewer fields than the server. Every unrecognized field is
// restored after the round trip, recursively, so a newer API field can never be
// silently dropped from convergence checking. Custom resources do not use
// native defaults; the official CustomResourceDefinition API itself does.
func declaredNativeObjectEquivalent(a, b *unstructured.Unstructured) bool {
	if a == nil || b == nil || a.GroupVersionKind() != b.GroupVersionKind() {
		return false
	}
	typ := declaredNativeType(a.GroupVersionKind())
	if typ == nil {
		return false
	}
	left, leftOK := canonicalDeclaredNativeValue(a.Object, typ)
	right, rightOK := canonicalDeclaredNativeValue(b.Object, typ)
	return leftOK && rightOK && reflect.DeepEqual(left, right)
}

func declaredNativeType(gvk schema.GroupVersionKind) reflect.Type {
	// CRDs themselves are official typed Kubernetes resources, although
	// client-go's ordinary scheme does not register the apiextensions API.
	// This does not register or normalize any custom resources defined by them.
	if gvk.Group == "apiextensions.k8s.io" && gvk.Kind == "CustomResourceDefinition" {
		switch gvk.Version {
		case "v1":
			return reflect.TypeOf(&apiextensionsv1.CustomResourceDefinition{})
		case "v1beta1":
			return reflect.TypeOf(&apiextensionsv1beta1.CustomResourceDefinition{})
		}
	}
	obj, err := kubescheme.Scheme.New(gvk)
	if err != nil {
		return nil
	}
	return reflect.TypeOf(obj)
}

func canonicalDeclaredNativeValue(original any, typ reflect.Type) (any, bool) {
	if typ == nil {
		return nil, false
	}
	raw, err := json.Marshal(original)
	if err != nil {
		return nil, false
	}
	native := reflect.New(typ).Interface()
	if err := json.Unmarshal(raw, native); err != nil {
		return nil, false
	}
	raw, err = json.Marshal(native)
	if err != nil {
		return nil, false
	}
	var canonical any
	if err := kjson.UnmarshalCaseSensitivePreserveInts(raw, &canonical); err != nil {
		return nil, false
	}
	return restoreUnknownDeclaredNative(original, canonical, typ), true
}

func restoreUnknownDeclaredNative(original, canonical any, typ reflect.Type) any {
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	switch typ.Kind() {
	case reflect.Struct, reflect.Map:
		originalMap, ok := original.(map[string]any)
		if !ok {
			return canonical
		}
		canonicalMap, _ := canonical.(map[string]any)
		var fields map[string]reflect.Type
		if typ.Kind() == reflect.Struct {
			fields = declaredNativeFields(typ)
		}
		for key, originalValue := range originalMap {
			childType := fields[key]
			if typ.Kind() == reflect.Map {
				childType = typ.Elem()
			}
			if childType == nil {
				if canonicalMap == nil {
					canonicalMap = map[string]any{}
				}
				canonicalMap[key] = runtime.DeepCopyJSONValue(originalValue)
				continue
			}
			previous, existed := canonicalMap[key]
			restored := restoreUnknownDeclaredNative(originalValue, previous, childType)
			if existed || restored != nil {
				if canonicalMap == nil {
					canonicalMap = map[string]any{}
				}
				canonicalMap[key] = restored
			}
		}
		if canonicalMap != nil {
			return canonicalMap
		}
	case reflect.Slice, reflect.Array:
		originalItems, ok := original.([]any)
		if !ok {
			return canonical
		}
		canonicalItems, ok := canonical.([]any)
		if !ok || len(originalItems) != len(canonicalItems) {
			return canonical
		}
		for i := range originalItems {
			canonicalItems[i] = restoreUnknownDeclaredNative(originalItems[i], canonicalItems[i], typ.Elem())
		}
		return canonicalItems
	}
	return canonical
}

var declaredNativeFieldCache sync.Map

func declaredNativeFields(typ reflect.Type) map[string]reflect.Type {
	if cached, ok := declaredNativeFieldCache.Load(typ); ok {
		if fields, valid := cached.(map[string]reflect.Type); valid {
			return fields
		}
	}
	fields := map[string]reflect.Type{}
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if field.PkgPath != "" {
			continue
		}
		tag := strings.Split(field.Tag.Get("json"), ",")
		if tag[0] == "-" {
			continue
		}
		name := tag[0]
		if name == "" && field.Anonymous {
			child := field.Type
			for child.Kind() == reflect.Pointer {
				child = child.Elem()
			}
			if child.Kind() == reflect.Struct {
				for key, typ := range declaredNativeFields(child) {
					fields[key] = typ
				}
				continue
			}
		}
		if name == "" {
			name = field.Name
		}
		fields[name] = field.Type
	}
	actual, _ := declaredNativeFieldCache.LoadOrStore(typ, fields)
	if cached, valid := actual.(map[string]reflect.Type); valid {
		return cached
	}
	return fields
}

func declaredNativePathType(root reflect.Type, path fieldpath.Path) reflect.Type {
	typ := root
	for _, element := range path {
		if typ == nil {
			return nil
		}
		for typ.Kind() == reflect.Pointer {
			typ = typ.Elem()
		}
		switch {
		case element.FieldName != nil && typ.Kind() == reflect.Struct:
			typ = declaredNativeFields(typ)[*element.FieldName]
		case element.FieldName != nil && typ.Kind() == reflect.Map:
			typ = typ.Elem()
		case (element.Key != nil || element.Value != nil || element.Index != nil) && (typ.Kind() == reflect.Slice || typ.Kind() == reflect.Array):
			typ = typ.Elem()
		default:
			return nil
		}
	}
	return typ
}

// Pod init containers execute in declaration order. Other name-keyed lists,
// including schema-free CR fields with the same spelling, retain their usual
// associative merge semantics.
func (m *intentDeclaredMerger) ordersInitContainers(path fieldpath.Path) bool {
	if len(path) == 0 || path[len(path)-1].FieldName == nil || *path[len(path)-1].FieldName != "initContainers" {
		return false
	}
	parent := declaredNativePathType(m.nativeType, path[:len(path)-1])
	if parent == nil {
		return false
	}
	return parent == reflect.TypeFor[corev1.PodSpec]() || parent == reflect.TypeFor[*corev1.PodSpec]()
}

// NativeInitContainersAt checks an absolute resource path against the actual
// registered native type. MID rendering can use this before the writer sees
// the combined baseline and instance declaration.
func NativeInitContainersAt(gvk schema.GroupVersionKind, fields ...string) bool {
	m := intentDeclaredMerger{nativeType: declaredNativeType(gvk)}
	path := make(fieldpath.Path, len(fields))
	for i := range fields {
		path[i].FieldName = &fields[i]
	}
	return m.ordersInitContainers(path)
}

// OrderDeclaredInitContainers places the explicitly ordered names into their
// existing slots after name-based field merging. All other members stay put.
func OrderDeclaredInitContainers(merged, wanted []any) error {
	if len(wanted) < 2 {
		return nil
	}
	order := make(map[string]int, len(wanted))
	for i, value := range wanted {
		item, _ := value.(map[string]any)
		name, _ := item["name"].(string)
		if _, duplicate := order[name]; duplicate || name == "" {
			return fmt.Errorf("declared initContainers require unique nonempty names")
		}
		order[name] = i
	}
	members := make([]any, len(wanted))
	slots := make([]int, 0, len(wanted))
	for i, value := range merged {
		item, _ := value.(map[string]any)
		name, _ := item["name"].(string)
		if index, declared := order[name]; declared {
			if members[index] != nil {
				return fmt.Errorf("merged initContainers contain duplicate declared names")
			}
			slots = append(slots, i)
			members[index] = value
		}
	}
	if len(slots) != len(wanted) {
		return fmt.Errorf("merged initContainers lost a declared member")
	}
	for i, slot := range slots {
		merged[slot] = members[i]
	}
	return nil
}
