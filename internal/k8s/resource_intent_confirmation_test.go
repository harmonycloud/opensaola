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
	"errors"
	"reflect"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestPendingIntentUsesLastSuccessfulHistory(t *testing.T) {
	for _, tc := range []struct {
		name             string
		mutate           func(*resourceIntent, *corev1.Secret)
		wantOld, wantErr bool
	}{
		{name: "unconfirmed survives process restart", wantOld: true},
		{name: "confirmed uses new declaration", mutate: func(_ *resourceIntent, s *corev1.Secret) {
			s.Annotations = map[string]string{annotationResourceIntentVerified: "committed-rv"}
		}},
		{name: "old v2 without deletion base remains valid", mutate: func(r *resourceIntent, _ *corev1.Secret) { r.DeletionBase = nil }},
		{name: "pending v1 deletion keeps v1 semantics", wantOld: true, mutate: func(r *resourceIntent, _ *corev1.Secret) { r.DeletionBase.Version = 1 }},
		{name: "nested histories rejected", wantErr: true, mutate: func(r *resourceIntent, _ *corev1.Secret) { r.DeletionBase.DeletionBase = &resourceIntent{} }},
		{name: "different target rejected", wantErr: true, mutate: func(r *resourceIntent, _ *corev1.Secret) { r.DeletionBase.ResourceUID = "foreign" }},
		{name: "different owner rejected", wantErr: true, mutate: func(r *resourceIntent, _ *corev1.Secret) { r.DeletionBase.OwnerUID = "foreign" }},
		{name: "different configuration rejected", wantErr: true, mutate: func(r *resourceIntent, _ *corev1.Secret) { r.DeletionBase.Configuration = "foreign" }},
		{name: "pause history rejected", wantErr: true, mutate: func(r *resourceIntent, _ *corev1.Secret) { r.DeletionBase.Pause = &reconcilePauseData{} }},
		{name: "unknown history version rejected", wantErr: true, mutate: func(r *resourceIntent, _ *corev1.Secret) { r.DeletionBase.Version = 100 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			owner, live := intentUnitOwner(), intentUnitResource()
			base := &resourceIntent{Version: 2, SchemaHash: "old", OwnerUID: owner.UID, ResourceUID: live.GetUID(), Desired: intentObject(live)}
			desired := intentObject(live)
			delete(desired.Object, "data")
			candidate := &resourceIntent{Version: 2, SchemaHash: "new", OwnerUID: owner.UID, ResourceUID: live.GetUID(), Desired: desired, DeletionBase: base}
			secret := &corev1.Secret{}
			if tc.mutate != nil {
				tc.mutate(candidate, secret)
			}
			data, err := encodeResourceIntent(*candidate)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := decodeResourceIntent(data)
			if err != nil {
				t.Fatal(err)
			}
			effective, err := effectiveResourceIntent(decoded, secret)
			if (err != nil) != tc.wantErr {
				t.Fatalf("unexpected pending history validation: %v", err)
			}
			if tc.wantErr {
				return
			}
			want := candidate
			if tc.wantOld {
				want = base
			}
			if effective.Version != want.Version || effective.SchemaHash != want.SchemaHash || !reflect.DeepEqual(effective.Desired, want.Desired) || effective.DeletionBase != nil {
				t.Fatal("selected history lost its values/semantics or retained recursive data")
			}
		})
	}
}

func TestLedgerConfirmationOnlyWritesHistoryAndRejectsStaleVersion(t *testing.T) {
	ctx := context.Background()
	owner, live := intentUnitOwner(), intentUnitResource()
	ref, err := intentOwnerReference(owner)
	if err != nil {
		t.Fatal(err)
	}
	live.SetOwnerReferences([]metav1.OwnerReference{ref})
	base := &resourceIntent{Version: 2, SchemaHash: "schema", OwnerUID: owner.UID, ResourceUID: live.GetUID(), Desired: intentObject(live)}
	desired := intentObject(live)
	delete(desired.Object, "data")
	candidate := newIntentCandidate(owner, live, desired, nil, "", "schema", nil)
	candidate.DeletionBase = base
	candidate.Attempt = &intentAttempt{ID: "test-operation"}
	secret, err := buildIntentLedger(owner, live, candidate)
	if err != nil {
		t.Fatal(err)
	}
	cli := intentUnitClient(t, owner, live, secret)
	if err := cli.Get(ctx, client.ObjectKeyFromObject(secret), secret); err != nil {
		t.Fatal(err)
	}
	previous := secret.DeepCopy()
	writer := &ManagedResourceWriter{Client: cli, Reader: cli}
	if err := writer.finishLedger(ctx, owner, live, secret, candidate); err != nil {
		t.Fatal(err)
	}
	if err := writer.finishLedger(ctx, owner, live, previous, candidate); !apierrors.IsConflict(err) {
		t.Fatalf("stale confirmation accepted: %v", err)
	}
	if ledgerPending(secret) || secret.ResourceVersion == previous.ResourceVersion {
		t.Fatal("confirmation failed to advance the record")
	}
	actual := live.DeepCopy()
	if err := cli.Get(ctx, client.ObjectKeyFromObject(live), actual); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(actual.Object, live.Object) {
		t.Fatal("confirmation patched the primary resource again")
	}
	decoded, err := decodeResourceIntent(secret.Data)
	if err != nil {
		t.Fatal(err)
	}
	confirmed, err := effectiveResourceIntent(decoded, secret)
	if err != nil || !reflect.DeepEqual(confirmed.Desired, desired) {
		t.Fatalf("confirmation did not promote verified history: %v", err)
	}
}

// Pending ledgers written by an older controller have no persisted check for
// withdrawals inside atomic fields. An operation token alone is not a receipt.
func TestPendingLedgerAtomicWithdrawalBeforeUpgrade(t *testing.T) {
	const schemaHash = "legacy-atomic"
	converter := declaredTestConverter(t)
	const withDefault = `{"atomicMap":{"keep":"yes","drop":"again","external":"keep"}}`
	const withoutDefault = `{"atomicMap":{"keep":"yes","external":"keep"}}`
	const operation = "previous-op"
	for _, tc := range []struct {
		name, spec, receipt               string
		confirmed, legacy, storedDeletion bool
	}{
		{name: "v2 legacy attempt still contains old field", spec: withDefault, receipt: operation},
		{name: "v2 verified receipt", spec: withoutDefault, receipt: operation, confirmed: true},
		{name: "v1 legacy attempt still contains old field", spec: withDefault, receipt: operation, legacy: true},
		{name: "v1 verified receipt", spec: withoutDefault, receipt: operation, legacy: true, confirmed: true},
		{name: "persisted strict check survives upgrade", spec: withDefault, receipt: operation, storedDeletion: true},
		{name: "persisted strict check fulfilled", spec: withoutDefault, receipt: operation, storedDeletion: true, confirmed: true},
		{name: "absent receipt cannot promote matching projection", spec: withoutDefault},
		{name: "different operation cannot promote matching projection", spec: withoutDefault, receipt: "another-op"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			owner := intentUnitOwner()
			live := intentTestObject(t, tc.spec)
			live.SetResourceVersion("11")
			live.SetNamespace(owner.Namespace)
			live.SetUID("target-uid")
			ref, err := intentOwnerReference(owner)
			if err != nil {
				t.Fatal(err)
			}
			live.SetOwnerReferences([]metav1.OwnerReference{ref})
			live.SetAnnotations(map[string]string{AnnotationResourceIntentOperation: tc.receipt})
			before := intentTestObject(t, `{"atomicMap":{"keep":"yes","drop":"old"}}`)
			raw := intentTestObject(t, `{"atomicMap":{"keep":"yes"}}`)
			before.SetNamespace(owner.Namespace)
			raw.SetNamespace(owner.Namespace)
			history := newIntentCandidate(owner, live, before, nil, "", schemaHash, nil)
			if tc.legacy {
				history.Version = 1
				history.SchemaHash = schemaHash
			}
			pending := newIntentCandidate(owner, live, raw, nil, "", schemaHash, nil)
			pending.BaseResourceVersion = "10"
			pending.Attempt = &intentAttempt{ID: operation}
			if tc.storedDeletion {
				pending.Attempt.AtomicWithdrawals = true
				pending.Attempt.Deletions = []StoredDeletionCheck{{Segments: []string{"f:spec", "f:atomicMap", "f:drop"}}}
			}
			pending.DeletionBase = history
			ledger, err := buildIntentLedger(owner, live, pending)
			if err != nil {
				t.Fatal(err)
			}
			cli := intentUnitClient(t, owner, live, ledger)
			if err := cli.Get(t.Context(), client.ObjectKeyFromObject(ledger), ledger); err != nil {
				t.Fatal(err)
			}
			writer := &ManagedResourceWriter{Client: cli, Reader: cli}
			recovered, _, err := writer.resolvePendingIntent(t.Context(), owner, live, raw, "", converter, schemaHash, history, ledger)
			if err != nil {
				t.Fatal(err)
			}
			want := history
			if tc.confirmed {
				want = pending
			}
			if !reflect.DeepEqual(recovered.Desired.Object, want.Desired.Object) {
				t.Fatalf("incorrect confirmed baseline: %v", recovered.Desired.Object["spec"])
			}
			if err := cli.Get(t.Context(), client.ObjectKeyFromObject(ledger), ledger); err != nil {
				t.Fatal(err)
			}
			if ledgerPending(ledger) == tc.confirmed {
				t.Fatalf("pending=%t, want confirmed=%t", ledgerPending(ledger), tc.confirmed)
			}
		})
	}
}

// Use the submitted JSON Patch for both preview and commit. The controller-runtime
// fake skips dry-run patches, so preview applies it to a private copy of the target.
type atomicReceiptClient struct {
	client.Client
	t                 *testing.T
	writes            int
	lastOperation     string
	crashAfterPrepare bool
}

var errCrashAfterPrepare = errors.New("simulated crash after preparing a retry")

func (c *atomicReceiptClient) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	if err := c.Client.Update(ctx, obj, opts...); err != nil {
		return err
	}
	if secret, ok := obj.(*corev1.Secret); ok && ledgerPending(secret) && c.crashAfterPrepare {
		c.crashAfterPrepare = false
		return errCrashAfterPrepare
	}
	return nil
}

func (c *atomicReceiptClient) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	target, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return c.Client.Patch(ctx, obj, patch, opts...)
	}
	options := (&client.PatchOptions{}).ApplyOptions(opts)
	if len(options.DryRun) != 0 {
		shadow := intentUnitClient(c.t, target.DeepCopy())
		return shadow.Patch(ctx, target, patch)
	}
	if err := c.Client.Patch(ctx, obj, patch, opts...); err != nil {
		return err
	}
	if token := target.GetAnnotations()[AnnotationResourceIntentOperation]; token == c.lastOperation {
		return nil // Recovery fenced the target RV without submitting a business write.
	} else {
		c.lastOperation = token
	}
	c.writes++
	if c.writes == 1 {
		if _, exists, err := unstructured.NestedFieldNoCopy(target.Object, "spec", "atomicMap", "limit"); err != nil || exists {
			c.t.Fatalf("first submission retained omitted limit: exists=%v err=%v", exists, err)
		}
		if err := unstructured.SetNestedField(target.Object, "wrong", "spec", "atomicMap", "max"); err != nil {
			return err
		}
	}
	if err := unstructured.SetNestedField(target.Object, "default", "spec", "atomicMap", "limit"); err != nil {
		return err
	}
	return c.Client.Update(ctx, target)
}

// A valid operation receipt plus the wrong declared max is an unconfirmed
// write. The same declaration must correct max even though limit was defaulted.
func TestAtomicReleaseRetriesAfterInvalidCommittedReceipt(t *testing.T) {
	for _, tc := range []struct {
		name  string
		crash bool
	}{
		{"retry with first receipt", false}, {"restart after retry preparation", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			const hash = "unit-schema"
			owner := intentUnitOwner()
			before := intentTestObject(t, `{"atomicMap":{"max":"2","limit":"old"}}`)
			raw := intentTestObject(t, `{"atomicMap":{"max":"2"}}`)
			for _, obj := range []*unstructured.Unstructured{before, raw} {
				obj.SetNamespace(owner.Namespace)
			}
			live := before.DeepCopy()
			live.SetUID("target-uid")
			live.SetResourceVersion("10")
			ref, err := intentOwnerReference(owner)
			if err != nil {
				t.Fatal(err)
			}
			live.SetOwnerReferences([]metav1.OwnerReference{ref})
			store := intentUnitClient(t, owner, live)
			hook := &atomicReceiptClient{Client: store, t: t}
			converter := declaredTestConverter(t)
			writer := &ManagedResourceWriter{Client: hook, Reader: store, IntentSchemas: intentUnitSchemas{converter: converter}}
			plan, err := PlanDeclaredResourceIntent(converter, before, before, raw, raw, nil)
			if err != nil {
				t.Fatal(err)
			}
			after := live.DeepCopy()
			after.Object["spec"] = plan.Object.Object["spec"]
			base := newIntentCandidate(owner, live, before, nil, "", hash, nil)
			pending := newIntentCandidate(owner, live, raw, nil, "", hash, nil)
			err = writer.publishIntent(ctx, owner, live, after, pending, base, nil, plan, converter, nil)
			if err == nil || !strings.Contains(err.Error(), "declared fields") {
				t.Fatalf("invalid committed max was not rejected: %v", err)
			}
			restarted := &ManagedResourceWriter{Client: hook, Reader: store, IntentSchemas: intentUnitSchemas{converter: converter}}
			if tc.crash {
				if err := store.Get(ctx, client.ObjectKeyFromObject(live), live); err != nil {
					t.Fatal(err)
				}
				firstReceipt, resourceVersion := live.GetAnnotations()[AnnotationResourceIntentOperation], live.GetResourceVersion()
				hook.crashAfterPrepare = true
				if err := restarted.reconcileResourceIntent(ctx, owner, live, raw, "", false); !errors.Is(err, errCrashAfterPrepare) {
					t.Fatalf("retry preparation: %v", err)
				}
				restarted = &ManagedResourceWriter{Client: hook, Reader: store, IntentSchemas: intentUnitSchemas{converter: converter}}
				if err := store.Get(ctx, client.ObjectKeyFromObject(live), live); err != nil {
					t.Fatal(err)
				}
				if live.GetResourceVersion() != resourceVersion || live.GetAnnotations()[AnnotationResourceIntentOperation] != firstReceipt {
					t.Fatal("target changed before the retry was submitted")
				}
				if err := restarted.reconcileResourceIntent(ctx, owner, live, raw, "", false); !apierrors.IsConflict(err) {
					t.Fatalf("expected old resourceVersion to be fenced after restart: %v", err)
				}
				if err := store.Get(ctx, client.ObjectKeyFromObject(live), live); err != nil {
					t.Fatal(err)
				}
				if live.GetResourceVersion() == resourceVersion || live.GetAnnotations()[AnnotationResourceIntentOperation] != firstReceipt || live.GetLabels()[LabelResourceIntentGCFence] == "" {
					t.Fatal("fence failed to expire the interrupted retry while preserving its last receipt")
				}
			}
			for i := range 2 {
				if err := store.Get(ctx, client.ObjectKeyFromObject(live), live); err != nil {
					t.Fatal(err)
				}
				if err := restarted.reconcileResourceIntent(ctx, owner, live, raw, "", false); err != nil {
					t.Fatalf("reconcile %d after restart: %v", i+1, err)
				}
			}
			if err := store.Get(ctx, client.ObjectKeyFromObject(live), live); err != nil {
				t.Fatal(err)
			}
			final, _, err := unstructured.NestedStringMap(live.Object, "spec", "atomicMap")
			if err != nil || !reflect.DeepEqual(final, map[string]string{"max": "2", "limit": "default"}) || hook.writes != 2 {
				t.Fatalf("final=%v total business writes=%d error=%v", final, hook.writes, err)
			}
			record, ledger, err := restarted.loadResourceHistory(ctx, owner, live, "", false)
			if err != nil || record == nil || ledgerPending(ledger) || !reflect.DeepEqual(record.Desired.Object, intentObject(raw).Object) {
				t.Fatalf("retry did not confirm the new raw declaration: %v", err)
			}
		})
	}
}

func TestChangingUnconfirmedIntentRequiresPreviousProjection(t *testing.T) {
	converter := declaredTestConverter(t)
	owner := intentUnitOwner()
	previous := intentTestObject(t, `{"atomicMap":{"keep":"yes","first":"old"}}`)
	attempted := intentTestObject(t, `{"atomicMap":{"keep":"yes","second":"attempt"}}`)
	base := newIntentCandidate(owner, previous, previous, nil, "", "unit-schema", nil)
	pending := newIntentCandidate(owner, previous, attempted, nil, "", "unit-schema", nil)
	for _, tc := range []struct {
		name, spec string
		reverted   bool
	}{
		{"only previous and external default remain", `{"atomicMap":{"keep":"yes","first":"old","external":"server"}}`, true},
		{"partial write leaves candidate-only field", `{"atomicMap":{"keep":"yes","first":"old","second":"attempt","external":"server"}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if ok := previousProjectionSatisfied(converter, base, pending, intentTestObject(t, tc.spec)); ok != tc.reverted {
				t.Fatalf("accept changed declaration=%t, want %t", ok, tc.reverted)
			}
		})
	}
}
