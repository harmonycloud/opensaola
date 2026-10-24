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
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	jsonpatch "gomodules.xyz/jsonpatch/v2"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/structured-merge-diff/v6/fieldpath"
)

// Only a Secret name is stored in public resource metadata. Rendered credentials
// must never be copied into annotations, Events, or logs.
const AnnotationResourceIntent = "middleware.cn/resource-intent"
const LabelResourceIntentGCFence = "middleware.cn/resource-intent-gc-fence"
const intentRecordLabel = "middleware.cn/resource-intent-record"
const intentPublicationProtocol = 1

type resourceIntent struct {
	Version             int                        `json:"version"`
	PublicationProtocol int                        `json:"publicationProtocol,omitempty"`
	BaseResourceVersion string                     `json:"baseResourceVersion,omitempty"`
	OwnerUID            types.UID                  `json:"ownerUID"`
	ResourceUID         types.UID                  `json:"resourceUID"`
	Configuration       string                     `json:"configuration,omitempty"`
	SchemaHash          string                     `json:"schemaHash,omitempty"`
	Desired             *unstructured.Unstructured `json:"desired"`
	Effective           *unstructured.Unstructured `json:"effective,omitempty"`
	Preserved           []string                   `json:"preserved,omitempty"`
	// Pause distinguishes an observed pause baseline from a rendered intent.
	Pause *reconcilePauseData `json:"pause,omitempty"`
}

// intentObject defines the writable surface. Lifecycle metadata is handled by
// the existing ownership code, not interpreted as template field deletions.
func intentObject(obj *unstructured.Unstructured) *unstructured.Unstructured {
	result := cleanApplyObject(obj)
	// stringData is a write-only convenience API. Persist its canonical data
	// representation so later removals refer to the actual Secret fields.
	if result.GetAPIVersion() == "v1" && result.GetKind() == "Secret" {
		if values, ok := result.Object["stringData"].(map[string]any); ok {
			data, _ := result.Object["data"].(map[string]any)
			if data == nil {
				data = map[string]any{}
			}
			for key, value := range values {
				if str, ok := value.(string); ok {
					data[key] = base64.StdEncoding.EncodeToString([]byte(str))
				}
			}
			result.Object["data"] = data
			delete(result.Object, "stringData")
		}
	}
	metadata := map[string]any{"name": obj.GetName()}
	if obj.GetNamespace() != "" {
		metadata["namespace"] = obj.GetNamespace()
	}
	if labels := result.GetLabels(); len(labels) > 0 {
		metadata["labels"] = stringMapJSON(labels)
	}
	if annotations := result.GetAnnotations(); len(annotations) > 0 {
		metadata["annotations"] = stringMapJSON(annotations)
	}
	result.Object["metadata"] = metadata
	return result
}

func stringMapJSON(input map[string]string) map[string]any {
	out := make(map[string]any, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}

func (w *ManagedResourceWriter) preflightResourceIntent(ctx context.Context, owner client.Object, desired *unstructured.Unstructured, configuration string) error {
	converter, hash, err := w.IntentSchemas.Resolve(ctx, desired.GroupVersionKind())
	if err != nil {
		return err
	}
	_, err = converter.ObjectToTyped(intentObject(desired))
	if err != nil {
		return fmt.Errorf("rendered intent does not match the resource schema")
	}
	// Validate both the resource and its compressed record with the API server.
	// Defaults may require Desired and Effective; the record has no application
	// size ceiling, and Kubernetes validates the actual stored Secret payload.
	preview := cleanApplyObject(desired)
	if err := w.Client.Patch(ctx, preview, client.Apply, client.FieldOwner(v1.FieldOwner), client.ForceOwnership, client.DryRunAll); err != nil {
		return err
	}
	input := intentObject(desired)
	effective, err := CaptureResourceIntentDefaults(converter, input, input, input, intentObject(preview))
	if err != nil {
		return err
	}
	record := resourceIntent{Version: 1, PublicationProtocol: intentPublicationProtocol, BaseResourceVersion: "18446744073709551615", OwnerUID: owner.GetUID(), ResourceUID: "00000000-0000-0000-0000-000000000000", Configuration: configuration, SchemaHash: hash, Desired: input}
	if !reflect.DeepEqual(input.Object, effective.Object) {
		record.Effective = effective
	}
	secret, err := buildResourceIntentSecret(owner, record)
	if err != nil {
		return err
	}
	if err := w.Client.Create(ctx, secret, client.FieldOwner(v1.FieldOwner), client.DryRunAll); err != nil {
		return fmt.Errorf("validate resource intent Secret: %w", err)
	}
	return nil
}

// A snapshot is authority only for a resource belonging to this exact lifecycle.
// A matching name or generic field-manager name is never sufficient.
func verifyIntentIdentity(owner client.Object, live, desired *unstructured.Unstructured, configuration string) error {
	if configuration == "" {
		return verifyManagedIdentity(owner, live)
	}
	if owner.GetUID() == "" || live.GetUID() == "" || live.GetDeletionTimestamp() != nil {
		return managedIdentityError(owner, live, configuration, "invalid configuration resource identity: owner UID=%q, resource UID=%q, resource deleting=%t",
			owner.GetUID(), live.GetUID(), live.GetDeletionTimestamp() != nil)
	}
	annotations := live.GetAnnotations()
	if annotations[v1.LabelConfigurations] != configuration {
		return managedIdentityError(owner, live, configuration, "%s",
			identityMarkerMismatch("annotation", v1.LabelConfigurations, annotations[v1.LabelConfigurations], configuration))
	}
	if ref := metav1.GetControllerOf(live); ref != nil {
		if err := verifyManagedIdentity(owner, live); err != nil {
			return fmt.Errorf("%w; configuration=%q", err, configuration)
		}
		if uid := annotations[v1.AnnotationConfigurationOwnerUID]; uid != "" && uid != string(owner.GetUID()) {
			return managedIdentityError(owner, live, configuration, "%s",
				identityMarkerMismatch("annotation", v1.AnnotationConfigurationOwnerUID, uid, string(owner.GetUID())))
		}
		return nil
	}
	// Cross-namespace, cluster-scoped and orphan resources cannot rely on a
	// namespaced controller reference. Configuration NAME denotes the logical
	// source even when that cluster-scoped Configuration has been recreated.
	if live.GetLabels()[v1.LabelApp] != owner.GetName() || annotations[v1.AnnotationConfigurationOwnerUID] != string(owner.GetUID()) {
		var mismatches []string
		if app := live.GetLabels()[v1.LabelApp]; app != owner.GetName() {
			mismatches = append(mismatches, identityMarkerMismatch("label", v1.LabelApp, app, owner.GetName()))
		}
		if uid := annotations[v1.AnnotationConfigurationOwnerUID]; uid != string(owner.GetUID()) {
			mismatches = append(mismatches, identityMarkerMismatch("annotation", v1.AnnotationConfigurationOwnerUID, uid, string(owner.GetUID())))
		}
		return managedIdentityError(owner, live, configuration, "%s", strings.Join(mismatches, "; "))
	}
	return nil
}

func (w *ManagedResourceWriter) loadResourceIntent(ctx context.Context, owner client.Object, live *unstructured.Unstructured, configuration string) (*resourceIntent, *corev1.Secret, error) {
	name := live.GetAnnotations()[AnnotationResourceIntent]
	if name == "" {
		return nil, nil, nil
	}
	secret := &corev1.Secret{}
	if err := w.Reader.Get(ctx, client.ObjectKey{Namespace: owner.GetNamespace(), Name: name}, secret); err != nil {
		return nil, nil, fmt.Errorf("read resource intent record %s: %w", name, err)
	}
	if secret.Immutable == nil || !*secret.Immutable || secret.Labels[intentRecordLabel] != string(owner.GetUID()) || secret.DeletionTimestamp != nil {
		return nil, nil, managedWriteBlocked("invalid resource intent record")
	}
	ref := metav1.GetControllerOf(secret)
	expected, err := intentOwnerReference(owner)
	if err != nil {
		return nil, nil, err
	}
	if ref == nil || ref.UID != expected.UID || ref.Name != expected.Name || ref.Kind != expected.Kind || ref.APIVersion != expected.APIVersion {
		return nil, nil, managedWriteBlocked("resource intent record owner mismatch")
	}
	record, err := decodeResourceIntent(secret.Data)
	if err != nil || record.Version != 1 || record.Desired == nil || record.Pause != nil {
		return nil, nil, managedWriteBlocked("invalid resource intent record data")
	}
	if record.OwnerUID != owner.GetUID() || record.ResourceUID != live.GetUID() || record.Configuration != configuration || record.Desired.GroupVersionKind() != live.GroupVersionKind() || client.ObjectKeyFromObject(record.Desired) != client.ObjectKeyFromObject(live) {
		return nil, nil, managedWriteBlocked("resource intent record identity mismatch")
	}
	return record, secret, nil
}

func intentOwnerReference(owner client.Object) (metav1.OwnerReference, error) {
	kind := ""
	switch owner.(type) {
	case *v1.Middleware:
		kind = "Middleware"
	case *v1.MiddlewareOperator:
		kind = "MiddlewareOperator"
	default:
		return metav1.OwnerReference{}, managedWriteBlocked("unsupported intent owner")
	}
	controller := true
	return metav1.OwnerReference{APIVersion: v1.GroupVersion.String(), Kind: kind, Name: owner.GetName(), UID: owner.GetUID(), Controller: &controller}, nil
}

// Immutable, uniquely named records are prepared BEFORE the resource write. The
// record pointer commits atomically with the business patch. A lost response or
// process restart cannot leave a successful write pointing at an older baseline.
func (w *ManagedResourceWriter) prepareResourceIntent(ctx context.Context, owner client.Object, live, desired *unstructured.Unstructured, configuration, schemaHash string, preserved []string, effective ...*unstructured.Unstructured) (*corev1.Secret, error) {
	if live.GetUID() == "" || live.GetResourceVersion() == "" {
		return nil, managedWriteBlocked("intent publication requires resource UID and resourceVersion")
	}
	record := resourceIntent{Version: 1, PublicationProtocol: intentPublicationProtocol, BaseResourceVersion: live.GetResourceVersion(), OwnerUID: owner.GetUID(), ResourceUID: live.GetUID(), Configuration: configuration, SchemaHash: schemaHash, Desired: intentObject(desired), Preserved: preserved}
	if len(effective) > 0 && effective[0] != nil && !reflect.DeepEqual(record.Desired.Object, effective[0].Object) {
		record.Effective = intentObject(effective[0])
	}
	secret, err := buildResourceIntentSecret(owner, record)
	if err != nil {
		return nil, err
	}
	if err := w.Client.Create(ctx, secret, client.FieldOwner(v1.FieldOwner)); err != nil {
		return nil, fmt.Errorf("create resource intent Secret: %w", err)
	}
	return secret, nil
}

func buildResourceIntentSecret(owner client.Object, record resourceIntent) (*corev1.Secret, error) {
	data, err := encodeResourceIntent(record)
	if err != nil {
		return nil, err
	}
	ref, err := intentOwnerReference(owner)
	if err != nil {
		return nil, err
	}
	immutable := true
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{GenerateName: "opensaola-intent-", Namespace: owner.GetNamespace(), Labels: map[string]string{intentRecordLabel: string(owner.GetUID())}, OwnerReferences: []metav1.OwnerReference{ref}}, Immutable: &immutable, Type: corev1.SecretTypeOpaque, Data: data}
	return secret, nil
}

func setIntentPointer(obj *unstructured.Unstructured, name string) {
	annotations := obj.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations[AnnotationResourceIntent] = name
	obj.SetAnnotations(annotations)
}

func (w *ManagedResourceWriter) discardIntentRecord(ctx context.Context, secret *corev1.Secret) {
	if secret == nil {
		return
	}
	uid, rv := secret.UID, secret.ResourceVersion
	if err := w.Client.Delete(ctx, secret, &client.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid, ResourceVersion: &rv}}); err != nil && !apierrors.IsNotFound(err) {
		log.FromContext(ctx).Error(err, "resource intent record cleanup deferred to background collection", "record", secret.Name)
	}
}

func (w *ManagedResourceWriter) cleanupUncommittedIntent(ctx context.Context, live *unstructured.Unstructured, prepared *corev1.Secret, writeErr error) {
	// A timeout/lost connection may leave the PATCH still committing on the
	// server. Even a GET showing the old pointer is not evidence of rejection.
	if !apierrors.IsConflict(writeErr) && !apierrors.IsInvalid(writeErr) && !apierrors.IsForbidden(writeErr) && !apierrors.IsUnauthorized(writeErr) && !apierrors.IsNotFound(writeErr) {
		return
	}
	current := &unstructured.Unstructured{}
	current.SetGroupVersionKind(live.GroupVersionKind())
	err := w.Reader.Get(ctx, client.ObjectKeyFromObject(live), current)
	// An uncertain transport error is not evidence that the write failed.
	if apierrors.IsNotFound(err) || err == nil && current.GetAnnotations()[AnnotationResourceIntent] != prepared.Name {
		w.discardIntentRecord(ctx, prepared)
	}
}

func (w *ManagedResourceWriter) initializeResourceIntent(ctx context.Context, owner client.Object, live, desired *unstructured.Unstructured, configuration string) error {
	if err := verifyIntentIdentity(owner, live, desired, configuration); err != nil {
		return err
	}
	converter, hash, err := w.IntentSchemas.Resolve(ctx, desired.GroupVersionKind())
	if err != nil {
		return err
	}
	input := intentObject(desired)
	effective, err := CaptureResourceIntentDefaults(converter, input, input, input, intentObject(live))
	if err != nil {
		return err
	}
	// Workload controllers can update status immediately after Create. Keep
	// the original successful create result as defaulting evidence across these
	// metadata-only retries; do not fall back to legacy bootstrap and lose it.
	current := live.DeepCopy()
	for attempt := 0; attempt < 5; attempt++ {
		// Each record is published against exactly one RV. A collector may fence
		// that RV; reusing the same record after refreshing it would be unsafe.
		prepared, prepareErr := w.prepareResourceIntent(ctx, owner, current, desired, configuration, hash, nil, effective)
		if prepareErr != nil {
			return prepareErr
		}
		after := current.DeepCopy()
		setIntentPointer(after, prepared.Name)
		err = w.commitIntentPatch(ctx, current, after)
		if err != nil {
			w.cleanupUncommittedIntent(ctx, current, prepared, err)
		}
		if !apierrors.IsConflict(err) {
			return err
		}
		if err = w.Reader.Get(ctx, client.ObjectKeyFromObject(live), current); err != nil {
			break
		}
		if current.GetUID() != live.GetUID() {
			err = managedWriteBlocked("new resource replaced before recording its intent")
			break
		}
		if err = verifyIntentIdentity(owner, current, desired, configuration); err != nil {
			break
		}
		if current.GetAnnotations()[AnnotationResourceIntent] != "" {
			// Another successful writer already committed its record. Never
			// replace that newer baseline with this older creation response.
			w.discardIntentRecord(ctx, prepared)
			return nil
		}
		err = apierrors.NewConflict(schema.GroupResource{Group: live.GroupVersionKind().Group, Resource: live.GetKind()}, live.GetName(), fmt.Errorf("resource changed while initializing intent"))
	}
	return err
}

func foreignApplyFields(live *unstructured.Unstructured) (*fieldpath.Set, error) {
	protected := fieldpath.NewSet()
	for _, entry := range live.GetManagedFields() {
		if entry.Operation != metav1.ManagedFieldsOperationApply || entry.Manager == v1.FieldOwner || entry.Subresource != "" {
			continue
		}
		if entry.APIVersion != live.GetAPIVersion() || entry.FieldsType != "FieldsV1" || entry.FieldsV1 == nil {
			return nil, managedWriteBlocked("cannot interpret external Apply field ownership in current API version")
		}
		fields := fieldpath.NewSet()
		if err := fields.FromJSON(bytes.NewReader(entry.FieldsV1.Raw)); err != nil {
			return nil, managedWriteBlocked("invalid external Apply fields")
		}
		protected = protected.Union(fields)
	}
	return protected, nil
}

func (w *ManagedResourceWriter) reconcileResourceIntent(ctx context.Context, owner client.Object, live, desired *unstructured.Unstructured, configuration string) error {
	if err := verifyIntentIdentity(owner, live, desired, configuration); err != nil {
		return err
	}
	converter, schemaHash, err := w.IntentSchemas.Resolve(ctx, desired.GroupVersionKind())
	if err != nil {
		return err
	}
	record, previous, err := w.loadResourceIntent(ctx, owner, live, configuration)
	if err != nil {
		return err
	}
	if record != nil && record.SchemaHash != schemaHash {
		return managedWriteBlocked("resource schema changed since recorded intent; refusing to infer deletions")
	}
	input := intentObject(desired)
	if _, err := converter.ObjectToTyped(input); err != nil {
		return fmt.Errorf("rendered intent does not match the resource schema")
	}
	if record == nil {
		// Bootstrap does NOT turn live leftovers into our desired intent. Existing
		// omission semantics remain conservative until a real baseline is available.
		submitted, _, err := MergeResourceIntent(converter, nil, intentObject(live), input, nil)
		if err != nil {
			return err
		}
		preview := cleanApplyObject(desired)
		preview.SetResourceVersion(live.GetResourceVersion())
		preview.SetUID(live.GetUID())
		if err := w.Client.Patch(ctx, preview, client.Apply, client.FieldOwner(v1.FieldOwner), client.ForceOwnership, client.DryRunAll); err != nil {
			return err
		}
		effective, err := CaptureResourceIntentDefaults(converter, input, input, submitted, intentObject(preview))
		if err != nil {
			return err
		}
		prepared, err := w.prepareResourceIntent(ctx, owner, live, desired, configuration, schemaHash, nil, effective)
		if err != nil {
			return err
		}
		apply := cleanApplyObject(desired)
		setIntentPointer(apply, prepared.Name)
		apply.SetResourceVersion(live.GetResourceVersion())
		apply.SetUID(live.GetUID())
		err = w.Client.Patch(ctx, apply, client.Apply, client.FieldOwner(v1.FieldOwner), client.ForceOwnership)
		if err != nil {
			w.cleanupUncommittedIntent(ctx, live, prepared, err)
			return err
		}
		reportIntentPreserved(ctx, owner, live, "baseline established; fields omitted before this baseline are preserved, not inferred from legacy manager names")
		return nil
	}
	protected, err := foreignApplyFields(live)
	if err != nil {
		return err
	}
	before := record.Desired
	if record.Effective != nil {
		before = record.Effective
	}
	effectiveInput, err := CarryResourceIntentDefaults(converter, record.Desired, before, input)
	if err != nil {
		return err
	}
	merged, preserved, err := MergeResourceIntent(converter, before, intentObject(live), effectiveInput, protected)
	if err != nil {
		return err
	}
	// Explicit resume-policy=apply retains its existing meaning: replay the
	// complete current desired values, including unchanged rendered values that
	// were modified during suspension. Ordinary updates still preserve drift.
	if mid, ok := owner.(*v1.Middleware); ok {
		policy, valid := v1.ReconcileResumePolicyFor(mid.Annotations)
		if valid && policy == v1.ReconcileResumePolicyApply {
			for _, condition := range mid.Status.Conditions {
				if condition.Type == v1.CondTypeReconcilePaused && condition.Status == metav1.ConditionTrue {
					merged, _, err = MergeResourceIntent(converter, nil, merged, input, nil)
					if err != nil {
						return err
					}
					break
				}
			}
		}
	}
	for _, path := range record.Preserved {
		reportIntentPreserved(ctx, owner, live, "previous update retained a field at "+path+"; this notice is an audit record, not proof of current convergence")
	}
	for _, path := range preserved {
		reportIntentPreserved(ctx, owner, live, "external or ambiguous field retained: "+path)
	}
	// Carry system and lifecycle metadata from live. The merge only authors the
	// rendered business surface and labels/annotations.
	after := live.DeepCopy()
	for key := range intentObject(live).Object {
		if key != "metadata" && key != "apiVersion" && key != "kind" {
			delete(after.Object, key)
		}
	}
	for key, value := range merged.Object {
		if key != "metadata" {
			after.Object[key] = value
		}
	}
	annotations := after.GetAnnotations()
	for key := range intentObject(live).GetAnnotations() {
		delete(annotations, key)
	}
	if annotations == nil {
		annotations = map[string]string{}
	}
	for key, value := range merged.GetAnnotations() {
		annotations[key] = value
	}
	after.SetAnnotations(annotations)
	labels := merged.GetLabels()
	if fence := live.GetLabels()[LabelResourceIntentGCFence]; fence != "" {
		if labels == nil {
			labels = map[string]string{}
		}
		labels[LabelResourceIntentGCFence] = fence
	}
	after.SetLabels(labels)
	if reflect.DeepEqual(record.Desired.Object, input.Object) && reflect.DeepEqual(live.Object, after.Object) {
		return nil
	}
	// Validate the COMPLETE business change before preparing any record. This
	// catches immutable fields and invalid value/valueFrom or volume transitions.
	accepted, err := w.validateIntentPatch(ctx, live, after)
	if err != nil {
		return err
	}
	effective, err := CaptureResourceIntentDefaults(converter, input, effectiveInput, intentObject(after), intentObject(accepted))
	if err != nil {
		return err
	}
	prepared, err := w.prepareResourceIntent(ctx, owner, live, desired, configuration, schemaHash, preserved, effective)
	if err != nil {
		return err
	}
	setIntentPointer(after, prepared.Name)
	if err = w.commitIntentPatch(ctx, live, after); err != nil {
		w.cleanupUncommittedIntent(ctx, live, prepared, err)
		return err
	}
	w.discardIntentRecord(ctx, previous)
	return nil
}

func intentPatch(live, after *unstructured.Unstructured) (client.Patch, error) {
	beforeJSON, err := json.Marshal(live.Object)
	if err != nil {
		return nil, err
	}
	afterJSON, err := json.Marshal(after.Object)
	if err != nil {
		return nil, err
	}
	operations, err := jsonpatch.CreatePatch(beforeJSON, afterJSON)
	if err != nil {
		return nil, err
	}
	preconditions := []jsonpatch.Operation{{Operation: "test", Path: "/metadata/uid", Value: string(live.GetUID())}, {Operation: "test", Path: "/metadata/resourceVersion", Value: live.GetResourceVersion()}}
	raw, err := json.Marshal(append(preconditions, operations...))
	return client.RawPatch(types.JSONPatchType, raw), err
}

func (w *ManagedResourceWriter) validateIntentPatch(ctx context.Context, live, after *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	patch, err := intentPatch(live, after)
	if err != nil {
		return nil, err
	}
	accepted := live.DeepCopy()
	err = w.Client.Patch(ctx, accepted, patch, client.FieldOwner(v1.FieldOwner), client.DryRunAll)
	return accepted, w.intentWriteError(ctx, live, err)
}

func (w *ManagedResourceWriter) commitIntentPatch(ctx context.Context, live, after *unstructured.Unstructured) error {
	patch, err := intentPatch(live, after)
	if err != nil {
		return err
	}
	err = w.Client.Patch(ctx, after, patch, client.FieldOwner(v1.FieldOwner))
	return w.intentWriteError(ctx, live, err)
}

func (w *ManagedResourceWriter) intentWriteError(ctx context.Context, live *unstructured.Unstructured, err error) error {
	if err == nil || !apierrors.IsInvalid(err) {
		return err
	}
	// JSON Patch test failures can be 422 rather than 409. Re-read instead of
	// treating every validation error as retryable.
	current := &unstructured.Unstructured{}
	current.SetGroupVersionKind(live.GroupVersionKind())
	if getErr := w.Reader.Get(ctx, client.ObjectKeyFromObject(live), current); getErr == nil && (current.GetUID() != live.GetUID() || current.GetResourceVersion() != live.GetResourceVersion()) {
		return apierrors.NewConflict(schema.GroupResource{Group: live.GroupVersionKind().Group, Resource: live.GetKind()}, live.GetName(), err)
	}
	return err
}

func reportIntentPreserved(ctx context.Context, owner client.Object, live *unstructured.Unstructured, reason string) {
	log.FromContext(ctx).Info("ResourceIntentPreserved", "kind", live.GetKind(), "namespace", live.GetNamespace(), "name", live.GetName(), "reason", reason)
	if recorder, ok := ctx.Value(intentRecorderKey{}).(record.EventRecorder); ok {
		recorder.Event(owner, "Warning", "ResourceIntentPreserved", fmt.Sprintf("%s %s/%s: %s", live.GetKind(), live.GetNamespace(), live.GetName(), reason))
	}
}

type intentRecorderKey struct{}

// WithResourceIntentEvents attaches the owning controller's recorder to writes.
func WithResourceIntentEvents(ctx context.Context, recorder record.EventRecorder) context.Context {
	return context.WithValue(ctx, intentRecorderKey{}, recorder)
}
