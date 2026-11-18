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
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
