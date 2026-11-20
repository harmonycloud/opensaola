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
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var errDeclaredNoopWrite = errors.New("write reached the no-op test probe")

type declaredNoopProbe struct {
	client.Client
	patches int
}

func (p *declaredNoopProbe) Patch(context.Context, client.Object, client.Patch, ...client.PatchOption) error {
	p.patches++
	return errDeclaredNoopWrite
}

// Exercise the real writer's early return with a confirmed field history ledger.
// A changed raw declaration must reach the write even when the live object is
// already semantically equivalent (for example adding/removing readOnly:false).
// Unchanged raw intent must not prepare new records for API normalization alone.
func TestDeclaredWriterNoopPreservesDeclarationHistory(t *testing.T) {
	for _, test := range []struct {
		name, before, desired, live string
		native, numericFloat, write bool
	}{
		{name: "unchanged opaque named declarations", before: `{"containers":[{"name":"broker","image":"one"}]}`, desired: `{"containers":[{"name":"broker","image":"one"}]}`, live: `{"containers":[{"name":"broker","image":"one","external":"keep"},{"name":"sidecar"}]}`},
		{name: "unchanged Quantity canonicalized", before: `{"resources":{"cpu":"1.0","memory":"1024Mi"}}`, desired: `{"resources":{"cpu":"1.0","memory":"1024Mi"}}`, live: `{"resources":{"cpu":"1","memory":"1Gi"}}`},
		{name: "int64 record and float64 raw intent", before: `{"replicas":2}`, desired: `{"replicas":2}`, live: `{"replicas":2}`, numericFloat: true},
		{name: "unchanged CR atomic map ignores arbitrary external fields", before: `{"atomicOpaque":{"entries":[{"limit":"1"}]}}`, desired: `{"atomicOpaque":{"entries":[{"limit":"1"}]}}`, live: `{"atomicOpaque":{"entries":[{"limit":"1","admission":{"notes":[1,2,3]}}],"injected":true}}`},
		{name: "explicit atomic field withdrawn is not a no-op", before: `{"atomicMap":{"own":"old","keep":"yes"}}`, desired: `{"atomicMap":{"keep":"yes"}}`, live: `{"atomicMap":{"own":"old","keep":"yes"}}`, write: true},
		{name: "confirmed atomic withdrawal does not republish an API default", before: `{"atomicMap":{"keep":"yes"}}`, desired: `{"atomicMap":{"keep":"yes"}}`, live: `{"atomicMap":{"keep":"yes","own":"admission"}}`},
		{name: "unchanged native omitted false", before: `{"containers":[{"name":"broker","volumeMounts":[{"name":"log","mountPath":"/logs","readOnly":false}]}]}`, desired: `{"containers":[{"name":"broker","volumeMounts":[{"name":"log","mountPath":"/logs","readOnly":false}]}]}`, live: `{"containers":[{"name":"broker","volumeMounts":[{"name":"log","mountPath":"/logs"}]}]}`, native: true},
		{name: "new false declaration still updates history", before: `{"containers":[{"name":"broker","volumeMounts":[{"name":"log","mountPath":"/logs"}]}]}`, desired: `{"containers":[{"name":"broker","volumeMounts":[{"name":"log","mountPath":"/logs","readOnly":false}]}]}`, live: `{"containers":[{"name":"broker","volumeMounts":[{"name":"log","mountPath":"/logs"}]}]}`, native: true, write: true},
		{name: "removed false declaration still updates history", before: `{"containers":[{"name":"broker","volumeMounts":[{"name":"log","mountPath":"/logs","readOnly":false}]}]}`, desired: `{"containers":[{"name":"broker","volumeMounts":[{"name":"log","mountPath":"/logs"}]}]}`, live: `{"containers":[{"name":"broker","volumeMounts":[{"name":"log","mountPath":"/logs"}]}]}`, native: true, write: true},
		{name: "new null declaration still updates history", before: `{}`, desired: `{"options":null}`, live: `{"options":null}`, write: true},
		{name: "removed null declaration still updates history", before: `{"options":null}`, desired: `{}`, live: `{}`, write: true},
		{name: "Quantity spelling change updates raw record once", before: `{"resources":{"cpu":"1.0"}}`, desired: `{"resources":{"cpu":"1"}}`, live: `{"resources":{"cpu":"1"}}`, write: true},
		{name: "declared drift must write", before: `{"enabled":true}`, desired: `{"enabled":true}`, live: `{"enabled":false}`, write: true},
		{name: "native true cannot satisfy false declaration", before: `{"containers":[{"name":"broker","volumeMounts":[{"name":"log","mountPath":"/logs","readOnly":false}]}]}`, desired: `{"containers":[{"name":"broker","volumeMounts":[{"name":"log","mountPath":"/logs","readOnly":false}]}]}`, live: `{"containers":[{"name":"broker","volumeMounts":[{"name":"log","mountPath":"/logs","readOnly":true}]}]}`, native: true, write: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			owner := intentUnitOwner()
			before, desired, live := intentTestObject(t, test.before), intentTestObject(t, test.desired), intentTestObject(t, test.live)
			converter := declaredTestConverter(t)
			if test.native {
				converter = declaredNativePodConverter(t)
				for _, object := range []*unstructured.Unstructured{before, desired, live} {
					object.SetAPIVersion("v1")
					object.SetKind("Pod")
				}
			}
			if test.numericFloat {
				if err := unstructured.SetNestedField(desired.Object, float64(2), "spec", "replicas"); err != nil {
					t.Fatal(err)
				}
			}
			ref, err := intentOwnerReference(owner)
			if err != nil {
				t.Fatal(err)
			}
			for _, object := range []*unstructured.Unstructured{before, desired, live} {
				object.SetNamespace(owner.Namespace)
				object.SetUID("resource-uid")
				object.SetResourceVersion("7")
				object.SetOwnerReferences([]metav1.OwnerReference{ref})
			}
			base := intentUnitClient(t, owner)
			probe := &declaredNoopProbe{Client: base}
			writer := &ManagedResourceWriter{Client: base, Reader: base, IntentSchemas: intentUnitSchemas{converter: converter}}
			candidate := newIntentCandidate(owner, live, before, nil, "", "unit-schema", nil)
			record, err := buildResourceIntentSecret(owner, *candidate)
			if err != nil {
				t.Fatal(err)
			}
			record.Name = intentLedgerName(owner, live, "")
			record.GenerateName = ""
			record.Immutable = nil
			record.Labels[labelIntentLedger] = "v1"
			if err := base.Create(ctx, record); err != nil {
				t.Fatal(err)
			}
			setIntentPointer(live, record.Name)
			writer.Client = probe
			for attempt := 0; attempt < 3; attempt++ {
				err := writer.reconcileResourceIntent(ctx, owner, live, desired, "", false)
				if test.write {
					if !errors.Is(err, errDeclaredNoopWrite) {
						t.Fatalf("changed declaration must reach write, got %v", err)
					}
					break
				}
				if err != nil {
					t.Fatalf("unchanged semantic state should not write: %v", err)
				}
			}
			if (probe.patches > 0) != test.write {
				t.Fatalf("patches=%d want write=%v", probe.patches, test.write)
			}
		})
	}
}
