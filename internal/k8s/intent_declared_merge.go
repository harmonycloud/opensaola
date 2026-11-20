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
	"fmt"
	"reflect"
	"sort"

	"github.com/harmonycloud/opensaola/pkg/tools"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/managedfields"
	"sigs.k8s.io/structured-merge-diff/v6/fieldpath"
	"sigs.k8s.io/structured-merge-diff/v6/schema"
	"sigs.k8s.io/structured-merge-diff/v6/value"
)

// MergeDeclaredResourceIntent enforces every field in rawDesired, not just the
// difference from before. effectiveDesired may additionally contain previously
// captured API defaults: these participate in deletion history, but are never
// promoted to authoritative declarations. A nil before does not infer deletion
// rights from the live object.
//
// Schema-defined associative/set/atomic relationships remain authoritative.
// Only genuinely untyped fields use the application-level unique-name list
// convention. Such arrays are still submitted as whole API fields, with the
// writer responsible for UID/resourceVersion fencing. External Apply protection
// on an atomic ancestor also protects inferred named members from deletion.
// All inputs remain untouched. Returned paths describe preserved deletions only.
func MergeDeclaredResourceIntent(converter managedfields.TypeConverter, before, live, effectiveDesired, rawDesired *unstructured.Unstructured, protected *fieldpath.Set) (*unstructured.Unstructured, []string, error) {
	plan, err := PlanDeclaredResourceIntent(converter, before, live, effectiveDesired, rawDesired, protected)
	if err != nil {
		return nil, nil, err
	}
	if err := plan.DeletionConflictError(); err != nil {
		return nil, nil, err
	}
	return plan.Object, plan.PreservedExternal, nil
}

// PlanDeclaredResourceIntent distinguishes harmless external leftovers from
// deletions that could not be fulfilled. Callers must reject deletion conflicts
// before preparing a record and validate DeletionChecks against server replies.
func PlanDeclaredResourceIntent(converter managedfields.TypeConverter, before, live, effectiveDesired, rawDesired *unstructured.Unstructured, protected *fieldpath.Set) (*DeclaredIntentMergeResult, error) {
	return planDeclaredResourceIntent(converter, before, live, effectiveDesired, rawDesired, protected, declaredUpdate)
}

// OverlayActionFields uses API list identities for a one-off edit. Within an
// atomic value, preserve the Action overlay contract for unspecified children.
func OverlayActionFields(converter managedfields.TypeConverter, live, edit *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	edit, err := actionWithNativeMountPaths(live, edit)
	if err != nil {
		return nil, err
	}
	plan, err := planDeclaredResourceIntent(converter, nil, live, edit, edit, nil, actionOverlay)
	if err != nil {
		return nil, err
	}
	return plan.Object, nil
}

type declaredMergeMode int

const (
	declaredUpdate declaredMergeMode = iota
	actionOverlay
	retryAfterAtomicRelease
)

func planDeclaredResourceIntent(converter managedfields.TypeConverter, before, live, effectiveDesired, rawDesired *unstructured.Unstructured, protected *fieldpath.Set, mode declaredMergeMode) (*DeclaredIntentMergeResult, error) {
	if converter == nil || live == nil || effectiveDesired == nil || rawDesired == nil {
		return nil, fmt.Errorf("declared intent merge requires schema, live, effective and raw desired objects")
	}
	for _, obj := range []*unstructured.Unstructured{before, effectiveDesired, rawDesired} {
		if obj != nil && (obj.GroupVersionKind() != live.GroupVersionKind() || obj.GetName() != live.GetName() || obj.GetNamespace() != live.GetNamespace()) {
			return nil, fmt.Errorf("declared intent merge requires matching resource identities")
		}
	}
	if live.GetAPIVersion() == "" || live.GetKind() == "" {
		return nil, fmt.Errorf("declared intent merge requires an explicit apiVersion and kind")
	}
	objects := make([]intentDeclaredValue, 4)
	var topology *schema.Schema
	var root schema.TypeRef
	for i, obj := range []*unstructured.Unstructured{before, live, effectiveDesired, rawDesired} {
		if obj == nil {
			continue
		}
		scoped := intentMergeScope(obj)
		parsed, err := converter.ObjectToTyped(scoped)
		if err != nil {
			return nil, fmt.Errorf("declared intent schema validation failed for input %d", i)
		}
		if i == 1 {
			topology, root = parsed.Schema(), parsed.TypeRef()
		}
		objects[i] = intentDeclaredValue{value: scoped.Object, present: true}
	}
	merger := intentDeclaredMerger{mode: mode, topology: topology, protected: protected, retained: map[string]struct{}{}, root: root, nativeType: declaredNativeType(live.GroupVersionKind())}
	merged, err := merger.merge(objects[0], objects[1], objects[2], objects[3], intentDeclaredType{ref: root}, nil, false)
	if err != nil {
		return nil, err
	}
	object, ok := merged.value.(map[string]any)
	if !merged.present || !ok {
		return nil, fmt.Errorf("declared intent merge did not produce a resource object")
	}
	result := &unstructured.Unstructured{Object: object}
	if _, err := converter.ObjectToTyped(result); err != nil {
		return nil, fmt.Errorf("merged declarations do not match the resource schema")
	}
	restoreIntentSystemFields(result, live)
	if len(merger.atomicReleases) == 0 && declaredNativeObjectEquivalent(intentMergeScope(result), intentMergeScope(live)) {
		// Avoid repeated patches for already satisfied native omitempty values.
		// This is semantic equality after merging, never removal of raw intent.
		result = live.DeepCopy()
	}
	paths := make([]string, 0, len(merger.retained))
	for path := range merger.retained {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return &DeclaredIntentMergeResult{Object: result, assertions: merger.assertions, namedEmptyLists: merger.namedEmptyLists, PreservedExternal: paths, BlockedDeletions: merger.blocked, DeletionChecks: compactDeclaredDeletionChecks(merger.checks), atomicReleases: merger.atomicReleases}, nil
}

// ValidateDeclaredResourceIntent checks declaration projection, not whole-object
// equality: unmentioned fields, associative/set members and API defaults may
// legitimately be present. Schema-typed Quantity values compare numerically;
// arbitrary strings are never parsed as quantities. Error messages contain
// paths only, never declared or accepted values.
func ValidateDeclaredResourceIntent(converter managedfields.TypeConverter, rawDesired, actual *unstructured.Unstructured) error {
	plan, err := NewDeclaredValidationPlan(converter, rawDesired, nil)
	if err != nil {
		return err
	}
	return plan.Validate(converter, actual)
}

type intentDeclaredValue struct {
	value   any
	present bool
}

type intentDeclaredType struct {
	ref          schema.TypeRef
	opaque       bool
	defaultValue any
}

type intentDeclaredMerger struct {
	mode            declaredMergeMode
	topology        *schema.Schema
	root            schema.TypeRef
	assertions      []declaredFieldAssertion
	namedEmptyLists [][]string
	forceNamedEmpty *fieldpath.Set
	protected       *fieldpath.Set
	retained        map[string]struct{}
	nativeType      reflect.Type
	blocked         []DeclaredDeletionConflict
	checks          []DeclaredDeletionCheck
	atomicReleases  []DeclaredDeletionCheck
}

func (m *intentDeclaredMerger) resolve(typ intentDeclaredType, path fieldpath.Path) (schema.Atom, bool, error) {
	atom, ok := m.topology.Resolve(typ.ref)
	if !ok {
		return schema.Atom{}, false, fmt.Errorf("cannot resolve declaration schema at %s", intentSafePath(path))
	}
	// kube-openapi deliberately retains this reference for unknown fields. A
	// typed array with untyped *elements* does not have this reference itself.
	// Nor does RawExtension's explicitly atomic untyped reference qualify.
	opaque := typ.opaque || typ.ref.NamedType != nil && *typ.ref.NamedType == "__untyped_deduced_" && typ.ref.ElementRelationship == nil
	return atom, opaque, nil
}

func (m *intentDeclaredMerger) merge(b, l, d, raw intentDeclaredValue, typ intentDeclaredType, path fieldpath.Path, member bool) (intentDeclaredValue, error) {
	if !raw.present {
		if d.present || !b.present {
			if !d.present && !b.present && l.present {
				m.retain(path)
			}
			return copyDeclaredValue(l), nil
		}
		return m.remove(b, l, typ, path, member)
	}
	if raw.value == nil {
		m.assert(path, typ, raw.value, assertValue)
		return copyDeclaredValue(raw), nil
	}
	atom, opaque, err := m.resolve(typ, path)
	if err != nil {
		return intentDeclaredValue{}, err
	}
	if !l.present && typ.defaultValue != nil && declaredEqual(raw.value, typ.defaultValue, typ.ref) {
		m.assert(path, typ, raw.value, assertValue)
		// A missing schema-defaulted scalar already means the declared value.
		return l, nil
	}
	switch wanted := raw.value.(type) {
	case map[string]any:
		if !opaque && (atom.Map == nil || atom.Map.ElementRelationship == schema.Atomic) {
			if current, ok := l.value.(map[string]any); m.mode == actionOverlay && ok {
				return intentDeclaredValue{value: tools.MergeMap(runtime.DeepCopyJSON(current), runtime.DeepCopyJSON(wanted)), present: true}, nil
			}
			return m.mergeAtomic(b, l, raw, typ, path)
		}
		m.assert(path, typ, raw.value, assertObject)
		return m.mergeMap(b, l, d, raw, atom, opaque, path)
	case []any:
		if atom.List == nil && !opaque {
			return intentDeclaredValue{}, fmt.Errorf("declaration is not a schema list at %s", intentSafePath(path))
		}
		keys, associative, err := declaredListKeys(atom, opaque, path, b, l, d, raw)
		if err != nil {
			return intentDeclaredValue{}, err
		}
		if opaque && len(wanted) == 0 && m.forceNamedEmpty != nil && m.forceNamedEmpty.Has(path) {
			keys, associative = []string{"name"}, true
		}
		if associative {
			if opaque && len(wanted) == 0 {
				segments, err := encodeFieldSegments(path)
				if err != nil {
					return intentDeclaredValue{}, err
				}
				m.namedEmptyLists = append(m.namedEmptyLists, segments)
			}
			m.assert(path, typ, raw.value, assertList)
			return m.mergeList(b, l, d, raw, atom, opaque, keys, path)
		}
		if current, ok := l.value.([]any); m.mode == actionOverlay && ok {
			// Both operands have been copied into the merge scope; the package
			// overlay preserves an existing array for an empty edit.
			return intentDeclaredValue{value: tools.MergeArray(current, wanted), present: true}, nil
		}
		return m.mergeAtomic(b, l, raw, typ, path)
	default:
		m.assert(path, typ, raw.value, assertValue)
		if l.present && declaredEqual(raw.value, l.value, typ.ref) {
			return copyDeclaredValue(l), nil
		}
		return copyDeclaredValue(raw), nil
	}
}

// Atomic values are written as a whole. A satisfied projection can keep the live
// value, unless it still contains a field withdrawn from the prior declaration.
func (m *intentDeclaredMerger) mergeAtomic(before, live, wanted intentDeclaredValue, typ intentDeclaredType, path fieldpath.Path) (intentDeclaredValue, error) {
	m.assert(path, typ, wanted.value, assertValue)
	satisfied := live.present && m.matchesDeclaredValue(wanted.value, live.value, typ, path)
	if m.mode == retryAfterAtomicRelease && satisfied {
		// The same declaration was already submitted. A retry for another
		// field must preserve the accepted value, including its supplements.
		return copyDeclaredValue(live), nil
	}
	needsRemoval := false
	if before.present && !reflect.DeepEqual(before.value, wanted.value) {
		lookup := declaredDeletionLookup{merger: m, lists: map[string]map[string]intentDeclaredMember{}}
		err := m.walkAtomicWithdrawals(before.value, wanted.value, typ, path, func(old any, child intentDeclaredType, removed fieldpath.Path) error {
			current, _, err := lookup.find(live.value, typ, removed[len(path):])
			if err != nil {
				return err
			}
			if current.present {
				needsRemoval = true
				// Admission may have filled this key in the preceding write.
				wasReleased := m.mode == retryAfterAtomicRelease && !m.deletionProtected(removed)
				if !wasReleased && !m.canRemoveLeaf(old, current.value, child, removed) {
					return nil
				}
			}
			// Submitting the omission releases this key inside a still-declared
			// atomic unit. Only recovery without a receipt needs proof of absence.
			m.atomicReleases = append(m.atomicReleases, declaredDeletionCheck(removed))
			return nil
		})
		if err != nil {
			return intentDeclaredValue{}, err
		}
	}
	if !needsRemoval && satisfied {
		return copyDeclaredValue(live), nil
	}
	return copyDeclaredValue(wanted), nil
}

func (m *intentDeclaredMerger) remove(b, l intentDeclaredValue, typ intentDeclaredType, path fieldpath.Path, member bool) (intentDeclaredValue, error) {
	if !b.present || !l.present {
		if b.present && !l.present {
			if err := m.checkAbsentDeletion(b, typ, path, member); err != nil {
				return intentDeclaredValue{}, err
			}
		}
		return copyDeclaredValue(l), nil
	}
	atom, opaque, err := m.resolve(typ, path)
	if err != nil {
		return intentDeclaredValue{}, err
	}
	if member {
		if len(path) > 0 && path[len(path)-1].Key != nil && (opaque || atom.Map != nil && atom.Map.ElementRelationship != schema.Atomic) {
			return m.removeMember(b, l, typ, atom, opaque, path)
		}
		// Set members and atomic map members have no independently removable
		// children, even when their runtime value happens to contain a name.
		return m.removeLeaf(b, l, typ, path), nil
	}
	if _, ok := b.value.(map[string]any); ok && (opaque || atom.Map != nil && atom.Map.ElementRelationship != schema.Atomic) {
		if _, ok := l.value.(map[string]any); !ok {
			return m.removeLeaf(b, l, typ, path), nil
		}
		result, err := m.mergeMap(b, l, intentDeclaredValue{}, intentDeclaredValue{}, atom, opaque, path)
		if err != nil {
			return intentDeclaredValue{}, err
		}
		values, ok := result.value.(map[string]any)
		if !ok {
			return intentDeclaredValue{}, fmt.Errorf("declaration map merge returned an invalid value at %s", intentSafePath(path))
		}
		if len(values) == 0 {
			if m.deletionProtected(path) {
				m.blockDeletion(path, "field is protected by another Apply manager")
				return copyDeclaredValue(l), nil
			}
			if previous, valid := b.value.(map[string]any); valid && len(previous) == 0 {
				m.checkDeletion(path)
			}
			return intentDeclaredValue{}, nil
		}
		return result, nil
	}
	if _, ok := b.value.([]any); ok {
		if _, ok := l.value.([]any); !ok {
			return m.removeLeaf(b, l, typ, path), nil
		}
		keys, associative, listErr := declaredListKeys(atom, opaque, path, b, l)
		if listErr != nil {
			m.blockDeletion(path, "list membership is ambiguous")
			return copyDeclaredValue(l), nil
		}
		if associative {
			result, err := m.mergeList(b, l, intentDeclaredValue{}, intentDeclaredValue{}, atom, opaque, keys, path)
			if err != nil {
				return intentDeclaredValue{}, err
			}
			values, ok := result.value.([]any)
			if !ok {
				return intentDeclaredValue{}, fmt.Errorf("declaration list merge returned an invalid value at %s", intentSafePath(path))
			}
			if len(values) == 0 {
				if m.deletionProtected(path) {
					m.blockDeletion(path, "list is protected by another Apply manager")
					return copyDeclaredValue(l), nil
				}
				if previous, valid := b.value.([]any); valid && len(previous) == 0 {
					m.checkDeletion(path)
				}
				return intentDeclaredValue{}, nil
			}
			return result, nil
		}
	}
	return m.removeLeaf(b, l, typ, path), nil
}

func (m *intentDeclaredMerger) removeLeaf(b, l intentDeclaredValue, typ intentDeclaredType, path fieldpath.Path) intentDeclaredValue {
	if !m.canRemoveLeaf(b.value, l.value, typ, path) {
		return copyDeclaredValue(l)
	}
	m.checkDeletion(path)
	return intentDeclaredValue{}
}

func (m *intentDeclaredMerger) canRemoveLeaf(before, live any, typ intentDeclaredType, path fieldpath.Path) bool {
	if m.deletionProtected(path) {
		m.blockDeletion(path, "field is protected by another Apply manager")
		return false
	}
	if !m.equivalent(before, live, typ, path) {
		m.blockDeletion(path, "historically declared value was changed externally")
		return false
	}
	return true
}

func (m *intentDeclaredMerger) removeMember(b, l intentDeclaredValue, typ intentDeclaredType, atom schema.Atom, opaque bool, path fieldpath.Path) (intentDeclaredValue, error) {
	before, beforeOK := b.value.(map[string]any)
	live, liveOK := l.value.(map[string]any)
	if !beforeOK || !liveOK {
		m.blockDeletion(path, "associative member no longer has its expected shape")
		return copyDeclaredValue(l), nil
	}
	beforePayload, livePayload := map[string]any{}, map[string]any{}
	identity := map[string]bool{}
	for _, key := range *path[len(path)-1].Key {
		identity[key.Name] = true
	}
	for key, val := range before {
		if !identity[key] {
			beforePayload[key] = val
		}
	}
	for key, val := range live {
		if !identity[key] {
			livePayload[key] = val
		}
	}
	result, err := m.mergeMap(intentDeclaredValue{value: beforePayload, present: true}, intentDeclaredValue{value: livePayload, present: true}, intentDeclaredValue{}, intentDeclaredValue{}, atom, opaque, path)
	if err != nil {
		return intentDeclaredValue{}, err
	}
	payload, ok := result.value.(map[string]any)
	if !ok {
		return intentDeclaredValue{}, fmt.Errorf("declaration member merge returned an invalid value at %s", intentSafePath(path))
	}
	if len(payload) == 0 {
		if m.deletionProtected(path) {
			m.blockDeletion(path, "member identity is protected by another Apply manager")
			return copyDeclaredValue(l), nil
		}
		m.checkMemberDeletion(path)
		return intentDeclaredValue{}, nil
	}
	// Remaining children are external or blocked historical deletions. Keep the
	// actual identity keys so residual external configuration stays a valid item;
	// the writer rejects the whole result if any historical deletion was blocked.
	for key := range identity {
		if val, exists := live[key]; exists {
			payload[key] = runtime.DeepCopyJSONValue(val)
		}
	}
	return result, nil
}

func (m *intentDeclaredMerger) mergeMap(b, l, d, raw intentDeclaredValue, atom schema.Atom, opaque bool, path fieldpath.Path) (intentDeclaredValue, error) {
	keys := map[string]struct{}{}
	for _, input := range []intentDeclaredValue{b, l, d, raw} {
		if values, ok := input.value.(map[string]any); ok {
			for key := range values {
				keys[key] = struct{}{}
			}
		}
	}
	ordered := make([]string, 0, len(keys))
	for key := range keys {
		ordered = append(ordered, key)
	}
	sort.Strings(ordered)
	out := map[string]any{}
	for _, key := range ordered {
		child := intentDeclaredType{opaque: opaque}
		if opaque {
			child.ref = opaqueDeclaredRef()
		} else if field, found := atom.Map.FindField(key); found {
			child.ref, child.defaultValue = field.Type, field.Default
		} else {
			child.ref = atom.Map.ElementType
		}
		childPath := appendDeclaredPath(path, fieldpath.PathElement{FieldName: &key})
		merged, err := m.merge(declaredMapValue(b, key), declaredMapValue(l, key), declaredMapValue(d, key), declaredMapValue(raw, key), child, childPath, false)
		if err != nil {
			return intentDeclaredValue{}, err
		}
		if merged.present {
			out[key] = merged.value
		}
	}
	return intentDeclaredValue{value: out, present: true}, nil
}

// A nil key slice denotes a set; a nonempty slice identifies map-list members.
func declaredListKeys(atom schema.Atom, opaque bool, path fieldpath.Path, inputs ...intentDeclaredValue) ([]string, bool, error) {
	if !opaque {
		if atom.List != nil && atom.List.ElementRelationship == schema.Associative {
			return atom.List.Keys, true, nil
		}
		return nil, false, nil
	}
	hasNamed, hasUnnamed := false, false
	for _, input := range inputs {
		items, ok := input.value.([]any)
		if !ok {
			continue
		}
		seen := map[string]bool{}
		for _, item := range items {
			object, isMap := item.(map[string]any)
			if !isMap {
				hasUnnamed = true
				continue
			}
			nameValue, hasName := object["name"]
			if !hasName {
				hasUnnamed = true
				continue
			}
			name, valid := nameValue.(string)
			if !valid || name == "" || seen[name] {
				return nil, false, fmt.Errorf("ambiguous name list at %s: names must be nonempty unique strings", intentSafePath(path))
			}
			seen[name], hasNamed = true, true
		}
	}
	if hasNamed && hasUnnamed {
		return nil, false, fmt.Errorf("ambiguous name list at %s: every member must have a name", intentSafePath(path))
	}
	if hasNamed {
		return []string{"name"}, true, nil
	}
	return nil, false, nil
}

type intentDeclaredMember struct {
	id    string
	value any
	path  fieldpath.PathElement
}

func (m *intentDeclaredMerger) listMembers(input intentDeclaredValue, atom schema.Atom, opaque bool, keys []string, path fieldpath.Path) ([]intentDeclaredMember, error) {
	items, _ := input.value.([]any)
	out := make([]intentDeclaredMember, 0, len(items))
	seen := map[string]bool{}
	for _, item := range items {
		var element fieldpath.PathElement
		if len(keys) == 0 {
			v := value.NewValueInterface(item)
			element.Value = &v
		} else {
			object, ok := item.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("invalid associative member at %s", intentSafePath(path))
			}
			fields := value.FieldList{}
			for _, keyName := range keys {
				keyValue, present := object[keyName]
				if !present && !opaque {
					itemSchema, ok := m.topology.Resolve(atom.List.ElementType)
					if ok && itemSchema.Map != nil {
						if field, ok := itemSchema.Map.FindField(keyName); ok && field.Default != nil {
							keyValue, present = field.Default, true
						}
					}
				}
				if !present || keyValue == nil {
					return nil, fmt.Errorf("missing associative key at %s", intentSafePath(path))
				}
				if opaque {
					if name, ok := keyValue.(string); !ok || name == "" {
						return nil, fmt.Errorf("invalid associative name at %s", intentSafePath(path))
					}
				}
				fields = append(fields, value.Field{Name: keyName, Value: value.NewValueInterface(keyValue)})
			}
			sort.Slice(fields, func(i, j int) bool { return fields[i].Name < fields[j].Name })
			element.Key = &fields
		}
		id, err := fieldpath.SerializePathElement(element)
		if err != nil {
			return nil, fmt.Errorf("cannot encode associative identity at %s", intentSafePath(path))
		}
		if seen[id] {
			return nil, fmt.Errorf("duplicate associative identity at %s", intentSafePath(path))
		}
		seen[id] = true
		out = append(out, intentDeclaredMember{id: id, value: item, path: element})
	}
	return out, nil
}

func (m *intentDeclaredMerger) mergeList(b, l, d, raw intentDeclaredValue, atom schema.Atom, opaque bool, keys []string, path fieldpath.Path) (intentDeclaredValue, error) {
	indexes := [4]map[string]intentDeclaredMember{}
	var ordered [4][]intentDeclaredMember
	order := []intentDeclaredMember{}
	seen := map[string]bool{}
	for i, input := range []intentDeclaredValue{b, l, d, raw} {
		members, err := m.listMembers(input, atom, opaque, keys, path)
		if err != nil {
			return intentDeclaredValue{}, err
		}
		indexes[i] = map[string]intentDeclaredMember{}
		for _, member := range members {
			indexes[i][member.id] = member
		}
		// Preserve live ordering, append new declarations, and visit historical
		// members missing on both sides so their deletion can still be verified.
		ordered[i] = members
	}
	for _, i := range []int{1, 3, 0} {
		for _, member := range ordered[i] {
			if !seen[member.id] {
				seen[member.id] = true
				order = append(order, member)
			}
		}
	}
	out := []any{}
	for _, member := range order {
		inputs := [4]intentDeclaredValue{}
		for i, index := range indexes {
			entry, present := index[member.id]
			inputs[i] = intentDeclaredValue{value: entry.value, present: present}
		}
		child := intentDeclaredType{opaque: opaque}
		if opaque {
			child.ref = opaqueDeclaredRef()
		} else {
			child.ref = atom.List.ElementType
		}
		childPath := appendDeclaredPath(path, member.path)
		merged, err := m.merge(inputs[0], inputs[1], inputs[2], inputs[3], child, childPath, true)
		if err != nil {
			return intentDeclaredValue{}, err
		}
		if merged.present {
			out = append(out, merged.value)
		}
	}
	if raw.present && m.ordersNamedMembers(path) {
		wanted, _ := raw.value.([]any)
		if err := OrderDeclaredNamedMembers(out, wanted); err != nil {
			return intentDeclaredValue{}, fmt.Errorf("%s: %w", safeDeclaredPath(path), err)
		}
	}
	if l.present && m.acceptsNullList(out, l.value, path) {
		return l, nil
	}
	return intentDeclaredValue{value: out, present: true}, nil
}

func (m *intentDeclaredMerger) retain(path fieldpath.Path) {
	m.retained[intentSafePath(path)] = struct{}{}
}

func declaredMapValue(parent intentDeclaredValue, key string) intentDeclaredValue {
	object, _ := parent.value.(map[string]any)
	v, present := object[key]
	return intentDeclaredValue{value: v, present: present}
}

func copyDeclaredValue(input intentDeclaredValue) intentDeclaredValue {
	if input.present {
		input.value = runtime.DeepCopyJSONValue(input.value)
	}
	return input
}

func appendDeclaredPath(path fieldpath.Path, element fieldpath.PathElement) fieldpath.Path {
	return append(append(fieldpath.Path{}, path...), element)
}

func opaqueDeclaredRef() schema.TypeRef {
	name := "__untyped_deduced_"
	return schema.TypeRef{NamedType: &name}
}

func declaredEqual(a, b any, ref schema.TypeRef) bool {
	if ref.NamedType != nil && *ref.NamedType == "io.k8s.apimachinery.pkg.api.resource.Quantity" {
		x, xOK := a.(string)
		y, yOK := b.(string)
		if xOK && yOK {
			qx, ex := resource.ParseQuantity(x)
			qy, ey := resource.ParseQuantity(y)
			if ex == nil && ey == nil {
				return qx.Cmp(qy) == 0
			}
		}
	}
	return value.Equals(value.NewValueInterface(a), value.NewValueInterface(b))
}

// A historical value authorizes deletion only under the original symmetric
// equality rules. Affirmative validation projects only submitted object keys.
func (m *intentDeclaredMerger) equivalent(a, b any, typ intentDeclaredType, path fieldpath.Path) bool {
	return (declaredValueComparison{intentDeclaredMerger: m}).equal(a, b, typ, path)
}

func (m *intentDeclaredMerger) matchesDeclaredValue(wanted, observed any, typ intentDeclaredType, path fieldpath.Path) bool {
	return (declaredValueComparison{intentDeclaredMerger: m, projectDeclaration: true}).equal(wanted, observed, typ, path)
}

type declaredValueComparison struct {
	*intentDeclaredMerger
	projectDeclaration bool
}

func (m declaredValueComparison) equal(a, b any, typ intentDeclaredType, path fieldpath.Path) bool {
	if declaredEqual(a, b, typ.ref) {
		return true
	}
	if m.projectDeclaration && m.acceptsNullList(a, b, path) {
		return true
	}
	if native := declaredNativePathType(m.nativeType, path); native != nil {
		left, leftOK := canonicalDeclaredNativeValue(a, native)
		right, rightOK := canonicalDeclaredNativeValue(b, native)
		if leftOK && rightOK && reflect.DeepEqual(left, right) {
			return true
		}
	}
	atom, opaque, err := m.resolve(typ, path)
	if err != nil {
		return false
	}
	switch left := a.(type) {
	case map[string]any:
		right, ok := b.(map[string]any)
		return ok && (opaque || atom.Map != nil) && m.equalMap(left, right, atom, opaque, path)
	case []any:
		right, ok := b.([]any)
		return ok && m.equalList(left, right, atom, opaque, path)
	default:
		return false
	}
}

func (m declaredValueComparison) equalMap(wanted, observed map[string]any, atom schema.Atom, opaque bool, path fieldpath.Path) bool {
	for key, expected := range wanted {
		child, _ := declaredMapChildType(atom, opaque, key)
		childPath := appendDeclaredPath(path, fieldpath.PathElement{FieldName: &key})
		actual, found := observed[key]
		if !found {
			if m.projectDeclaration && m.nativeFieldOmittable(childPath, expected) {
				continue
			}
			if child.defaultValue == nil {
				return false
			}
			actual = child.defaultValue
		}
		if !m.equal(expected, actual, child, childPath) {
			return false
		}
	}
	if m.projectDeclaration {
		return true
	}
	for key, actual := range observed {
		if _, found := wanted[key]; found {
			continue
		}
		child, _ := declaredMapChildType(atom, opaque, key)
		childPath := appendDeclaredPath(path, fieldpath.PathElement{FieldName: &key})
		if child.defaultValue != nil && m.equal(child.defaultValue, actual, child, childPath) {
			continue
		}
		return false
	}
	return true
}

func (m declaredValueComparison) equalList(al, bl []any, atom schema.Atom, opaque bool, path fieldpath.Path) bool {
	if len(al) != len(bl) {
		return false
	}
	child := intentDeclaredType{opaque: opaque}
	if opaque {
		child.ref = opaqueDeclaredRef()
	} else if atom.List != nil {
		child.ref = atom.List.ElementType
	} else {
		return false
	}
	// An opaque array reached through an atomic value has no schema key.
	// The named-list overlay is applied only where merge visits the list.
	if !opaque && atom.List.ElementRelationship == schema.Associative {
		keys := atom.List.Keys
		left, leftErr := m.listMembers(intentDeclaredValue{value: al, present: true}, atom, opaque, keys, path)
		right, rightErr := m.listMembers(intentDeclaredValue{value: bl, present: true}, atom, opaque, keys, path)
		if leftErr != nil || rightErr != nil {
			return false
		}
		index := map[string]int{}
		for i, member := range right {
			index[member.id] = i
		}
		for _, member := range left {
			i, ok := index[member.id]
			if !ok || !m.equal(member.value, right[i].value, child, appendDeclaredPath(path, member.path)) {
				return false
			}
		}
		return true
	}
	for i := range al {
		if !m.equal(al[i], bl[i], child, appendDeclaredPath(path, fieldpath.PathElement{Index: &i})) {
			return false
		}
	}
	return true
}

// Unlike overlap, this excludes protected descendants: deleting a granular map
// can still remove unprotected siblings, while an atomic ancestor protects all
// application-level named members below it.
func declaredProtectedAncestor(set *fieldpath.Set, path fieldpath.Path) bool {
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
	return false
}
