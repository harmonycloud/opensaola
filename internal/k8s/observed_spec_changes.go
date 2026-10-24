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
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/managedfields"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/structured-merge-diff/v6/typed"
)

// ErrObservedSpecChanged sends resume back through adoption rather than writing
// desired state computed from an earlier actual spec.
var ErrObservedSpecChanged = errors.New("paused primary custom resource spec changed after adoption")

func verifyObservedSpecForWrite(owner client.Object, live *unstructured.Unstructured, configuration string) error {
	mid, ok := owner.(*v1.Middleware)
	if !ok || configuration != "" || mid.Status.ReconcilePause == nil {
		return nil
	}
	policy, valid := v1.ReconcileResumePolicyFor(mid.Annotations)
	if !valid || policy != v1.ReconcileResumePolicyMerge {
		return nil
	}
	snapshot := mid.Status.ReconcilePause
	if live == nil || snapshot.ResourceUID == "" || string(live.GetUID()) != snapshot.ResourceUID || live.GetDeletionTimestamp() != nil ||
		live.GetName() != snapshot.Name || live.GetNamespace() != snapshot.Namespace ||
		live.GroupVersionKind().Group != snapshot.GVK.Group || live.GroupVersionKind().Version != snapshot.GVK.Version || live.GroupVersionKind().Kind != snapshot.GVK.Kind {
		return fmt.Errorf("%w: primary resource is missing or replaced", ErrObservedSpecChanged)
	}
	raw, err := json.Marshal(live.Object["spec"])
	if err != nil {
		return fmt.Errorf("%w: cannot read live spec", ErrObservedSpecChanged)
	}
	hash := sha256.Sum256(raw)
	if snapshot.AdoptedLiveSpecHash == "" || snapshot.AdoptedLiveSpecHash != hex.EncodeToString(hash[:]) {
		return ErrObservedSpecChanged
	}
	return nil
}

// AdoptObservedSpecChanges overlays only the changes observed between two
// actual specs onto desired. Unlike normal intent updates, changes made during
// an explicitly approved pause are authoritative at the changed paths. Values
// already present before the pause, including API defaults, are not adopted.
func (w *ManagedResourceWriter) AdoptObservedSpecChanges(ctx context.Context, before, after, desired *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	if w.IntentSchemas == nil || desired == nil {
		return nil, fmt.Errorf("pause adoption requires the resource schema resolver and desired object")
	}
	converter, _, err := w.IntentSchemas.Resolve(ctx, desired.GroupVersionKind())
	if err != nil {
		return nil, fmt.Errorf("resolve pause adoption schema: %w", err)
	}
	return applyObservedSpecChanges(converter, before, after, desired)
}

func applyObservedSpecChanges(converter managedfields.TypeConverter, before, after, desired *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	scoped := make([]*unstructured.Unstructured, 3)
	for i, obj := range []*unstructured.Unstructured{before, after, desired} {
		if obj == nil {
			return nil, fmt.Errorf("pause adoption requires before, after and desired objects")
		}
		scoped[i] = &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": obj.GetAPIVersion(), "kind": obj.GetKind(),
			"metadata": map[string]any{"name": obj.GetName(), "namespace": obj.GetNamespace()},
		}}
		if _, exists := obj.Object["spec"]; exists {
			scoped[i].Object["spec"] = obj.DeepCopy().Object["spec"]
		}
	}
	objects, err := intentDefaultsObjects(converter, scoped...)
	if err != nil {
		return nil, fmt.Errorf("validate pause adoption specs: %w", err)
	}
	change, err := objects[0].Compare(objects[1])
	if err != nil {
		return nil, fmt.Errorf("compare pause-time and current actual specs: %w", err)
	}
	// Extract changed leaves with schema-defined list keys. Extracting a parent
	// membership marker would copy its unchanged children (including defaults).
	patch := objects[1].ExtractItems(change.Added.Union(change.Modified).Leaves(), typed.WithAppendKeyFields())
	merged, err := objects[2].RemoveItems(change.Removed).Merge(patch)
	if err != nil {
		return nil, fmt.Errorf("apply observed spec changes: %w", err)
	}
	obj, err := converter.TypedToObject(merged)
	if err != nil {
		return nil, fmt.Errorf("decode adopted spec: %w", err)
	}
	result, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return nil, fmt.Errorf("schema converter returned unsupported adopted resource %T", obj)
	}
	// Preserve all desired metadata and status; adoption concerns spec only.
	output := desired.DeepCopy()
	delete(output.Object, "spec")
	if spec, exists := result.Object["spec"]; exists {
		output.Object["spec"] = spec
	}
	return output, nil
}
