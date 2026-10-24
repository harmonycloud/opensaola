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
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/managedfields"
	"sigs.k8s.io/structured-merge-diff/v6/fieldpath"
	"sigs.k8s.io/structured-merge-diff/v6/schema"
	"sigs.k8s.io/structured-merge-diff/v6/typed"
	"sigs.k8s.io/structured-merge-diff/v6/value"
)

// MergeResourceIntent applies only changes between the last successfully written
// desired object (before) and desired to live. The API schema, not field names,
// determines map/list membership and atomic boundaries. A nil before bootstraps
// by overlaying desired without inferring any omitted field's ownership.
//
// Deletions preserve externally changed values and protected fields. A removed
// associative-list item is preserved in full if deleting it would also remove
// externally added or changed children. The returned paths describe these
// retained deletions; the caller must report them rather than claim convergence.
// protected is an optional set of fields independently protected by the caller.
// This function never derives provenance from managedFields manager names.
//
// Status and system metadata are copied unchanged from live; only labels and
// annotations participate in the merge. All arguments are left untouched.
func MergeResourceIntent(converter managedfields.TypeConverter, before, live, desired *unstructured.Unstructured, protected *fieldpath.Set) (*unstructured.Unstructured, []string, error) {
	if converter == nil || live == nil || desired == nil {
		return nil, nil, fmt.Errorf("intent merge requires a schema converter, live object and desired object")
	}
	for _, obj := range []*unstructured.Unstructured{before, desired} {
		if obj == nil {
			continue
		}
		if obj.GroupVersionKind() != live.GroupVersionKind() || obj.GetName() != live.GetName() || obj.GetNamespace() != live.GetNamespace() {
			return nil, nil, fmt.Errorf("intent merge requires matching apiVersion, kind, name and namespace")
		}
	}
	if live.GetAPIVersion() == "" || live.GetKind() == "" {
		return nil, nil, fmt.Errorf("intent merge requires an explicit apiVersion and kind")
	}
	l, err := converter.ObjectToTyped(intentMergeScope(live))
	if err != nil {
		return nil, nil, fmt.Errorf("live resource schema validation failed for %s %s/%s", live.GetKind(), live.GetNamespace(), live.GetName())
	}
	d, err := converter.ObjectToTyped(intentMergeScope(desired))
	if err != nil {
		return nil, nil, fmt.Errorf("desired resource schema validation failed for %s %s/%s", desired.GetKind(), desired.GetNamespace(), desired.GetName())
	}
	var merged *typed.TypedValue
	var preserved []string
	if before == nil {
		merged, err = l.Merge(d)
	} else {
		b, parseErr := converter.ObjectToTyped(intentMergeScope(before))
		if parseErr != nil {
			return nil, nil, fmt.Errorf("previous resource schema validation failed for %s %s/%s", before.GetKind(), before.GetNamespace(), before.GetName())
		}
		merged, preserved, err = mergeTypedIntent(b, l, d, protected)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("merge resource intent: %w", err)
	}
	obj, err := converter.TypedToObject(merged)
	if err != nil {
		return nil, nil, fmt.Errorf("decode merged resource: %w", err)
	}
	result, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return nil, nil, fmt.Errorf("schema converter returned unsupported merged resource %T", obj)
	}
	result = result.DeepCopy()
	restoreIntentSystemFields(result, live)
	return result, preserved, nil
}

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

// CaptureResourceIntentDefaults captures only fields added by the API server
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
	return supplementIntentDefaults(converter, objects[3], objects[0], objects[1], effectiveDesired, change.Added.Leaves())
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

func mergeTypedIntent(b, l, d *typed.TypedValue, protected *fieldpath.Set) (*typed.TypedValue, []string, error) {
	change, err := b.Compare(d)
	if err != nil {
		return nil, nil, err
	}
	drift, err := b.Compare(l)
	if err != nil {
		return nil, nil, err
	}
	liveFields, err := l.ToFieldSet()
	if err != nil {
		return nil, nil, err
	}
	// An absent leaf needs no deletion. A still-present list item whose child
	// was removed, however, was externally edited just like a changed child.
	// The live-field presence check below distinguishes these two cases.
	externalChanges := drift.Added.Union(drift.Modified).Union(drift.Removed)
	blockedItems := fieldpath.NewSet()
	change.Removed.Iterate(func(path fieldpath.Path) {
		if len(path) == 0 || path[len(path)-1].Key == nil {
			return
		}
		if intentPathsOverlap(externalChanges, path) || intentPathsOverlap(protected, path) {
			if intentContainsPath(liveFields, path) {
				blockedItems.Insert(path)
			}
		}
	})
	remove := fieldpath.NewSet()
	retained := fieldpath.NewSet()
	change.Removed.Iterate(func(path fieldpath.Path) {
		if !intentContainsPath(liveFields, path) {
			return
		}
		if intentPathsOverlap(blockedItems, path) || intentPathsOverlap(externalChanges, path) || intentPathsOverlap(protected, path) {
			retained.Insert(path)
			return
		}
		remove.Insert(path)
	})
	write := change.Added.Union(change.Modified)
	liveDesired, err := l.Compare(d)
	if err != nil {
		return nil, nil, err
	}
	stillDifferent := liveDesired.Added.Union(liveDesired.Modified).Union(liveDesired.Removed)
	blockedWrites := fieldpath.NewSet()
	var schemaErr error
	write.Iterate(func(path fieldpath.Path) {
		if schemaErr != nil || !intentContainsPath(liveFields, path) || !intentPathsOverlap(externalChanges, path) || !intentPathsOverlap(stillDifferent, path) {
			return
		}
		var atomic bool
		atomic, schemaErr = intentAtomicContainerAt(d, path)
		if atomic {
			blockedWrites.Insert(path)
			retained.Insert(path)
		}
	})
	if schemaErr != nil {
		return nil, nil, schemaErr
	}
	// Extract only B -> D additions/changes, with their associative key fields.
	// Re-merging all of D would overwrite live values unchanged in our intent.
	// A container declared atomic has no safe independently editable children:
	// when both parties changed it, preserve live and report the conflict.
	// Compare includes both newly added containers and their descendants.
	// Extract leaves only: passing the parent membership marker as well would
	// extract a partial container instead of its descendant values.
	patch := d.ExtractItems(write.Leaves().RecursiveDifference(blockedWrites), typed.WithAppendKeyFields())
	merged, err := l.RemoveItems(remove).Merge(patch)
	if err != nil {
		return nil, nil, err
	}
	var preserved []string
	retained.Iterate(func(path fieldpath.Path) {
		// A parent retained path explains its descendants without an enormous
		// warning for every leaf in a removed container/list item.
		for i := 1; i < len(path); i++ {
			if retained.Has(path[:i]) {
				return
			}
		}
		preserved = append(preserved, intentSafePath(path))
	})
	sort.Strings(preserved)
	if len(preserved) > 1 {
		unique := preserved[:1]
		for _, path := range preserved[1:] {
			if path != unique[len(unique)-1] {
				unique = append(unique, path)
			}
		}
		preserved = unique
	}
	return merged, preserved, nil
}

func intentAtomicContainerAt(obj *typed.TypedValue, path fieldpath.Path) (bool, error) {
	ref := obj.TypeRef()
	for _, element := range path {
		atom, ok := obj.Schema().Resolve(ref)
		if !ok {
			return false, fmt.Errorf("cannot resolve schema at %s", intentSafePath(path))
		}
		switch {
		case element.FieldName != nil && atom.Map != nil:
			if field, exists := atom.Map.FindField(*element.FieldName); exists {
				ref = field.Type
			} else {
				ref = atom.Map.ElementType
			}
		case (element.Key != nil || element.Value != nil || element.Index != nil) && atom.List != nil:
			ref = atom.List.ElementType
		default:
			return false, fmt.Errorf("cannot traverse schema at %s", intentSafePath(path))
		}
	}
	atom, ok := obj.Schema().Resolve(ref)
	if !ok {
		return false, fmt.Errorf("cannot resolve schema at %s", intentSafePath(path))
	}
	return (atom.Map != nil && atom.Map.ElementRelationship == schema.Atomic) ||
		(atom.List != nil && atom.List.ElementRelationship == schema.Atomic), nil
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
