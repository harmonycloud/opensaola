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

package packages

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	"github.com/harmonycloud/opensaola/internal/k8s"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var definitionDirs = map[string]string{
	"MiddlewareBaseline":         "baselines",
	"MiddlewareOperatorBaseline": "baselines",
	"MiddlewareActionBaseline":   "actions",
	"MiddlewareConfiguration":    "configurations",
}

// Definitions returns the installable cluster-scoped resources in archive path
// and YAML document order. Callers may modify the returned objects.
func (pkg *Package) Definitions() ([]*unstructured.Unstructured, error) {
	return pkg.readDefinitions("")
}

// A controller looking up a baseline need not decode the configuration templates
// or action scripts. The install and uninstall paths request all four kinds.
func (pkg *Package) readDefinitions(kind string) ([]*unstructured.Unstructured, error) {
	var paths []string
	for filename := range pkg.Files {
		dir, _, _ := strings.Cut(filename, "/")
		if kind != "" && dir != definitionDirs[kind] && !(dir == "crds" && kind == "MiddlewareConfiguration") {
			continue
		}
		switch dir {
		case "baselines", "actions", "configurations", "crds":
			ext := strings.ToLower(path.Ext(filename))
			if ext == ".yaml" || ext == ".yml" {
				paths = append(paths, filename)
			}
		}
	}
	sort.Strings(paths)

	var result []*unstructured.Unstructured
	seen := make(map[[2]string]string)
	for _, filename := range paths {
		dir, _, _ := strings.Cut(filename, "/")
		content := pkg.Files[filename]
		source := bytes.NewReader(content)
		buffered := bufio.NewReader(source)
		reader := yaml.NewYAMLReader(buffered)
		start := 0
		for doc := 1; ; doc++ {
			_, err := reader.Read()
			if err == io.EOF {
				break
			}
			where := fmt.Sprintf("%s#%d", filename, doc)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", where, err)
			}
			// YAMLReader adds a newline to an unterminated last line. Decode the
			// original bytes so a literal template or script keeps its exact value.
			end := len(content) - source.Len() - buffered.Buffered()
			var obj unstructured.Unstructured
			if err := yaml.Unmarshal(content[start:end], &obj.Object); err != nil {
				return nil, fmt.Errorf("%s: %w", where, err)
			}
			start = end
			if obj.Object == nil {
				continue // comments and empty YAML documents
			}
			// Packages can ship either a plain CRD or a MiddlewareConfiguration
			// wrapping a CRD here. Only the wrapper is an installed package definition.
			defDir := dir
			if dir == "crds" {
				if obj.GetKind() == "CustomResourceDefinition" && obj.GroupVersionKind().Group == "apiextensions.k8s.io" {
					continue
				}
				defDir = "configurations"
			}
			if obj.GetAPIVersion() != v1.GroupVersion.String() || definitionDirs[obj.GetKind()] != defDir {
				return nil, fmt.Errorf("%s: unexpected package definition %q %q", where, obj.GetAPIVersion(), obj.GetKind())
			}
			if obj.GetName() == "" {
				return nil, fmt.Errorf("%s: %s requires metadata.name", where, obj.GetKind())
			}
			key := [2]string{obj.GetKind(), obj.GetName()}
			if first, ok := seen[key]; ok {
				return nil, fmt.Errorf("%s: duplicate %s %q already declared in %s", where, key[0], key[1], first)
			}
			seen[key] = where
			if kind == "" || obj.GetKind() == kind {
				result = append(result, &obj)
			}
		}
	}
	return result, nil
}

func packageDefinitions[T any](ctx context.Context, cli client.Client, packageName, kind string) ([]*T, error) {
	pkg, err := Get(ctx, cli, packageName)
	if err != nil {
		return nil, err
	}
	defs, err := pkg.readDefinitions(kind)
	if err != nil {
		return nil, err
	}
	var out []*T
	for _, def := range defs {
		item := new(T)
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(def.Object, item); err != nil {
			return nil, fmt.Errorf("decode %s %q: %w", kind, def.GetName(), err)
		}
		out = append(out, item)
	}
	return out, nil
}

func GetConfigurations(ctx context.Context, cli client.Client, packageName string) (map[string]*v1.MiddlewareConfiguration, error) {
	defs, err := packageDefinitions[v1.MiddlewareConfiguration](ctx, cli, packageName, "MiddlewareConfiguration")
	if err != nil {
		return nil, err
	}
	out := make(map[string]*v1.MiddlewareConfiguration, len(defs))
	for _, def := range defs {
		out[def.Name] = def
	}
	return out, nil
}

func GetMiddlewareBaselines(ctx context.Context, cli client.Client, packageName string) ([]*v1.MiddlewareBaseline, error) {
	return packageDefinitions[v1.MiddlewareBaseline](ctx, cli, packageName, "MiddlewareBaseline")
}

func GetMiddlewareOperatorBaselines(ctx context.Context, cli client.Client, packageName string) ([]*v1.MiddlewareOperatorBaseline, error) {
	return packageDefinitions[v1.MiddlewareOperatorBaseline](ctx, cli, packageName, "MiddlewareOperatorBaseline")
}

func GetMiddlewareActionBaselines(ctx context.Context, cli client.Client, packageName string) ([]*v1.MiddlewareActionBaseline, error) {
	return packageDefinitions[v1.MiddlewareActionBaseline](ctx, cli, packageName, "MiddlewareActionBaseline")
}

func GetMiddlewareBaseline(ctx context.Context, cli client.Client, name, packageName string) (*v1.MiddlewareBaseline, error) {
	items, err := GetMiddlewareBaselines(ctx, cli, packageName)
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		if item.Name == name {
			return item, nil
		}
	}
	return nil, apierrors.NewNotFound(k8s.MiddlewareBaselineGroupResource(), name)
}

func GetMiddlewareOperatorBaseline(ctx context.Context, cli client.Client, name, packageName string) (*v1.MiddlewareOperatorBaseline, error) {
	items, err := GetMiddlewareOperatorBaselines(ctx, cli, packageName)
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		if item.Name == name {
			return item, nil
		}
	}
	return nil, apierrors.NewNotFound(k8s.MiddlewareOperatorBaselineGroupResource(), name)
}
