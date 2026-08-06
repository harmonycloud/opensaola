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

package middleware

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	jsonpatch "github.com/evanphx/json-patch/v5"
	v1 "github.com/harmonycloud/opensaola/api/v1"
	"github.com/harmonycloud/opensaola/internal/k8s"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ReconcilePauseSnapshotMaxBytes bounds the durable status payload. A pause
// snapshot is deliberately short lived, but it still shares the Kubernetes
// object size limit with the rest of Middleware status.
const ReconcilePauseSnapshotMaxBytes = 256 * 1024

var (
	ErrReconcilePauseSnapshotInvalid  = errors.New("reconcile pause snapshot is invalid")
	ErrReconcilePauseResourceMissing  = errors.New("paused primary custom resource is missing")
	ErrReconcilePauseResourceChanged  = errors.New("paused primary custom resource identity changed")
	ErrReconcileOverrideTargetChanged = errors.New("reconcile override target changed")
	ErrReconcilePauseLiveSpecChanged  = errors.New("paused primary custom resource spec changed after merge")
)

// ReconcilePauseMergeConflictError reports JSON Pointer paths changed both by
// an external writer during the pause and by current desired state since the
// pause began.
type ReconcilePauseMergeConflictError struct {
	Paths []string
}

func (e *ReconcilePauseMergeConflictError) Error() string {
	return fmt.Sprintf("reconcile pause merge conflict at %s", strings.Join(e.Paths, ", "))
}

// PausedPrimaryCustomResourceAdoption is the output of one B/L/D merge. The
// live spec hash is kept with the pause session until the resumed SSA write
// succeeds, closing the gap between calculating L and applying the result.
type PausedPrimaryCustomResourceAdoption struct {
	Overrides    *v1.ReconcileOverrides
	LiveSpecHash string
}

// BuildReconcilePauseSnapshot renders the effective primary CR desired state
// and records enough immutable identity to fail closed if the actual resource
// is deleted and recreated while reconciliation is paused.
func BuildReconcilePauseSnapshot(ctx context.Context, cli client.Client, m *v1.Middleware) (*v1.ReconcilePauseStatus, error) {
	cr, err := RenderPrimaryCustomResource(ctx, cli, m)
	if err != nil {
		return nil, fmt.Errorf("render primary custom resource: %w", err)
	}
	live, err := k8s.GetCustomResource(ctx, cli, cr.GetName(), cr.GetNamespace(), cr.GroupVersionKind())
	if err != nil {
		if isNotFound(err) {
			return nil, ErrReconcilePauseResourceMissing
		}
		return nil, fmt.Errorf("get primary custom resource: %w", err)
	}
	spec, raw, err := primarySpec(cr)
	if err != nil {
		return nil, err
	}
	if len(raw) > ReconcilePauseSnapshotMaxBytes {
		return nil, fmt.Errorf("reconcile pause snapshot is %d bytes, limit is %d", len(raw), ReconcilePauseSnapshotMaxBytes)
	}
	if spec == nil {
		return nil, fmt.Errorf("%w: rendered primary custom resource spec is empty", ErrReconcilePauseSnapshotInvalid)
	}
	gvk := cr.GroupVersionKind()
	return &v1.ReconcilePauseStatus{
		DesiredSpec: runtime.RawExtension{Raw: raw},
		GVK: v1.GVK{
			Group:   gvk.Group,
			Version: gvk.Version,
			Kind:    gvk.Kind,
		},
		Namespace:   cr.GetNamespace(),
		Name:        cr.GetName(),
		ResourceUID: string(live.GetUID()),
		Generation:  m.Generation,
		Hash:        specHash(raw),
		CapturedAt:  metav1.Now(),
	}, nil
}

// AdoptPausedPrimaryCustomResource compares the pause-time desired spec (B),
// the actual CR immediately before resume (L), and the current desired spec
// (D). It returns a complete merge patch relative to the current base render,
// so previous overrides remain effective instead of being accidentally lost.
func AdoptPausedPrimaryCustomResource(ctx context.Context, cli client.Client, m *v1.Middleware, snapshot *v1.ReconcilePauseStatus) (*PausedPrimaryCustomResourceAdoption, error) {
	if snapshot == nil {
		return nil, fmt.Errorf("%w: no snapshot", ErrReconcilePauseSnapshotInvalid)
	}
	base, err := decodeSpec(snapshot.DesiredSpec.Raw)
	if err != nil {
		return nil, fmt.Errorf("%w: decode desired spec: %v", ErrReconcilePauseSnapshotInvalid, err)
	}
	if err := verifySnapshotHash(snapshot, base); err != nil {
		return nil, err
	}

	current, err := RenderPrimaryCustomResource(ctx, cli, m)
	if err != nil {
		return nil, fmt.Errorf("render current primary custom resource: %w", err)
	}
	if err := matchesSnapshotTarget(snapshot, current); err != nil {
		return nil, err
	}
	live, err := k8s.GetCustomResource(ctx, cli, current.GetName(), current.GetNamespace(), current.GroupVersionKind())
	if err != nil {
		if isNotFound(err) {
			return nil, ErrReconcilePauseResourceMissing
		}
		return nil, fmt.Errorf("get current primary custom resource: %w", err)
	}
	if snapshot.ResourceUID == "" || snapshot.ResourceUID != string(live.GetUID()) {
		return nil, ErrReconcilePauseResourceChanged
	}
	_, currentRaw, err := primarySpec(current)
	if err != nil {
		return nil, err
	}
	desired, err := decodeSpec(currentRaw)
	if err != nil {
		return nil, fmt.Errorf("decode current desired spec: %w", err)
	}
	actual, actualRaw, err := primarySpec(live)
	if err != nil {
		return nil, fmt.Errorf("read actual primary custom resource spec: %w", err)
	}
	if len(actualRaw) > ReconcilePauseSnapshotMaxBytes {
		return nil, fmt.Errorf("actual primary custom resource spec is %d bytes, limit is %d", len(actualRaw), ReconcilePauseSnapshotMaxBytes)
	}

	merged, conflicts, err := mergePausedSpec(base, actual, desired)
	if err != nil {
		return nil, err
	}
	if len(conflicts) > 0 {
		return nil, &ReconcilePauseMergeConflictError{Paths: conflicts}
	}

	withoutOverrides, err := RenderPrimaryCustomResourceWithoutOverrides(ctx, cli, m)
	if err != nil {
		return nil, fmt.Errorf("render primary custom resource without overrides: %w", err)
	}
	if err := matchesSnapshotTarget(snapshot, withoutOverrides); err != nil {
		return nil, err
	}
	_, renderRaw, err := primarySpec(withoutOverrides)
	if err != nil {
		return nil, err
	}
	mergedRaw, err := json.Marshal(merged)
	if err != nil {
		return nil, fmt.Errorf("marshal merged primary custom resource spec: %w", err)
	}
	patch, err := jsonpatch.CreateMergePatch(renderRaw, mergedRaw)
	if err != nil {
		return nil, fmt.Errorf("create reconcile override patch: %w", err)
	}
	if string(patch) == "{}" {
		return &PausedPrimaryCustomResourceAdoption{LiveSpecHash: specHash(actualRaw)}, nil
	}
	patchObject, err := decodeSpec(patch)
	if err != nil {
		return nil, fmt.Errorf("decode reconcile override patch: %w", err)
	}
	if len(renderRaw) > ReconcilePauseSnapshotMaxBytes {
		return nil, fmt.Errorf("reconcile override base spec is %d bytes, limit is %d", len(renderRaw), ReconcilePauseSnapshotMaxBytes)
	}
	// Keep only a canonical object patch. This also verifies that the merge
	// patch cannot turn the CR spec root into a scalar/array.
	patch, err = json.Marshal(patchObject)
	if err != nil {
		return nil, fmt.Errorf("normalize reconcile override patch: %w", err)
	}
	gvk := withoutOverrides.GroupVersionKind()
	return &PausedPrimaryCustomResourceAdoption{
		Overrides: &v1.ReconcileOverrides{
			SpecPatch: runtime.RawExtension{Raw: patch},
			BaseSpec:  runtime.RawExtension{Raw: renderRaw},
			GVK: v1.GVK{
				Group:   gvk.Group,
				Version: gvk.Version,
				Kind:    gvk.Kind,
			},
			Namespace: withoutOverrides.GetNamespace(),
			Name:      withoutOverrides.GetName(),
		},
		LiveSpecHash: specHash(actualRaw),
	}, nil
}

// ApplyReconcileOverrides applies the persisted merge patch to an already
// rendered primary CR. It intentionally affects only the target CR spec; it
// does not leak adopted live values into Configuration resources or PreActions.
func ApplyReconcileOverrides(m *v1.Middleware, cr *unstructured.Unstructured) error {
	if m == nil || m.Spec.ReconcileOverrides == nil || len(m.Spec.ReconcileOverrides.SpecPatch.Raw) == 0 {
		return nil
	}
	overrides := m.Spec.ReconcileOverrides
	if err := validateReconcileOverrideTarget(overrides, cr); err != nil {
		return err
	}
	currentSpec, current, err := primarySpec(cr)
	if err != nil {
		return err
	}
	patch, err := effectiveReconcileOverridePatch(overrides, currentSpec)
	if err != nil {
		return err
	}
	if len(patch) == 0 || string(patch) == "{}" {
		return nil
	}
	merged, err := jsonpatch.MergePatch(current, patch)
	if err != nil {
		return fmt.Errorf("apply reconcile override patch: %w", err)
	}
	mergedSpec, err := decodeSpec(merged)
	if err != nil {
		return fmt.Errorf("decode reconciled primary custom resource spec: %w", err)
	}
	if err := unstructured.SetNestedField(cr.Object, mergedSpec, "spec"); err != nil {
		return fmt.Errorf("set reconciled primary custom resource spec: %w", err)
	}
	return nil
}

func validateReconcileOverrideTarget(overrides *v1.ReconcileOverrides, cr *unstructured.Unstructured) error {
	if overrides == nil || cr == nil {
		return ErrReconcileOverrideTargetChanged
	}
	gvk := cr.GroupVersionKind()
	if overrides.GVK.Group == "" || overrides.GVK.Version == "" || overrides.GVK.Kind == "" ||
		overrides.GVK.Group != gvk.Group || overrides.GVK.Version != gvk.Version || overrides.GVK.Kind != gvk.Kind ||
		overrides.Namespace != cr.GetNamespace() || overrides.Name != cr.GetName() {
		return fmt.Errorf("%w: override targets %s/%s %s/%s/%s, rendered target is %s/%s %s/%s/%s",
			ErrReconcileOverrideTargetChanged,
			overrides.Namespace,
			overrides.Name,
			overrides.GVK.Group,
			overrides.GVK.Version,
			overrides.GVK.Kind,
			cr.GetNamespace(),
			cr.GetName(),
			gvk.Group,
			gvk.Version,
			gvk.Kind,
		)
	}
	return nil
}

// effectiveReconcileOverridePatch keeps an adopted patch only at paths whose
// normal rendered desired input is unchanged from the base that produced the
// patch. A later explicit MID/Baseline edit at an adopted path therefore wins
// without silently discarding unrelated adopted changes from the pause.
func effectiveReconcileOverridePatch(overrides *v1.ReconcileOverrides, current map[string]any) ([]byte, error) {
	if overrides == nil {
		return nil, nil
	}
	base, err := decodeSpec(overrides.BaseSpec.Raw)
	if err != nil {
		return nil, fmt.Errorf("decode reconcile override base spec: %w", err)
	}
	patch, err := decodeSpec(overrides.SpecPatch.Raw)
	if err != nil {
		return nil, fmt.Errorf("decode reconcile override patch: %w", err)
	}
	effective := filterReconcileOverridePatch(base, current, patch)
	raw, err := json.Marshal(effective)
	if err != nil {
		return nil, fmt.Errorf("marshal effective reconcile override patch: %w", err)
	}
	return raw, nil
}

func filterReconcileOverridePatch(base, current, patch map[string]any) map[string]any {
	result := make(map[string]any)
	for key, patchValue := range patch {
		baseValue, baseExists := base[key]
		currentValue, currentExists := current[key]
		value, include := filterReconcileOverridePatchValue(
			mergeValue{exists: baseExists, value: baseValue},
			mergeValue{exists: currentExists, value: currentValue},
			patchValue,
		)
		if include {
			result[key] = value
		}
	}
	return result
}

func filterReconcileOverridePatchValue(base, current mergeValue, patchValue any) (any, bool) {
	patchMap, patchIsMap := patchValue.(map[string]any)
	baseMap, baseIsMap := valueAsMap(base)
	currentMap, currentIsMap := valueAsMap(current)
	if patchIsMap && baseIsMap && currentIsMap {
		result := filterReconcileOverridePatch(baseMap, currentMap, patchMap)
		if len(result) == 0 {
			return nil, false
		}
		return result, true
	}
	if mergeValueEqual(base, current) {
		return cloneJSONValue(patchValue), true
	}
	return nil, false
}

func primarySpec(cr *unstructured.Unstructured) (map[string]any, []byte, error) {
	if cr == nil {
		return nil, nil, fmt.Errorf("primary custom resource is nil")
	}
	spec, found := cr.Object["spec"]
	if !found {
		spec = map[string]any{}
	}
	raw, err := json.Marshal(spec)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal primary custom resource spec: %w", err)
	}
	decoded, err := decodeSpec(raw)
	if err != nil {
		return nil, nil, err
	}
	// Decode and marshal once more so the persisted snapshot and its hash use
	// canonical map-key ordering regardless of how a RawExtension was written.
	normalized, err := json.Marshal(decoded)
	if err != nil {
		return nil, nil, fmt.Errorf("normalize primary custom resource spec: %w", err)
	}
	return decoded, normalized, nil
}

func decodeSpec(raw []byte) (map[string]any, error) {
	if len(raw) == 0 {
		return map[string]any{}, nil
	}
	var spec map[string]any
	if err := json.Unmarshal(raw, &spec); err != nil {
		return nil, err
	}
	if spec == nil {
		return nil, fmt.Errorf("spec must be a JSON object")
	}
	return spec, nil
}

func verifySnapshotHash(snapshot *v1.ReconcilePauseStatus, spec map[string]any) error {
	raw, err := json.Marshal(spec)
	if err != nil {
		return err
	}
	if snapshot.Hash == "" || snapshot.Hash != specHash(raw) {
		return fmt.Errorf("%w: desired spec hash mismatch", ErrReconcilePauseSnapshotInvalid)
	}
	return nil
}

func specHash(raw []byte) string {
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

func matchesSnapshotTarget(snapshot *v1.ReconcilePauseStatus, cr *unstructured.Unstructured) error {
	gvk := cr.GroupVersionKind()
	if snapshot.GVK.Group != gvk.Group || snapshot.GVK.Version != gvk.Version || snapshot.GVK.Kind != gvk.Kind ||
		snapshot.Namespace != cr.GetNamespace() || snapshot.Name != cr.GetName() {
		return ErrReconcilePauseResourceChanged
	}
	return nil
}

// VerifyPausedPrimaryCustomResourceForApply closes the recovery race between
// B/L/D calculation and the following SSA request. It is called immediately
// before the primary CR write; a changed live spec sends the controller back
// through merge instead of overwriting the new value.
func VerifyPausedPrimaryCustomResourceForApply(ctx context.Context, cli client.Client, snapshot *v1.ReconcilePauseStatus, rendered *unstructured.Unstructured) error {
	if snapshot == nil || snapshot.AdoptedLiveSpecHash == "" {
		return fmt.Errorf("%w: missing adopted live spec hash", ErrReconcilePauseLiveSpecChanged)
	}
	if err := matchesSnapshotTarget(snapshot, rendered); err != nil {
		return err
	}
	matches, err := PausedPrimaryCustomResourceSpecMatchesAdoption(ctx, cli, snapshot)
	if err != nil {
		return err
	}
	if !matches {
		return ErrReconcilePauseLiveSpecChanged
	}
	return nil
}

// PausedPrimaryCustomResourceSpecMatchesAdoption checks whether L is still
// the same spec that was used for the successful merge. Status-only updates do
// not affect this check, so a target controller's normal status heartbeat does
// not block recovery.
func PausedPrimaryCustomResourceSpecMatchesAdoption(ctx context.Context, cli client.Client, snapshot *v1.ReconcilePauseStatus) (bool, error) {
	if snapshot == nil || snapshot.AdoptedLiveSpecHash == "" {
		return false, nil
	}
	gvk := schema.GroupVersionKind{
		Group:   snapshot.GVK.Group,
		Version: snapshot.GVK.Version,
		Kind:    snapshot.GVK.Kind,
	}
	live, err := k8s.GetCustomResource(ctx, cli, snapshot.Name, snapshot.Namespace, gvk)
	if err != nil {
		if isNotFound(err) {
			return false, ErrReconcilePauseResourceMissing
		}
		return false, fmt.Errorf("get primary custom resource before resume apply: %w", err)
	}
	if snapshot.ResourceUID == "" || snapshot.ResourceUID != string(live.GetUID()) {
		return false, ErrReconcilePauseResourceChanged
	}
	_, raw, err := primarySpec(live)
	if err != nil {
		return false, fmt.Errorf("read primary custom resource before resume apply: %w", err)
	}
	return specHash(raw) == snapshot.AdoptedLiveSpecHash, nil
}

type mergeValue struct {
	exists bool
	value  any
}

// mergePausedSpec implements the B/L/D merge rule: keep D when L did not
// change from B; adopt L when D did not change from B; accept identical L/D
// changes; recurse through maps; and treat arrays as atomic values.
func mergePausedSpec(base, actual, desired map[string]any) (map[string]any, []string, error) {
	merged, conflicts, err := mergePausedValue(
		mergeValue{exists: true, value: base},
		mergeValue{exists: true, value: actual},
		mergeValue{exists: true, value: desired},
		"",
	)
	if err != nil {
		return nil, nil, err
	}
	if !merged.exists {
		return nil, nil, fmt.Errorf("merged primary custom resource spec unexpectedly absent")
	}
	result, ok := merged.value.(map[string]any)
	if !ok {
		return nil, nil, fmt.Errorf("merged primary custom resource spec is %T, want object", merged.value)
	}
	return result, conflicts, nil
}

func mergePausedValue(base, actual, desired mergeValue, path string) (mergeValue, []string, error) {
	// Operators that round-trip typed CR specs can serialize an omitted pointer
	// field as JSON null. Treat that observed live value as unchanged from B so
	// it does not become an RFC 7396 deletion override. Only L is normalized;
	// B and D keep their declarative JSON merge-patch semantics.
	if actual.exists && actual.value == nil {
		actual = cloneMergeValue(base)
	}
	baseMap, baseIsMap := valueAsMap(base)
	actualMap, actualIsMap := valueAsMap(actual)
	desiredMap, desiredIsMap := valueAsMap(desired)
	// Recurse through maps before shortcutting equal parent objects. This lets
	// the merge preserve independent nested edits while normalizing observed
	// live null values at their individual paths.
	if baseIsMap && actualIsMap && desiredIsMap {
		keys := make(map[string]struct{}, len(baseMap)+len(actualMap)+len(desiredMap))
		for key := range baseMap {
			keys[key] = struct{}{}
		}
		for key := range actualMap {
			keys[key] = struct{}{}
		}
		for key := range desiredMap {
			keys[key] = struct{}{}
		}
		ordered := make([]string, 0, len(keys))
		for key := range keys {
			ordered = append(ordered, key)
		}
		sort.Strings(ordered)

		result := make(map[string]any, len(keys))
		var conflicts []string
		for _, key := range ordered {
			baseValue, baseOK := baseMap[key]
			actualValue, actualOK := actualMap[key]
			desiredValue, desiredOK := desiredMap[key]
			merged, childConflicts, err := mergePausedValue(
				mergeValue{exists: baseOK, value: baseValue},
				mergeValue{exists: actualOK, value: actualValue},
				mergeValue{exists: desiredOK, value: desiredValue},
				joinJSONPointer(path, key),
			)
			if err != nil {
				return mergeValue{}, nil, err
			}
			conflicts = append(conflicts, childConflicts...)
			if merged.exists {
				result[key] = merged.value
			}
		}
		return mergeValue{exists: true, value: result}, conflicts, nil
	}

	if mergeValueEqual(actual, base) {
		return cloneMergeValue(desired), nil, nil
	}
	if mergeValueEqual(desired, base) {
		return cloneMergeValue(actual), nil, nil
	}
	if mergeValueEqual(desired, actual) {
		return cloneMergeValue(desired), nil, nil
	}

	return cloneMergeValue(desired), []string{displayJSONPointer(path)}, nil
}

func mergeValueEqual(left, right mergeValue) bool {
	return left.exists == right.exists && (!left.exists || reflect.DeepEqual(left.value, right.value))
}

func cloneMergeValue(value mergeValue) mergeValue {
	if !value.exists {
		return mergeValue{}
	}
	return mergeValue{exists: true, value: cloneJSONValue(value.value)}
}

func valueAsMap(value mergeValue) (map[string]any, bool) {
	if !value.exists {
		return nil, false
	}
	object, ok := value.value.(map[string]any)
	return object, ok
}

func cloneJSONValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, child := range typed {
			result[key] = cloneJSONValue(child)
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for i, child := range typed {
			result[i] = cloneJSONValue(child)
		}
		return result
	default:
		return typed
	}
}

func joinJSONPointer(parent, key string) string {
	escaped := strings.NewReplacer("~", "~0", "/", "~1").Replace(key)
	return parent + "/" + escaped
}

func displayJSONPointer(path string) string {
	if path == "" {
		return "/"
	}
	return path
}

func isNotFound(err error) bool {
	return apierrors.IsNotFound(err)
}
