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

package middlewareconfiguration

import (
	"context"
	"errors"
	"fmt"
	"strings"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	"github.com/harmonycloud/opensaola/internal/k8s"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

// migrateLegacyConfigurationReferences detaches retained resources of instances
// published before lifecycle inventory existed. Inspect the kinds named in the
// effective configuration list, without executing templates or claiming prunable objects.
func migrateLegacyConfigurationReferences(ctx context.Context, cli client.Client, owner client.Object, configs []v1.Configuration) error {
	var problems []error
	seen := make(map[string]bool, len(configs))
	for _, cfg := range configs {
		if seen[cfg.Name] {
			continue
		}
		seen[cfg.Name] = true
		mcf, err := Get(ctx, cli, cfg.Name, owner.GetLabels()[v1.LabelPackageName])
		if err == nil {
			err = detachLegacyConfigurationReferences(ctx, cli, owner, &mcf)
		}
		if err != nil {
			problems = append(problems, fmt.Errorf("migrate legacy Configuration %s: %w", cfg.Name, err))
		}
	}
	return errors.Join(problems...)
}

func detachLegacyConfigurationReferences(ctx context.Context, cli client.Client, owner client.Object, mcf *v1.MiddlewareConfiguration) error {
	if strings.TrimSpace(mcf.Spec.Template) == "" {
		return nil
	}
	var head metav1.TypeMeta
	// Already-valid YAML and inline JSON expose the kind directly. A templated
	// body may not parse as YAML until rendered, even when its GVK is literal.
	if err := yaml.Unmarshal([]byte(mcf.Spec.Template), &head); err != nil || head.APIVersion == "" || head.Kind == "" {
		version, kind, _, _ := deleteTemplateLineValues(mcf.Spec.Template)
		head.APIVersion, head.Kind = normalizeRenderedName(version), normalizeRenderedName(kind)
	}
	apiVersion, kind := head.APIVersion, head.Kind
	if apiVersion == "" || kind == "" || strings.Contains(apiVersion+kind, "{{") {
		return fmt.Errorf("no static apiVersion/kind for legacy ownership lookup")
	}
	gvk := schema.FromAPIVersionAndKind(apiVersion, kind)
	mapping, err := cli.RESTMapper().RESTMapping(gvk.GroupKind(), gvk.Version)
	if meta.IsNoMatchError(err) || k8s.IsCRDNotInstalled(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if mapping.Scope.Name() != meta.RESTScopeNameNamespace {
		return nil
	}
	items, err := k8s.ListCustomResources(ctx, cli, owner.GetNamespace(), gvk, client.MatchingLabels{v1.LabelApp: owner.GetName()})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for i := range items {
		live := &items[i]
		live.SetGroupVersionKind(gvk)
		if !canDetachConfigurationOwner(owner, live, mcf.Name) {
			continue
		}
		disablePolicy, err := configurationDisablePolicy(mcf, live)
		if err != nil {
			return err
		}
		if !configurationRequiresControllerReference(mcf, live, disablePolicy) {
			if err := stripLiveConfigurationOwnerReference(ctx, cli, live, owner); err != nil && !apierrors.IsNotFound(err) {
				return err
			}
		}
	}
	return nil
}
