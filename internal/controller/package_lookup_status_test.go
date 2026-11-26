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

package controller

import (
	"archive/tar"
	"bytes"
	"errors"
	"strings"
	"testing"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	"github.com/harmonycloud/opensaola/internal/service/consts"
	"github.com/harmonycloud/opensaola/internal/service/middleware"
	"github.com/harmonycloud/opensaola/internal/service/middlewareactionbaseline"
	"github.com/harmonycloud/opensaola/internal/service/packages"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func packageLookupClient(t *testing.T, objects ...client.Object) client.Client {
	t.Helper()
	s := runtime.NewScheme()
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := v1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return fake.NewClientBuilder().WithScheme(s).WithObjects(objects...).WithStatusSubresource(&v1.MiddlewareAction{}, &v1.Middleware{}).Build()
}

func lookupPackageSecret(t *testing.T, name string) *corev1.Secret {
	t.Helper()
	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	for path, data := range map[string]string{
		"metadata.yaml":           "name: Redis\nversion: '2'\n",
		"actions/noop.yaml":       "apiVersion: middleware.cn/v1\nkind: MiddlewareActionBaseline\nmetadata: {name: noop}\nspec: {steps: []}\n",
		"baselines/standard.yaml": "apiVersion: middleware.cn/v1\nkind: MiddlewareBaseline\nmetadata: {name: standard}\n",
	} {
		if err := w.WriteHeader(&tar.Header{Name: name + "/" + path, Size: int64(len(data)), Mode: 0644}); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(data)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	data, _, err := packages.Compress(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { packages.InvalidatePackageCache(name) })
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: packages.DataNamespace}, Data: map[string][]byte{packages.Release: data}}
}

func TestActionBaselineLookupStatus(t *testing.T) {
	const pkg = "redis-actions-2"
	t.Cleanup(func() { middlewareactionbaseline.ActionBaselineCache.Delete(pkg + "/noop") })
	ctx := t.Context()
	cli := packageLookupClient(t)
	r := &MiddlewareActionReconciler{Client: cli, Recorder: record.NewFakeRecorder(10)}
	action := &v1.MiddlewareAction{ObjectMeta: metav1.ObjectMeta{Name: "retry", Namespace: "default", Labels: map[string]string{v1.LabelPackageName: pkg}}, Spec: v1.MiddlewareActionSpec{Baseline: "noop"}}
	if err := cli.Create(ctx, action); err != nil {
		t.Fatal(err)
	}
	reconcile := func() error {
		key := client.ObjectKeyFromObject(action)
		_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: key})
		if readErr := cli.Get(ctx, key, action); readErr != nil {
			t.Fatal(readErr)
		}
		return err
	}
	if err := reconcile(); err == nil || action.Status.State != "" {
		t.Fatalf("missing Secret: err=%v status=%+v", err, action.Status)
	}
	if err := cli.Create(ctx, lookupPackageSecret(t, pkg)); err != nil {
		t.Fatal(err)
	}
	if err := reconcile(); err != nil || action.Status.State != v1.StateAvailable {
		t.Fatalf("archive fallback: err=%v status=%+v", err, action.Status)
	}
	action = &v1.MiddlewareAction{ObjectMeta: metav1.ObjectMeta{Name: "absent", Namespace: "default", Labels: map[string]string{v1.LabelPackageName: pkg}}, Spec: v1.MiddlewareActionSpec{Baseline: "redis-backup"}}
	if err := cli.Create(ctx, action); err != nil {
		t.Fatal(err)
	}
	err := reconcile()
	c := meta.FindStatusCondition(action.Status.Conditions, v1.CondTypeChecked)
	if err == nil || action.Status.State != v1.StateUnavailable || c == nil || c.Status != metav1.ConditionFalse || !strings.Contains(string(action.Status.Reason), pkg) || !strings.Contains(c.Message, "redis-backup") {
		t.Fatalf("missing action: err=%v status=%+v", err, action.Status)
	}
	if err := reconcile(); err != nil {
		t.Fatalf("terminal action retried: %v", err)
	}
}

func TestMiddlewareUpgradeConditionWaitsForPackage(t *testing.T) {
	ctx := t.Context()
	secret := lookupPackageSecret(t, "redis-upgrade-2")
	mp := &v1.MiddlewarePackage{ObjectMeta: metav1.ObjectMeta{Name: secret.Name, Labels: map[string]string{v1.LabelProject: consts.ProjectOpenSaola, v1.LabelComponent: "Redis", v1.LabelPackageVersion: "2"}}}
	mid := &v1.Middleware{ObjectMeta: metav1.ObjectMeta{Name: "upgrade", Namespace: "default", Labels: map[string]string{v1.LabelComponent: "Redis", v1.LabelPackageVersion: "1"}, Annotations: map[string]string{v1.LabelUpdate: "2"}}, Spec: v1.MiddlewareSpec{Baseline: "standard"}}
	mid.Status.Conditions = []metav1.Condition{{Type: v1.CondTypeChecked, Status: metav1.ConditionTrue}, {Type: v1.CondTypeUpdating, Status: metav1.ConditionTrue, Reason: v1.CondReasonUpdatingSuccess}}
	cli := packageLookupClient(t, mp, secret, mid)
	readStatus := func() *metav1.Condition {
		if err := cli.Get(ctx, client.ObjectKeyFromObject(mid), mid); err != nil {
			t.Fatal(err)
		}
		c := meta.FindStatusCondition(mid.Status.Conditions, v1.CondTypeUpdating)
		if c == nil {
			t.Fatal("missing Updating condition")
		}
		return c
	}
	if err := middleware.ReplacePackage(ctx, cli, mid); !errors.Is(err, consts.ErrPackageNotReady) {
		t.Fatalf("expected retry: %v", err)
	}
	if c := readStatus(); c.Status != metav1.ConditionUnknown || c.Reason != "PackageNotReady" || mid.Labels[v1.LabelPackageVersion] != "1" || mid.Annotations[v1.LabelUpdate] != "2" {
		t.Fatalf("waiting: %+v labels=%v annotations=%v", c, mid.Labels, mid.Annotations)
	}
	if err := cli.Get(ctx, client.ObjectKeyFromObject(secret), secret); err != nil {
		t.Fatal(err)
	}
	secret.Labels = map[string]string{v1.LabelEnabled: "true"}
	if err := cli.Update(ctx, secret); err != nil {
		t.Fatal(err)
	}
	if err := middleware.ReplacePackage(ctx, cli, mid); err != nil {
		t.Fatal(err)
	}
	if c := readStatus(); c.Status != metav1.ConditionTrue || c.Reason != v1.CondReasonUpdatingSuccess || mid.Labels[v1.LabelPackageVersion] != "2" || mid.Annotations[v1.LabelUpdate] != "" {
		t.Fatalf("switched: %+v labels=%v annotations=%v", c, mid.Labels, mid.Annotations)
	}
}
