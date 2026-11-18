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
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/managedfields"
	"sigs.k8s.io/structured-merge-diff/v6/fieldpath"
	"sigs.k8s.io/structured-merge-diff/v6/typed"
	"sigs.k8s.io/structured-merge-diff/v6/value"
)

// CarryResourceIntentDefaults carries previously captured API defaults into the
// next effective desired object. It never carries defaults across a removed
// associative item or a removed immediate parent (for example secret to
// configMap volume sources), and explicit new desired values always win.
func CarryResourceIntentDefaults(converter managedfields.TypeConverter, rawBefore, effectiveBefore, newDesired *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	objects, err := intentDefaultsObjects(converter, rawBefore, effectiveBefore, newDesired)
	if err != nil {
		return nil, err
	}
	change, err := objects[0].Compare(objects[1])
	if err != nil {
		return nil, fmt.Errorf("cannot compare previously captured resource defaults")
	}
	return supplementIntentDefaults(converter, objects[1], objects[2], objects[2], newDesired, change.Added.Leaves())
}

// CaptureResourceIntentDefaults captures only recognized defaults added by the API server
// during this specific write/preview. Existing live additions and changed
// values are not defaults. A captured field must belong to associative items
// explicitly present in rawDesired, and its immediate parent must already
// exist in effectiveDesired; entire injected sidecars/subtrees are excluded.
// submitted and accepted may have different resourceVersions because accepted
// is the result of a write. The writer is responsible for the write's identity
// and concurrency guards. The returned baseline must remain separate from raw
// desired intent.
func CaptureResourceIntentDefaults(converter managedfields.TypeConverter, rawDesired, effectiveDesired, submitted, accepted *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	objects, err := intentDefaultsObjects(converter, rawDesired, effectiveDesired, submitted, accepted)
	if err != nil {
		return nil, err
	}
	if submitted.GetUID() != "" && accepted.GetUID() != submitted.GetUID() {
		return nil, fmt.Errorf("resource defaults require the accepted object to retain its submitted UID")
	}
	change, err := objects[2].Compare(objects[3])
	if err != nil {
		return nil, fmt.Errorf("cannot compare submitted and accepted resource defaults")
	}
	// A mutating webhook can add an arbitrary leaf to an explicitly declared
	// member in the same request. Its mere appearance must never authorize a
	// future deletion. Match actual values to trusted schema or native defaults.
	parsed := objects[3]
	root := intentDeclaredType{ref: parsed.TypeRef()}
	merger := intentDeclaredMerger{topology: parsed.Schema(), root: root.ref, nativeType: declaredNativeType(accepted.GroupVersionKind())}
	lookup := declaredDeletionLookup{merger: &merger, lists: map[string]map[string]intentDeclaredMember{}}
	known := fieldpath.NewSet()
	change.Added.Leaves().Iterate(func(path fieldpath.Path) {
		if len(path) < 2 || path[len(path)-1].FieldName == nil {
			return
		}
		actual, typ, lookupErr := lookup.find(accepted.Object, root, path)
		if lookupErr == nil && actual.present && (merger.acceptedDefault(actual.value, typ, path) || lookup.nativeDefaultAt(accepted.Object, root, path, actual.value)) {
			known.Insert(path)
		}
	})
	return supplementIntentDefaults(converter, parsed, objects[0], objects[1], effectiveDesired, known)
}

func supplementIntentDefaults(converter managedfields.TypeConverter, source, rawDesired, effectiveDesired *typed.TypedValue, original *unstructured.Unstructured, candidates *fieldpath.Set) (*unstructured.Unstructured, error) {
	rawFields, err := rawDesired.ToFieldSet()
	if err != nil {
		return nil, fmt.Errorf("cannot inspect declared resource fields for defaults")
	}
	effectiveFields, err := effectiveDesired.ToFieldSet()
	if err != nil {
		return nil, fmt.Errorf("cannot inspect effective resource fields for defaults")
	}
	include := fieldpath.NewSet()
	candidates.Iterate(func(path fieldpath.Path) {
		if len(path) < 2 || intentContainsPath(effectiveFields, path) || !intentContainsPath(effectiveFields, path[:len(path)-1]) || !intentConcreteParent(effectiveDesired, path[:len(path)-1]) {
			return
		}
		hasItem := false
		for i, element := range path {
			if element.Key == nil {
				continue
			}
			hasItem = true
			// Every nested list member must be explicitly declared. Merely
			// sharing the outer container does not authorize an injected env
			// entry, nested volume, or other unrelated member.
			if !intentContainsPath(rawFields, path[:i+1]) {
				return
			}
		}
		if hasItem {
			include.Insert(path)
		}
	})
	if include.Empty() {
		return original.DeepCopy(), nil
	}
	defaults := source.ExtractItems(include, typed.WithAppendKeyFields())
	completed, err := effectiveDesired.Merge(defaults)
	if err != nil {
		return nil, fmt.Errorf("cannot supplement resource intent defaults")
	}
	obj, err := converter.TypedToObject(completed)
	if err != nil {
		return nil, fmt.Errorf("cannot decode resource intent defaults")
	}
	result, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return nil, fmt.Errorf("schema converter returned unsupported defaulted intent %T", obj)
	}
	result = result.DeepCopy()
	restoreIntentSystemFields(result, original)
	return result, nil
}

// Field sets distinguish absence but also contain explicit null fields. A null
// source is not a parent into which a previously captured default may be merged.
func intentConcreteParent(obj *typed.TypedValue, path fieldpath.Path) bool {
	current, ref := obj.AsValue(), obj.TypeRef()
	for _, element := range path {
		atom, ok := obj.Schema().Resolve(ref)
		if !ok || current == nil || current.IsNull() {
			return false
		}
		switch {
		case element.FieldName != nil && atom.Map != nil && current.IsMap():
			var exists bool
			current, exists = current.AsMap().Get(*element.FieldName)
			if !exists {
				return false
			}
			ref = atom.Map.ElementType
			if field, exists := atom.Map.FindField(*element.FieldName); exists {
				ref = field.Type
			}
		case atom.List != nil && current.IsList():
			list := current.AsList()
			itemType, exists := obj.Schema().Resolve(atom.List.ElementType)
			if !exists {
				return false
			}
			var selected value.Value
			for i := 0; i < list.Length(); i++ {
				item := list.At(i)
				switch {
				case element.Key != nil && item.IsMap() && itemType.Map != nil:
					matches := true
					for _, key := range *element.Key {
						v, found := item.AsMap().Get(key.Name)
						if !found {
							if field, exists := itemType.Map.FindField(key.Name); exists && field.Default != nil {
								v, found = value.NewValueInterface(field.Default), true
							}
						}
						if !found || !value.Equals(v, key.Value) {
							matches = false
							break
						}
					}
					if matches {
						selected = item
					}
				case element.Value != nil && value.Equals(item, *element.Value):
					selected = item
				case element.Index != nil && i == *element.Index:
					selected = item
				}
				if selected != nil {
					break
				}
			}
			if selected == nil {
				return false
			}
			current, ref = selected, atom.List.ElementType
		default:
			return false
		}
	}
	return current != nil && !current.IsNull() && (current.IsMap() || current.IsList())
}

func intentDefaultsObjects(converter managedfields.TypeConverter, objects ...*unstructured.Unstructured) ([]*typed.TypedValue, error) {
	if converter == nil {
		return nil, fmt.Errorf("resource intent defaults requires a schema converter")
	}
	parsed := make([]*typed.TypedValue, len(objects))
	for i, obj := range objects {
		if obj == nil || obj.GetAPIVersion() == "" || obj.GetKind() == "" {
			return nil, fmt.Errorf("resource intent defaults requires objects with explicit apiVersion and kind")
		}
		if obj.GroupVersionKind() != objects[0].GroupVersionKind() || obj.GetName() != objects[0].GetName() || obj.GetNamespace() != objects[0].GetNamespace() {
			return nil, fmt.Errorf("resource intent defaults requires matching apiVersion, kind, name and namespace")
		}
		var err error
		parsed[i], err = converter.ObjectToTyped(intentMergeScope(obj))
		if err != nil {
			return nil, fmt.Errorf("resource intent defaults schema validation failed for object %d", i)
		}
	}
	return parsed, nil
}

func intentPathsOverlap(set *fieldpath.Set, path fieldpath.Path) bool {
	if set == nil {
		return false
	}
	for _, element := range path {
		if set.Members.Has(element) {
			return true
		}
		child, exists := set.Children.Get(element)
		if !exists {
			return false
		}
		set = child
	}
	return !set.Empty()
}

func intentContainsPath(set *fieldpath.Set, path fieldpath.Path) bool {
	if set == nil {
		return false
	}
	for i, element := range path {
		if i == len(path)-1 && set.Members.Has(element) {
			return true
		}
		child, exists := set.Children.Get(element)
		if !exists {
			return false
		}
		set = child
	}
	return !set.Empty()
}

// Set members and associative key values can contain credentials. They must
// never be copied into owner annotations, warning events, or error logs.
func intentSafePath(path fieldpath.Path) string {
	var result strings.Builder
	for _, element := range path {
		switch {
		case element.FieldName != nil:
			result.WriteString("." + *element.FieldName)
		case element.Key != nil:
			result.WriteByte('[')
			for i, field := range *element.Key {
				if i > 0 {
					result.WriteByte(',')
				}
				result.WriteString(field.Name + "=<redacted>")
			}
			result.WriteByte(']')
		case element.Value != nil:
			result.WriteString("[set-item]")
		case element.Index != nil:
			fmt.Fprintf(&result, "[%d]", *element.Index)
		}
	}
	return result.String()
}

func intentMergeScope(obj *unstructured.Unstructured) *unstructured.Unstructured {
	copy := obj.DeepCopy()
	delete(copy.Object, "status")
	meta, ok := copy.Object["metadata"].(map[string]interface{})
	if ok {
		for key := range meta {
			switch key {
			case "name", "namespace", "labels", "annotations":
			default:
				delete(meta, key)
			}
		}
	}
	return copy
}

func restoreIntentSystemFields(result, live *unstructured.Unstructured) {
	if status, exists := live.Object["status"]; exists {
		result.Object["status"] = runtime.DeepCopyJSONValue(status)
	}
	liveMeta, ok := live.Object["metadata"].(map[string]interface{})
	if !ok {
		return
	}
	meta, _ := result.Object["metadata"].(map[string]interface{})
	if meta == nil {
		meta = map[string]interface{}{}
		result.Object["metadata"] = meta
	}
	for key, val := range liveMeta {
		if key != "labels" && key != "annotations" {
			meta[key] = runtime.DeepCopyJSONValue(val)
		}
	}
}
