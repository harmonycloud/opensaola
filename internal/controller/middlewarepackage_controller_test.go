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

package controller

import (
	"context"
	"fmt"
	"testing"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	"github.com/harmonycloud/opensaola/internal/service/consts"
	corev1 "k8s.io/api/core/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/event"
)

func TestMiddlewarePackageSecretPredicate_UpdateFilter(t *testing.T) {
	r := &MiddlewarePackageReconciler{}
	pred := r.secretPredicate()

	base := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pkg-a",
			Namespace: "ns",
			Labels: map[string]string{
				v1.LabelProject: consts.ProjectOpenSaola,
			},
			Annotations: map[string]string{},
		},
		Data: map[string][]byte{
			"k": []byte("v"),
		},
	}

	t.Run("metadata-only change ignored", func(t *testing.T) {
		oldSecret := base.DeepCopy()
		newSecret := base.DeepCopy()
		newSecret.Labels[v1.LabelEnabled] = "true"

		if pred.Update(event.UpdateEvent{ObjectOld: oldSecret, ObjectNew: newSecret}) {
			t.Fatalf("expected Update predicate to ignore metadata-only changes")
		}
	})

	t.Run("data change enqueued", func(t *testing.T) {
		oldSecret := base.DeepCopy()
		newSecret := base.DeepCopy()
		newSecret.Data["k"] = []byte("v2")

		if !pred.Update(event.UpdateEvent{ObjectOld: oldSecret, ObjectNew: newSecret}) {
			t.Fatalf("expected Update predicate to enqueue on data change")
		}
	})

	t.Run("install annotation change enqueued", func(t *testing.T) {
		oldSecret := base.DeepCopy()
		newSecret := base.DeepCopy()
		newSecret.Annotations[v1.LabelInstall] = "true"

		if !pred.Update(event.UpdateEvent{ObjectOld: oldSecret, ObjectNew: newSecret}) {
			t.Fatalf("expected Update predicate to enqueue on install annotation change")
		}
	})
}

func TestMiddlewarePackageSecretToRequests(t *testing.T) {
	r := &MiddlewarePackageReconciler{}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pkg-a",
			Namespace: "ns",
			Labels: map[string]string{
				v1.LabelProject: consts.ProjectOpenSaola,
			},
		},
	}

	reqs := r.secretToRequests(context.Background(), secret)
	if len(reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(reqs))
	}
	if got, want := reqs[0].Namespace, "ns"; got != want {
		t.Fatalf("unexpected namespace: got %q want %q", got, want)
	}
	if got, want := reqs[0].Name, secretRequestPrefix+"pkg-a"; got != want {
		t.Fatalf("unexpected name: got %q want %q", got, want)
	}
}

func TestMiddlewarePackageSecretErrorResult(t *testing.T) {
	t.Run("wrapped not found requeues", func(t *testing.T) {
		err := fmt.Errorf("publish package resources: %w", apiErrors.NewNotFound(
			schema.GroupResource{Group: "middleware.cn", Resource: "middlewarepackages"},
			"pkg-a",
		))

		result, gotErr := middlewarePackageSecretErrorResult(err)
		if gotErr != nil {
			t.Fatalf("expected not found to be swallowed for retry, got %v", gotErr)
		}
		if result.RequeueAfter <= 0 {
			t.Fatalf("expected retry for transient not found, got %#v", result)
		}
	})

	t.Run("other errors are returned", func(t *testing.T) {
		err := fmt.Errorf("boom")
		result, gotErr := middlewarePackageSecretErrorResult(err)
		if gotErr == nil {
			t.Fatalf("expected error to be returned")
		}
		if result.Requeue || result.RequeueAfter != 0 {
			t.Fatalf("expected no retry result for non-not-found error, got %#v", result)
		}
	})
}
