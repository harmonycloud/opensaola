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

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// RecreateMissing restores a missing mutable primary without ever updating an
// object which appeared concurrently. POST is deliberate: SSA cannot express
// a create-only operation. The new intent record supplies deletion evidence,
// independently of the Create workflow's managedFields entry.
func (w *ManagedResourceWriter) RecreateMissing(ctx context.Context, owner client.Object, desired *unstructured.Unstructured, configuration string) error {
	if desired == nil || configuration != "" || isImmutableResource(desired) {
		return managedWriteBlocked("missing-resource recovery requires a mutable primary resource")
	}
	if w.IntentSchemas == nil {
		return managedWriteBlocked("mutable resource requires a configured intent schema resolver")
	}
	fresh, err := w.freshWriteOwner(ctx, owner)
	if err != nil {
		return err
	}
	live := &unstructured.Unstructured{}
	live.SetGroupVersionKind(desired.GroupVersionKind())
	created := false
	err = w.Reader.Get(ctx, client.ObjectKeyFromObject(desired), live)
	if apierrors.IsNotFound(err) {
		if err := verifyObservedSpecForWrite(fresh, nil, configuration); err != nil {
			return err
		}
		intent := cleanApplyObject(desired)
		if err := w.preflightResourceIntent(ctx, fresh, intent, configuration); err != nil {
			return err
		}
		// The preflight may have taken time. Recheck owner generation and pause
		// before creating; a racing object is protected by POST/AlreadyExists.
		if fresh, err = w.freshWriteOwner(ctx, owner); err != nil {
			return err
		}
		created, err = w.createMutableResource(ctx, intent)
		if err == nil {
			live = intent
		} else if apierrors.IsAlreadyExists(err) {
			err = w.Reader.Get(ctx, client.ObjectKeyFromObject(desired), live)
		}
	}
	if err != nil {
		return err
	}
	if err := verifyIntentIdentity(fresh, live, desired, configuration); err != nil {
		return err
	}
	// Re-read any pending history even if the pointer already reached the
	// target. A retry may only attribute defaults to the original POST reply.
	return w.initializeResourceIntent(ctx, fresh, live, desired, configuration, created)
}
