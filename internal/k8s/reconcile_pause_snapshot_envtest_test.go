//go:build envtest || integration

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
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

func TestReconcilePauseSnapshotAPI(t *testing.T) {
	environment := &envtest.Environment{CRDDirectoryPaths: []string{filepath.Join("..", "..", "config", "crd", "bases")}, ErrorIfCRDPathMissing: true}
	cfg, err := environment.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := environment.Stop(); err != nil {
			t.Error(err)
		}
	})
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{v1.AddToScheme, corev1.AddToScheme, appsv1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	cli, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	w := &ManagedResourceWriter{Client: cli, Reader: cli}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	fixture := func(t *testing.T) (*v1.Middleware, *unstructured.Unstructured) {
		t.Helper()
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "pause-storage-"}}
		if err := cli.Create(ctx, ns); err != nil {
			t.Fatal(err)
		}
		mid := &v1.Middleware{
			ObjectMeta: metav1.ObjectMeta{Name: "database", Namespace: ns.Name, Annotations: map[string]string{v1.AnnotationSuspendReconcile: "true"}},
			Spec:       v1.MiddlewareSpec{Baseline: "fixture", Parameters: runtime.RawExtension{Raw: []byte(`{"replicas":0}`)}},
		}
		if err := cli.Create(ctx, mid); err != nil {
			t.Fatal(err)
		}
		ref, err := intentOwnerReference(mid)
		if err != nil {
			t.Fatal(err)
		}
		replicas := int32(0)
		deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: mid.Name, Namespace: mid.Namespace, OwnerReferences: []metav1.OwnerReference{ref}}, Spec: appsv1.DeploymentSpec{
			Replicas: &replicas, Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "database"}},
			Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "database"}}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "database", Image: "registry.k8s.io/pause:3.10"}}}},
		}}
		if err := cli.Create(ctx, deployment); err != nil {
			t.Fatal(err)
		}
		object, err := runtime.DefaultUnstructuredConverter.ToUnstructured(deployment)
		if err != nil {
			t.Fatal(err)
		}
		live := &unstructured.Unstructured{Object: object}
		live.SetGroupVersionKind(appsv1.SchemeGroupVersion.WithKind("Deployment"))
		return mid, live
	}
	prepare := func(t *testing.T, mid *v1.Middleware, live *unstructured.Unstructured) (*v1.ReconcilePauseStatus, *corev1.Secret) {
		t.Helper()
		snapshot, err := w.PrepareReconcilePauseSnapshot(ctx, mid, live)
		if err != nil {
			t.Fatal(err)
		}
		secret := &corev1.Secret{}
		if err := cli.Get(ctx, client.ObjectKey{Namespace: mid.Namespace, Name: snapshot.SnapshotSecret}, secret); err != nil {
			t.Fatal(err)
		}
		return snapshot, secret
	}
	publish := func(t *testing.T, mid *v1.Middleware, snapshot *v1.ReconcilePauseStatus) {
		t.Helper()
		if err := w.PublishReconcilePauseSnapshot(ctx, mid, snapshot, func(*v1.MiddlewareStatus) {}); err != nil {
			t.Fatal(err)
		}
	}
	collector := NewResourceIntentCollector(cli, cli)
	collect := func(t *testing.T, secret *corev1.Secret, want bool) {
		t.Helper()
		deleted, err := collector.collect(ctx, secret, time.Now().Add(2*time.Hour))
		if err != nil || deleted != want {
			t.Fatalf("collect: deleted=%v want=%v err=%v", deleted, want, err)
		}
	}

	t.Run("large_observation_and_overrides_leave_small_MID_status", func(t *testing.T) {
		mid, live := fixture(t)
		value := strings.Repeat("large-private-value-", 40000)
		if err := unstructured.SetNestedSlice(live.Object, []any{map[string]any{"name": "database", "image": "registry.k8s.io/pause:3.10", "env": []any{map[string]any{"name": "LARGE", "value": value}}}}, "spec", "template", "spec", "containers"); err != nil {
			t.Fatal(err)
		}
		if err := cli.Update(ctx, live); err != nil {
			t.Fatal(err)
		}
		base, err := json.Marshal(map[string]any{"private": value})
		if err != nil {
			t.Fatal(err)
		}
		mid.Spec.ReconcileOverrides = &v1.ReconcileOverrides{BaseSpec: runtime.RawExtension{Raw: base}, SpecPatch: runtime.RawExtension{Raw: []byte(`{"private":"adopted"}`)}}
		if err := cli.Update(ctx, mid); err != nil {
			t.Fatal(err)
		}
		snapshot, secret := prepare(t, mid, live)
		if len(secret.Data[intentZstdKey]) == 0 {
			t.Fatal("large snapshot did not use zstd")
		}
		publish(t, mid, snapshot)
		if err := cli.Get(ctx, client.ObjectKeyFromObject(mid), mid); err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(mid.Status)
		if err != nil {
			t.Fatal(err)
		}
		if len(raw) > 1024 || strings.Contains(string(raw), "large-private-value") || strings.Contains(string(raw), "previousOverrides") {
			t.Fatal("bulky snapshot duplicated in MID status")
		}
		data, err := w.LoadReconcilePauseSnapshot(ctx, mid, mid.Status.ReconcilePause)
		if err != nil || data.PreviousOverrides == nil || !jsonEquivalent(data.PreviousOverrides.BaseSpec.Raw, base) {
			t.Fatalf("large snapshot/overrides did not round trip: %v", err)
		}
		t.Logf("actual/override payload > 1 MiB; MID status=%d bytes; compressed Secret=%d bytes", len(raw), len(secret.Data[intentZstdKey]))
		collect(t, secret, false)
	})

	t.Run("Kubernetes_Secret_size_rejection_leaves_MID_and_primary_unchanged", func(t *testing.T) {
		mid, live := fixture(t)
		randomString := func(n int) string {
			buf := make([]byte, n)
			if _, err := rand.Read(buf); err != nil {
				t.Fatal(err)
			}
			return base64.StdEncoding.EncodeToString(buf)
		}
		if err := unstructured.SetNestedSlice(live.Object, []any{map[string]any{"name": "database", "image": "registry.k8s.io/pause:3.10", "env": []any{map[string]any{"name": "LARGE", "value": randomString(480 * 1024)}}}}, "spec", "template", "spec", "containers"); err != nil {
			t.Fatal(err)
		}
		if err := cli.Update(ctx, live); err != nil {
			t.Fatal(err)
		}
		base, err := json.Marshal(map[string]any{"private": randomString(650 * 1024)})
		if err != nil {
			t.Fatal(err)
		}
		mid.Spec.ReconcileOverrides = &v1.ReconcileOverrides{BaseSpec: runtime.RawExtension{Raw: base}}
		if err := cli.Update(ctx, mid); err != nil {
			t.Fatal(err)
		}
		midRV, liveRV := mid.ResourceVersion, live.GetResourceVersion()
		if _, err := w.PrepareReconcilePauseSnapshot(ctx, mid, live); !apierrors.IsInvalid(err) {
			t.Fatalf("expected Kubernetes Secret data-size rejection, got %v", err)
		}
		if err := cli.Get(ctx, client.ObjectKeyFromObject(mid), mid); err != nil {
			t.Fatal(err)
		}
		if err := cli.Get(ctx, client.ObjectKeyFromObject(live), live); err != nil {
			t.Fatal(err)
		}
		if mid.ResourceVersion != midRV || live.GetResourceVersion() != liveRV || mid.Status.ReconcilePause != nil {
			t.Fatal("failed preparation modified MID or primary")
		}
	})

	t.Run("published_pause_survives_collection_then_detached_record_is_removed", func(t *testing.T) {
		mid, live := fixture(t)
		snapshot, secret := prepare(t, mid, live)
		publish(t, mid, snapshot)
		collect(t, secret, false)
		if err := cli.Get(ctx, client.ObjectKeyFromObject(mid), mid); err != nil {
			t.Fatal(err)
		}
		mid.Status.ReconcilePause = nil
		if err := cli.Status().Update(ctx, mid); err != nil {
			t.Fatal(err)
		}
		collect(t, secret, true)
	})

	t.Run("abandoned_preparation_fences_delayed_status_publication", func(t *testing.T) {
		mid, live := fixture(t)
		snapshot, secret := prepare(t, mid, live)
		collect(t, secret, true)
		// An in-flight status write prepared BEFORE the fence must also fail,
		// even if it already loaded the record before GC removed that Secret.
		mid.Status.ReconcilePause = snapshot
		if err := cli.Status().Update(ctx, mid); !apierrors.IsConflict(err) {
			t.Fatalf("stale status publication did not conflict: %v", err)
		}
		if err := w.PublishReconcilePauseSnapshot(ctx, mid, snapshot, func(*v1.MiddlewareStatus) {}); err == nil {
			t.Fatal("deleted preparation was republished")
		}
	})

	t.Run("concurrent_publication_wins_over_collection", func(t *testing.T) {
		mid, live := fixture(t)
		snapshot, secret := prepare(t, mid, live)
		intercept := &pauseSnapshotPatchClient{Client: cli, before: func() { publish(t, mid, snapshot) }}
		racingCollector := NewResourceIntentCollector(intercept, cli)
		deleted, err := racingCollector.collect(ctx, secret, time.Now().Add(2*time.Hour))
		if deleted || err == nil {
			t.Fatalf("collection did not yield to concurrent reference: deleted=%v err=%v", deleted, err)
		}
		collect(t, secret, false)
		if _, err := w.LoadReconcilePauseSnapshot(ctx, mid, snapshot); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("changed_MID_blocks_stale_capture_and_collects_preparation", func(t *testing.T) {
		mid, live := fixture(t)
		snapshot, secret := prepare(t, mid, live)
		now := mid.DeepCopy()
		now.Spec.Parameters.Raw = []byte(`{"replicas":2}`)
		if err := cli.Update(ctx, now); err != nil {
			t.Fatal(err)
		}
		if err := w.PublishReconcilePauseSnapshot(ctx, mid, snapshot, func(*v1.MiddlewareStatus) {}); !apierrors.IsConflict(err) {
			t.Fatalf("stale owner accepted: %v", err)
		}
		collect(t, secret, true)
	})

	t.Run("lost_status_response_retains_committed_snapshot", func(t *testing.T) {
		mid, live := fixture(t)
		snapshot, secret := prepare(t, mid, live)
		uncertain := &ManagedResourceWriter{Client: &pauseSnapshotLostStatusClient{Client: cli}, Reader: cli}
		if err := uncertain.PublishReconcilePauseSnapshot(ctx, mid, snapshot, func(*v1.MiddlewareStatus) {}); err == nil {
			t.Fatal("response-loss fixture did not fail")
		}
		if err := cli.Get(ctx, client.ObjectKeyFromObject(mid), mid); err != nil {
			t.Fatal(err)
		}
		if mid.Status.ReconcilePause == nil || mid.Status.ReconcilePause.SnapshotSecret != snapshot.SnapshotSecret {
			t.Fatal("committed reference was lost")
		}
		collect(t, secret, false)
	})

	t.Run("lost_Secret_create_response_reuses_preparation_on_retry", func(t *testing.T) {
		mid, live := fixture(t)
		uncertain := &ManagedResourceWriter{Client: &pauseSnapshotLostCreateClient{Client: cli}, Reader: cli}
		if _, err := uncertain.PrepareReconcilePauseSnapshot(ctx, mid, live); err == nil {
			t.Fatal("response-loss fixture did not fail")
		}
		snapshot, secret := prepare(t, mid, live)
		var records corev1.SecretList
		if err := cli.List(ctx, &records, client.InNamespace(mid.Namespace)); err != nil {
			t.Fatal(err)
		}
		if len(records.Items) != 1 {
			t.Fatal("retry created duplicate pause Secrets")
		}
		publish(t, mid, snapshot)
		collect(t, secret, false)
	})

	t.Run("cleanup_error_is_retried_by_collector", func(t *testing.T) {
		mid, live := fixture(t)
		snapshot, secret := prepare(t, mid, live)
		publish(t, mid, snapshot)
		if err := cli.Get(ctx, client.ObjectKeyFromObject(mid), mid); err != nil {
			t.Fatal(err)
		}
		mid.Status.ReconcilePause = nil
		if err := cli.Status().Update(ctx, mid); err != nil {
			t.Fatal(err)
		}
		failing := &ManagedResourceWriter{Client: &pauseSnapshotDeleteFailClient{Client: cli}, Reader: cli}
		if err := failing.CleanupReconcilePauseSnapshot(ctx, mid, snapshot); err == nil {
			t.Fatal("delete failure was swallowed")
		}
		collect(t, secret, true)
	})
}

type pauseSnapshotPatchClient struct {
	client.Client
	before func()
}

func (c *pauseSnapshotPatchClient) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	options := &client.PatchOptions{}
	for _, opt := range opts {
		opt.ApplyToPatch(options)
	}
	if len(options.DryRun) == 0 && c.before != nil {
		before := c.before
		c.before = nil
		before()
	}
	return c.Client.Patch(ctx, obj, patch, opts...)
}

type pauseSnapshotLostStatusClient struct{ client.Client }

func (c *pauseSnapshotLostStatusClient) Status() client.SubResourceWriter {
	return pauseSnapshotLostStatusWriter{SubResourceWriter: c.Client.Status()}
}

type pauseSnapshotLostStatusWriter struct{ client.SubResourceWriter }

func (w pauseSnapshotLostStatusWriter) Update(ctx context.Context, obj client.Object, opts ...client.SubResourceUpdateOption) error {
	if err := w.SubResourceWriter.Update(ctx, obj, opts...); err != nil {
		return err
	}
	return errors.New("simulated lost response after status committed")
}

type pauseSnapshotDeleteFailClient struct{ client.Client }

func (*pauseSnapshotDeleteFailClient) Delete(context.Context, client.Object, ...client.DeleteOption) error {
	return errors.New("simulated cleanup transport failure")
}

type pauseSnapshotLostCreateClient struct{ client.Client }

func (c *pauseSnapshotLostCreateClient) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	if err := c.Client.Create(ctx, obj, opts...); err != nil {
		return err
	}
	return errors.New("simulated lost response after Secret created")
}
