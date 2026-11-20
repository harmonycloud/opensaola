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
	"sigs.k8s.io/controller-runtime/pkg/client"
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
	id, err := parseDeleteType(mcf.Spec.Template)
	if err != nil {
		return err
	}
	if id.APIVersion.Dynamic() || id.Kind.Dynamic() {
		return fmt.Errorf("no static apiVersion/kind for legacy ownership lookup")
	}
	version, err := id.APIVersion.Render(ctx, nil)
	if err != nil {
		return fmt.Errorf("legacy apiVersion: %w", err)
	}
	kind, err := id.Kind.Render(ctx, nil)
	if err != nil {
		return fmt.Errorf("legacy kind: %w", err)
	}
	gvk, err := deleteResourceGVK(version, kind)
	if err != nil {
		return err
	}
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
