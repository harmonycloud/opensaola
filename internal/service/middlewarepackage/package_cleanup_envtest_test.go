//go:build envtest

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

package middlewarepackage

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	"github.com/harmonycloud/opensaola/internal/service/consts"
	"github.com/harmonycloud/opensaola/internal/service/packages"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

func TestUninstallRejectedArchiveAPI(t *testing.T) {
	env := &envtest.Environment{CRDDirectoryPaths: []string{filepath.Join("..", "..", "..", "config", "crd", "bases")}, ErrorIfCRDPathMissing: true}
	cfg, err := env.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := env.Stop(); err != nil {
			t.Error(err)
		}
	})
	cfg.QPS, cfg.Burst = 200, 400
	s := runtime.NewScheme()
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := v1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	cli, err := client.NewWithWatch(cfg, client.Options{Scheme: s})
	if err != nil {
		t.Fatal(err)
	}
	ns := packages.DataNamespace
	packages.SetDataNamespace("default")
	t.Cleanup(func() { packages.SetDataNamespace(ns) })
	ctx := t.Context()
	f := &packageSwitchFixture{t: t, cli: cli, component: "rejected", previous: "rejected-v1", candidate: "rejected-v2", shared: "shared"}
	original := &v1.MiddlewareConfiguration{ObjectMeta: metav1.ObjectMeta{Name: f.shared}, Spec: v1.MiddlewareConfigurationSpec{Template: "v1"}}
	f.check(cli.Create(ctx, original))
	doc := "apiVersion: middleware.cn/v1\nkind: MiddlewareConfiguration\nmetadata: {name: shared}\nspec: {template: v2}\n"
	target := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: f.candidate, Namespace: "default", Labels: map[string]string{v1.LabelProject: consts.ProjectOpenSaola, v1.LabelComponent: f.component}, Annotations: map[string]string{v1.LabelInstall: "true"}}, Data: map[string][]byte{packages.Release: buildPackageRelease(t, f.candidate, map[string]string{"metadata.yaml": "name: rejected\nversion: v2\n", "configurations/a.yaml": doc, "configurations/b.yaml": doc})}}
	t.Cleanup(func() { packages.InvalidatePackageCache(target.Name) })
	f.check(cli.Create(ctx, target))
	f.check(f.reconcile(cli, target.Name))
	target = f.secret(target.Name)
	if !strings.Contains(target.Annotations[v1.AnnotationInstallError], "duplicate") || len(target.Finalizers) == 0 {
		t.Fatalf("expected rejected archive with finalizer: %+v", target.ObjectMeta)
	}
	var mp v1.MiddlewarePackage
	f.check(cli.Get(ctx, client.ObjectKey{Name: target.Name}, &mp))

	// Simulate a partially labeled definition from a former publication. The
	// owner UID must surface it even when a label selector could not find it.
	leftover := &v1.MiddlewareConfiguration{ObjectMeta: metav1.ObjectMeta{Name: "leftover", OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(&mp, v1.GroupVersion.WithKind("MiddlewarePackage"))}}, Spec: v1.MiddlewareConfigurationSpec{Template: "previous"}}
	f.check(cli.Create(ctx, leftover))
	f.check(cli.Delete(ctx, target))
	unavailable := errors.New("inventory unavailable")
	interrupted := interceptor.NewClient(cli, interceptor.Funcs{List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
		if _, isMetadata := list.(*metav1.PartialObjectMetadataList); isMetadata {
			return unavailable
		}
		return c.List(ctx, list, opts...)
	}})
	if err := f.reconcile(interrupted, target.Name); !errors.Is(err, unavailable) {
		t.Fatalf("incomplete inventory: %v", err)
	}
	if err := f.reconcile(cli, target.Name); err == nil || !strings.Contains(err.Error(), "ownership") {
		t.Fatalf("incomplete ownership: %v", err)
	}
	if got := f.secret(target.Name); len(got.Finalizers) == 0 {
		t.Fatal("ownership conflict released finalizer")
	}
	f.check(cli.Get(ctx, client.ObjectKeyFromObject(&mp), &mp))
	leftover.Labels = map[string]string{v1.LabelPackageName: mp.Name, v1.LabelPackageVersion: mp.Spec.Version}
	f.check(cli.Update(ctx, leftover))
	f.check(f.reconcile(cli, target.Name))
	f.assertAbsent(client.ObjectKeyFromObject(leftover), &v1.MiddlewareConfiguration{})
	f.assertAbsent(client.ObjectKeyFromObject(&mp), &v1.MiddlewarePackage{})
	f.assertAbsent(client.ObjectKeyFromObject(target), &corev1.Secret{})
	if got := f.config(f.shared); got.UID != original.UID || got.ResourceVersion != original.ResourceVersion {
		t.Fatalf("foreign definition changed: %+v", got.ObjectMeta)
	}
}
