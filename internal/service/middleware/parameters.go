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
	"fmt"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	"github.com/harmonycloud/opensaola/internal/k8s"
	"github.com/harmonycloud/opensaola/internal/service/customresource"
	"github.com/harmonycloud/opensaola/pkg/tools"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	kjson "sigs.k8s.io/json"
	"sigs.k8s.io/yaml"
)

func renderMiddlewareParameters(ctx context.Context, parameters *runtime.RawExtension, values *tools.TemplateValues) (map[string]any, error) {
	source, err := yaml.Marshal(*parameters)
	if err != nil {
		return nil, err
	}
	rendered, err := tools.TemplateParse(ctx, string(tools.ProcessYAMLGoTemp(source)), values)
	if err != nil {
		return nil, fmt.Errorf("template parse error: %w", err)
	}
	if err := yaml.Unmarshal([]byte(rendered), parameters); err != nil {
		return nil, fmt.Errorf("unmarshal mid error: %w", err)
	}
	var result map[string]any
	if err := kjson.UnmarshalCaseSensitivePreserveInts(parameters.Raw, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// Merge package parameters with the registered Kubernetes mount identities and
// explicit init container and environment variable order in a native PodSpec.
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
	var envOrder []containerEnvOrder
	var podPath []string
	for _, path := range [][]string{nil, {"template", "spec"}, {"jobTemplate", "spec", "template", "spec"}} {
		overlay, found, _ := unstructured.NestedFieldNoCopy(declared, path...)
		pod, ok := overlay.(map[string]any)
		if !found || !ok {
			continue
		}
		init, _ := pod["initContainers"].([]any)
		env := declaredContainerEnvOrders(pod)
		if len(init) < 2 && len(env) == 0 && !k8s.HasPodMountEdits(pod) {
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
		if !k8s.NativePodSpecAt(gvk, checkPath...) {
			continue
		}
		source, _, _ := unstructured.NestedFieldNoCopy(inherited, path...)
		basePod, _ := source.(map[string]any)
		if err := k8s.PreparePackagePodMounts(basePod, pod); err != nil {
			return err
		}
		declaredInit = init
		envOrder = env
		podPath = path
		break
	}
	merged := tools.MergeMap(inherited, declared)
	if len(declaredInit) > 1 || len(envOrder) > 0 {
		value, _, _ := unstructured.NestedFieldNoCopy(merged, podPath...)
		pod, _ := value.(map[string]any)
		orderedInit, _ := pod["initContainers"].([]any)
		if err := k8s.OrderDeclaredNamedMembers(orderedInit, declaredInit); err != nil {
			return fmt.Errorf("initContainers: %w", err)
		}
		for _, order := range envOrder {
			if err := order.apply(pod); err != nil {
				return err
			}
		}
	}
	encoded, err := json.Marshal(merged)
	if err != nil {
		return err
	}
	return mid.Spec.Parameters.UnmarshalJSON(encoded)
}

type containerEnvOrder struct {
	list string
	name string
	env  []any
}

func declaredContainerEnvOrders(pod map[string]any) []containerEnvOrder {
	var orders []containerEnvOrder
	for _, list := range []string{"containers", "initContainers", "ephemeralContainers"} {
		containers, _ := pod[list].([]any)
		for _, item := range containers {
			container, _ := item.(map[string]any)
			env, _ := container["env"].([]any)
			if len(env) > 1 {
				name, _ := container["name"].(string)
				orders = append(orders, containerEnvOrder{list: list, name: name, env: env})
			}
		}
	}
	return orders
}

func (o containerEnvOrder) apply(pod map[string]any) error {
	containers, _ := pod[o.list].([]any)
	for _, item := range containers {
		container, _ := item.(map[string]any)
		if container["name"] == o.name && o.name != "" {
			env, _ := container["env"].([]any)
			if err := k8s.OrderDeclaredNamedMembers(env, o.env); err != nil {
				return fmt.Errorf("%s[%q].env: %w", o.list, o.name, err)
			}
			return nil
		}
	}
	return fmt.Errorf("%s[%q].env: cannot locate the declared container", o.list, o.name)
}
