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
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestPauseSnapshotSecretRoundTrip(t *testing.T) {
	ctx := context.Background()
	w, mid, live := pauseSecretUnitFixture(t)
	live.Object["spec"] = map[string]any{"secretValue": "must-stay-in-secret", "large": strings.Repeat("x", 700*1024)}
	mid.Spec.ReconcileOverrides = &v1.ReconcileOverrides{
		BaseSpec:  runtime.RawExtension{Raw: []byte(` {"replicas":3} `)},
		SpecPatch: runtime.RawExtension{Raw: []byte(` {"replicas":5} `)},
	}
	snapshot, err := w.PrepareReconcilePauseSnapshot(ctx, mid, live)
	if err != nil {
		t.Fatal(err)
	}
	again, err := w.PrepareReconcilePauseSnapshot(ctx, mid, live)
	if err != nil || again.SnapshotSecret != snapshot.SnapshotSecret {
		t.Fatalf("identical preparation not reused: %#v, %v", again, err)
	}
	secret := &corev1.Secret{}
	if err := w.Reader.Get(ctx, client.ObjectKey{Namespace: mid.Namespace, Name: snapshot.SnapshotSecret}, secret); err != nil {
		t.Fatal(err)
	}
	if len(secret.Data[intentZstdKey]) == 0 || secret.Immutable == nil || !*secret.Immutable || metav1.GetControllerOf(secret).UID != mid.UID {
		t.Fatal("pause snapshot was not compressed, immutable and owned")
	}
	data, err := w.LoadReconcilePauseSnapshot(ctx, mid, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(data.ActualSpec, live.Object["spec"]) {
		t.Fatal("actual spec was not preserved")
	}
	if data.PreviousOverrides == nil || !jsonEquivalent(data.PreviousOverrides.SpecPatch.Raw, mid.Spec.ReconcileOverrides.SpecPatch.Raw) {
		t.Fatal("pre-pause overrides were not preserved")
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > 1024 || strings.Contains(string(raw), "actualSpec") || strings.Contains(string(raw), "previousOverrides") || strings.Contains(string(raw), "must-stay-in-secret") {
		t.Fatal("MID status contains snapshot payload")
	}
	// A pause record must never become a normal write/delete intent baseline.
	setIntentPointer(live, snapshot.SnapshotSecret)
	if _, _, err := w.loadResourceIntent(ctx, mid, live, ""); err == nil {
		t.Fatal("normal writer accepted an observed pause baseline")
	}
}

func TestPauseSnapshotRejectsUntrustedReferences(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*v1.Middleware, *v1.ReconcilePauseStatus, *corev1.Secret)
	}{
		{"missing reference", func(_ *v1.Middleware, s *v1.ReconcilePauseStatus, _ *corev1.Secret) { s.SnapshotSecret = "" }},
		{"missing Secret", func(_ *v1.Middleware, s *v1.ReconcilePauseStatus, _ *corev1.Secret) { s.SnapshotSecret = "missing" }},
		{"wrong namespace", func(m *v1.Middleware, _ *v1.ReconcilePauseStatus, _ *corev1.Secret) { m.Namespace = "foreign" }},
		{"replaced MID", func(m *v1.Middleware, _ *v1.ReconcilePauseStatus, _ *corev1.Secret) { m.UID = "replacement" }},
		{"wrong controller UID", func(_ *v1.Middleware, _ *v1.ReconcilePauseStatus, s *corev1.Secret) {
			s.OwnerReferences[0].UID = "foreign"
		}},
		{"wrong owner label", func(_ *v1.Middleware, _ *v1.ReconcilePauseStatus, s *corev1.Secret) {
			s.Labels[intentRecordLabel] = "foreign"
		}},
		{"mutable record", func(_ *v1.Middleware, _ *v1.ReconcilePauseStatus, s *corev1.Secret) { b := false; s.Immutable = &b }},
		{"hash changed", func(_ *v1.Middleware, s *v1.ReconcilePauseStatus, _ *corev1.Secret) { s.Hash = "wrong" }},
		{"target UID changed", func(_ *v1.Middleware, s *v1.ReconcilePauseStatus, _ *corev1.Secret) { s.ResourceUID = "wrong" }},
		{"target GVK changed", func(_ *v1.Middleware, s *v1.ReconcilePauseStatus, _ *corev1.Secret) { s.GVK.Kind = "Wrong" }},
		{"capture generation changed", func(_ *v1.Middleware, s *v1.ReconcilePauseStatus, _ *corev1.Secret) { s.Generation++ }},
		{"corrupt content", func(_ *v1.Middleware, _ *v1.ReconcilePauseStatus, s *corev1.Secret) {
			s.Data = map[string][]byte{intentJSONKey: []byte(`{"version":1}`)}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			w, mid, live := pauseSecretUnitFixture(t)
			snapshot, err := w.PrepareReconcilePauseSnapshot(ctx, mid, live)
			if err != nil {
				t.Fatal(err)
			}
			secret := &corev1.Secret{}
			if err := w.Reader.Get(ctx, client.ObjectKey{Namespace: mid.Namespace, Name: snapshot.SnapshotSecret}, secret); err != nil {
				t.Fatal(err)
			}
			tc.mutate(mid, snapshot, secret)
			if err := w.Client.Update(ctx, secret); err != nil {
				t.Fatal(err)
			}
			if _, err := w.LoadReconcilePauseSnapshot(ctx, mid, snapshot); !errors.Is(err, ErrReconcilePauseSnapshotInvalid) {
				t.Fatalf("invalid baseline accepted: %v", err)
			}
		})
	}
}

func pauseSecretUnitFixture(t *testing.T) (*ManagedResourceWriter, *v1.Middleware, *unstructured.Unstructured) {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{v1.AddToScheme, corev1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	mid := &v1.Middleware{ObjectMeta: metav1.ObjectMeta{Name: "database", Namespace: "test", UID: "mid-uid", ResourceVersion: "10", Generation: 1, Annotations: map[string]string{v1.AnnotationSuspendReconcile: "true"}}}
	ref, err := intentOwnerReference(mid)
	if err != nil {
		t.Fatal(err)
	}
	live := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "pause.test.io/v1", "kind": "Database", "spec": map[string]any{"replicas": int64(3)}}}
	live.SetName(mid.Name)
	live.SetNamespace(mid.Namespace)
	live.SetUID("primary-uid")
	live.SetOwnerReferences([]metav1.OwnerReference{ref})
	cli := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(mid).WithObjects(mid).Build()
	return &ManagedResourceWriter{Client: cli, Reader: cli}, mid, live
}

func jsonEquivalent(a, b []byte) bool {
	var left, right any
	return json.Unmarshal(a, &left) == nil && json.Unmarshal(b, &right) == nil && reflect.DeepEqual(left, right)
}
