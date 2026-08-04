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
	"fmt"
	"sort"
	"strings"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	"github.com/harmonycloud/opensaola/internal/k8s"
	"github.com/harmonycloud/opensaola/internal/service/consts"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// ReconcileRenderedConfigurationResources applies the currently rendered MCF
// resources and, on update, removes prior lifecycle-managed resources that no
// longer render. Resources are deleted only when their prior snapshot resolved
// to configurationDisablePolicy=delete.
func ReconcileRenderedConfigurationResources(
	ctx context.Context,
	cli client.Client,
	owner metav1.Object,
	action consts.HandleAction,
	configurations []*v1.MiddlewareConfiguration,
	previous []v1.RenderedConfigurationResource,
) ([]v1.RenderedConfigurationResource, error) {
	if action != consts.HandleActionPublish && action != consts.HandleActionUpdate {
		return previous, nil
	}

	current := make([]v1.RenderedConfigurationResource, 0, len(configurations))
	currentlyRendered := make([]v1.RenderedConfigurationResource, 0, len(configurations))
	for _, configuration := range configurations {
		result, err := Handle(ctx, cli, owner, action, configuration)
		if err != nil {
			return nil, fmt.Errorf("%s middleware configuration %s error: %w", action, configuration.Name, err)
		}
		if result != nil {
			currentlyRendered = append(currentlyRendered, result.identity)
			if result.inventory != nil {
				current = append(current, *result.inventory)
			}
		}
	}

	var err error
	currentlyRendered, err = normalizeRenderedConfigurationResources(currentlyRendered)
	if err != nil {
		return nil, err
	}
	current, err = normalizeRenderedConfigurationResources(current)
	if err != nil {
		return nil, err
	}
	if action == consts.HandleActionUpdate {
		if err := DeleteDisabledRenderedConfigurationResources(ctx, cli, owner, previous, currentlyRendered); err != nil {
			return nil, err
		}
	}
	return current, nil
}

// DeleteDisabledRenderedConfigurationResources removes only stale inventory
// entries whose disable policy opted into delete. It never derives deletion
// targets from the newly rendered template, so an empty template and a removed
// spec.configurations entry are handled identically.
func DeleteDisabledRenderedConfigurationResources(
	ctx context.Context,
	cli client.Client,
	owner metav1.Object,
	previous []v1.RenderedConfigurationResource,
	currentlyRendered []v1.RenderedConfigurationResource,
) error {
	currentKeys := make(map[string]struct{}, len(currentlyRendered))
	for _, resource := range currentlyRendered {
		if key := renderedConfigurationResourceKey(resource); key != "" {
			currentKeys[key] = struct{}{}
		}
	}

	// Old statuses are user-visible and can survive controller upgrades. Deduping
	// by physical identity avoids duplicate deletion attempts while favoring an
	// explicit delete policy if a legacy status happened to contain duplicates.
	stale := make(map[string]v1.RenderedConfigurationResource)
	for _, resource := range previous {
		key := renderedConfigurationResourceKey(resource)
		if key == "" {
			log.FromContext(ctx).Info("skipping invalid rendered configuration inventory entry", "warning", true, "resource", resource)
			continue
		}
		if _, stillRendered := currentKeys[key]; stillRendered {
			continue
		}
		if old, found := stale[key]; !found || (old.DisablePolicy != v1.ConfigurationDisablePolicyDelete && resource.DisablePolicy == v1.ConfigurationDisablePolicyDelete) {
			stale[key] = resource
		}
	}

	keys := make([]string, 0, len(stale))
	for key := range stale {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if err := deleteDisabledRenderedConfigurationResource(ctx, cli, owner, stale[key]); err != nil {
			return err
		}
	}
	return nil
}

func deleteDisabledRenderedConfigurationResource(ctx context.Context, cli client.Client, owner metav1.Object, resource v1.RenderedConfigurationResource) error {
	if resource.DisablePolicy != v1.ConfigurationDisablePolicyDelete {
		log.FromContext(ctx).Info("leaving disabled configuration resource orphaned by policy",
			"configuration", resource.ConfigurationName,
			"gvk", renderedConfigurationResourceKey(resource),
			"namespace", resource.Namespace,
			"name", resource.Name,
		)
		return nil
	}

	gvk := schema.GroupVersionKind{Group: resource.Group, Version: resource.Version, Kind: resource.Kind}
	if isCustomResourceDefinition(gvk) {
		log.FromContext(ctx).Info("leaving disabled CustomResourceDefinition intact",
			"configuration", resource.ConfigurationName,
			"gvk", gvk.String(),
			"name", resource.Name,
		)
		return nil
	}
	obj, err := k8s.GetCustomResource(ctx, cli, resource.Name, resource.Namespace, gvk)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		if k8s.IsCRDNotInstalled(err) {
			log.FromContext(ctx).Info("skipping disabled configuration cleanup because its CRD is not installed",
				"configuration", resource.ConfigurationName,
				"gvk", gvk.String(),
			)
			return nil
		}
		return fmt.Errorf("get disabled configuration resource %s %s/%s: %w", gvk.String(), resource.Namespace, resource.Name, err)
	}
	if !shouldDeleteDisabledRenderedResource(owner, obj, resource) {
		log.FromContext(ctx).Info("skipping disabled configuration cleanup because live identity is not managed by this inventory",
			"warning", true,
			"configuration", resource.ConfigurationName,
			"gvk", gvk.String(),
			"namespace", resource.Namespace,
			"name", resource.Name,
			"ownerUID", resource.OwnerUID,
			"resourceUID", resource.ResourceUID,
			"controllerOwner", metav1.GetControllerOf(obj),
		)
		return nil
	}
	if err := k8s.DeleteCustomResource(ctx, cli, obj); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete disabled configuration resource %s %s/%s: %w", gvk.String(), resource.Namespace, resource.Name, err)
	}
	log.FromContext(ctx).Info("deleted disabled configuration resource",
		"configuration", resource.ConfigurationName,
		"gvk", gvk.String(),
		"namespace", resource.Namespace,
		"name", resource.Name,
	)
	return nil
}

func normalizeRenderedConfigurationResources(resources []v1.RenderedConfigurationResource) ([]v1.RenderedConfigurationResource, error) {
	byKey := make(map[string]v1.RenderedConfigurationResource, len(resources))
	for _, resource := range resources {
		key := renderedConfigurationResourceKey(resource)
		if key == "" {
			return nil, fmt.Errorf("rendered configuration %q has an incomplete resource identity", resource.ConfigurationName)
		}
		if previous, found := byKey[key]; found {
			return nil, fmt.Errorf("configurations %q and %q render the same resource %s; shared rendered resources are not supported", previous.ConfigurationName, resource.ConfigurationName, key)
		}
		byKey[key] = resource
	}

	keys := make([]string, 0, len(byKey))
	for key := range byKey {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]v1.RenderedConfigurationResource, 0, len(keys))
	for _, key := range keys {
		result = append(result, byKey[key])
	}
	return result, nil
}

func renderedConfigurationResourceKey(resource v1.RenderedConfigurationResource) string {
	if resource.Version == "" || resource.Kind == "" || resource.Name == "" {
		return ""
	}
	return strings.Join([]string{resource.Group, resource.Version, resource.Kind, resource.Namespace, resource.Name}, "/")
}
