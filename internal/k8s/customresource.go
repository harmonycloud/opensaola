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
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/harmonycloud/opensaola/internal/k8s/kubeclient"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ErrGroupVersionNotFound indicates that the API group/version is not available in the cluster,
// typically because the corresponding CRD is not installed.
var ErrGroupVersionNotFound = fmt.Errorf("group version not found in cluster")

// IsCRDNotInstalled checks if an error indicates that a CRD is not installed.
func IsCRDNotInstalled(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrGroupVersionNotFound) {
		return true
	}
	errMsg := err.Error()
	return strings.Contains(errMsg, "the server could not find the requested resource") ||
		strings.Contains(errMsg, "no matches for kind")
}

// CreateCustomResource creates a CustomResource.
func CreateCustomResource(ctx context.Context, cli client.Client, cr *unstructured.Unstructured) error {
	// First check if it already exists
	_, err := GetCustomResource(ctx, cli, cr.GetName(), cr.GetNamespace(), cr.GroupVersionKind())
	if err == nil {
		return apierrors.NewAlreadyExists(schema.GroupResource{
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
		return false, fmt.Errorf("%w: %s: %v", ErrGroupVersionNotFound, gvk.GroupVersion().String(), err)
	}

	// Iterate through resources to find a match
	for _, resource := range resourceList.APIResources {
		if resource.Kind == gvk.Kind {
			return resource.Namespaced, nil
		}
	}

	return false, fmt.Errorf("%w: kind %s not found in %s", ErrGroupVersionNotFound, gvk.Kind, gvk.GroupVersion().String())
}

// ValidateActionMetadata protects history both on its business target and
// when a one-off Action explicitly addresses the history Secret as its target.
func ValidateActionMetadata(live, after *unstructured.Unstructured) error {
	for _, obj := range []*unstructured.Unstructured{live, after} {
		if obj != nil && obj.GetAPIVersion() == "v1" && obj.GetKind() == "Secret" {
			labels := obj.GetLabels()
			_, record := labels[intentRecordLabel]
			_, ledger := labels[labelIntentLedger]
			if record || ledger {
				return managedWriteBlocked("action cannot edit resource history Secrets")
			}
		}
	}
	if live == nil {
		live = &unstructured.Unstructured{}
	}
	for _, key := range []string{AnnotationResourceIntent, AnnotationResourceIntentOperation} {
		before, had := live.GetAnnotations()[key]
		next, has := after.GetAnnotations()[key]
		if had != has || before != next {
			return managedWriteBlocked("action edit cannot change field-management annotations")
		}
	}
	before, had := live.GetLabels()[LabelResourceIntentGCFence]
	next, has := after.GetLabels()[LabelResourceIntentGCFence]
	if had != has || before != next || !reflect.DeepEqual(live.GetManagedFields(), after.GetManagedFields()) {
		return managedWriteBlocked("action edit cannot change field-management metadata")
	}
	return nil
}

// PatchActionResource sends only the difference produced by a one-off action.
// UID and resourceVersion tests fence edits to stale or replaced objects.
func PatchActionResource(ctx context.Context, cli client.Client, live, after *unstructured.Unstructured) error {
	if isImmutableResource(live) {
		return nil
	}
	if live.GetUID() == "" || live.GetResourceVersion() == "" || live.GetUID() != after.GetUID() || live.GetResourceVersion() != after.GetResourceVersion() ||
		live.GroupVersionKind() != after.GroupVersionKind() || client.ObjectKeyFromObject(live) != client.ObjectKeyFromObject(after) {
		return managedWriteBlocked("action edit requires the original target identity and resourceVersion")
	}
	if err := ValidateActionMetadata(live, after); err != nil {
		return err
	}
	if reflect.DeepEqual(live.Object, after.Object) {
		return nil
	}
	patch, err := intentPatch(live, after)
	if err != nil {
		return err
	}
	return cli.Patch(ctx, after, patch, client.FieldOwner("opensaola-action"))
}
