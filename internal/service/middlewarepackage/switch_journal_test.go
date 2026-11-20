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

package middlewarepackage

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"math/rand"
	"reflect"
	"strings"
	"testing"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	"github.com/harmonycloud/opensaola/internal/service/packages"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/uuid"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

const journalTestComponent = "JournalTest"

func journalTestClient() client.WithWatch {
	return fake.NewClientBuilder().WithInterceptorFuncs(interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.CreateOption) error {
			o.SetUID(uuid.NewUUID()) // the API server normally supplies this
			return c.Create(ctx, o, opts...)
		},
	}).Build()
}

func journalTestStart(t *testing.T, cli client.Client) (*corev1.Secret, *switchJournal) {
	t.Helper()
	target := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "target", Namespace: packages.DataNamespace, UID: "target-uid"}}
	j := &switchJournal{Phase: "planning", Target: target.Name, TargetUID: target.UID, Digest: "archive-digest"}
	record, err := createSwitchJournal(t.Context(), cli, journalTestComponent, target, j)
	if err != nil {
		t.Fatal(err)
	}
	if record.Labels[v1.LabelProject] != "" {
		t.Fatal("record would be watched as a package")
	}
	return record, j
}

func TestSwitchJournalPhaseAndOwnership(t *testing.T) {
	ctx := t.Context()
	var lastPatch []byte
	var deleted *metav1.Preconditions
	cli := interceptor.NewClient(journalTestClient(), interceptor.Funcs{
		Patch: func(ctx context.Context, c client.WithWatch, o client.Object, p client.Patch, opts ...client.PatchOption) error {
			var err error
			lastPatch, err = p.Data(o)
			if err != nil {
				return err
			}
			return c.Patch(ctx, o, p, opts...)
		},
		Delete: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.DeleteOption) error {
			deleted = (&client.DeleteOptions{}).ApplyOptions(opts).Preconditions
			return c.Delete(ctx, o, opts...)
		},
	})
	if r, j, err := readSwitchJournal(ctx, cli, journalTestComponent); err != nil || r != nil || j != nil {
		t.Fatalf("missing record: %v %v %v", r, j, err)
	}
	record, j := journalTestStart(t, cli)
	stale := record.DeepCopy()
	j.Phase = "prepared"
	j.Sources = []switchSource{{Name: "old", UID: "old-uid", Version: "1.3", Retire: []definitionKey{{Kind: "MiddlewareActionBaseline", Name: "retire"}}}}
	before := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "middleware.cn/v1", "kind": "MiddlewareConfiguration", "spec": map[string]any{"replicas": int64(9007199254740993)}}}
	j.Changes = []switchChange{{Key: definitionKey{Kind: "MiddlewareConfiguration", Name: "shared"}, Before: before, Expected: "expected-hash"}}
	if err := saveSwitchJournal(ctx, cli, record, j); err != nil {
		t.Fatal(err)
	}
	if len(record.Data[switchSnapshotKey]) == 0 || record.ResourceVersion == stale.ResourceVersion {
		t.Fatal("snapshot or updated resourceVersion missing")
	}
	if err := saveSwitchJournal(ctx, cli, stale, j); !apierrors.IsConflict(err) {
		t.Fatalf("stale writer result: %v", err)
	}
	if err := deleteSwitchJournal(ctx, cli, stale); !apierrors.IsConflict(err) {
		t.Fatalf("stale deleter result: %v", err)
	}
	if deleted == nil || *deleted.UID != stale.UID || *deleted.ResourceVersion != stale.ResourceVersion {
		t.Fatalf("missing delete precondition: %v", deleted)
	}
	record, loaded, err := readSwitchJournal(ctx, cli, journalTestComponent)
	if err != nil || !reflect.DeepEqual(loaded, j) {
		t.Fatalf("prepared roundtrip differs: %#v %v", loaded, err)
	}
	loaded.Phase = "rollback"
	loaded.Failure = "write conflict"
	loaded.Terminal = true
	if err := saveSwitchJournal(ctx, cli, record, loaded); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(lastPatch, []byte(switchSnapshotKey)) {
		t.Fatal("phase-only patch rewrote the snapshot")
	}
	_, rolledBack, err := readSwitchJournal(ctx, cli, journalTestComponent)
	if err != nil || !reflect.DeepEqual(rolledBack, loaded) {
		t.Fatalf("rollback receipt: %#v, %v", rolledBack, err)
	}
	if err := deleteSwitchJournal(ctx, cli, record); err != nil {
		t.Fatal(err)
	}
	if r, j, err := readSwitchJournal(ctx, cli, journalTestComponent); err != nil || r != nil || j != nil {
		t.Fatal("deleted record is still visible")
	}
}

func TestSwitchJournalRejectsForeignRecord(t *testing.T) {
	cli := journalTestClient()
	ctx := t.Context()
	record := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: switchRecordName(journalTestComponent), Namespace: packages.DataNamespace, Annotations: map[string]string{v1.LabelComponent: journalTestComponent}}, Type: corev1.SecretTypeOpaque, Data: map[string][]byte{"unrelated": []byte("user data")}}
	if err := cli.Create(ctx, record); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readSwitchJournal(ctx, cli, journalTestComponent); err == nil {
		t.Fatal("unexpectedly read external record")
	}
	target := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "target", Namespace: packages.DataNamespace, UID: types.UID("target-uid")}}
	j := &switchJournal{Phase: "planning", Target: target.Name, TargetUID: target.UID, Digest: "archive-digest"}
	if _, err := createSwitchJournal(ctx, cli, journalTestComponent, target, j); !apierrors.IsAlreadyExists(err) {
		t.Fatalf("occupied name: %v", err)
	}
	if err := saveSwitchJournal(ctx, cli, record, j); err == nil {
		t.Fatal("unexpectedly wrote foreign record")
	}
	if err := deleteSwitchJournal(ctx, cli, record); err == nil {
		t.Fatal("unexpectedly deleted foreign record")
	}
	current := new(corev1.Secret)
	if err := cli.Get(ctx, client.ObjectKeyFromObject(record), current); err != nil || !reflect.DeepEqual(current.Data, record.Data) || current.ResourceVersion != record.ResourceVersion {
		t.Fatalf("unexpected modification of foreign record: %v", err)
	}
}

func TestSwitchJournalRejectsCorruptSnapshot(t *testing.T) {
	ctx := t.Context()
	cli := journalTestClient()
	record, j := journalTestStart(t, cli)
	j.Phase = "prepared"
	if err := saveSwitchJournal(ctx, cli, record, j); err != nil {
		t.Fatal(err)
	}
	record.Data[switchSnapshotKey] = []byte("damaged")
	if err := cli.Update(ctx, record); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readSwitchJournal(ctx, cli, journalTestComponent); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("corrupt snapshot: %v", err)
	}
}

func TestSwitchJournalShardedPreparationSurvivesInterruption(t *testing.T) {
	checkSwitchJournalShardedPreparation(t, journalTestClient())
}

func checkSwitchJournalShardedPreparation(t *testing.T, base client.WithWatch) {
	t.Helper()
	ctx := t.Context()
	// The aggregate compressed snapshot exceeds Kubernetes' 1 MiB limit.
	raw := make([]byte, 2*1024*1024)
	if _, err := rand.New(rand.NewSource(1)).Read(raw); err != nil {
		t.Fatal(err)
	}
	payload := base64.StdEncoding.EncodeToString(raw)
	var first *corev1.Secret
	stop := errors.New("interrupted between snapshot parts")
	cli := interceptor.NewClient(base, interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.CreateOption) error {
			part, ok := o.(*corev1.Secret)
			if !ok || part.Type != switchPartType {
				return c.Create(ctx, o, opts...)
			}
			if first != nil {
				return stop
			}
			if err := c.Create(ctx, o, opts...); err != nil {
				return err
			}
			first = part.DeepCopy()
			return nil
		},
	})
	record, j := journalTestStart(t, cli)
	j.Phase = "prepared"
	j.Changes = []switchChange{{Expected: payload}}
	if err := saveSwitchJournal(ctx, cli, record, j); !errors.Is(err, stop) {
		t.Fatalf("interrupted preparation: %v", err)
	}
	_, partial, err := readSwitchJournal(ctx, base, journalTestComponent)
	if err != nil || partial.Phase != "planning" || len(partial.Changes) != 0 {
		t.Fatalf("partially exposed snapshot: %#v %v", partial, err)
	}
	if first == nil || !metav1.IsControlledBy(first, record) || first.Immutable == nil || !*first.Immutable {
		t.Fatal("staged part is mutable or orphaned")
	}
	if err := saveSwitchJournal(ctx, base, record, j); err != nil {
		t.Fatal(err)
	}
	h, err := decodeSwitchRecord(record, journalTestComponent)
	if err != nil || h.Parts < 2 || len(record.Data[switchSnapshotKey]) != 0 {
		t.Fatalf("unexpected sharded header: %d %v", h.Parts, err)
	}
	var parts corev1.SecretList
	if err := base.List(ctx, &parts, client.InNamespace(record.Namespace)); err != nil {
		t.Fatal(err)
	}
	var count int
	for _, s := range parts.Items {
		if !metav1.IsControlledBy(&s, record) {
			continue
		}
		count++
		if len(s.Data[switchSnapshotKey]) > switchPartSize || s.Labels[v1.LabelProject] != "" {
			t.Fatalf("invalid part %s", s.Name)
		}
		if s.Name == first.Name && (s.UID != first.UID || s.ResourceVersion != first.ResourceVersion) {
			t.Fatal("existing immutable part replaced during retry")
		}
	}
	if count != h.Parts {
		t.Fatalf("%d immutable parts, want %d", count, h.Parts)
	}
	current, prepared, err := readSwitchJournal(ctx, base, journalTestComponent)
	if err != nil || !reflect.DeepEqual(prepared, j) {
		t.Fatalf("prepared snapshot after restart differs (%v)", err)
	}
	prepared.Phase = "committed"
	if err := saveSwitchJournal(ctx, base, current, prepared); err != nil {
		t.Fatal(err)
	}
	// A missing or foreign replacement shard must block recovery.
	if err := base.Delete(ctx, first); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readSwitchJournal(ctx, base, journalTestComponent); !apierrors.IsNotFound(err) {
		t.Fatalf("missing shard result: %v", err)
	}
	foreign := first.DeepCopy()
	foreign.UID = ""
	foreign.ResourceVersion = ""
	foreign.OwnerReferences = nil
	if err := base.Create(ctx, foreign); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readSwitchJournal(ctx, base, journalTestComponent); err == nil {
		t.Fatal("accepted a foreign replacement shard")
	}
}
