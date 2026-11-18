//go:build envtest

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
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func testRecreateDoesNotClaimExternalFields(t *testing.T, ctx context.Context, cli client.Client, resolver IntentSchemaResolver, ns string) {
	for _, stage := range []string{"before_receipt", "after_receipt"} {
		t.Run(stage, func(t *testing.T) {
			f := newDeletionAPIFixture(t, ctx, cli, resolver, typedDeletionGVK, ns)
			f.set(f.desired, []any{map[string]any{"name": "broker"}}, "spec", "containers")
			lost := errors.New("lost response recording primary creation")
			fault := &intentPolicyFaultClient{Client: cli}
			if stage == "before_receipt" {
				fault.prepared = func(context.Context) error { return lost }
			} else {
				fault.beforeSecretPatch = func(context.Context, *corev1.Secret) error { return lost }
			}
			f.writer.Client = fault
			if err := f.writer.RecreateMissing(ctx, f.owner, f.desired, ""); !errors.Is(err, lost) {
				t.Fatalf("expected lost response, got %v", err)
			}
			live := f.get()
			if hasReceipt := live.GetAnnotations()[AnnotationResourceIntent] != ""; hasReceipt != (stage == "after_receipt") {
				t.Fatalf("unexpected target receipt before retry: %v", hasReceipt)
			}
			external := []any{map[string]any{"name": "broker", "image": "external"}}
			f.set(live, external, "spec", "containers")
			f.update(live)
			f.writer = &ManagedResourceWriter{Client: cli, Reader: cli, IntentSchemas: resolver}
			if err := f.writer.RecreateMissing(ctx, f.owner, f.desired, ""); err != nil {
				t.Fatal(err)
			}
			base, history, err := f.writer.loadResourceIntent(ctx, f.owner, f.get(), "")
			if err != nil || base == nil || ledgerPending(history) {
				t.Fatalf("recreation reported success without confirming its history: %v", err)
			}
			unstructured.RemoveNestedField(f.desired.Object, "spec", "containers")
			f.publish()
			got, _, err := unstructured.NestedSlice(f.get().Object, "spec", "containers")
			if err != nil || !reflect.DeepEqual(got, external) {
				t.Fatalf("recovery claimed external fields: containers=%v error=%v", got, err)
			}
		})
	}
}
