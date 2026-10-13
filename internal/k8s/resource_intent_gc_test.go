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

package k8s

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"testing/synctest"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestIntentCollectionSafety(t *testing.T) {
	for _, tc := range []struct {
		name                             string
		mutate                           func(*corev1.Secret, *unstructured.Unstructured, *resourceIntent)
		absent                           bool
		wantDelete, wantFence, wantError bool
	}{
		{name: "current_record_never_expires", mutate: func(s *corev1.Secret, o *unstructured.Unstructured, _ *resourceIntent) { setIntentPointer(o, s.Name) }},
		{name: "young_prepared_record_retained", mutate: func(s *corev1.Secret, _ *unstructured.Unstructured, _ *resourceIntent) {
			s.CreationTimestamp = metav1.Now()
		}},
		{name: "unreferenced_same_rv_fenced", wantDelete: true, wantFence: true},
		{name: "changed_rv_needs_no_fence", mutate: func(_ *corev1.Secret, o *unstructured.Unstructured, _ *resourceIntent) { o.SetResourceVersion("4") }, wantDelete: true},
		{name: "target_absent", absent: true, wantDelete: true},
		{name: "target_replaced_never_modified", mutate: func(_ *corev1.Secret, o *unstructured.Unstructured, _ *resourceIntent) { o.SetUID("replacement") }, wantDelete: true},
		{name: "replacement_points_to_record_retained", mutate: func(s *corev1.Secret, o *unstructured.Unstructured, _ *resourceIntent) {
			o.SetUID("replacement")
			setIntentPointer(o, s.Name)
		}},
		{name: "old_publication_protocol_retained", mutate: func(_ *corev1.Secret, _ *unstructured.Unstructured, r *resourceIntent) { r.PublicationProtocol = 0 }},
		{name: "unknown_future_protocol_retained", mutate: func(_ *corev1.Secret, _ *unstructured.Unstructured, r *resourceIntent) { r.PublicationProtocol = 100 }},
		{name: "missing_base_rv_retained", mutate: func(_ *corev1.Secret, _ *unstructured.Unstructured, r *resourceIntent) { r.BaseResourceVersion = "" }},
		{name: "record_owner_mismatch", mutate: func(s *corev1.Secret, _ *unstructured.Unstructured, _ *resourceIntent) {
			s.OwnerReferences[0].UID = "foreign"
		}, wantError: true},
		{name: "resource_owner_mismatch", mutate: func(_ *corev1.Secret, o *unstructured.Unstructured, _ *resourceIntent) {
			refs := o.GetOwnerReferences()
			refs[0].UID = "foreign"
			o.SetOwnerReferences(refs)
		}, wantError: true},
		{name: "mutable_record_rejected", mutate: func(s *corev1.Secret, _ *unstructured.Unstructured, _ *resourceIntent) { b := false; s.Immutable = &b }, wantError: true},
		{name: "unknown_target_api_retained", mutate: func(_ *corev1.Secret, _ *unstructured.Unstructured, r *resourceIntent) {
			r.Desired.SetAPIVersion("missing.invalid/v1")
		}, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			owner := intentUnitOwner()
			target := intentUnitResource()
			ref, err := intentOwnerReference(owner)
			if err != nil {
				t.Fatal(err)
			}
			target.SetOwnerReferences([]metav1.OwnerReference{ref})
			immutable := true
			s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "record", Namespace: owner.Namespace, UID: "secret-uid", ResourceVersion: "1", CreationTimestamp: metav1.NewTime(time.Now().Add(-365 * 24 * time.Hour)), Labels: map[string]string{intentRecordLabel: string(owner.UID)}, OwnerReferences: []metav1.OwnerReference{ref}}, Immutable: &immutable, Type: corev1.SecretTypeOpaque}
			r := resourceIntent{Version: 1, PublicationProtocol: intentPublicationProtocol, BaseResourceVersion: target.GetResourceVersion(), OwnerUID: owner.UID, ResourceUID: target.GetUID(), SchemaHash: "schema", Desired: intentObject(target)}
			if tc.mutate != nil {
				tc.mutate(s, target, &r)
			}
			raw, err := json.Marshal(r)
			if err != nil {
				t.Fatal(err)
			}
			s.Data = map[string][]byte{"intent.json": raw}
			objects := []client.Object{owner, s}
			if !tc.absent {
				objects = append(objects, target)
			}
			cli := intentUnitClient(t, objects...)
			collector := NewResourceIntentCollector(cli, cli)
			deleted, err := collector.collect(ctx, s, time.Now())
			if deleted != tc.wantDelete || (err != nil) != tc.wantError {
				t.Fatalf("deleted=%v error=%v", deleted, err)
			}
			getErr := cli.Get(ctx, client.ObjectKeyFromObject(s), &corev1.Secret{})
			if apierrors.IsNotFound(getErr) != tc.wantDelete {
				t.Fatalf("record existence: %v", getErr)
			}
			if !tc.absent {
				latest := target.DeepCopy()
				if err := cli.Get(ctx, client.ObjectKeyFromObject(target), latest); err != nil {
					t.Fatal(err)
				}
				if (latest.GetLabels()[LabelResourceIntentGCFence] != "") != tc.wantFence {
					t.Fatalf("unexpected fence: %v", latest.GetAnnotations())
				}
				if !reflect.DeepEqual(intentObject(latest).Object, intentObject(target).Object) {
					t.Fatal("collection changed business fields")
				}
				if !tc.wantFence && latest.GetResourceVersion() != target.GetResourceVersion() {
					t.Fatal("collection unexpectedly wrote target")
				}
			}
		})
	}
}

type intentGCFailureClient struct {
	client.Client
	deleteFail  bool
	getErr      error
	beforeFence func(context.Context) error
}

func (c *intentGCFailureClient) Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
	if c.deleteFail {
		return apierrors.NewForbidden(schema.GroupResource{Resource: "secrets"}, obj.GetName(), errors.New("denied"))
	}
	return c.Client.Delete(ctx, obj, opts...)
}
func (c *intentGCFailureClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if c.getErr != nil && obj.GetObjectKind().GroupVersionKind().Kind == "ConfigMap" {
		return c.getErr
	}
	return c.Client.Get(ctx, key, obj, opts...)
}
func (c *intentGCFailureClient) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	options := &client.PatchOptions{}
	for _, opt := range opts {
		opt.ApplyToPatch(options)
	}
	if c.beforeFence != nil && len(options.DryRun) == 0 {
		fn := c.beforeFence
		c.beforeFence = nil
		if err := fn(ctx); err != nil {
			return err
		}
	}
	return c.Client.Patch(ctx, obj, patch, opts...)
}

func gcUnitPrepared(t *testing.T) (client.Client, *unstructured.Unstructured, *corev1.Secret) {
	t.Helper()
	ctx := context.Background()
	owner := intentUnitOwner()
	target := intentUnitResource()
	ref, err := intentOwnerReference(owner)
	if err != nil {
		t.Fatal(err)
	}
	target.SetOwnerReferences([]metav1.OwnerReference{ref})
	cli := intentUnitClient(t, owner, target)
	w := &ManagedResourceWriter{Client: cli, Reader: cli}
	s, err := w.prepareResourceIntent(ctx, owner, target, target, "", "schema", nil)
	if err != nil {
		t.Fatal(err)
	}
	s.UID = "prepared-uid"
	s.CreationTimestamp = metav1.NewTime(time.Now().Add(-time.Hour))
	if err := cli.Update(ctx, s); err != nil {
		t.Fatal(err)
	}
	return cli, target, s
}

func TestIntentCollectionFailuresRetry(t *testing.T) {
	for _, tc := range []struct {
		name       string
		readErr    error
		deleteFail bool
	}{
		{name: "delete_forbidden", deleteFail: true},
		{name: "target_forbidden", readErr: apierrors.NewForbidden(schema.GroupResource{Resource: "configmaps"}, "target", errors.New("denied"))},
		{name: "endpoint_404_is_not_object_absence", readErr: &apierrors.StatusError{ErrStatus: metav1.Status{Code: 404, Reason: metav1.StatusReasonNotFound}}},
		{name: "uncertain_target_timeout", readErr: apierrors.NewTimeoutError("timeout", 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			cli, target, s := gcUnitPrepared(t)
			wrapped := &intentGCFailureClient{Client: cli, deleteFail: tc.deleteFail, getErr: tc.readErr}
			collector := NewResourceIntentCollector(wrapped, wrapped)
			if deleted, err := collector.collect(ctx, s, time.Now()); deleted || err == nil {
				t.Fatalf("deleted=%v err=%v", deleted, err)
			}
			if err := cli.Get(ctx, client.ObjectKeyFromObject(s), s); err != nil {
				t.Fatal("failed cleanup lost record", err)
			}
			wrapped.deleteFail = false
			wrapped.getErr = nil
			if deleted, err := collector.collect(ctx, s, time.Now()); !deleted || err != nil {
				t.Fatalf("retry deleted=%v err=%v", deleted, err)
			}
			latest := target.DeepCopy()
			if err := cli.Get(ctx, client.ObjectKeyFromObject(target), latest); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(intentObject(latest).Object, intentObject(target).Object) {
				t.Fatal("retry changed business fields")
			}
		})
	}
}

func TestIntentCollectionLosesRaceToPublication(t *testing.T) {
	ctx := context.Background()
	cli, target, s := gcUnitPrepared(t)
	wrapped := &intentGCFailureClient{Client: cli}
	wrapped.beforeFence = func(ctx context.Context) error {
		latest := target.DeepCopy()
		if err := cli.Get(ctx, client.ObjectKeyFromObject(target), latest); err != nil {
			return err
		}
		setIntentPointer(latest, s.Name)
		return cli.Update(ctx, latest)
	}
	collector := NewResourceIntentCollector(wrapped, cli)
	if deleted, err := collector.collect(ctx, s, time.Now()); deleted || err == nil {
		t.Fatalf("race deleted=%v err=%v", deleted, err)
	}
	if err := cli.Get(ctx, client.ObjectKeyFromObject(s), s); err != nil {
		t.Fatal("publication winner's record deleted", err)
	}
	if deleted, err := collector.collect(ctx, s, time.Now()); deleted || err != nil {
		t.Fatalf("current record deleted=%v err=%v", deleted, err)
	}
}

func TestIntentCollectorStopsWithLeaderContext(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cli := intentUnitClient(t)
		collector := NewResourceIntentCollector(cli, cli)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- collector.Start(ctx) }()
		synctest.Wait()
		cancel()
		synctest.Wait()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	})
}

type intentGCAdmissionClient struct {
	client.Client
	mutate     func(*unstructured.Unstructured)
	realWrites int
}

func (c *intentGCAdmissionClient) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	options := &client.PatchOptions{}
	for _, opt := range opts {
		opt.ApplyToPatch(options)
	}
	if len(options.DryRun) > 0 {
		if u, ok := obj.(*unstructured.Unstructured); ok {
			c.mutate(u)
			return nil
		}
	} else {
		c.realWrites++
	}
	return c.Client.Patch(ctx, obj, patch, opts...)
}
func TestIntentCollectionRejectsAdmissionSideEffects(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*unstructured.Unstructured)
	}{
		{name: "business_data", mutate: func(u *unstructured.Unstructured) { u.Object["data"] = map[string]any{"changed": "no"} }},
		{name: "generation", mutate: func(u *unstructured.Unstructured) { u.SetGeneration(u.GetGeneration() + 1) }},
		{name: "status", mutate: func(u *unstructured.Unstructured) { u.Object["status"] = map[string]any{"changed": true} }},
		{name: "owner_reference", mutate: func(u *unstructured.Unstructured) { u.SetOwnerReferences(nil) }},
		{name: "current_pointer", mutate: func(u *unstructured.Unstructured) { setIntentPointer(u, "unexpected-record") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			cli, target, s := gcUnitPrepared(t)
			wrapped := &intentGCAdmissionClient{Client: cli, mutate: tc.mutate}
			collector := NewResourceIntentCollector(wrapped, cli)
			if deleted, err := collector.collect(ctx, s, time.Now()); deleted || err == nil || wrapped.realWrites != 0 {
				t.Fatalf("deleted=%v error=%v realWrites=%d", deleted, err, wrapped.realWrites)
			}
			if err := cli.Get(ctx, client.ObjectKeyFromObject(s), &corev1.Secret{}); err != nil {
				t.Fatal("unsafe admission lost record", err)
			}
			latest := target.DeepCopy()
			if err := cli.Get(ctx, client.ObjectKeyFromObject(target), latest); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(target.Object, latest.Object) {
				t.Fatal("unsafe admission reached real resource")
			}
		})
	}
}
