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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var ErrReconcilePauseSnapshotInvalid = errors.New("reconcile pause snapshot is invalid")

// The complete observed spec and pre-pause overrides belong in an owned Secret,
// not in Middleware status. The record reuses the intent codec and collector,
// but is explicitly distinct from a rendered-intent baseline.
type reconcilePauseData struct {
	Generation        int64                  `json:"generation"`
	PreviousOverrides *v1.ReconcileOverrides `json:"previousOverrides,omitempty"`
}

type ReconcilePauseSnapshotData struct {
	ActualSpec        map[string]any
	PreviousOverrides *v1.ReconcileOverrides
}

// PrepareReconcilePauseSnapshot stores a baseline before publishing its name in
// MID status. BaseResourceVersion is the MID RV: the reference may be published
// only against that exact RV. The collector fences that publication before
// deleting an abandoned preparation. Retries with identical inputs reuse a name.
func (w *ManagedResourceWriter) PrepareReconcilePauseSnapshot(ctx context.Context, mid *v1.Middleware, live *unstructured.Unstructured) (*v1.ReconcilePauseStatus, error) {
	if mid == nil || live == nil || mid.UID == "" || mid.ResourceVersion == "" {
		return nil, invalidPauseSnapshot("owner UID and resourceVersion are required")
	}
	if err := verifyManagedIdentity(mid, live); err != nil {
		return nil, err
	}
	if live.GetName() != mid.Name || live.GetNamespace() != mid.Namespace {
		return nil, invalidPauseSnapshot("primary resource target mismatch")
	}
	gvk := live.GroupVersionKind()
	if gvk.Version == "" || gvk.Kind == "" {
		return nil, invalidPauseSnapshot("primary resource GVK is required")
	}
	spec, found, err := unstructured.NestedMap(live.Object, "spec")
	if err != nil {
		return nil, invalidPauseSnapshot("actual spec must be an object")
	}
	if !found {
		spec = map[string]any{}
	}
	actual := &unstructured.Unstructured{Object: map[string]any{"spec": spec}}
	actual.SetGroupVersionKind(gvk)
	actual.SetName(live.GetName())
	actual.SetNamespace(live.GetNamespace())
	record := resourceIntent{
		Version: 1, PublicationProtocol: intentPublicationProtocol,
		BaseResourceVersion: mid.ResourceVersion, OwnerUID: mid.UID, ResourceUID: live.GetUID(),
		Desired: actual, Pause: &reconcilePauseData{Generation: mid.Generation, PreviousOverrides: mid.Spec.ReconcileOverrides.DeepCopy()},
	}
	hash, err := pauseRecordHash(&record)
	if err != nil {
		return nil, err
	}
	secret, err := buildResourceIntentSecret(mid, record)
	if err != nil {
		return nil, err
	}
	secret.GenerateName = ""
	secret.Name = "opensaola-pause-" + hash[:40]
	snapshot := &v1.ReconcilePauseStatus{
		SnapshotSecret: secret.Name, GVK: v1.GVK{Group: gvk.Group, Version: gvk.Version, Kind: gvk.Kind},
		Namespace: live.GetNamespace(), Name: live.GetName(), ResourceUID: string(live.GetUID()),
		Generation: mid.Generation, Hash: hash, CapturedAt: metav1.Now(),
	}
	if err := w.Client.Create(ctx, secret, client.FieldOwner(v1.FieldOwner)); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return nil, fmt.Errorf("create reconcile pause Secret: %w", err)
		}
		// An existing deterministic name is reusable only after verifying its
		// owner, immutable contents and full hash through the direct reader.
		if _, _, err := w.loadReconcilePauseSnapshot(ctx, mid, snapshot); err != nil {
			return nil, err
		}
	}
	return snapshot, nil
}

func (w *ManagedResourceWriter) LoadReconcilePauseSnapshot(ctx context.Context, mid *v1.Middleware, snapshot *v1.ReconcilePauseStatus) (*ReconcilePauseSnapshotData, error) {
	record, _, err := w.loadReconcilePauseSnapshot(ctx, mid, snapshot)
	if err != nil {
		return nil, err
	}
	spec, found, err := unstructured.NestedMap(record.Desired.Object, "spec")
	if err != nil || !found {
		return nil, invalidPauseSnapshot("actual spec must be an object")
	}
	return &ReconcilePauseSnapshotData{ActualSpec: spec, PreviousOverrides: record.Pause.PreviousOverrides.DeepCopy()}, nil
}

func (w *ManagedResourceWriter) loadReconcilePauseSnapshot(ctx context.Context, mid *v1.Middleware, snapshot *v1.ReconcilePauseStatus) (*resourceIntent, *corev1.Secret, error) {
	if mid == nil || mid.UID == "" || snapshot == nil || snapshot.SnapshotSecret == "" || snapshot.Hash == "" ||
		snapshot.ResourceUID == "" || snapshot.Name == "" || snapshot.GVK.Version == "" || snapshot.GVK.Kind == "" {
		return nil, nil, invalidPauseSnapshot("Secret reference and target identity are required; use resume-policy=apply only to discard paused changes")
	}
	secret := &corev1.Secret{}
	if err := w.Reader.Get(ctx, client.ObjectKey{Namespace: mid.Namespace, Name: snapshot.SnapshotSecret}, secret); err != nil {
		return nil, nil, fmt.Errorf("%w: read Secret %s: %w", ErrReconcilePauseSnapshotInvalid, snapshot.SnapshotSecret, err)
	}
	expected, err := intentOwnerReference(mid)
	if err != nil {
		return nil, nil, err
	}
	ref := metav1.GetControllerOf(secret)
	if secret.Type != corev1.SecretTypeOpaque || secret.Immutable == nil || !*secret.Immutable || secret.DeletionTimestamp != nil ||
		secret.Labels[intentRecordLabel] != string(mid.UID) || ref == nil || ref.UID != expected.UID || ref.Name != expected.Name ||
		ref.Kind != expected.Kind || ref.APIVersion != expected.APIVersion {
		return nil, nil, invalidPauseSnapshot("Secret ownership or immutable identity mismatch")
	}
	record, err := decodeResourceIntent(secret.Data)
	if err != nil || record.Version != 1 || record.Pause == nil || record.Desired == nil || record.OwnerUID != mid.UID ||
		string(record.ResourceUID) != snapshot.ResourceUID || record.Pause.Generation != snapshot.Generation ||
		record.PublicationProtocol != intentPublicationProtocol || record.BaseResourceVersion == "" ||
		record.Configuration != "" || record.SchemaHash != "" || record.Effective != nil || len(record.Preserved) != 0 {
		return nil, nil, invalidPauseSnapshot("Secret record identity mismatch")
	}
	gvk := schema.GroupVersionKind{Group: snapshot.GVK.Group, Version: snapshot.GVK.Version, Kind: snapshot.GVK.Kind}
	if record.Desired.GroupVersionKind() != gvk || record.Desired.GetNamespace() != snapshot.Namespace || record.Desired.GetName() != snapshot.Name ||
		snapshot.Namespace != mid.Namespace || snapshot.Name != mid.Name {
		return nil, nil, invalidPauseSnapshot("Secret target mismatch")
	}
	hash, err := pauseRecordHash(record)
	if err != nil || hash != snapshot.Hash {
		return nil, nil, invalidPauseSnapshot("Secret record hash mismatch")
	}
	return record, secret, nil
}

// PublishReconcilePauseSnapshot never retries a prepared reference at a newer
// owner RV. This prevents a delayed status write from reattaching a Secret that
// the collector has fenced and removed. Conflict means recapture on next pass.
func (w *ManagedResourceWriter) PublishReconcilePauseSnapshot(ctx context.Context, mid *v1.Middleware, snapshot *v1.ReconcilePauseStatus, mutate func(*v1.MiddlewareStatus)) error {
	record, _, err := w.loadReconcilePauseSnapshot(ctx, mid, snapshot)
	if err != nil {
		return err
	}
	now := &v1.Middleware{}
	if err := w.Reader.Get(ctx, client.ObjectKeyFromObject(mid), now); err != nil {
		return err
	}
	if now.UID != mid.UID || now.DeletionTimestamp != nil || now.Status.ReconcilePause != nil ||
		now.ResourceVersion != record.BaseResourceVersion || mid.ResourceVersion != record.BaseResourceVersion ||
		!v1.IsReconcileSuspended(now.Annotations) || now.Annotations[v1.AnnotationResumePolicy] != "" {
		return apierrors.NewConflict(schema.GroupResource{Group: v1.GroupVersion.Group, Resource: "middlewares"}, mid.Name, errors.New("pause capture owner changed"))
	}
	now.Status.ReconcilePause = snapshot.DeepCopy()
	mutate(&now.Status)
	return w.Client.Status().Update(ctx, now)
}

// CleanupReconcilePauseSnapshot deletes only a verified, detached record. A
// failed cleanup is handled by the existing background collector; a current
// reference or a still-publishable preparation is always retained here.
func (w *ManagedResourceWriter) CleanupReconcilePauseSnapshot(ctx context.Context, mid *v1.Middleware, snapshot *v1.ReconcilePauseStatus) error {
	if snapshot == nil || snapshot.SnapshotSecret == "" {
		return nil
	}
	record, secret, err := w.loadReconcilePauseSnapshot(ctx, mid, snapshot)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	now := &v1.Middleware{}
	if err := w.Reader.Get(ctx, client.ObjectKeyFromObject(mid), now); err != nil {
		return err
	}
	if now.UID != mid.UID || now.DeletionTimestamp != nil || now.ResourceVersion == record.BaseResourceVersion ||
		now.Status.ReconcilePause != nil && now.Status.ReconcilePause.SnapshotSecret == secret.Name {
		return nil
	}
	_, err = (&ResourceIntentCollector{Client: w.Client}).deleteRecord(ctx, secret)
	return err
}

func pauseRecordHash(record *resourceIntent) (string, error) {
	raw, err := json.Marshal(record)
	if err != nil {
		return "", invalidPauseSnapshot("cannot encode record")
	}
	// Canonicalize RawExtension contents as well as map ordering, so immutable
	// storage and subsequent decoding produce the same complete-record hash.
	var normalized any
	if err := json.Unmarshal(raw, &normalized); err != nil {
		return "", invalidPauseSnapshot("cannot normalize record")
	}
	raw, err = json.Marshal(normalized)
	if err != nil {
		return "", invalidPauseSnapshot("cannot normalize record")
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

func invalidPauseSnapshot(message string) error {
	return fmt.Errorf("%w: %s", ErrReconcilePauseSnapshotInvalid, message)
}
