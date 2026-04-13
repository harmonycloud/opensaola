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
	"context"
	"fmt"

	v1 "github.com/OpenSaola/opensaola/api/v1"
	"github.com/OpenSaola/opensaola/internal/k8s/kubeclient"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// CreateOrUpdateCustomResource creates or updates a CustomResource.
func CreateOrUpdateCustomResource(ctx context.Context, cli client.Client, cr *unstructured.Unstructured) error {
	old, err := GetCustomResource(ctx, cli, cr.GetName(), cr.GetNamespace(), cr.GroupVersionKind())
	if err != nil && !errors.IsNotFound(err) {
		return err
	}
	if old == nil {
		return CreateCustomResource(ctx, cli, cr)
	}
	return UpdateCustomResource(ctx, cli, cr)
}

// CreateOrPatchCustomResource creates or patches a CustomResource.
func CreateOrPatchCustomResource(ctx context.Context, cli client.Client, cr *unstructured.Unstructured) error {
	old, err := GetCustomResource(ctx, cli, cr.GetName(), cr.GetNamespace(), cr.GroupVersionKind())
	if err != nil && !errors.IsNotFound(err) {
		return err
	}
	if old == nil {
		return CreateCustomResource(ctx, cli, cr)
	}
	return PatchCustomResource(ctx, cli, cr)
}

// CreateCustomResource creates a CustomResource.
func CreateCustomResource(ctx context.Context, cli client.Client, cr *unstructured.Unstructured) error {
	// First check if it already exists
	_, err := GetCustomResource(ctx, cli, cr.GetName(), cr.GetNamespace(), cr.GroupVersionKind())
	if err == nil {
		return errors.NewAlreadyExists(schema.GroupResource{
			Group:    cr.GroupVersionKind().Group,
			Resource: cr.GroupVersionKind().Kind,
		}, cr.GetName())
	}
	return cli.Create(ctx, cr)
}

// DeleteCustomResource deletes a CustomResource.
func DeleteCustomResource(ctx context.Context, cli client.Client, cr *unstructured.Unstructured) error {
	return cli.Delete(ctx, cr)
}

// UpdateCustomResource uses Server-Side Apply to update a CustomResource.
// (see English comment above)
func UpdateCustomResource(ctx context.Context, cli client.Client, cr *unstructured.Unstructured) error {
	if isImmutableResource(cr) {
		return nil
	}

	cr.SetResourceVersion("")
	cr.SetManagedFields(nil)
	if metadata, ok := cr.Object["metadata"].(map[string]interface{}); ok {
		delete(metadata, "creationTimestamp")
	}

	return cli.Patch(ctx, cr, client.Apply, client.FieldOwner(v1.FieldOwner), client.ForceOwnership)
}

// GetCustomResource retrieves a CustomResource.
func GetCustomResource(ctx context.Context, cli client.Client, name, namespace string, gvk schema.GroupVersionKind) (*unstructured.Unstructured, error) {
	cr := new(unstructured.Unstructured)
	cr.SetGroupVersionKind(gvk)
	err := cli.Get(ctx, client.ObjectKey{
		Name:      name,
		Namespace: namespace,
	}, cr)
	if err != nil {
		return nil, err
	}
	return cr, nil
}

// ListCustomResources lists CustomResources.
func ListCustomResources(ctx context.Context, cli client.Client, namespace string, gvk schema.GroupVersionKind, labelsSelector client.MatchingLabels) ([]unstructured.Unstructured, error) {
	list := new(unstructured.UnstructuredList)
	list.SetGroupVersionKind(gvk)
	listOpts := []client.ListOption{labelsSelector}
	if namespace != "" {
		listOpts = append(listOpts, client.InNamespace(namespace))
	}
	err := cli.List(ctx, list, listOpts...)
	if err != nil {
		return nil, err
	}

	results := make([]unstructured.Unstructured, len(list.Items))
	copy(results, list.Items)
	return results, nil
}

// isImmutableResource checks whether a resource is immutable.
func isImmutableResource(obj *unstructured.Unstructured) bool {
	gvk := obj.GroupVersionKind()

	// Define the list of immutable resource types
	immutableResources := map[string][]string{
		"batch/v1":      {"Job"},
		"batch/v1beta1": {"CronJob"}, // Some versions of CronJob also have immutable fields
		// Add more immutable resource types as needed
	}

	groupVersion := gvk.GroupVersion().String()
	if kinds, exists := immutableResources[groupVersion]; exists {
		for _, kind := range kinds {
			if gvk.Kind == kind {
				return true
			}
		}
	}

	return false
}

func IsNamespaced(obj *unstructured.Unstructured) (bool, error) {
	// Get the GroupVersionKind of the resource
	gvk := obj.GroupVersionKind()
	discoveryClient, err := kubeclient.GetDiscoveryClient()
	if err != nil {
		return false, err
	}

	// Query metadata for all resources
	resourceList, err := discoveryClient.ServerResourcesForGroupVersion(gvk.GroupVersion().String())
	if err != nil {
		return false, fmt.Errorf("failed to get resource metadata: %w", err)
	}

	// Iterate through resources to find a match
	for _, resource := range resourceList.APIResources {
		if resource.Kind == gvk.Kind {
			return resource.Namespaced, nil
		}
	}

	return false, fmt.Errorf("matching resource type not found: %v", gvk)
}

// PatchCustomResource uses Server-Side Apply to update a CustomResource.
// SSA only manages fields owned by "opensaola"; fields added manually
// (owned by other managers like "kubectl") are preserved automatically.
// (see English comment above)
func PatchCustomResource(ctx context.Context, cli client.Client, cr *unstructured.Unstructured) error {
	if isImmutableResource(cr) {
		return nil
	}

	// SSA requires clean metadata: no resourceVersion, no managedFields, no creationTimestamp.
	// (see English comment above)
	cr.SetResourceVersion("")
	cr.SetManagedFields(nil)
	if metadata, ok := cr.Object["metadata"].(map[string]interface{}); ok {
		delete(metadata, "creationTimestamp")
	}

	return cli.Patch(ctx, cr, client.Apply, client.FieldOwner(v1.FieldOwner), client.ForceOwnership)
}
