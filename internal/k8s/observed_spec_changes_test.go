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
	"reflect"
	"testing"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestApplyObservedSpecChanges(t *testing.T) {
	t.Parallel()
	cases := []struct{ name, before, after, desired, want string }{
		{
			name:   "unchanged defaults and pre-pause drift are not adopted",
			before: `{"data":{"value":"drift","default":"yes"}}`, after: `{"data":{"value":"drift","default":"yes"}}`,
			desired: `{"data":{"value":"desired"}}`, want: `{"data":{"value":"desired"}}`,
		},
		{
			name:   "independent desired edits survive and observed changed paths win",
			before: `{"data":{"a":"before","b":"before","default":"yes"}}`, after: `{"data":{"a":"external","b":"before","default":"yes"}}`,
			desired: `{"data":{"a":"desired","b":"desired"}}`, want: `{"data":{"a":"external","b":"desired"}}`,
		},
		{
			name:   "map additions and deletions",
			before: `{"data":{"remove":"yes","default":"yes"}}`, after: `{"data":{"add":"new","default":"yes"}}`,
			desired: `{"data":{"remove":"yes","keep":"desired"}}`, want: `{"data":{"add":"new","keep":"desired"}}`,
		},
		{
			name:    "changed list member excludes unchanged children",
			before:  `{"env":[{"id":"AUTH","value":"old","extra":"default"},{"id":"EXTERNAL","value":"drift"}]}`,
			after:   `{"env":[{"id":"AUTH","value":"new","extra":"default"},{"id":"EXTERNAL","value":"drift"}]}`,
			desired: `{"env":[{"id":"AUTH","value":"old"}]}`, want: `{"env":[{"id":"AUTH","value":"new"}]}`,
		},
		{
			name:    "list addition removal and reordering use schema keys",
			before:  `{"env":[{"id":"REMOVE","value":"old","extra":"default"},{"id":"KEEP","value":"old","extra":"default"}]}`,
			after:   `{"env":[{"id":"NEW","value":"new"},{"id":"KEEP","value":"old","extra":"default"}]}`,
			desired: `{"env":[{"id":"REMOVE","value":"old"},{"id":"KEEP","value":"old"}]}`,
			want:    `{"env":[{"id":"KEEP","value":"old"},{"id":"NEW","value":"new"}]}`,
		},
		{
			name:    "nested list delta does not copy outer defaults",
			before:  `{"env":[{"id":"PARENT","extra":"default","children":[{"id":"CHILD","value":"old","extra":"default"}]}]}`,
			after:   `{"env":[{"id":"PARENT","extra":"default","children":[{"id":"CHILD","value":"new","extra":"default"}]}]}`,
			desired: `{"env":[{"id":"PARENT","children":[{"id":"CHILD","value":"old"}]}]}`,
			want:    `{"env":[{"id":"PARENT","children":[{"id":"CHILD","value":"new"}]}]}`,
		},
		{
			name:    "valueFrom to value removes previous source",
			before:  `{"env":[{"id":"AUTH","valueFrom":{"secret":"old","optional":false}}]}`,
			after:   `{"env":[{"id":"AUTH","value":"literal"}]}`,
			desired: `{"env":[{"id":"AUTH","valueFrom":{"secret":"old"}}]}`,
			want:    `{"env":[{"id":"AUTH","value":"literal"}]}`,
		},
		{
			name:   "list cleared explicitly",
			before: `{"env":[{"id":"REMOVE","value":"old"}]}`, after: `{"env":[]}`,
			desired: `{"env":[{"id":"REMOVE","value":"old"}]}`, want: `{"env":[]}`,
		},
		{
			name:   "whole list omitted",
			before: `{"env":[{"id":"REMOVE","value":"old"}]}`, after: `{}`,
			desired: `{"env":[{"id":"REMOVE","value":"old"}]}`, want: `{}`,
		},
		{
			name:    "compound list keys with API default",
			before:  `{"ports":[{"port":80,"protocol":"TCP","target":"old"}]}`,
			after:   `{"ports":[{"port":80,"protocol":"TCP","target":"new"}]}`,
			desired: `{"ports":[{"port":80,"target":"old"}]}`,
			want:    `{"ports":[{"port":80,"protocol":"TCP","target":"new"}]}`,
		},
		{
			name:   "set membership delta",
			before: `{"tags":["remove","keep"]}`, after: `{"tags":["keep","new"]}`,
			desired: `{"tags":["remove","keep","desired"]}`, want: `{"tags":["keep","desired","new"]}`,
		},
		{
			name:   "atomic list change applies as one value",
			before: `{"atomicList":["before"]}`, after: `{"atomicList":["after"]}`,
			desired: `{"atomicList":["before"]}`, want: `{"atomicList":["after"]}`,
		},
		{
			name:   "unchanged atomic map is excluded",
			before: `{"atomicMap":{"default":"yes"},"data":{"change":"old"}}`, after: `{"atomicMap":{"default":"yes"},"data":{"change":"new"}}`,
			desired: `{"data":{"change":"old"}}`, want: `{"data":{"change":"new"}}`,
		},
		{
			name:   "unchanged observed null is excluded",
			before: `{"data":null}`, after: `{"data":null}`, desired: `{}`, want: `{}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b, a, d := intentTestObject(t, tc.before), intentTestObject(t, tc.after), intentTestObject(t, tc.desired)
			d.SetLabels(map[string]string{"desired": "preserve"})
			a.SetLabels(map[string]string{"external": "ignored"})
			d.Object["status"] = map[string]any{"ready": true}
			original := d.DeepCopy()
			got, err := applyObservedSpecChanges(intentTestConverter(t), b, a, d)
			if err != nil {
				t.Fatal(err)
			}
			want := intentTestObject(t, tc.want)
			if !reflect.DeepEqual(got.Object["spec"], want.Object["spec"]) {
				t.Fatalf("adopted spec:\n got %s\nwant %s", intentTestJSON(got.Object["spec"]), tc.want)
			}
			if !reflect.DeepEqual(got.GetLabels(), d.GetLabels()) || !reflect.DeepEqual(got.Object["status"], d.Object["status"]) || !reflect.DeepEqual(d.Object, original.Object) {
				t.Fatal("adoption changed desired metadata, status or input")
			}
		})
	}
}

func TestApplyObservedSpecChangesRejectsInvalidInput(t *testing.T) {
	t.Parallel()
	d := intentTestObject(t, `{}`)
	a := intentTestObject(t, `{"env":[{"id":"duplicate"},{"id":"duplicate"}]}`)
	if _, err := applyObservedSpecChanges(intentTestConverter(t), d, a, d); err == nil {
		t.Fatal("duplicate list keys must fail before adopting any spec")
	}
	a = d.DeepCopy()
	a.SetName("replacement")
	if _, err := applyObservedSpecChanges(intentTestConverter(t), d, a, d); err == nil {
		t.Fatal("changed target must fail before adopting any spec")
	}
}

func TestManagedWriterFencesPauseChangesIncludingConflictRetries(t *testing.T) {
	for _, scenario := range []string{"changed before writer read", "changed during conflict retry", "missing primary resource"} {
		t.Run(scenario, func(t *testing.T) {
			original := intentTestObject(t, `{"data":{"value":"adopted"}}`)
			original.SetUID("actual-uid")
			raw, err := json.Marshal(original.Object["spec"])
			if err != nil {
				t.Fatal(err)
			}
			hash := sha256.Sum256(raw)
			gvk := original.GroupVersionKind()
			owner := &v1.Middleware{
				ObjectMeta: metav1.ObjectMeta{Name: original.GetName(), Namespace: original.GetNamespace(), UID: "owner-uid", Annotations: map[string]string{v1.AnnotationResumePolicy: "merge"}},
				Status: v1.MiddlewareStatus{ReconcilePause: &v1.ReconcilePauseStatus{
					GVK: v1.GVK{Group: gvk.Group, Version: gvk.Version, Kind: gvk.Kind}, Name: original.GetName(), Namespace: original.GetNamespace(), ResourceUID: string(original.GetUID()), AdoptedLiveSpecHash: hex.EncodeToString(hash[:]),
				}},
			}
			controller := true
			original.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: v1.GroupVersion.String(), Kind: "Middleware", Name: owner.Name, UID: owner.UID, Controller: &controller}})
			scheme := runtime.NewScheme()
			if err := v1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			scheme.AddKnownTypeWithName(gvk, original)
			objects := []client.Object{owner}
			if scenario != "missing primary resource" {
				objects = append(objects, original)
			}
			cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
			if scenario == "changed before writer read" {
				if err := cli.Get(context.Background(), client.ObjectKeyFromObject(original), original); err != nil {
					t.Fatal(err)
				}
				if err := unstructured.SetNestedField(original.Object, "second-change", "spec", "data", "value"); err != nil {
					t.Fatal(err)
				}
				if err := cli.Update(context.Background(), original); err != nil {
					t.Fatal(err)
				}
			}
			patches := 0
			writerClient := interceptor.NewClient(cli, interceptor.Funcs{Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				patches++
				if err := cli.Get(ctx, client.ObjectKeyFromObject(original), original); err != nil {
					return err
				}
				if err := unstructured.SetNestedField(original.Object, "second-change", "spec", "data", "value"); err != nil {
					return err
				}
				if err := cli.Update(ctx, original); err != nil {
					return err
				}
				return apierrors.NewConflict(schema.GroupResource{Group: gvk.Group, Resource: "things"}, original.GetName(), errors.New("external writer raced"))
			}})
			writer := &ManagedResourceWriter{Client: writerClient, Reader: cli}
			err = writer.Reconcile(context.Background(), owner, intentTestObject(t, `{"data":{"value":"adopted"}}`), "")
			if !errors.Is(err, ErrObservedSpecChanged) {
				t.Fatalf("write was not sent back to adoption: %v", err)
			}
			wantPatches := 0
			if scenario == "changed during conflict retry" {
				wantPatches = 1
			}
			if patches != wantPatches {
				t.Fatalf("stale writes attempted: patches=%d, want %d", patches, wantPatches)
			}
		})
	}
}
