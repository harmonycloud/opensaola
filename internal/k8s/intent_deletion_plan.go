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
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/managedfields"
	"sigs.k8s.io/structured-merge-diff/v6/fieldpath"
	"sigs.k8s.io/structured-merge-diff/v6/schema"
)

type DeclaredIntentMergeResult struct {
	Object            *unstructured.Unstructured
	assertions        []declaredFieldAssertion
	namedEmptyLists   [][]string
	PreservedExternal []string
	BlockedDeletions  []DeclaredDeletionConflict
	DeletionChecks    []DeclaredDeletionCheck
}

// Public diagnostics contain only redacted paths and fixed reasons. Actual
// associative keys remain private and must never appear in Events or logs.
type DeclaredDeletionConflict struct {
	Path   string
	Reason string
}

type DeclaredDeletionCheck struct {
	Path                 string
	path                 fieldpath.Path
	allowExternalPayload bool
}

func (p *DeclaredIntentMergeResult) DeletionConflictError() error {
	if p == nil {
		return fmt.Errorf("resource deletion plan is missing")
	}
	if len(p.BlockedDeletions) == 0 {
		return nil
	}
	parts := []string{}
	for _, conflict := range p.BlockedDeletions {
		if len(parts) == 8 {
			break
		}
		parts = append(parts, conflict.Path+": "+conflict.Reason)
	}
	return fmt.Errorf("resource intent deletion blocked: %s", strings.Join(parts, "; "))
}

func (m *intentDeclaredMerger) blockDeletion(path fieldpath.Path, reason string) {
	m.blocked = append(m.blocked, DeclaredDeletionConflict{Path: safeDeclaredPath(path), Reason: reason})
}

func (m *intentDeclaredMerger) checkDeletion(path fieldpath.Path) {
	m.checks = append(m.checks, DeclaredDeletionCheck{Path: safeDeclaredPath(path), path: append(fieldpath.Path{}, path...)})
}

func (m *intentDeclaredMerger) checkMemberDeletion(path fieldpath.Path) {
	m.checks = append(m.checks, DeclaredDeletionCheck{Path: safeDeclaredPath(path), path: append(fieldpath.Path{}, path...), allowExternalPayload: true})
}

// A deletion remains an obligation when the field is already absent. This is
// essential when retrying an unconfirmed write: admission must not resurrect a
// historical field merely because the previous attempt already removed it.
// Granular parents are traversed so unrelated future children remain allowed.
func (m *intentDeclaredMerger) checkAbsentDeletion(before intentDeclaredValue, typ intentDeclaredType, path fieldpath.Path, member bool) error {
	atom, opaque, err := m.resolve(typ, path)
	if err != nil {
		return err
	}
	if member {
		if len(path) > 0 && path[len(path)-1].Key != nil && (opaque || atom.Map != nil && atom.Map.ElementRelationship != schema.Atomic) {
			previous, ok := before.value.(map[string]any)
			if !ok {
				return fmt.Errorf("historical associative member has an invalid shape at %s", safeDeclaredPath(path))
			}
			payload := make(map[string]any, len(previous))
			for key, val := range previous {
				payload[key] = val
			}
			for _, key := range *path[len(path)-1].Key {
				delete(payload, key.Name)
			}
			if len(payload) > 0 {
				if err := m.checkAbsentDeletion(intentDeclaredValue{value: payload, present: true}, typ, path, false); err != nil {
					return err
				}
			}
			m.checkMemberDeletion(path)
		} else {
			m.checkDeletion(path)
		}
		return nil
	}
	switch previous := before.value.(type) {
	case map[string]any:
		if len(previous) > 0 && (opaque || atom.Map != nil && atom.Map.ElementRelationship != schema.Atomic) {
			keys := make([]string, 0, len(previous))
			for key := range previous {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				child, childErr := declaredMapChildType(atom, opaque, key)
				if childErr != nil {
					return childErr
				}
				childPath := appendDeclaredPath(path, fieldpath.PathElement{FieldName: &key})
				if err := m.checkAbsentDeletion(intentDeclaredValue{value: previous[key], present: true}, child, childPath, false); err != nil {
					return err
				}
			}
			return nil
		}
	case []any:
		keys, associative, listErr := declaredListKeys(atom, opaque, path, before)
		if listErr != nil {
			return listErr
		}
		if associative && len(previous) > 0 {
			members, listErr := m.listMembers(before, atom, opaque, keys, path)
			if listErr != nil {
				return listErr
			}
			child := intentDeclaredType{opaque: opaque}
			if opaque {
				child.ref = opaqueDeclaredRef()
			} else {
				child.ref = atom.List.ElementType
			}
			for _, member := range members {
				if err := m.checkAbsentDeletion(intentDeclaredValue{value: member.value, present: true}, child, appendDeclaredPath(path, member.path), true); err != nil {
					return err
				}
			}
			return nil
		}
	}
	m.checkDeletion(path)
	return nil
}

func compactDeclaredDeletionChecks(checks []DeclaredDeletionCheck) []DeclaredDeletionCheck {
	sort.Slice(checks, func(i, j int) bool { return checks[i].path.Compare(checks[j].path) < 0 })
	result := checks[:0]
	for _, check := range checks {
		if len(result) > 0 {
			previous := result[len(result)-1].path
			if !result[len(result)-1].allowExternalPayload && len(previous) <= len(check.path) && previous.Equals(check.path[:len(previous)]) {
				continue
			}
		}
		result = append(result, check)
	}
	return result
}

func safeDeclaredPath(path fieldpath.Path) string {
	if len(path) == 0 {
		return "<root>"
	}
	return intentSafePath(path)
}

// ValidateResourceIntentDeletions validates only paths this plan actually
// removed, allowing unrelated concurrent/defaulted fields in the API response.
// An omitted native zero value or real schema default can be semantically
// absent. The same assumption is never made for a schema-free CR field.
func ValidateResourceIntentDeletions(converter managedfields.TypeConverter, plan *DeclaredIntentMergeResult, actual *unstructured.Unstructured) error {
	if plan == nil {
		return fmt.Errorf("resource deletion plan is missing")
	}
	deletionPlan := *plan
	deletionPlan.assertions = nil
	return deletionPlan.Validate(converter, actual)
}

func (plan *DeclaredIntentMergeResult) validateDeletions(lookup *declaredDeletionLookup, scoped *unstructured.Unstructured, root intentDeclaredType) error {
	merger := lookup.merger
	failed := []string{}
	for _, check := range plan.DeletionChecks {
		found, typ, err := lookup.find(scoped.Object, root, check.path)
		if err != nil {
			return err
		}
		if !found.present {
			continue
		}
		if check.allowExternalPayload {
			// Identity may survive only to hold external payload. Independently
			// retained descendant checks still reject every resurrected owned
			// field; never collapse them into this conditional membership check.
			if object, ok := found.value.(map[string]any); ok && len(check.path) > 0 {
				identity := check.path[len(check.path)-1].Key
				if identity != nil {
					hasExternalPayload := false
					for key := range object {
						isIdentity := false
						for _, field := range *identity {
							if field.Name == key {
								isIdentity = true
								break
							}
						}
						if !isIdentity {
							hasExternalPayload = true
							break
						}
					}
					if hasExternalPayload {
						continue
					}
				}
			}
			if len(failed) < 8 {
				failed = append(failed, check.Path)
			}
			continue
		}
		if merger.acceptedDefault(found.value, typ, check.path) || lookup.nativeDefaultAt(scoped.Object, root, check.path, found.value) {
			continue
		}
		if merger.nativeFieldOmittable(check.path, found.value) {
			continue
		}
		if len(failed) < 8 {
			failed = append(failed, check.Path)
		}
	}
	if len(failed) != 0 {
		return fmt.Errorf("resource did not fulfill intended deletions at %s", strings.Join(failed, ", "))
	}
	return nil
}

type declaredDeletionLookup struct {
	merger *intentDeclaredMerger
	// Logical paths include private key values only in memory, never diagnostics.
	lists map[string]map[string]intentDeclaredMember
}

func (l *declaredDeletionLookup) indexList(current any, atom schema.Atom, opaque bool, keys []string, path fieldpath.Path) (map[string]intentDeclaredMember, error) {
	key, err := fieldCacheKey(path)
	if err != nil {
		return nil, err
	}
	if index, ok := l.lists[key]; ok {
		return index, nil
	}
	members, err := l.merger.listMembers(intentDeclaredValue{value: current, present: true}, atom, opaque, keys, path)
	if err != nil {
		return nil, err
	}
	index := make(map[string]intentDeclaredMember, len(members))
	for _, member := range members {
		index[member.id] = member
	}
	l.lists[key] = index
	return index, nil
}

func (l *declaredDeletionLookup) find(root any, rootType intentDeclaredType, path fieldpath.Path) (intentDeclaredValue, intentDeclaredType, error) {
	current, typ := root, rootType
	for i, element := range path {
		atom, opaque, err := l.merger.resolve(typ, path[:i])
		if err != nil {
			return intentDeclaredValue{}, typ, err
		}
		switch {
		case element.FieldName != nil:
			if current == nil {
				return intentDeclaredValue{}, typ, nil
			}
			object, ok := current.(map[string]any)
			if !ok {
				return intentDeclaredValue{}, typ, fmt.Errorf("accepted deletion path has an invalid object shape at %s", safeDeclaredPath(path[:i]))
			}
			key := *element.FieldName
			typ, err = declaredMapChildType(atom, opaque, key)
			if err != nil {
				return intentDeclaredValue{}, typ, err
			}
			var exists bool
			current, exists = object[key]
			if !exists {
				return intentDeclaredValue{}, typ, nil
			}
		case element.Key != nil || element.Value != nil:
			if current == nil {
				return intentDeclaredValue{}, typ, nil
			}
			if _, ok := current.([]any); !ok {
				return intentDeclaredValue{}, typ, fmt.Errorf("accepted deletion path has an invalid list shape at %s", safeDeclaredPath(path[:i]))
			}
			var keys []string
			if opaque {
				if element.Key == nil {
					return intentDeclaredValue{}, typ, fmt.Errorf("unsupported opaque deletion membership at %s", safeDeclaredPath(path[:i]))
				}
				for _, key := range *element.Key {
					keys = append(keys, key.Name)
				}
			} else if atom.List != nil && atom.List.ElementRelationship == schema.Associative {
				keys = atom.List.Keys
			} else {
				return intentDeclaredValue{}, typ, fmt.Errorf("accepted deletion path no longer has associative topology at %s", safeDeclaredPath(path[:i]))
			}
			index, err := l.indexList(current, atom, opaque, keys, path[:i])
			if err != nil {
				return intentDeclaredValue{}, typ, err
			}
			id, err := fieldpath.SerializePathElement(element)
			if err != nil {
				return intentDeclaredValue{}, typ, err
			}
			member, exists := index[id]
			if !exists {
				return intentDeclaredValue{}, typ, nil
			}
			current = member.value
			typ = intentDeclaredType{opaque: opaque}
			if opaque {
				typ.ref = opaqueDeclaredRef()
			} else {
				typ.ref = atom.List.ElementType
			}
		default:
			return intentDeclaredValue{}, typ, fmt.Errorf("unsupported deletion path at %s", safeDeclaredPath(path[:i]))
		}
	}
	return intentDeclaredValue{value: current, present: true}, typ, nil
}

func declaredMapChildType(atom schema.Atom, opaque bool, key string) (intentDeclaredType, error) {
	if opaque {
		return intentDeclaredType{ref: opaqueDeclaredRef(), opaque: true}, nil
	}
	if atom.Map == nil {
		return intentDeclaredType{}, fmt.Errorf("deletion path requires an object schema")
	}
	if field, found := atom.Map.FindField(key); found {
		return intentDeclaredType{ref: field.Type, defaultValue: field.Default}, nil
	}
	return intentDeclaredType{ref: atom.Map.ElementType}, nil
}

// PlanLegacyResourceIntentDeletions uses only the old SMD removal paths. It
// never derives name-level deletion authority inside a legacy atomic array.
// Canonical native/default representations are compared semantically, while
// genuine external edits and external Apply protection block the transaction.
func PlanLegacyResourceIntentDeletions(converter managedfields.TypeConverter, before, live, desired *unstructured.Unstructured, protected *fieldpath.Set) (*DeclaredIntentMergeResult, error) {
	if converter == nil || live == nil || desired == nil {
		return nil, fmt.Errorf("legacy deletion planning requires schema and resource inputs")
	}
	if before == nil {
		return &DeclaredIntentMergeResult{Object: live.DeepCopy()}, nil
	}
	for _, object := range []*unstructured.Unstructured{before, desired} {
		if object.GroupVersionKind() != live.GroupVersionKind() || object.GetName() != live.GetName() || object.GetNamespace() != live.GetNamespace() {
			return nil, fmt.Errorf("legacy deletion planning requires matching resource identities")
		}
	}
	b, err := converter.ObjectToTyped(intentMergeScope(before))
	if err != nil {
		return nil, fmt.Errorf("previous resource schema validation failed during legacy deletion planning")
	}
	l, err := converter.ObjectToTyped(intentMergeScope(live))
	if err != nil {
		return nil, fmt.Errorf("live resource schema validation failed during legacy deletion planning")
	}
	d, err := converter.ObjectToTyped(intentMergeScope(desired))
	if err != nil {
		return nil, fmt.Errorf("desired resource schema validation failed during legacy deletion planning")
	}
	change, err := b.Compare(d)
	if err != nil {
		return nil, fmt.Errorf("cannot compare legacy deletion intent")
	}
	drift, err := b.Compare(l)
	if err != nil {
		return nil, fmt.Errorf("cannot compare legacy live resource")
	}
	merger := intentDeclaredMerger{topology: b.Schema(), root: b.TypeRef(), nativeType: declaredNativeType(live.GroupVersionKind()), retained: map[string]struct{}{}, protected: protected}
	beforeLookup := declaredDeletionLookup{merger: &merger, lists: map[string]map[string]intentDeclaredMember{}}
	liveLookup := declaredDeletionLookup{merger: &merger, lists: map[string]map[string]intentDeclaredMember{}}
	rootType := intentDeclaredType{ref: b.TypeRef()}
	beforeScope, liveScope := intentMergeScope(before), intentMergeScope(live)
	type removedTarget struct {
		path         fieldpath.Path
		before, live intentDeclaredValue
		typ          intentDeclaredType
	}
	var targets []removedTarget
	var lookupErr error
	change.Removed.Iterate(func(path fieldpath.Path) {
		if lookupErr != nil {
			return
		}
		previous, typ, err := beforeLookup.find(beforeScope.Object, rootType, path)
		if err != nil {
			lookupErr = err
			return
		}
		actual, _, err := liveLookup.find(liveScope.Object, rootType, path)
		if err != nil {
			lookupErr = err
			return
		}
		targets = append(targets, removedTarget{path: append(fieldpath.Path{}, path...), before: previous, live: actual, typ: typ})
	})
	if lookupErr != nil {
		return nil, lookupErr
	}
	externalChanges := drift.Added.Union(drift.Modified).Union(drift.Removed)
	blockedItems := fieldpath.NewSet()
	for _, target := range targets {
		path := target.path
		if len(path) == 0 || path[len(path)-1].Key == nil {
			continue
		}
		if target.live.present && (merger.deletionProtected(path) || intentPathsOverlap(externalChanges, path) && !merger.equivalent(target.before.value, target.live.value, target.typ, path)) {
			blockedItems.Insert(path)
		}
	}
	remove := fieldpath.NewSet()
	for _, target := range targets {
		path := target.path
		blocked := target.live.present && (intentPathsOverlap(blockedItems, path) || merger.deletionProtected(path) ||
			intentPathsOverlap(externalChanges, path) && !merger.equivalent(target.before.value, target.live.value, target.typ, path))
		if !blocked {
			if target.live.present {
				remove.Insert(path)
			}
			atom, opaque, resolveErr := merger.resolve(target.typ, path)
			if resolveErr != nil {
				return nil, resolveErr
			}
			membershipOnly := false
			if len(path) > 0 && path[len(path)-1].FieldName != nil {
				if previous, ok := target.before.value.(map[string]any); ok && len(previous) > 0 && (opaque || atom.Map != nil && atom.Map.ElementRelationship != schema.Atomic) {
					membershipOnly = true
				}
				if previous, ok := target.before.value.([]any); ok && len(previous) > 0 && !opaque && atom.List != nil && atom.List.ElementRelationship == schema.Associative {
					membershipOnly = true
				}
			}
			if !membershipOnly {
				merger.checkDeletion(path)
			}
			continue
		}
		atom, opaque, resolveErr := merger.resolve(target.typ, path)
		if resolveErr != nil {
			return nil, resolveErr
		}
		_, liveMap := target.live.value.(map[string]any)
		_, beforeMap := target.before.value.(map[string]any)
		// A granular map's membership marker may survive solely to contain an
		// external child. Its independently removed historical children still
		// receive their own checks/conflicts. Keyed-member markers are excluded.
		if beforeMap && liveMap && (opaque || atom.Map != nil && atom.Map.ElementRelationship != schema.Atomic) &&
			len(path) > 0 && path[len(path)-1].FieldName != nil && !declaredProtectedAncestor(protected, path) {
			merger.retain(path)
			continue
		}
		reason := "historically declared value was changed externally under legacy deletion policy"
		if merger.deletionProtected(path) {
			reason = "field is protected by another Apply manager"
		}
		merger.blockDeletion(path, reason)
	}
	if lookupErr != nil {
		return nil, lookupErr
	}
	object, err := converter.TypedToObject(l.RemoveItems(remove))
	if err != nil {
		return nil, fmt.Errorf("cannot decode legacy deletion result")
	}
	result, ok := object.(*unstructured.Unstructured)
	if !ok {
		return nil, fmt.Errorf("schema converter returned unsupported legacy deletion result")
	}
	result = result.DeepCopy()
	restoreIntentSystemFields(result, live)
	preserved := []string{}
	for path := range merger.retained {
		preserved = append(preserved, path)
	}
	sort.Strings(preserved)
	return &DeclaredIntentMergeResult{Object: result, PreservedExternal: preserved, BlockedDeletions: merger.blocked, DeletionChecks: compactDeclaredDeletionChecks(merger.checks)}, nil
}
