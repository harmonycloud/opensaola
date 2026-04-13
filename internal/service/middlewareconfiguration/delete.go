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

package middlewareconfiguration

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/api/errors"

	"github.com/OpenSaola/opensaola/internal/k8s"
	"k8s.io/apimachinery/pkg/runtime/schema"

	v1 "github.com/OpenSaola/opensaola/api/v1"
	"github.com/OpenSaola/opensaola/internal/resource/logger"
	"github.com/OpenSaola/opensaola/pkg/tools"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func deleteTemplateLineValues(template string) (apiVersion string, kind string, nameExpr string) {
	lines := strings.Split(template, "\n")
	metadataIndent := -1
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		if apiVersion == "" && strings.HasPrefix(trimmed, "apiVersion:") {
			apiVersion = strings.TrimSpace(strings.TrimPrefix(trimmed, "apiVersion:"))
			continue
		}
		if kind == "" && strings.HasPrefix(trimmed, "kind:") {
			kind = strings.TrimSpace(strings.TrimPrefix(trimmed, "kind:"))
			continue
		}

		indent := len(line) - len(strings.TrimLeft(line, " "))
		if trimmed == "metadata:" {
			metadataIndent = indent
			continue
		}
		if metadataIndent >= 0 {
			if indent <= metadataIndent {
				metadataIndent = -1
				continue
			}
			if nameExpr == "" && strings.HasPrefix(trimmed, "name:") {
				nameExpr = strings.TrimSpace(strings.TrimPrefix(trimmed, "name:"))
				continue
			}
		}
	}
	return apiVersion, kind, nameExpr
}

func normalizeRenderedName(s string) string {
	name := strings.TrimSpace(s)
	name = strings.Trim(name, "\"'")
	return strings.TrimSpace(name)
}

func applyConfigurationValues(ctx context.Context, cfg v1.Configuration, templateValues *tools.TemplateValues) error {
	if len(cfg.Values.Raw) == 0 {
		return nil
	}
	data, err := cfg.Values.MarshalJSON()
	if err != nil {
		return err
	}
	parse, err := tools.TemplateParse(ctx, string(data), templateValues)
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(parse), &templateValues.Values)
}

type deleteByNameResult string

const (
	deleteByNameDeleted  deleteByNameResult = "deleted"
	deleteByNameNotFound deleteByNameResult = "not_found"
)

func deleteByGVKAndName(ctx context.Context, cli client.Client, gvk schema.GroupVersionKind, namespace string, name string) (deleteByNameResult, error) {
	logger.Log.Infoj(map[string]interface{}{
		"amsg":      "deleting configuration rendered resource by name",
		"gvk":       gvk.String(),
		"namespace": namespace,
		"name":      name,
	})
	obj := new(unstructured.Unstructured)
	obj.SetGroupVersionKind(gvk)
	obj.SetName(name)
	if namespace != "" {
		obj.SetNamespace(namespace)
	}
	err := k8s.DeleteCustomResource(ctx, cli, obj)
	if errors.IsNotFound(err) {
		logger.Log.Warnj(map[string]interface{}{
			"amsg":      "configuration rendered resource not found by name",
			"gvk":       gvk.String(),
			"namespace": namespace,
			"name":      name,
		})
		return deleteByNameNotFound, nil
	}
	if err != nil {
		logger.Log.Errorj(map[string]interface{}{
			"amsg":      "failed to delete configuration rendered resource by name",
			"gvk":       gvk.String(),
			"namespace": namespace,
			"name":      name,
			"err":       err.Error(),
		})
		return "", err
	}
	logger.Log.Infoj(map[string]interface{}{
		"amsg":      "configuration rendered resource delete request submitted by name",
		"gvk":       gvk.String(),
		"namespace": namespace,
		"name":      name,
	})
	return deleteByNameDeleted, nil
}

// DeleteTemplateRenderedResources cleans up extra resources in the delete path without relying on full template rendering.
// Strategy:
// 1) Extract apiVersion/kind from the template (no rendering); only render metadata.name;
// 2) If name rendering fails, fall back to listing and deleting by label middleware.cn/app=<owner.Name> (same GVK dimension).
func DeleteTemplateRenderedResources(ctx context.Context, cli client.Client, owner metav1.Object, quoter tools.Quoter) error {
	templateValuesBase, err := tools.GetTemplateValues(ctx, quoter)
	if err != nil {
		return fmt.Errorf("failed to get template values: %w", err)
	}

	pkgName := quoter.GetLabels()[v1.LabelPackageName]
	logger.Log.Infoj(map[string]interface{}{
		"amsg":                "starting cleanup of configuration rendered resources",
		"ownerName":           owner.GetName(),
		"ownerNamespace":      owner.GetNamespace(),
		"packageName":         pkgName,
		"configurationsCount": len(quoter.GetConfigurations()),
	})
	if pkgName == "" {
		return fmt.Errorf("package name is empty")
	}

	var errList []string
	for _, cfg := range quoter.GetConfigurations() {
		logger.Log.Infoj(map[string]interface{}{
			"amsg":           "processing configuration delete cleanup",
			"ownerName":      owner.GetName(),
			"ownerNamespace": owner.GetNamespace(),
			"packageName":    pkgName,
			"cfgName":        cfg.Name,
		})
		mc, getErr := Get(ctx, cli, cfg.Name, pkgName)
		if getErr != nil {
			logger.Log.Errorj(map[string]interface{}{
				"amsg":        "failed to get configuration",
				"ownerName":   owner.GetName(),
				"packageName": pkgName,
				"cfgName":     cfg.Name,
				"err":         getErr.Error(),
			})
			errList = append(errList, fmt.Sprintf("get configuration %s error: %v", cfg.Name, getErr))
			continue
		}

		apiVersion, kind, nameExpr := deleteTemplateLineValues(mc.Spec.Template)
		logger.Log.Infoj(map[string]interface{}{
			"amsg":       "extracted delete info from configuration template",
			"cfgName":    cfg.Name,
			"apiVersion": apiVersion,
			"kind":       kind,
			"nameExpr":   nameExpr,
		})
		if apiVersion == "" || kind == "" {
			errList = append(errList, fmt.Sprintf("configuration %s missing apiVersion/kind", cfg.Name))
			continue
		}
		gvk := schema.FromAPIVersionAndKind(apiVersion, kind)

		// CRDs are cluster-wide shared resources, must not be deleted with Middleware
		// CRDs are cluster-wide shared resources, must not be deleted with Middleware
		if gvk.Kind == "CustomResourceDefinition" {
			continue
		}

		templateValues := *templateValuesBase
		// Avoid nil pointer from missing values in template .Values.xxx: ensure map exists (missing keys may still be nil)
		if templateValues.Values == nil {
			templateValues.Values = make(map[string]interface{})
		}

		// Try to apply cfg.Values (failure does not block deletion, fallback follows)
		_ = applyConfigurationValues(ctx, cfg, &templateValues)

		renderedName := ""
		if nameExpr != "" {
			if n, renderErr := tools.TemplateParse(ctx, nameExpr, &templateValues); renderErr == nil {
				renderedName = normalizeRenderedName(n)
			} else {
				logger.Log.Warnj(map[string]interface{}{
					"amsg":     "failed to render configuration delete name, proceeding to fallback",
					"cfgName":  cfg.Name,
					"nameExpr": nameExpr,
					"err":      renderErr.Error(),
				})
			}
		}

		// Determine if the resource is namespaced: if so, use namespace=owner.Namespace; otherwise leave empty
		ns := ""
		tmp := new(unstructured.Unstructured)
		tmp.SetGroupVersionKind(gvk)
		namespaced, isNSErr := k8s.IsNamespaced(tmp)
		if isNSErr != nil {
			errList = append(errList, fmt.Sprintf("configuration %s isNamespaced error: %v", cfg.Name, isNSErr))
			continue
		}
		if namespaced {
			ns = owner.GetNamespace()
		}
		logger.Log.Infoj(map[string]interface{}{
			"amsg":         "configuration delete target resolved",
			"cfgName":      cfg.Name,
			"gvk":          gvk.String(),
			"renderedName": renderedName,
			"namespaced":   namespaced,
			"namespace":    ns,
		})

		// Prefer deletion by rendered name
		if renderedName != "" {
			if delResult, delErr := deleteByGVKAndName(ctx, cli, gvk, ns, renderedName); delErr == nil && delResult == deleteByNameDeleted {
				logger.Log.Infoj(map[string]interface{}{
					"amsg":         "configuration deleted by name, skipping fallback",
					"cfgName":      cfg.Name,
					"gvk":          gvk.String(),
					"renderedName": renderedName,
					"namespace":    ns,
				})
				continue
			} else if delErr == nil {
				logger.Log.Infoj(map[string]interface{}{
					"amsg":         "configuration not found by name, proceeding to fallback",
					"cfgName":      cfg.Name,
					"gvk":          gvk.String(),
					"renderedName": renderedName,
					"namespace":    ns,
				})
			} else {
				// Deletion failed: continue to fallback list deletion
				logger.Log.Warnf("delete %s %s/%s failed, fallback to list: %v", gvk.String(), ns, renderedName, delErr)
			}
		}

		// Fallback: delete by label middleware.cn/app=<owner.Name> + GVK list
		logger.Log.Infoj(map[string]interface{}{
			"amsg":      "configuration delete entering fallback",
			"cfgName":   cfg.Name,
			"gvk":       gvk.String(),
			"namespace": ns,
			"selector":  fmt.Sprintf("%s=%s", v1.LabelApp, owner.GetName()),
		})
		items, listErr := k8s.ListCustomResources(ctx, cli, ns, gvk, client.MatchingLabels{v1.LabelApp: owner.GetName()})
		if listErr != nil && !errors.IsNotFound(listErr) {
			errList = append(errList, fmt.Sprintf("configuration %s list fallback error: %v", cfg.Name, listErr))
			continue
		}
		logger.Log.Infoj(map[string]interface{}{
			"amsg":      "configuration fallback list query completed",
			"cfgName":   cfg.Name,
			"gvk":       gvk.String(),
			"namespace": ns,
			"items":     len(items),
		})
		for _, item := range items {
			logger.Log.Infoj(map[string]interface{}{
				"amsg":      "configuration fallback deleting object",
				"cfgName":   cfg.Name,
				"gvk":       gvk.String(),
				"namespace": item.GetNamespace(),
				"name":      item.GetName(),
			})
			obj := item.DeepCopy()
			obj.SetGroupVersionKind(gvk)
			if delErr := k8s.DeleteCustomResource(ctx, cli, obj); delErr != nil && !errors.IsNotFound(delErr) {
				errList = append(errList, fmt.Sprintf("configuration %s delete fallback %s/%s error: %v", cfg.Name, obj.GetNamespace(), obj.GetName(), delErr))
			}
		}
	}

	if len(errList) > 0 {
		return fmt.Errorf("%s", strings.Join(errList, ";"))
	}
	return nil
}
