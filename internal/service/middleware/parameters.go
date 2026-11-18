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

package middleware

import (
	"bytes"
	"context"
	"encoding/json"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	"github.com/harmonycloud/opensaola/internal/k8s"
	"github.com/harmonycloud/opensaola/internal/service/customresource"
	"github.com/harmonycloud/opensaola/pkg/tools"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Merge package parameters with the registered Kubernetes mount identities and
// init container execution order when the target contains a native PodSpec.
func mergeMiddlewareParameters(ctx context.Context, cli client.Client, baseline *v1.MiddlewareBaseline, mid *v1.Middleware) error {
	base, err := baseline.Spec.Parameters.MarshalJSON()
	if err != nil {
		return err
	}
	override, err := mid.Spec.Parameters.MarshalJSON()
	if err != nil {
		return err
	}
	if bytes.Equal(base, []byte("null")) || bytes.Equal(override, []byte("null")) {
		return tools.StructMerge(&baseline.Spec.Parameters, &mid.Spec.Parameters, tools.StructMergeMapType)
	}
	var inherited, declared map[string]any
	decode := func(raw []byte, result *map[string]any) error {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		return decoder.Decode(result)
	}
	if err := decode(base, &inherited); err != nil {
		return err
	}
	if err := decode(override, &declared); err != nil {
		return err
	}
	// Inspect only locations of PodSpec in the native API. Most parameters
	// never need GVK lookup. Custom resources continue to use package rules.
	var declaredInit []any
	var podPath []string
	for _, path := range [][]string{nil, {"template", "spec"}, {"jobTemplate", "spec", "template", "spec"}} {
		overlay, found, _ := unstructured.NestedFieldNoCopy(declared, path...)
		pod, ok := overlay.(map[string]any)
		if !found || !ok {
			continue
		}
		init, _ := pod["initContainers"].([]any)
		if len(init) < 2 && !k8s.HasPodMountEdits(pod) {
			continue
		}
		var gvk schema.GroupVersionKind
		if baseline.Spec.OperatorBaseline.Name == "" && mid.Spec.OperatorBaseline.Name == "" {
			gvk = schema.GroupVersionKind{Group: baseline.Spec.GVK.Group, Version: baseline.Spec.GVK.Version, Kind: baseline.Spec.GVK.Kind}
		} else {
			resolved, err := customresource.HandleGvk(ctx, cli, mid)
			if err != nil {
				return err
			}
			gvk = *resolved
		}
		checkPath := append([]string{"spec"}, path...)
		if !k8s.NativeInitContainersAt(gvk, append(checkPath, "initContainers")...) {
			continue
		}
		source, _, _ := unstructured.NestedFieldNoCopy(inherited, path...)
		basePod, _ := source.(map[string]any)
		if err := k8s.PreparePackagePodMounts(basePod, pod); err != nil {
			return err
		}
		declaredInit = init
		podPath = path
		break
	}
	merged := tools.MergeMap(inherited, declared)
	if len(declaredInit) > 1 {
		value, _, _ := unstructured.NestedFieldNoCopy(merged, append(podPath, "initContainers")...)
		orderedInit, _ := value.([]any)
		if err := k8s.OrderDeclaredInitContainers(orderedInit, declaredInit); err != nil {
			return err
		}
	}
	encoded, err := json.Marshal(merged)
	if err != nil {
		return err
	}
	return mid.Spec.Parameters.UnmarshalJSON(encoded)
}
