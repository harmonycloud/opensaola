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

	v1 "github.com/harmonycloud/opensaola/api/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var managedResourceReader client.Reader
var resourceIntentSchemas IntentSchemaResolver

type ManagedResourceWriter struct {
	Client        client.Client
	Reader        client.Reader
	IntentSchemas IntentSchemaResolver
}

// ConfigureResourceIntents enables persisted, schema-aware three-way updates.
// It must be called before controllers start, like ConfigureManagedResources.
func ConfigureResourceIntents(schemas IntentSchemaResolver) { resourceIntentSchemas = schemas }

// ConfigureManagedResources installs the direct reader before starting controllers.
func ConfigureManagedResources(reader client.Reader) {
	managedResourceReader = reader
}
func NewManagedResourceWriter(cli client.Client) *ManagedResourceWriter {
	reader := managedResourceReader
	if reader == nil {
		reader = cli
	}
	return &ManagedResourceWriter{Client: cli, Reader: reader, IntentSchemas: resourceIntentSchemas}
}
func managedWriteBlocked(format string, args ...any) error {
	return fmt.Errorf("managed resource write blocked: "+format, args...)
}

// deepCopyClientObject deep-copies a client.Object without an unchecked type
// assertion; the copy always keeps the concrete type, so a mismatch is a bug.
func deepCopyClientObject(owner client.Object) (client.Object, error) {
	fresh, ok := owner.DeepCopyObject().(client.Object)
	if !ok {
		return nil, managedWriteBlocked("owner deep copy lost client.Object: %T", owner.DeepCopyObject())
	}
	return fresh, nil
}
func verifyManagedIdentity(owner client.Object, live *unstructured.Unstructured) error {
	kind := ""
	switch owner.(type) {
	case *v1.Middleware:
		kind = "Middleware"
	case *v1.MiddlewareOperator:
		kind = "MiddlewareOperator"
	default:
		return managedWriteBlocked("unsupported owner %T", owner)
	}
	if owner.GetUID() == "" || live.GetUID() == "" || owner.GetDeletionTimestamp() != nil || live.GetDeletionTimestamp() != nil {
		return managedWriteBlocked("missing UID or object deleting")
	}
	ref := metav1.GetControllerOf(live)
	if ref == nil {
		return managedWriteBlocked("primary resource lacks controller owner")
	}
	gv, err := schema.ParseGroupVersion(ref.APIVersion)
	if err != nil || gv.Group != v1.GroupVersion.Group || ref.Kind != kind || ref.Name != owner.GetName() || ref.UID != owner.GetUID() || live.GetNamespace() != owner.GetNamespace() {
		return managedWriteBlocked("primary resource owner mismatch")
	}
	return nil
}
func cleanApplyObject(desired *unstructured.Unstructured) *unstructured.Unstructured {
	result := desired.DeepCopy()
	result.SetManagedFields(nil)
	result.SetResourceVersion("")
	result.SetUID("")
	delete(result.Object, "status")
	for _, key := range []string{"creationTimestamp", "deletionTimestamp", "deletionGracePeriodSeconds", "generation", "selfLink"} {
		unstructured.RemoveNestedField(result.Object, "metadata", key)
	}
	annotations := result.GetAnnotations()
	for _, key := range []string{AnnotationResourceIntent} {
		delete(annotations, key)
	}
	result.SetAnnotations(annotations)
	labels := result.GetLabels()
	delete(labels, LabelResourceIntentGCFence)
	result.SetLabels(labels)
	return result
}

// Reconcile never rewrites managedFields. Normal updates establish and use a
// rendered-intent baseline; missing history is not inferred from manager names.
// Existing resources are updated only through the normal owner lifecycle.
func (w *ManagedResourceWriter) Reconcile(ctx context.Context, owner client.Object, desired *unstructured.Unstructured, configuration string) error {
	if desired == nil {
		return nil
	}
	for attempt := 0; attempt < 5; attempt++ {
		fresh, err := deepCopyClientObject(owner)
		if err != nil {
			return err
		}
		if err := w.Reader.Get(ctx, client.ObjectKeyFromObject(owner), fresh); err != nil {
			return err
		}
		if fresh.GetUID() != owner.GetUID() || fresh.GetGeneration() != owner.GetGeneration() || fresh.GetDeletionTimestamp() != nil {
			return managedWriteBlocked("owner changed or deleting")
		}
		switch o := fresh.(type) {
		case *v1.Middleware:
			if v1.IsMiddlewareReconcileWriteSuspended(o.Annotations, o.Status.Conditions) {
				return managedWriteBlocked("Middleware paused")
			}
		case *v1.MiddlewareOperator:
			if v1.IsReconcileSuspended(o.Annotations) {
				return managedWriteBlocked("MiddlewareOperator paused")
			}
		}
		live := &unstructured.Unstructured{}
		live.SetGroupVersionKind(desired.GroupVersionKind())
		err = w.Reader.Get(ctx, client.ObjectKeyFromObject(desired), live)
		if err == nil || apierrors.IsNotFound(err) {
			observed := live
			if err != nil {
				observed = nil
			}
			// Check the exact live object used to build the RV-guarded patch,
			// including on conflict retries. An earlier service-layer GET alone
			// cannot fence changes arriving between that GET and this read.
			if verifyErr := verifyObservedSpecForWrite(fresh, observed, configuration); verifyErr != nil {
				return verifyErr
			}
		}
		if apierrors.IsNotFound(err) {
			intent := cleanApplyObject(desired)
			if w.IntentSchemas != nil && !isImmutableResource(intent) {
				if err := w.preflightResourceIntent(ctx, fresh, intent, configuration); err != nil {
					return err
				}
			}
			if isImmutableResource(intent) {
				err = w.Client.Create(ctx, intent)
			} else {
				err = w.Client.Patch(ctx, intent, client.Apply, client.FieldOwner(v1.FieldOwner), client.ForceOwnership)
			}
			if err == nil && w.IntentSchemas != nil && !isImmutableResource(intent) {
				err = w.initializeResourceIntent(ctx, fresh, intent, desired, configuration)
			}
		} else if err == nil {
			if configuration == "" {
				if err = verifyManagedIdentity(fresh, live); err != nil {
					return err
				}
			}
			if !isImmutableResource(live) {
				if w.IntentSchemas != nil {
					err = w.reconcileResourceIntent(ctx, fresh, live, desired, configuration)
				} else {
					intent := cleanApplyObject(desired)
					intent.SetResourceVersion(live.GetResourceVersion())
					intent.SetUID(live.GetUID())
					err = w.Client.Patch(ctx, intent, client.Apply, client.FieldOwner(v1.FieldOwner), client.ForceOwnership)
				}
			}
		}
		if apierrors.IsConflict(err) {
			continue
		}
		if err != nil {
			return err
		}
		return nil
	}
	return fmt.Errorf("resource changed during all managed write retries")
}
