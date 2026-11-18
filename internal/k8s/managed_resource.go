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

	v1 "github.com/harmonycloud/opensaola/api/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/uuid"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var managedResourceReader client.Reader
var resourceIntentSchemas IntentSchemaResolver

type ManagedResourceWriter struct {
	Client        client.Client
	Reader        client.Reader
	IntentSchemas IntentSchemaResolver
}

// ConfigureResourceIntents installs the required schema resolver for mutable resources.
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

// Identity failures reach MID/MO status as well as logs. Include only the
// identity and known ownership markers, never rendered data or arbitrary metadata.
func managedIdentityError(owner client.Object, live *unstructured.Unstructured, configuration, format string, args ...any) error {
	kind := owner.GetObjectKind().GroupVersionKind().Kind
	switch owner.(type) {
	case *v1.Middleware:
		kind = "Middleware"
	case *v1.MiddlewareOperator:
		kind = "MiddlewareOperator"
	}
	resourceName := live.GetName()
	if live.GetNamespace() != "" {
		resourceName = live.GetNamespace() + "/" + resourceName
	} else {
		resourceName += " (cluster-scoped)"
	}
	controller := "<none>"
	if ref := metav1.GetControllerOf(live); ref != nil {
		controller = fmt.Sprintf("%s %s (apiVersion=%s, uid=%q)", ref.Kind, ref.Name, ref.APIVersion, ref.UID)
	}
	details := fmt.Sprintf("resource=%s %s (apiVersion=%s); expected owner=%s %s (apiVersion=%s, uid=%q); actual controller owner=%s",
		live.GetKind(), resourceName, live.GetAPIVersion(), kind, client.ObjectKeyFromObject(owner), v1.GroupVersion, owner.GetUID(), controller)
	if configuration != "" {
		details += fmt.Sprintf("; configuration=%q", configuration)
	}
	if managedBy := live.GetLabels()["app.kubernetes.io/managed-by"]; managedBy != "" {
		details += fmt.Sprintf("; managed-by=%q", managedBy)
	}
	annotations := live.GetAnnotations()
	if release := annotations["meta.helm.sh/release-name"]; release != "" {
		details += fmt.Sprintf("; helm release=%q; helm release namespace=%q", release, annotations["meta.helm.sh/release-namespace"])
	}
	return fmt.Errorf("%w; %s", managedWriteBlocked(format, args...), details)
}

func identityMarkerMismatch(marker, key, actual, expected string) string {
	if actual == "" {
		return fmt.Sprintf("%s %q is missing or empty (expected %q)", marker, key, expected)
	}
	return fmt.Sprintf("%s %q=%q (expected %q)", marker, key, actual, expected)
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

func (w *ManagedResourceWriter) freshWriteOwner(ctx context.Context, owner client.Object) (client.Object, error) {
	fresh, err := deepCopyClientObject(owner)
	if err != nil {
		return nil, err
	}
	if err := w.Reader.Get(ctx, client.ObjectKeyFromObject(owner), fresh); err != nil {
		return nil, err
	}
	if fresh.GetUID() != owner.GetUID() || fresh.GetGeneration() != owner.GetGeneration() || fresh.GetDeletionTimestamp() != nil {
		return nil, managedWriteBlocked("owner changed or deleting")
	}
	switch o := fresh.(type) {
	case *v1.Middleware:
		if v1.IsMiddlewareReconcileWriteSuspended(o.Annotations, o.Status.Conditions) {
			return nil, managedWriteBlocked("Middleware paused")
		}
	case *v1.MiddlewareOperator:
		if v1.IsReconcileSuspended(o.Annotations) {
			return nil, managedWriteBlocked("MiddlewareOperator paused")
		}
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
		return managedIdentityError(owner, live, "", "unsupported owner %T", owner)
	}
	if owner.GetUID() == "" || live.GetUID() == "" || owner.GetDeletionTimestamp() != nil || live.GetDeletionTimestamp() != nil {
		return managedIdentityError(owner, live, "", "invalid resource identity: owner UID=%q, resource UID=%q, owner deleting=%t, resource deleting=%t",
			owner.GetUID(), live.GetUID(), owner.GetDeletionTimestamp() != nil, live.GetDeletionTimestamp() != nil)
	}
	ref := metav1.GetControllerOf(live)
	if ref == nil {
		return managedIdentityError(owner, live, "", "primary resource lacks controller owner")
	}
	gv, err := schema.ParseGroupVersion(ref.APIVersion)
	if err != nil || gv.Group != v1.GroupVersion.Group || ref.Kind != kind || ref.Name != owner.GetName() || ref.UID != owner.GetUID() || live.GetNamespace() != owner.GetNamespace() {
		return managedIdentityError(owner, live, "", "primary resource owner mismatch")
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
	for _, key := range []string{AnnotationResourceIntent, AnnotationResourceIntentOperation} {
		delete(annotations, key)
	}
	result.SetAnnotations(annotations)
	labels := result.GetLabels()
	delete(labels, LabelResourceIntentGCFence)
	result.SetLabels(labels)
	return result
}

// createMutableResource identifies this particular POST even when its response
// is lost. A direct GET can recover explicit declarations, never POST defaults.
// If a timeout preceded the commit, fail and let normal reconciliation retry.
func (w *ManagedResourceWriter) createMutableResource(ctx context.Context, target *unstructured.Unstructured) (postResponse bool, err error) {
	receipt := "create-" + string(uuid.NewUUID())
	annotations := target.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations[AnnotationResourceIntentOperation] = receipt
	target.SetAnnotations(annotations)
	if err = w.Client.Create(ctx, target, client.FieldOwner(v1.FieldOwner)); err == nil {
		return true, nil
	}
	var status apierrors.APIStatus
	if errors.As(err, &status) && status.Status().Code < 500 && !apierrors.IsTimeout(err) {
		return false, err
	}
	actual := &unstructured.Unstructured{}
	actual.SetGroupVersionKind(target.GroupVersionKind())
	if readErr := w.Reader.Get(ctx, client.ObjectKeyFromObject(target), actual); readErr != nil || actual.GetUID() == "" || actual.GetAnnotations()[AnnotationResourceIntentOperation] != receipt {
		return false, fmt.Errorf("cannot verify initial resource creation or establish its field history: %w", err)
	}
	*target = *actual
	return false, nil
}

// Reconcile never rewrites managedFields. Normal updates establish and use a
// rendered-intent baseline; missing history is not inferred from manager names.
// Existing resources are updated only through the normal owner lifecycle.
func (w *ManagedResourceWriter) Reconcile(ctx context.Context, owner client.Object, desired *unstructured.Unstructured, configuration string) error {
	_, err := w.ReconcileWithUID(ctx, owner, desired, configuration)
	return err
}

// ReconcileWithUID returns the UID verified by this write for binding cleanup
// inventory. A later GET by name may already refer to a replacement object.
func (w *ManagedResourceWriter) ReconcileWithUID(ctx context.Context, owner client.Object, desired *unstructured.Unstructured, configuration string) (types.UID, error) {
	if desired == nil {
		return "", nil
	}
	mutable := !isImmutableResource(desired)
	if mutable && w.IntentSchemas == nil {
		return "", managedWriteBlocked("mutable resource requires a configured intent schema resolver")
	}

	for attempt := 0; attempt < 5; attempt++ {
		fresh, err := w.freshWriteOwner(ctx, owner)
		if err != nil {
			return "", err
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
				return "", verifyErr
			}
		}
		if apierrors.IsNotFound(err) {
			intent := cleanApplyObject(desired)
			if mutable {
				if err := w.preflightResourceIntent(ctx, fresh, intent, configuration); err != nil {
					if apierrors.IsAlreadyExists(err) {
						continue
					}
					return "", err
				}
			}
			if err := w.checkIntentWriteOwner(ctx, fresh, nil, configuration); err != nil {
				return "", err
			}
			live = intent
			createdResponse := false
			if mutable {
				createdResponse, err = w.createMutableResource(ctx, intent)
			} else {
				err = w.Client.Create(ctx, intent, client.FieldOwner(v1.FieldOwner))
			}
			if apierrors.IsAlreadyExists(err) {
				continue
			}
			if err == nil && mutable {
				err = w.initializeResourceIntent(ctx, fresh, intent, desired, configuration, createdResponse)
			}
		} else if err == nil {
			adopt := false
			if configuration == "" {
				if err = verifyManagedIdentity(fresh, live); err != nil {
					return "", err
				}
			} else {
				adopt, err = w.configurationNeedsAdoption(ctx, fresh, live, desired, configuration)
				if err != nil {
					return "", err
				}
			}
			if !isImmutableResource(live) {
				err = w.reconcileResourceIntent(ctx, fresh, live, desired, configuration, adopt)
			}
		}
		if apierrors.IsConflict(err) {
			continue
		}
		if err != nil {
			return "", err
		}
		return live.GetUID(), nil
	}
	return "", fmt.Errorf("resource changed during all managed write retries")
}
