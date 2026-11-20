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

	v1 "github.com/harmonycloud/opensaola/api/v1"
	"github.com/harmonycloud/opensaola/internal/k8s"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// A published record fixes the deletion target even when it has since been
// removed or replaced. Only releases without a usable record need discovery.
func retireRecordedConfiguration(ctx context.Context, cli client.Client, owner metav1.Object, configuration *v1.MiddlewareConfiguration) (bool, error) {
	obj, ok := owner.(client.Object)
	if !ok || owner.GetUID() == "" {
		return false, nil
	}
	records, _ := configurationInventory(obj)
	if records == nil {
		return false, nil
	}

	handled := false
	for _, entry := range *records {
		if entry.ConfigurationName != configuration.Name || entry.OwnerUID != owner.GetUID() || !hasPublishedIdentity(entry) {
			continue
		}
		handled = true
		gvk := schema.GroupVersionKind{Group: entry.Group, Version: entry.Version, Kind: entry.Kind}
		if isCustomResourceDefinition(gvk) {
			continue
		}
		live, err := getRecordedConfigurationResource(ctx, cli, entry, gvk)
		if err != nil {
			return true, fmt.Errorf("get recorded %s %s/%s: %w", gvk, entry.Namespace, entry.Name, err)
		}
		if live == nil || live.GetUID() != entry.ResourceUID {
			continue
		}
		if _, err := retireConfigurationResource(ctx, cli, owner, live, configuration); err != nil {
			return true, err
		}
	}
	return handled, nil
}

// A CRD can stop serving the published version without replacing the stored
// object. Kubernetes identity is unchanged across versions of the same kind.
func getRecordedConfigurationResource(ctx context.Context, cli client.Client, entry v1.RenderedConfigurationResource, gvk schema.GroupVersionKind) (*unstructured.Unstructured, error) {
	return getRecordedConfigurationResourceWithReader(ctx, cli, cli, entry, gvk)
}

func getRecordedConfigurationResourceWithReader(ctx context.Context, cli client.Client, reader client.Reader, entry v1.RenderedConfigurationResource, gvk schema.GroupVersionKind) (*unstructured.Unstructured, error) {
	get := func(version schema.GroupVersionKind) (*unstructured.Unstructured, error) {
		obj := &unstructured.Unstructured{}
		obj.SetGroupVersionKind(version)
		if err := reader.Get(ctx, client.ObjectKey{Namespace: entry.Namespace, Name: entry.Name}, obj); err != nil {
			return nil, err
		}
		return obj, nil
	}
	live, err := get(gvk)
	if !configurationVersionUnavailable(err) {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return live, err
	}
	// A positive mapping can survive a CRD version change. Refresh only
	// after the resource endpoint itself disappears, never for an absent object.
	mapper := cli.RESTMapper()
	if provider, ok := mapper.(interface {
		FreshRESTMapper() (meta.RESTMapper, error)
	}); ok {
		mapper, err = provider.FreshRESTMapper()
		if err != nil {
			return nil, err
		}
	} else if resettable, ok := mapper.(meta.ResettableRESTMapper); ok {
		resettable.Reset()
	}
	mappings, resolveErr := mapper.RESTMappings(gvk.GroupKind())
	var missingKind *meta.NoKindMatchError
	if errors.As(resolveErr, &missingKind) {
		return nil, nil
	}
	if resolveErr != nil {
		return nil, resolveErr
	}
	for _, mapping := range mappings {
		if mapping.GroupVersionKind == gvk {
			continue
		}
		live, err = get(mapping.GroupVersionKind)
		if !configurationVersionUnavailable(err) {
			if apierrors.IsNotFound(err) {
				return nil, nil
			}
			return live, err
		}
	}
	return nil, fmt.Errorf("no reachable served version for recorded %s; retry discovery", gvk.GroupKind())
}

func configurationVersionUnavailable(err error) bool {
	return meta.IsNoMatchError(err) || k8s.IsCRDNotInstalled(err)
}

// A legacy configuration can resolve to a name already recorded for another
// source or another incarnation. Compare GroupKind across served API versions.
func recordedConfigurationConflict(owner metav1.Object, live *unstructured.Unstructured, configurationName string) bool {
	obj, ok := owner.(client.Object)
	if !ok || owner.GetUID() == "" {
		return false
	}
	entries, _ := configurationInventory(obj)
	if entries == nil {
		return false
	}
	found := false
	for _, entry := range *entries {
		if !atRecordedAddress(entry, live) || !hasPublishedIdentity(entry) {
			continue
		}
		found = true
		if matchesRecordedConfiguration(entry, owner, live, configurationName) {
			controller := metav1.GetControllerOf(live)
			return controller != nil && controller.UID != owner.GetUID()
		}
	}
	return found
}

func hasPublishedIdentity(entry v1.RenderedConfigurationResource) bool {
	return entry.OwnerUID != "" && entry.ResourceUID != "" && entry.ConfigurationName != "" &&
		renderedConfigurationResourceKey(entry) != "" && (!entry.Namespaced || entry.Namespace != "")
}

func atRecordedAddress(entry v1.RenderedConfigurationResource, live *unstructured.Unstructured) bool {
	gvk := live.GroupVersionKind()
	return entry.Group == gvk.Group && entry.Kind == gvk.Kind &&
		entry.Name == live.GetName() && entry.Namespace == live.GetNamespace()
}

func matchesRecordedConfiguration(entry v1.RenderedConfigurationResource, owner metav1.Object, live *unstructured.Unstructured, name string) bool {
	return entry.ConfigurationName == name && entry.OwnerUID == owner.GetUID() &&
		entry.ResourceUID != "" && entry.ResourceUID == live.GetUID() &&
		hasConfigurationLifecycleMarkers(owner, live, name, entry.ConfigurationUID)
}
