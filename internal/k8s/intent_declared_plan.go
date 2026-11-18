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
	"strconv"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/managedfields"
	kjson "sigs.k8s.io/json"
	"sigs.k8s.io/structured-merge-diff/v6/fieldpath"
	"sigs.k8s.io/structured-merge-diff/v6/schema"
	"sigs.k8s.io/structured-merge-diff/v6/value"
)

// A granular assertion checks container shape, then separate assertions verify
// its declared children. Atomic nodes, scalar fields and explicit nulls have a
// single value assertion. Every assertion is produced when its write is planned.
type assertionKind int

const (
	assertValue assertionKind = iota
	assertObject
	assertList
)

type declaredFieldAssertion struct {
	path   fieldpath.Path
	typ    intentDeclaredType
	wanted any
	kind   assertionKind
}

func (m *intentDeclaredMerger) assert(path fieldpath.Path, typ intentDeclaredType, wanted any, kind assertionKind) {
	m.assertions = append(m.assertions, declaredFieldAssertion{path: path, typ: typ, wanted: wanted, kind: kind})
}

type StoredDeletionCheck struct {
	Segments             []string `json:"segments"`
	AllowExternalPayload bool     `json:"allowExternalPayload,omitempty"`
}

func encodeFieldSegments(path fieldpath.Path) ([]string, error) {
	segments := make([]string, len(path))
	for i, pe := range path {
		encoded, err := fieldpath.SerializePathElement(pe)
		if err != nil {
			return nil, fmt.Errorf("invalid field plan path element")
		}
		segments[i] = encoded
	}
	return segments, nil
}

// SMD's path parser decodes JSON numbers as float64. Keep integer key and
// set identities exact across restart, including values above 2^53.
func decodeFieldElement(segment string) (fieldpath.PathElement, error) {
	if strings.HasPrefix(segment, "f:") {
		name := segment[2:]
		return fieldpath.PathElement{FieldName: &name}, nil
	}
	if strings.HasPrefix(segment, "v:") || strings.HasPrefix(segment, "k:") {
		var item any
		if strict, err := kjson.UnmarshalStrict([]byte(segment[2:]), &item); err == nil && len(strict) == 0 {
			if segment[0] == 'v' {
				v := value.NewValueInterface(item)
				return fieldpath.PathElement{Value: &v}, nil
			}
			if keys, ok := item.(map[string]any); ok && len(keys) > 0 {
				fields := make(value.FieldList, 0, len(keys))
				for name, key := range keys {
					fields = append(fields, value.Field{Name: name, Value: value.NewValueInterface(key)})
				}
				fields.Sort()
				return fieldpath.PathElement{Key: &fields}, nil
			}
		}
	}
	return fieldpath.PathElement{}, fmt.Errorf("invalid stored field plan path element")
}

func decodeFieldSegments(segments []string) (fieldpath.Path, error) {
	if len(segments) == 0 {
		return nil, fmt.Errorf("empty stored field plan path")
	}
	path := make(fieldpath.Path, len(segments))
	for i, s := range segments {
		pe, err := decodeFieldElement(s)
		if err != nil {
			return nil, err
		}
		path[i] = pe
	}
	return path, nil
}

func fieldCacheKey(path fieldpath.Path) (string, error) {
	var key strings.Builder
	for _, pe := range path {
		var segment string
		if pe.FieldName != nil {
			segment = "f:" + *pe.FieldName
		} else {
			var err error
			segment, err = fieldpath.SerializePathElement(pe)
			if err != nil {
				return "", fmt.Errorf("invalid field plan path element")
			}
		}
		key.WriteString(strconv.Itoa(len(segment)))
		key.WriteByte(':')
		key.WriteString(segment)
	}
	return key.String(), nil
}

func StoreDeletionChecks(checks []DeclaredDeletionCheck) ([]StoredDeletionCheck, error) {
	result := make([]StoredDeletionCheck, 0, len(checks))
	for _, check := range checks {
		segments, err := encodeFieldSegments(check.path)
		if err != nil {
			return nil, err
		}
		result = append(result, StoredDeletionCheck{Segments: segments, AllowExternalPayload: check.allowExternalPayload})
	}
	return result, nil
}

func RestoreDeletionChecks(stored []StoredDeletionCheck) ([]DeclaredDeletionCheck, error) {
	checks := make([]DeclaredDeletionCheck, 0, len(stored))
	for _, s := range stored {
		path, err := decodeFieldSegments(s.Segments)
		if err != nil {
			return nil, err
		}
		if s.AllowExternalPayload && path[len(path)-1].Key == nil {
			return nil, fmt.Errorf("stored member assertion lacks identity keys")
		}
		checks = append(checks, DeclaredDeletionCheck{Path: safeDeclaredPath(path), path: path, allowExternalPayload: s.AllowExternalPayload})
	}
	return checks, nil
}

func (p *DeclaredIntentMergeResult) NamedEmptyLists() [][]string { return p.namedEmptyLists }

// Recompile the affirmative declaration from the immutable snapshot. Only
// opaque empty lists need an extra bit of topology from the original plan.
// No live resource participates in this reconstruction after a restart.
func NewDeclaredValidationPlan(converter managedfields.TypeConverter, desired *unstructured.Unstructured, checks []DeclaredDeletionCheck, namedPaths ...[]string) (*DeclaredIntentMergeResult, error) {
	if converter == nil || desired == nil {
		return nil, fmt.Errorf("field verification requires schema and declaration")
	}
	scoped := intentMergeScope(desired)
	parsed, err := converter.ObjectToTyped(scoped)
	if err != nil {
		return nil, fmt.Errorf("declared resource schema validation failed")
	}
	named := fieldpath.NewSet()
	for _, segments := range namedPaths {
		path, err := decodeFieldSegments(segments)
		if err != nil {
			return nil, err
		}
		named.Insert(path)
	}
	merger := intentDeclaredMerger{topology: parsed.Schema(), root: parsed.TypeRef(), nativeType: declaredNativeType(desired.GroupVersionKind()), forceNamedEmpty: named, retained: map[string]struct{}{}}
	input := intentDeclaredValue{value: scoped.Object, present: true}
	if _, err := merger.merge(intentDeclaredValue{}, intentDeclaredValue{}, input, input, intentDeclaredType{ref: parsed.TypeRef()}, nil, false); err != nil {
		return nil, err
	}
	if len(namedPaths) != len(merger.namedEmptyLists) {
		return nil, fmt.Errorf("stored named-list topology disagrees with declaration")
	}
	return &DeclaredIntentMergeResult{Object: desired.DeepCopy(), assertions: merger.assertions, DeletionChecks: checks, namedEmptyLists: merger.namedEmptyLists}, nil
}

// Validate uses only the checks from this plan. It never replans an observed
// object, so dry-run, real response and recovery have identical obligations.
func (p *DeclaredIntentMergeResult) Validate(converter managedfields.TypeConverter, actual *unstructured.Unstructured) error {
	if err := p.DeletionConflictError(); err != nil {
		return err
	}
	if converter == nil || actual == nil || p.Object == nil || actual.GroupVersionKind() != p.Object.GroupVersionKind() || actual.GetName() != p.Object.GetName() || actual.GetNamespace() != p.Object.GetNamespace() || p.Object.GetUID() != "" && actual.GetUID() != p.Object.GetUID() {
		return fmt.Errorf("field verification requires matching resource identities")
	}
	scope := intentMergeScope(actual)
	parsed, err := converter.ObjectToTyped(scope)
	if err != nil {
		return fmt.Errorf("accepted resource schema validation failed")
	}
	root := intentDeclaredType{ref: parsed.TypeRef()}
	merger := intentDeclaredMerger{topology: parsed.Schema(), root: root.ref, nativeType: declaredNativeType(actual.GroupVersionKind())}
	lookup := declaredDeletionLookup{merger: &merger, lists: map[string]map[string]intentDeclaredMember{}}
	for _, a := range p.assertions {
		v, _, err := lookup.find(scope.Object, root, a.path)
		if err != nil {
			return err
		}
		if !a.satisfied(&merger, v) {
			return fmt.Errorf("resource did not converge to its declared fields at %s", safeDeclaredPath(a.path))
		}
		if a.kind == assertList && v.present {
			atom, opaque, err := merger.resolve(a.typ, a.path)
			if err != nil {
				return err
			}
			keys := []string{"name"}
			if !opaque {
				if atom.List == nil || atom.List.ElementRelationship != schema.Associative {
					return fmt.Errorf("declared list topology changed at %s", safeDeclaredPath(a.path))
				}
				keys = atom.List.Keys
			}
			if _, err := lookup.indexList(v.value, atom, opaque, keys, a.path); err != nil {
				return err
			}
		}
	}
	return p.validateDeletions(&lookup, scope, root)
}

func (a declaredFieldAssertion) satisfied(m *intentDeclaredMerger, v intentDeclaredValue) bool {
	if !v.present {
		if m.acceptedDefault(a.wanted, a.typ, a.path) {
			return true
		}
		return m.nativeFieldOmittable(a.path, a.wanted)
	}
	switch a.kind {
	case assertObject:
		_, ok := v.value.(map[string]any)
		return ok
	case assertList:
		actual, ok := v.value.([]any)
		if !ok || !m.ordersInitContainers(a.path) {
			return ok
		}
		// Project the API result onto the declared members. Admission can add
		// independent init containers but cannot reverse two declared steps.
		wanted, ok := a.wanted.([]any)
		if !ok {
			return false
		}
		next := 0
		for _, item := range actual {
			if next == len(wanted) {
				break
			}
			current, ok := item.(map[string]any)
			expected, valid := wanted[next].(map[string]any)
			if ok && valid && current["name"] == expected["name"] {
				next++
			}
		}
		return next == len(wanted)
	default:
		return m.matchesDeclaredValue(a.wanted, v.value, a.typ, a.path)
	}
}

func (m *intentDeclaredMerger) nativeFieldOmittable(path fieldpath.Path, val any) bool {
	if m.nativeType == nil || len(path) == 0 || path[len(path)-1].FieldName == nil {
		return false
	}
	parent := declaredNativePathType(m.nativeType, path[:len(path)-1])
	if parent == nil {
		return false
	}
	a, aOK := canonicalDeclaredNativeValue(map[string]any{*path[len(path)-1].FieldName: val}, parent)
	b, bOK := canonicalDeclaredNativeValue(map[string]any{}, parent)
	return aOK && bOK && reflect.DeepEqual(a, b)
}

// Workload replica defaults are defined by the built-in versioned API
// defaulters; unlike CRD defaults these do not appear in OpenAPI. Match the
// exact native spec type to avoid accepting a similarly named custom field.
func (m *intentDeclaredMerger) acceptedDefault(v any, typ intentDeclaredType, path fieldpath.Path) bool {
	if typ.defaultValue != nil && m.equivalent(v, typ.defaultValue, typ, path) {
		return true
	}
	if m.nativeType == nil || len(path) != 2 || path[0].FieldName == nil || *path[0].FieldName != "spec" || path[1].FieldName == nil || *path[1].FieldName != "replicas" {
		return false
	}
	switch m.nativeType {
	case reflect.TypeFor[*appsv1.Deployment](), reflect.TypeFor[*appsv1.StatefulSet](), reflect.TypeFor[*appsv1.ReplicaSet](), reflect.TypeFor[*corev1.ReplicationController]():
		return declaredEqual(v, int64(1), typ.ref)
	default:
		return false
	}
}

// A granular ancestor's SSA membership marker (".") protects its existence,
// whereas ownership of an atomic ancestor covers the whole subtree.
func (m *intentDeclaredMerger) deletionProtected(path fieldpath.Path) bool {
	set := m.protected
	typ := intentDeclaredType{ref: m.root}
	for i, pe := range path {
		if set == nil {
			return false
		}
		atom, opaque, err := m.resolve(typ, path[:i])
		if err != nil {
			return true
		}
		switch {
		case pe.FieldName != nil:
			typ, err = declaredMapChildType(atom, opaque, *pe.FieldName)
		case opaque:
			typ = intentDeclaredType{opaque: true, ref: opaqueDeclaredRef()}
		case atom.List != nil:
			typ = intentDeclaredType{ref: atom.List.ElementType}
		default:
			return true
		}
		if err != nil {
			return true
		}
		if set.Members.Has(pe) {
			if i == len(path)-1 {
				return true
			}
			next, isOpaque, err := m.resolve(typ, path[:i+1])
			if err != nil {
				return true
			}
			if path[i+1].FieldName == nil && (isOpaque || next.List == nil || next.List.ElementRelationship == schema.Atomic) {
				return true
			}
			if path[i+1].FieldName != nil && !isOpaque && (next.Map == nil || next.Map.ElementRelationship == schema.Atomic) {
				return true
			}
		}
		set, _ = set.Children.Get(pe)
	}
	return set != nil && !set.Empty()
}
