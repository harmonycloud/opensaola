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

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/managedfields"
	"sigs.k8s.io/structured-merge-diff/v6/fieldpath"
	"sigs.k8s.io/structured-merge-diff/v6/schema"
	"sigs.k8s.io/structured-merge-diff/v6/typed"
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
// Retained as the version-one algorithm reference for compatibility tests;
// ordinary writes use PlanDeclaredResourceIntent and legacy migration uses
// PlanLegacyResourceIntentDeletions.
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
