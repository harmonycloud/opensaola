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

package k8s

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

// configurationNeedsAdoption separates write authority from disable/delete
// authority. It runs against the exact live object fenced by the final patch.
// Only a proven OpenSaola logical source can bootstrap a new owner lifecycle.
func (w *ManagedResourceWriter) configurationNeedsAdoption(ctx context.Context, owner client.Object, live, desired *unstructured.Unstructured, configuration string) (bool, error) {
	identityErr := verifyIntentIdentity(owner, live, desired, configuration)
	if identityErr == nil {
		return false, nil
	}
	if owner.GetUID() == "" || owner.GetDeletionTimestamp() != nil || live.GetUID() == "" || live.GetDeletionTimestamp() != nil ||
		live.GroupVersionKind() != desired.GroupVersionKind() || client.ObjectKeyFromObject(live) != client.ObjectKeyFromObject(desired) ||
		live.GetLabels()[v1.LabelApp] != owner.GetName() || live.GetAnnotations()[v1.LabelConfigurations] != configuration {
		return false, identityErr
	}
	oldUID := types.UID(live.GetAnnotations()[v1.AnnotationConfigurationOwnerUID])
	if ref := metav1.GetControllerOf(live); ref != nil {
		expected, err := intentOwnerReference(owner)
		if err != nil {
			return false, err
		}
		gv, err := schema.ParseGroupVersion(ref.APIVersion)
		if err != nil || gv.Group != v1.GroupVersion.Group || ref.Kind != expected.Kind || ref.Name != expected.Name ||
			live.GetNamespace() != owner.GetNamespace() || ref.UID == "" || (oldUID != "" && oldUID != ref.UID) {
			return false, identityErr
		}
		oldUID = ref.UID
	}
	if err := w.checkConfigurationClaimants(ctx, owner, live, configuration, oldUID); err != nil {
		return false, err
	}
	return true, nil
}

// A deleting or paused owner still claims its resources. Use the direct reader,
// including baseline references and existing inventories, rather than assuming
// app names are unique across namespaces or MID/MO kinds.
func (w *ManagedResourceWriter) checkConfigurationClaimants(ctx context.Context, owner client.Object, live *unstructured.Unstructured, configuration string, oldUID types.UID) error {
	var mids v1.MiddlewareList
	if err := w.Reader.List(ctx, &mids); err != nil {
		return fmt.Errorf("list Middleware owners before configuration adoption: %w", err)
	}
	var mos v1.MiddlewareOperatorList
	if err := w.Reader.List(ctx, &mos); err != nil {
		return fmt.Errorf("list MiddlewareOperator owners before configuration adoption: %w", err)
	}
	check := func(candidate client.Object, configurations []v1.Configuration, inventory []v1.RenderedConfigurationResource, baseline string, operator bool) error {
		if candidate.GetUID() == owner.GetUID() {
			return nil
		}
		conflict := candidate.GetUID() == oldUID && oldUID != ""
		for _, entry := range inventory {
			if entry.Group == live.GroupVersionKind().Group && entry.Kind == live.GetKind() && entry.Namespace == live.GetNamespace() && entry.Name == live.GetName() {
				conflict = true
			}
		}
		referencesSource := func(configurations []v1.Configuration) bool {
			for _, c := range configurations {
				if c.Name == configuration {
					return true
				}
			}
			return false
		}
		// The legacy app marker identifies an owner name, not its namespace or
		// kind. A same-name source reference also claims that identity before
		// its first publication produces inventory. Other instance names may
		// reuse the same template; the UID/inventory checks above still apply.
		if !conflict && candidate.GetName() == owner.GetName() {
			conflict = referencesSource(configurations)
		}
		if !conflict && candidate.GetName() == owner.GetName() && baseline != "" {
			if operator {
				var b v1.MiddlewareOperatorBaseline
				if err := w.Reader.Get(ctx, client.ObjectKey{Name: baseline}, &b); err != nil {
					return fmt.Errorf("read operator baseline %q before configuration adoption: %w", baseline, err)
				}
				conflict = referencesSource(b.Spec.Configurations)
			} else {
				var b v1.MiddlewareBaseline
				if err := w.Reader.Get(ctx, client.ObjectKey{Name: baseline}, &b); err != nil {
					return fmt.Errorf("read middleware baseline %q before configuration adoption: %w", baseline, err)
				}
				conflict = referencesSource(b.Spec.Configurations)
			}
		}
		if conflict {
			return managedIdentityError(owner, live, configuration, "configuration adoption conflicts with existing owner %T %s (uid=%q, deleting=%t)",
				candidate, client.ObjectKeyFromObject(candidate), candidate.GetUID(), candidate.GetDeletionTimestamp() != nil)
		}
		return nil
	}
	for i := range mids.Items {
		m := &mids.Items[i]
		if err := check(m, m.Spec.Configurations, m.Status.RenderedConfigurationResources, m.Spec.Baseline, false); err != nil {
			return err
		}
	}
	for i := range mos.Items {
		m := &mos.Items[i]
		if err := check(m, m.Spec.Configurations, m.Status.RenderedConfigurationResources, m.Spec.Baseline, true); err != nil {
			return err
		}
	}
	return nil
}

// adoptConfigurationReferences replaces the obsolete controller reference and
// retains independent non-controller references from the directly observed target.
func adoptConfigurationReferences(after, live, desired *unstructured.Unstructured) {
	// Work from the directly read references, not a possibly cached service
	// snapshot. Only the obsolete controller reference is replaced or removed.
	var refs []metav1.OwnerReference
	for _, ref := range live.GetOwnerReferences() {
		if ref.Controller == nil || !*ref.Controller {
			refs = append(refs, ref)
		}
	}
	for _, ref := range desired.GetOwnerReferences() {
		replaced := false
		for i := range refs {
			if refs[i].UID == ref.UID {
				refs[i] = ref
				replaced = true
				break
			}
		}
		if !replaced {
			refs = append(refs, ref)
		}
	}
	after.SetOwnerReferences(refs)
}
