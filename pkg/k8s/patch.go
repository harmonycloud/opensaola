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
	"encoding/json"
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

// PatchType defines the patch type.
type PatchType string

const (
	// JSONPatchType represents the JSON Patch type.
	JSONPatchType PatchType = "application/json-patch+json"
	// MergePatchType represents the JSON Merge Patch type.
	MergePatchType PatchType = "application/merge-patch+json"
	// StrategicMergePatchType represents the Strategic Merge Patch type.
	StrategicMergePatchType PatchType = "application/strategic-merge-patch+json"
)

// Patch defines a patch operation.
type Patch struct {
	// Type is the patch type.
	Type PatchType
	// Data is the patch content.
	Data []byte
}

// CreatePatch creates a patch.
func CreatePatch(original, modified runtime.Object, patchType PatchType) (*Patch, error) {
	// Marshal the objects to JSON
	originalJSON, err := json.Marshal(original)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal original object: %w", err)
	}

	modifiedJSON, err := json.Marshal(modified)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal modified object: %w", err)
	}

	var patchData []byte
	switch patchType {
	case StrategicMergePatchType:
		// Create a patch using the strategicpatch package
		patchData, err = strategicpatch.CreateTwoWayMergePatch(originalJSON, modifiedJSON, original)
		if err != nil {
			return nil, fmt.Errorf("failed to create strategic merge patch: %w", err)
		}
	case MergePatchType:
		// Create a JSON Merge Patch using the strategicpatch package
		patchData, err = strategicpatch.CreateTwoWayMergePatch(originalJSON, modifiedJSON, original)
		if err != nil {
			return nil, fmt.Errorf("failed to create merge patch: %w", err)
		}
	default:
		return nil, fmt.Errorf("unsupported patch type: %s", patchType)
	}

	return &Patch{
		Type: patchType,
		Data: patchData,
	}, nil
}

// ApplyPatch applies a patch to an object.
func ApplyPatch(original runtime.Object, patch *Patch) (runtime.Object, error) {
	// Marshal the original object to JSON
	originalJSON, err := json.Marshal(original)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal original object: %w", err)
	}

	var patchedJSON []byte
	switch patch.Type {
	case StrategicMergePatchType:
		// Apply the patch using the strategicpatch package
		patchedJSON, err = strategicpatch.StrategicMergePatch(originalJSON, patch.Data, original)
		if err != nil {
			return nil, fmt.Errorf("failed to apply strategic merge patch: %w", err)
		}
	case MergePatchType:
		// Apply the JSON Merge Patch using the strategicpatch package
		patchedJSON, err = strategicpatch.StrategicMergePatch(originalJSON, patch.Data, original)
		if err != nil {
			return nil, fmt.Errorf("failed to apply merge patch: %w", err)
		}
	default:
		return nil, fmt.Errorf("unsupported patch type: %s", patch.Type)
	}

	// Create a new object instance
	newObj := original.DeepCopyObject()
	if err := json.Unmarshal(patchedJSON, newObj); err != nil {
		return nil, fmt.Errorf("failed to unmarshal patched object: %w", err)
	}

	return newObj, nil
}

// ValidatePatch validates whether a patch is valid.
func ValidatePatch(original, modified runtime.Object, patch *Patch) error {
	// Apply the patch
	patched, err := ApplyPatch(original, patch)
	if err != nil {
		return err
	}

	// Marshal the modified object to JSON
	modifiedJSON, err := json.Marshal(modified)
	if err != nil {
		return fmt.Errorf("failed to marshal modified object: %w", err)
	}

	// Marshal the patched object to JSON
	patchedJSON, err := json.Marshal(patched)
	if err != nil {
		return fmt.Errorf("failed to marshal patched object: %w", err)
	}

	// Compare whether the two JSONs are equal
	if string(modifiedJSON) != string(patchedJSON) {
		return field.Invalid(field.NewPath("patch"), patch, "patch does not produce the expected result")
	}

	return nil
}
