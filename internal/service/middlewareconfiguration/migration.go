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
	"slices"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	"github.com/harmonycloud/opensaola/internal/k8s"
	"github.com/harmonycloud/opensaola/internal/service/middlewarebaseline"
	"github.com/harmonycloud/opensaola/internal/service/middlewareoperatorbaseline"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// MigrateConfigurationOwnerReferences upgrades the inventory produced before
// publication recorded whether each object should carry the owner's reference.
// Only metadata of the identified live object is patched; templates and actions
// must not be replayed when a reconciled owner is first seen after an upgrade.
func MigrateConfigurationOwnerReferences(ctx context.Context, cli client.Client, owner client.Object) error {
	stored, storedGeneration := configurationInventory(owner)
	if stored == nil {
		return fmt.Errorf("unsupported Configuration owner %T", owner)
	}
	inventory := *stored
	generation := *storedGeneration
	var updated []v1.RenderedConfigurationResource
	var errs []error
	if len(inventory) == 0 && generation == 0 {
		// Before inventory existed, the published owner and Configuration name
		// are the only reliable way to discover a resource. Record completion
		// without granting disable cleanup authority over previously untracked objects.
		configurations, err := legacyConfigurationSources(ctx, cli, owner)
		if err != nil {
			return err
		}
		if err := migrateLegacyConfigurationReferences(ctx, cli, owner, configurations); err != nil {
			return err
		}
		generation = owner.GetGeneration()
	}
	for i := range inventory {
		entry := &inventory[i]
		if entry.ControllerReferenceRequired != nil || !entry.Namespaced || entry.Namespace != owner.GetNamespace() ||
			entry.OwnerUID == "" || entry.OwnerUID != owner.GetUID() || entry.ResourceUID == "" || renderedConfigurationResourceKey(*entry) == "" {
			continue
		}
		required, err := migrateConfigurationReference(ctx, cli, owner, *entry)
		if err != nil {
			errs = append(errs, fmt.Errorf("migrate Configuration %s resource %s/%s: %w", entry.ConfigurationName, entry.Namespace, entry.Name, err))
			continue
		}
		if required == nil {
			continue
		}
		if updated == nil {
			updated = slices.Clone(inventory)
		}
		updated[i].ControllerReferenceRequired = required
	}
	if updated == nil && generation == *storedGeneration {
		return errors.Join(errs...)
	}
	// A concurrent status or spec change must cause a new read. A retry after
	// a successfully removed reference can safely finish the inventory patch.
	patched, ok := owner.DeepCopyObject().(client.Object)
	if !ok {
		return fmt.Errorf("invalid Configuration owner copy %T", owner)
	}
	targetEntries, targetGeneration := configurationInventory(patched)
	if updated != nil {
		*targetEntries = updated
	}
	*targetGeneration = generation
	if err := cli.Status().Patch(ctx, patched, client.MergeFromWithOptions(owner, client.MergeFromWithOptimisticLock{})); err != nil {
		errs = append(errs, err)
	} else if *targetGeneration != generation || (updated != nil && !equality.Semantic.DeepEqual(updated, *targetEntries)) {
		errs = append(errs, fmt.Errorf("Configuration inventory migration was not persisted; verify that the installed MID/MO CRDs support controllerReferenceRequired"))
	} else {
		// Keep later writes in this reconcile consistent without replacing the
		// in-memory spec or TypeMeta with the result of a status-only request.
		if updated != nil {
			*stored = updated
		}
		*storedGeneration = generation
		owner.SetResourceVersion(patched.GetResourceVersion())
	}
	return errors.Join(errs...)
}

func configurationInventory(owner client.Object) (*[]v1.RenderedConfigurationResource, *int64) {
	switch m := owner.(type) {
	case *v1.Middleware:
		return &m.Status.RenderedConfigurationResources, &m.Status.RenderedConfigurationResourcesGeneration
	case *v1.MiddlewareOperator:
		return &m.Status.RenderedConfigurationResources, &m.Status.RenderedConfigurationResourcesGeneration
	default:
		return nil, nil
	}
}

// Legacy discovery needs only the names of referenced Configurations. It reads
// both sources so configurations inherited from a baseline are also inspected.
func legacyConfigurationSources(ctx context.Context, cli client.Client, owner client.Object) ([]v1.Configuration, error) {
	pkg := owner.GetLabels()[v1.LabelPackageName]
	switch m := owner.(type) {
	case *v1.Middleware:
		if m.Spec.Baseline == "" {
			return m.Spec.Configurations, nil
		}
		baseline, err := middlewarebaseline.Get(ctx, cli, m.Spec.Baseline, pkg)
		if err != nil {
			return nil, err
		}
		return append(baseline.Spec.Configurations, m.Spec.Configurations...), nil
	case *v1.MiddlewareOperator:
		if m.Spec.Baseline == "" {
			return m.Spec.Configurations, nil
		}
		baseline, err := middlewareoperatorbaseline.Get(ctx, cli, m.Spec.Baseline, pkg)
		if err != nil {
			return nil, err
		}
		return append(baseline.Spec.Configurations, m.Spec.Configurations...), nil
	default:
		return nil, fmt.Errorf("unsupported Configuration owner %T", owner)
	}
}

func migrateConfigurationReference(ctx context.Context, cli client.Client, owner client.Object, entry v1.RenderedConfigurationResource) (*bool, error) {
	live := &unstructured.Unstructured{}
	live.SetGroupVersionKind(schema.GroupVersionKind{Group: entry.Group, Version: entry.Version, Kind: entry.Kind})
	if err := k8s.NewManagedResourceWriter(cli).Reader.Get(ctx, client.ObjectKey{Namespace: entry.Namespace, Name: entry.Name}, live); err != nil {
		if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
			return nil, nil
		}
		return nil, err
	}
	if live.GetUID() != entry.ResourceUID || !hasConfigurationLifecycleMarkers(owner, live, entry.ConfigurationName, entry.ConfigurationUID) {
		return nil, nil
	}
	if ref := metav1.GetControllerOf(live); ref != nil && ref.UID != owner.GetUID() {
		return nil, nil
	}

	// The resource annotation is the published template's override. When the
	// policy is inherited, use the same package-scoped MCF as owner deletion.
	var configuration *v1.MiddlewareConfiguration
	if entry.DisablePolicy != v1.ConfigurationDisablePolicyOrphan && live.GetAnnotations()[v1.AnnotationConfigurationDeletePolicy] == "" {
		mcf, err := Get(ctx, cli, entry.ConfigurationName, owner.GetLabels()[v1.LabelPackageName])
		if err != nil {
			return nil, err
		}
		configuration = &mcf
	}
	required := configurationRequiresControllerReference(configuration, live, entry.DisablePolicy)
	if !required {
		if err := stripLiveConfigurationOwnerReference(ctx, cli, live, owner); err != nil {
			return nil, err
		}
	}
	return &required, nil
}
