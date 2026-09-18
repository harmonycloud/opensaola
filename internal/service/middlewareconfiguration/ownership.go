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
	"fmt"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func ownerIdentity(owner metav1.Object) (apiVersion, kind, name string, uid types.UID) {
	name = owner.GetName()
	uid = owner.GetUID()
	switch owner.(type) {
	case *v1.Middleware:
		return v1.GroupVersion.String(), "Middleware", name, uid
	case *v1.MiddlewareOperator:
		return v1.GroupVersion.String(), "MiddlewareOperator", name, uid
	default:
		return "", "", name, uid
	}
}

func sameControllerOwner(owner metav1.Object, ref *metav1.OwnerReference) bool {
	if owner == nil || ref == nil {
		return false
	}
	apiVersion, kind, name, uid := ownerIdentity(owner)
	if uid != "" && ref.UID != "" {
		return uid == ref.UID
	}
	if apiVersion != "" && kind != "" {
		return ref.APIVersion == apiVersion && ref.Kind == kind && ref.Name == name
	}
	return ref.Name == name
}

func resourceID(obj metav1.Object) string {
	if obj.GetNamespace() == "" {
		return obj.GetName()
	}
	return fmt.Sprintf("%s/%s", obj.GetNamespace(), obj.GetName())
}

func configurationPolicy(m *v1.MiddlewareConfiguration, obj metav1.Object, key string) string {
	if obj != nil {
		if value := obj.GetAnnotations()[key]; value != "" {
			return value
		}
	}
	if m != nil {
		return m.GetAnnotations()[key]
	}
	return ""
}

// configurationDisablePolicy resolves the cleanup policy used when a
// configuration template stops rendering a resource. An explicit policy wins
// except for CRDs, which are never deleted by the disable lifecycle; otherwise
// only PVC and PV resources are retained by default.
func configurationDisablePolicy(m *v1.MiddlewareConfiguration, obj metav1.Object) (string, error) {
	gvk := configurationResourceGVK(obj)
	policy := configurationPolicy(m, obj, v1.AnnotationConfigurationDisablePolicy)
	if policy != "" && policy != v1.ConfigurationDisablePolicyOrphan && policy != v1.ConfigurationDisablePolicyDelete {
		return "", fmt.Errorf("unsupported configuration disable policy %q: expected %q or %q",
			policy,
			v1.ConfigurationDisablePolicyDelete,
			v1.ConfigurationDisablePolicyOrphan,
		)
	}
	if isCustomResourceDefinition(gvk) {
		return v1.ConfigurationDisablePolicyOrphan, nil
	}
	if policy == "" {
		return defaultConfigurationDisablePolicy(gvk), nil
	}
	return policy, nil
}

func configurationResourceGVK(obj metav1.Object) schema.GroupVersionKind {
	if obj == nil {
		return schema.GroupVersionKind{}
	}
	if withGVK, ok := obj.(interface {
		GroupVersionKind() schema.GroupVersionKind
	}); ok {
		return withGVK.GroupVersionKind()
	}
	return schema.GroupVersionKind{}
}

func defaultConfigurationDisablePolicy(gvk schema.GroupVersionKind) string {
	if isDefaultOrphanConfigurationGVK(gvk) {
		return v1.ConfigurationDisablePolicyOrphan
	}
	return v1.ConfigurationDisablePolicyDelete
}

func isDefaultOrphanConfigurationGVK(gvk schema.GroupVersionKind) bool {
	if isCustomResourceDefinition(gvk) {
		return true
	}
	return gvk.Group == "" && (gvk.Kind == "PersistentVolume" || gvk.Kind == "PersistentVolumeClaim")
}

func isCustomResourceDefinition(gvk schema.GroupVersionKind) bool {
	return gvk.Group == "apiextensions.k8s.io" && gvk.Kind == "CustomResourceDefinition"
}

// hasConfigurationLifecycleMarkers proves that a cluster-scoped rendered
// resource was already managed by this exact owner/configuration pair. It is
// deliberately stricter than the legacy app-name marker because owner names
// are not globally unique.
func hasConfigurationLifecycleMarkers(owner metav1.Object, obj metav1.Object, configurationName string, configurationUID types.UID) bool {
	if owner == nil || obj == nil || configurationName == "" || owner.GetUID() == "" {
		return false
	}
	labels := obj.GetLabels()
	annotations := obj.GetAnnotations()
	if labels[v1.LabelApp] != owner.GetName() || annotations[v1.LabelConfigurations] != configurationName {
		return false
	}
	if annotations[v1.AnnotationConfigurationOwnerUID] != string(owner.GetUID()) {
		return false
	}
	if configurationUID != "" && annotations[v1.AnnotationConfigurationUID] != string(configurationUID) {
		return false
	}
	return true
}

// shouldDeleteDisabledRenderedResource validates the status snapshot against
// the live object before an opt-in disable cleanup. The existing
// configurationDeletePolicy intentionally permits force-delete; this helper
// must never inherit that behavior because a disabled feature must not remove
// externally owned or recreated resources.
func shouldDeleteDisabledRenderedResource(owner metav1.Object, obj metav1.Object, resource v1.RenderedConfigurationResource) bool {
	if owner == nil || obj == nil || resource.DisablePolicy != v1.ConfigurationDisablePolicyDelete {
		return false
	}
	if resource.OwnerUID == "" || owner.GetUID() != resource.OwnerUID {
		return false
	}
	if resource.ResourceUID == "" || obj.GetUID() != resource.ResourceUID {
		return false
	}
	if !hasConfigurationLifecycleMarkers(owner, obj, resource.ConfigurationName, resource.ConfigurationUID) {
		return false
	}
	controller := metav1.GetControllerOf(obj)
	// Only same-namespace resources can carry a namespaced owner's controller
	// reference. Cross-namespace resources use the UID/marker checks above,
	// just like cluster-scoped resources, while still rejecting foreign owners.
	if resource.Namespaced && obj.GetNamespace() == owner.GetNamespace() {
		return sameControllerOwner(owner, controller)
	}
	return controller == nil || sameControllerOwner(owner, controller)
}

func shouldSetControllerReference(owner metav1.Object, old metav1.Object, m *v1.MiddlewareConfiguration, desired metav1.Object) (bool, error) {
	if old == nil {
		return true, nil
	}
	controller := metav1.GetControllerOf(old)
	if controller == nil || sameControllerOwner(owner, controller) {
		return true, nil
	}
	if configurationPolicy(m, desired, v1.AnnotationConfigurationOwnershipPolicy) == v1.ConfigurationOwnershipPolicyManaged {
		return false, fmt.Errorf("configuration resource %s is already controlled by %s %s/%s; refusing to take controller ownership",
			resourceID(old), controller.APIVersion, controller.Kind, controller.Name)
	}
	return false, nil
}

func isOpenSaolaManagedConfiguration(owner metav1.Object, obj metav1.Object, configurationName string) bool {
	if owner == nil || obj == nil || configurationName == "" {
		return false
	}
	labels := obj.GetLabels()
	annotations := obj.GetAnnotations()
	return labels[v1.LabelApp] == owner.GetName() && annotations[v1.LabelConfigurations] == configurationName
}

func shouldDeleteRenderedResource(owner metav1.Object, obj metav1.Object, configurationName, deletePolicy string) bool {
	if obj == nil {
		return false
	}
	if deletePolicy == v1.ConfigurationDeletePolicyOrphan {
		return false
	}
	if deletePolicy == v1.ConfigurationDeletePolicyDelete {
		return true
	}
	controller := metav1.GetControllerOf(obj)
	if controller != nil {
		return sameControllerOwner(owner, controller)
	}
	return isOpenSaolaManagedConfiguration(owner, obj, configurationName)
}

// removeControllerOwnerReference drops any controller reference pointing at
// owner while preserving unrelated owners. Resources marked to outlive their
// owner (configurationDisablePolicy=orphan) must stay outside the owner's
// garbage-collection domain; on update this also migrates resources that a
// previous revision had already taken controller ownership of.
func removeControllerOwnerReference(refs []metav1.OwnerReference, owner metav1.Object) []metav1.OwnerReference {
	if len(refs) == 0 || owner == nil {
		return refs
	}
	kept := make([]metav1.OwnerReference, 0, len(refs))
	for _, ref := range refs {
		if ref.Controller != nil && *ref.Controller && ref.UID == owner.GetUID() {
			continue
		}
		kept = append(kept, ref)
	}
	return kept
}

// stripLiveControllerOwnerReference removes the owner's controller reference
// from the live object with an explicit merge patch. Server-side apply only
// drops fields the current field manager owns, and older references may
// predate it, so migration of orphan-marked resources cannot rely on apply
// alone.
func stripLiveControllerOwnerReference(ctx context.Context, cli client.Client, obj *unstructured.Unstructured, owner metav1.Object) error {
	live := &unstructured.Unstructured{}
	live.SetGroupVersionKind(obj.GroupVersionKind())
	if err := cli.Get(ctx, client.ObjectKeyFromObject(obj), live); err != nil {
		return err
	}
	controller := metav1.GetControllerOf(live)
	if controller == nil || controller.UID != owner.GetUID() {
		return nil
	}
	base := client.MergeFrom(live.DeepCopy())
	live.SetOwnerReferences(removeControllerOwnerReference(live.GetOwnerReferences(), owner))
	return cli.Patch(ctx, live, base)
}

// resolveConfigurationDeletePolicy resolves the effective delete policy for a
// rendered resource. An explicit configurationDeletePolicy wins; otherwise an
// orphan disable policy also keeps the resource when its owner is deleted,
// because the resource was created to outlive the owner.
func resolveConfigurationDeletePolicy(m *v1.MiddlewareConfiguration, obj metav1.Object) string {
	if policy := configurationPolicy(m, obj, v1.AnnotationConfigurationDeletePolicy); policy != "" {
		return policy
	}
	if disablePolicy, err := configurationDisablePolicy(m, obj); err == nil && disablePolicy == v1.ConfigurationDisablePolicyOrphan {
		return v1.ConfigurationDeletePolicyOrphan
	}
	return ""
}
