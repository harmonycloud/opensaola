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
	"crypto/sha256"
	"fmt"
	"reflect"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/managedfields"
	"k8s.io/apimachinery/pkg/util/uuid"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const labelIntentLedger = "middleware.cn/field-history"
const AnnotationResourceIntentOperation = "middleware.cn/resource-intent-operation"

// Versioned independently from the v2 field semantics in the payload. Old writers
// reject the mutable Secret. Its name binds one target lifetime to one owner/source.
func intentLedgerName(owner client.Object, target *unstructured.Unstructured, configuration string) string {
	id := fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s", owner.GetUID(), configuration, target.GetUID(), target.GetNamespace(), target.GetName())
	return fmt.Sprintf("opensaola-history-%x", sha256.Sum256([]byte(id)))
}

type intentAttempt struct {
	ID              string                `json:"id"`
	Deletions       []StoredDeletionCheck `json:"deletions,omitempty"`
	NamedEmptyLists [][]string            `json:"namedEmptyLists,omitempty"`
}

func isIntentLedger(secret *corev1.Secret) bool {
	return secret != nil && secret.Labels[labelIntentLedger] == "v1"
}
func ledgerPending(secret *corev1.Secret) bool {
	return isIntentLedger(secret) && secret.Annotations[AnnotationResourceIntentOperation] != ""
}

func newIntentCandidate(owner client.Object, live, input, effective *unstructured.Unstructured, configuration, hash string, preserved []string) *resourceIntent {
	r := &resourceIntent{Version: currentResourceIntentVersion, PublicationProtocol: intentPublicationProtocol, BaseResourceVersion: live.GetResourceVersion(), OwnerUID: owner.GetUID(), ResourceUID: live.GetUID(), Configuration: configuration, SchemaHash: DeclaredResourceIntentSchemaHash(hash), Desired: intentObject(input), Preserved: preserved}
	if effective != nil && !reflect.DeepEqual(r.Desired.Object, effective.Object) {
		r.Effective = intentObject(effective)
	}
	return r
}

func buildIntentLedger(owner client.Object, live *unstructured.Unstructured, candidate *resourceIntent) (*corev1.Secret, error) {
	ledger, err := buildResourceIntentSecret(owner, *candidate)
	if err != nil {
		return nil, err
	}
	ledger.Name = intentLedgerName(owner, live, candidate.Configuration)
	ledger.GenerateName = ""
	ledger.Immutable = nil
	ledger.Labels[labelIntentLedger] = "v1"
	ledger.Annotations = map[string]string{AnnotationResourceIntentOperation: candidate.Attempt.ID}
	return ledger, nil
}

func readIntentLedger(s *corev1.Secret, owner client.Object, live *unstructured.Unstructured, config string) (*resourceIntent, error) {
	ref := metav1.GetControllerOf(s)
	expected, err := intentOwnerReference(owner)
	if err != nil {
		return nil, err
	}
	if !isIntentLedger(s) || s.Name != intentLedgerName(owner, live, config) || s.Namespace != owner.GetNamespace() || s.DeletionTimestamp != nil || s.Type != corev1.SecretTypeOpaque || s.Immutable != nil && *s.Immutable || s.Labels[intentRecordLabel] != string(owner.GetUID()) || ref == nil || ref.UID != expected.UID || ref.Kind != expected.Kind || ref.Name != expected.Name || ref.APIVersion != expected.APIVersion {
		return nil, managedWriteBlocked("field history identity mismatch")
	}
	r, err := decodeResourceIntent(s.Data)
	if err != nil || r.Version != currentResourceIntentVersion || r.Desired == nil || r.Pause != nil || r.OwnerUID != owner.GetUID() || r.ResourceUID != live.GetUID() || r.Configuration != config || r.SchemaHash == "" || r.BaseResourceVersion == "" || r.PublicationProtocol != intentPublicationProtocol || r.Desired.GroupVersionKind() != live.GroupVersionKind() || client.ObjectKeyFromObject(r.Desired) != client.ObjectKeyFromObject(live) {
		return nil, managedWriteBlocked("invalid field history")
	}
	if r.Attempt == nil {
		if ledgerPending(s) || r.DeletionBase != nil {
			return nil, managedWriteBlocked("inconsistent confirmed field history")
		}
	} else {
		if r.Attempt.ID == "" || r.Attempt.ID != s.Annotations[AnnotationResourceIntentOperation] {
			return nil, managedWriteBlocked("inconsistent pending field history")
		}
		if r.DeletionBase != nil && r.DeletionBase.Attempt != nil {
			return nil, managedWriteBlocked("nested pending field history")
		}
		if _, err := effectiveResourceIntent(r, s); err != nil {
			return nil, err
		}
	}
	return r, nil
}

func ledgerConflict(name string) error {
	return apierrors.NewConflict(schema.GroupResource{Resource: "secrets"}, name, fmt.Errorf("field transaction changed; recompute from fresh reads"))
}

// A CAS guards every change of candidate; a UID/RV guard independently fences
// the business write. Neither an annotation nor this process's memory is the
// sole means of recovering an operation after its target request loses a response.
func (w *ManagedResourceWriter) publishIntent(ctx context.Context, owner client.Object, live, after *unstructured.Unstructured, candidate, base *resourceIntent, previous *corev1.Secret, plan *DeclaredIntentMergeResult, converter managedfields.TypeConverter, check func() error) error {
	if live.GetUID() == "" || live.GetResourceVersion() == "" {
		return managedWriteBlocked("intent publication requires resource UID and resourceVersion")
	}
	checks, err := StoreDeletionChecks(plan.DeletionChecks)
	if err != nil {
		return err
	}
	candidate.Attempt = &intentAttempt{ID: string(uuid.NewUUID()), Deletions: checks, NamedEmptyLists: plan.NamedEmptyLists()}
	candidate.DeletionBase = base
	ledger, err := buildIntentLedger(owner, live, candidate)
	if err != nil {
		return err
	}
	name := ledger.Name
	submittedData := ledger.DeepCopy().Data
	if isIntentLedger(previous) {
		// Preserve unrelated Secret metadata, and never overwrite another candidate
		// based on a refreshed Secret version.
		ledgerData := ledger.Data
		ledger = previous.DeepCopy()
		ledger.Data = ledgerData
		if ledger.Annotations == nil {
			ledger.Annotations = map[string]string{}
		}
		ledger.Annotations[AnnotationResourceIntentOperation] = candidate.Attempt.ID
		err = w.Client.Update(ctx, ledger)
	} else {
		err = w.Client.Create(ctx, ledger)
		if apierrors.IsAlreadyExists(err) {
			return ledgerConflict(name)
		}
	}
	if err != nil {
		return fmt.Errorf("register field transaction: %w", err)
	}
	if _, err := readIntentLedger(ledger, owner, live, candidate.Configuration); err != nil {
		return err
	}
	if !reflect.DeepEqual(submittedData, ledger.Data) || ledger.Annotations[AnnotationResourceIntentOperation] != candidate.Attempt.ID {
		return managedWriteBlocked("field transaction was not persisted as submitted")
	}
	if err := w.checkIntentWriteOwner(ctx, owner, live, candidate.Configuration); err != nil {
		return err
	}
	if check != nil {
		if err := check(); err != nil {
			return err
		}
	}
	a := after.GetAnnotations()
	if a == nil {
		a = map[string]string{}
	}
	a[AnnotationResourceIntent] = name
	a[AnnotationResourceIntentOperation] = candidate.Attempt.ID
	after.SetAnnotations(a)
	if err := w.commitIntentPatch(ctx, live, after); err != nil {
		return err
	}
	if after.GetUID() != live.GetUID() {
		return managedWriteBlocked("resource identity changed in write response")
	}
	if err := verifyIntentIdentity(owner, after, candidate.Desired, candidate.Configuration); err != nil {
		return err
	}
	if err := plan.Validate(converter, intentObject(after)); err != nil {
		return err
	}
	if after.GetAnnotations()[AnnotationResourceIntent] != name || after.GetAnnotations()[AnnotationResourceIntentOperation] != candidate.Attempt.ID {
		return managedWriteBlocked("resource intent receipt was not accepted")
	}
	if err := w.finishLedger(ctx, owner, live, ledger, candidate); err != nil {
		return err
	}
	if previous != nil && !isIntentLedger(previous) {
		w.discardIntentRecord(ctx, previous)
	}
	return nil
}

func (w *ManagedResourceWriter) finishLedger(ctx context.Context, owner client.Object, live *unstructured.Unstructured, ledger *corev1.Secret, candidate *resourceIntent) error {
	flat := *candidate
	flat.Attempt = nil
	flat.DeletionBase = nil
	flat.pendingDeletion = false
	data, err := encodeResourceIntent(flat)
	if err != nil {
		return err
	}
	old := ledger.DeepCopy()
	ledger.Data = data
	expectedData := ledger.DeepCopy().Data
	delete(ledger.Annotations, AnnotationResourceIntentOperation)
	if err := w.Client.Patch(ctx, ledger, client.MergeFromWithOptions(old, client.MergeFromWithOptimisticLock{})); err != nil {
		return fmt.Errorf("confirm field transaction: %w", err)
	}
	if !reflect.DeepEqual(expectedData, ledger.Data) || ledgerPending(ledger) {
		return managedWriteBlocked("field history confirmation was not accepted")
	}
	_, err = readIntentLedger(ledger, owner, live, candidate.Configuration)
	return err
}

func (w *ManagedResourceWriter) resolvePendingIntent(ctx context.Context, owner client.Object, live, desired *unstructured.Unstructured, config string, converter managedfields.TypeConverter, hash string, base *resourceIntent, previous *corev1.Secret) (*resourceIntent, *corev1.Secret, error) {
	if previous == nil {
		return base, previous, nil
	}
	if !isIntentLedger(previous) {
		return resolveLegacyPending(converter, hash, live, desired, base, previous)
	}
	if !ledgerPending(previous) {
		return base, previous, nil
	}
	pending, err := readIntentLedger(previous, owner, live, config)
	if err != nil {
		return nil, nil, err
	}
	if pending.SchemaHash != DeclaredResourceIntentSchemaHash(hash) || base != nil && !base.matchesSchema(hash) {
		return nil, nil, managedWriteBlocked("pending field topology changed")
	}
	// The original live read preceded the ledger read. Only a fresh observation
	// can prove a target resourceVersion fence was crossed.
	actual := live.DeepCopy()
	if err = w.Reader.Get(ctx, client.ObjectKeyFromObject(live), actual); err != nil {
		return nil, nil, err
	}
	if actual.GetResourceVersion() != live.GetResourceVersion() || actual.GetUID() != live.GetUID() {
		return nil, nil, ledgerConflict(previous.Name)
	}
	if live.GetResourceVersion() == pending.BaseResourceVersion {
		// A timed-out request may still commit. Expire its RV before replacing
		// the pending slot (including when the user withdrew the change).
		if err := w.checkIntentWriteOwner(ctx, owner, live, config); err != nil {
			return nil, nil, err
		}
		fence := previous.DeepCopy()
		fence.UID = types.UID(pending.Attempt.ID)
		gc := ResourceIntentCollector{Client: w.Client, Reader: w.Reader}
		if err := gc.fencePublication(ctx, live, fence); err != nil {
			return nil, nil, err
		}
		return nil, nil, ledgerConflict(previous.Name)
	}
	checks, err := RestoreDeletionChecks(pending.Attempt.Deletions)
	if err != nil {
		return nil, nil, err
	}
	plan, err := NewDeclaredValidationPlan(converter, pending.Desired, checks, pending.Attempt.NamedEmptyLists...)
	if err != nil {
		return nil, nil, err
	}
	if live.GetAnnotations()[AnnotationResourceIntentOperation] == pending.Attempt.ID && verifyIntentIdentity(owner, live, pending.Desired, config) == nil && plan.Validate(converter, intentObject(live)) == nil {
		if err := w.finishLedger(ctx, owner, live, previous, pending); err != nil {
			return nil, nil, err
		}
		pending.Attempt = nil
		pending.DeletionBase = nil
		return pending, previous, nil
	}
	// No target receipt and the RV was fenced by our own recovery, or a
	// competing write expired the original RV before any business write.
	fenced := live.GetLabels()[LabelResourceIntentGCFence] == pending.Attempt.ID
	if !fenced && !reflect.DeepEqual(intentObject(desired).Object, pending.Desired.Object) {
		if !previousProjectionSatisfied(converter, base, pending, live) {
			return nil, nil, managedWriteBlocked("pending field transaction only partially verified; restore the previous or pending declaration before retrying")
		}
	}
	return base, previous, nil
}

// Accept a changed declaration after a failed mixed write only if even the
// candidate-only fields would already be absent under the confirmed baseline.
func previousProjectionSatisfied(c managedfields.TypeConverter, base, pending *resourceIntent, live *unstructured.Unstructured) bool {
	if base == nil {
		return false
	}
	p, err := PlanDeclaredResourceIntent(c, pending.Desired, intentObject(live), base.Desired, base.Desired, nil)
	return err == nil && p.DeletionConflictError() == nil && p.Validate(c, intentObject(live)) == nil
}

// An old v2 pending payload predates persisted checks. Reconstruct only the
// obligations justified by the original baseline and its topology.
func resolveLegacyPending(c managedfields.TypeConverter, hash string, live, desired *unstructured.Unstructured, base *resourceIntent, s *corev1.Secret) (*resourceIntent, *corev1.Secret, error) {
	if base == nil || !base.pendingDeletion {
		return base, s, nil
	}
	candidate, err := decodeResourceIntent(s.Data)
	if err != nil {
		return nil, nil, err
	}
	expectedHash := DeclaredResourceIntentSchemaHash(hash)
	if candidate.SchemaHash != expectedHash || !base.matchesSchema(hash) {
		return nil, nil, managedWriteBlocked("legacy pending field topology changed")
	}
	before := base.Desired
	if base.Effective != nil {
		before = base.Effective
	}
	effective := candidate.Desired
	if candidate.Effective != nil {
		effective = candidate.Effective
	}
	var plan *DeclaredIntentMergeResult
	if base.Version == 1 {
		legacy, e := PlanLegacyResourceIntentDeletions(c, before, before, effective, nil)
		if e != nil {
			return nil, nil, e
		}
		plan, err = NewDeclaredValidationPlan(c, candidate.Desired, legacy.DeletionChecks)
	} else {
		plan, err = PlanDeclaredResourceIntent(c, before, before, effective, candidate.Desired, nil)
	}
	if err != nil {
		return nil, nil, err
	}
	if plan.Validate(c, intentObject(live)) == nil {
		candidate.DeletionBase = nil
		return candidate, s, nil
	}
	if !reflect.DeepEqual(intentObject(desired).Object, candidate.Desired.Object) && !previousProjectionSatisfied(c, base, candidate, live) {
		return nil, nil, managedWriteBlocked("legacy pending field transaction is only partially verified")
	}
	return base, s, nil
}
