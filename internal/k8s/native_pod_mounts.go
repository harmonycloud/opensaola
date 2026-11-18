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

	"github.com/harmonycloud/opensaola/pkg/tools"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

var podContainerLists = []string{"containers", "initContainers", "ephemeralContainers"}

// HasPodMountEdits reports whether any container supplies a nonempty mount overlay.
func HasPodMountEdits(pod map[string]any) bool {
	for _, key := range podContainerLists {
		items, _ := pod[key].([]any)
		for _, value := range items {
			c, _ := value.(map[string]any)
			mounts, _ := c["volumeMounts"].([]any)
			if len(mounts) > 0 {
				return true
			}
		}
	}
	return false
}

// PreparePackagePodMounts folds native mount edits into the overlay, removing
// the matching mounts from the private baseline copy before package name-based
// merging can run a second time over the same list.
func PreparePackagePodMounts(base, overlay map[string]any) error {
	return preparePodMounts(base, overlay, true)
}

// With an Action, only fill the list keys; the schema-based planner owns the
// merge. Both modes accept a missing path only for an unambiguous volume name.
func preparePodMounts(base, overlay map[string]any, mergePackage bool) error {
	for _, key := range podContainerLists {
		baseContainers, _ := base[key].([]any)
		containers := map[string]map[string]any{}
		for _, v := range baseContainers {
			c, _ := v.(map[string]any)
			name, _ := c["name"].(string)
			if name != "" {
				containers[name] = c
			}
		}
		overrides, _ := overlay[key].([]any)
		for _, v := range overrides {
			c, _ := v.(map[string]any)
			mounts, _ := c["volumeMounts"].([]any)
			if len(mounts) == 0 {
				continue
			}
			name, _ := c["name"].(string)
			parent := containers[name]
			previous, _ := parent["volumeMounts"].([]any)
			merged, err := resolveNativeMountEdits(previous, mounts, mergePackage)
			if err != nil {
				return fmt.Errorf("%s[%q].volumeMounts: %w", key, name, err)
			}
			c["volumeMounts"] = merged
			if mergePackage {
				delete(parent, "volumeMounts")
			}
		}
	}
	return nil
}

func resolveNativeMountEdits(base, edits []any, mergePackage bool) ([]any, error) {
	paths := map[string]int{}
	byName := map[string][]string{}
	for i, v := range base {
		m, _ := v.(map[string]any)
		path, _ := m["mountPath"].(string)
		name, _ := m["name"].(string)
		if _, duplicate := paths[path]; path == "" || duplicate {
			return nil, fmt.Errorf("existing mounts require unique nonempty mountPath keys")
		}
		paths[path] = i
		byName[name] = append(byName[name], path)
	}
	edited := map[string]bool{}
	for _, v := range edits {
		m, ok := v.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("expected a mount object")
		}
		path, hasPath := m["mountPath"].(string)
		if _, explicit := m["mountPath"]; !explicit {
			name, _ := m["name"].(string)
			matches := byName[name]
			if len(matches) != 1 {
				return nil, fmt.Errorf("supply mountPath when volume name %q does not identify exactly one existing mount", name)
			}
			path = matches[0]
			m["mountPath"] = path
		} else if !hasPath || path == "" {
			return nil, fmt.Errorf("mountPath must be a nonempty string")
		}
		if edited[path] {
			return nil, fmt.Errorf("duplicate mountPath %q in overlay", path)
		}
		edited[path] = true
		if !mergePackage {
			continue
		}
		if i, ok := paths[path]; ok {
			original, _ := base[i].(map[string]any)
			base[i] = tools.MergeMap(original, m)
		} else {
			base = append(base, m)
		}
	}
	if mergePackage {
		return base, nil
	}
	return edits, nil
}

// Normalize legacy name-only mount edits before validating the partial Action
// declaration. Always inspect the registered native type before using Pod rules.
func actionWithNativeMountPaths(live, edit *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	if live == nil || edit == nil {
		return edit, nil
	}
	for _, path := range [][]string{{"spec"}, {"template", "spec"}, {"spec", "template", "spec"}, {"spec", "jobTemplate", "spec", "template", "spec"}} {
		raw, _, _ := unstructured.NestedFieldNoCopy(edit.Object, path...)
		pod, _ := raw.(map[string]any)
		if !HasPodMountEdits(pod) || !NativeInitContainersAt(live.GroupVersionKind(), append(path, "initContainers")...) {
			continue
		}
		raw, _, _ = unstructured.NestedFieldNoCopy(live.Object, path...)
		existing, _ := raw.(map[string]any)
		result := edit.DeepCopy()
		raw, _, _ = unstructured.NestedFieldNoCopy(result.Object, path...)
		edits, _ := raw.(map[string]any)
		if err := preparePodMounts(existing, edits, false); err != nil {
			return nil, err
		}
		return result, nil
	}
	return edit, nil
}
