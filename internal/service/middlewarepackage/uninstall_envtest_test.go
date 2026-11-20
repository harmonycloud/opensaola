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
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	"github.com/harmonycloud/opensaola/internal/service/consts"
	"github.com/harmonycloud/opensaola/internal/service/packages"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

func TestUninstallPackageOwnershipAPI(t *testing.T) {
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
	scheme := runtime.NewScheme()
	if err := v1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	cli, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	previousNamespace := packages.DataNamespace
	packages.SetDataNamespace("default")
	t.Cleanup(func() { packages.SetDataNamespace(previousNamespace) })

	// Both package archives define the same four cluster-scoped names.
	definitions := []struct {
		kind, path, spec string
		object           client.Object
	}{
		{"MiddlewareBaseline", "baselines/app.yaml", "{}", &v1.MiddlewareBaseline{}},
		{"MiddlewareOperatorBaseline", "baselines/operator.yaml", "{}", &v1.MiddlewareOperatorBaseline{}},
		{"MiddlewareActionBaseline", "actions/restart.yaml", "{actionType: restart, baselineType: NormalAction, steps: []}", &v1.MiddlewareActionBaseline{}},
		{"MiddlewareConfiguration", "configurations/config.yaml", "{template: example}", &v1.MiddlewareConfiguration{}},
	}
	install := func(version string) *corev1.Secret {
		t.Helper()
		name := "audit-" + version
		files := map[string]string{"metadata.yaml": "name: audit\nversion: " + version + "\n"}
		for _, def := range definitions {
			files[def.path] = fmt.Sprintf("apiVersion: middleware.cn/v1\nkind: %s\nmetadata: {name: shared}\nspec: %s\n", def.kind, def.spec)
		}
		secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
			Namespace: "default", Name: name,
			Annotations: map[string]string{v1.LabelInstall: "true"},
			Finalizers:  []string{v1.FinalizerPackageSecret},
			Labels:      map[string]string{v1.LabelProject: consts.ProjectOpenSaola, v1.LabelComponent: "audit"},
		}, Data: map[string][]byte{packages.Release: buildPackageRelease(t, name, files)}}
		if err := cli.Create(ctx, secret); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { packages.InvalidatePackageCache(name) })
		if err := HandleSecret(ctx, cli, secret, consts.HandleActionPublish); err != nil {
			t.Fatal(err)
		}
		return secret
	}
	get := func(key client.ObjectKey, obj client.Object) {
		t.Helper()
		if err := cli.Get(ctx, key, obj); err != nil {
			t.Fatal(err)
		}
	}
	checkGone := func(key client.ObjectKey, obj client.Object) {
		t.Helper()
		if err := cli.Get(ctx, key, obj); !apierrors.IsNotFound(err) {
			t.Fatalf("expected %T %s gone, got %v", obj, key, err)
		}
	}
	key := client.ObjectKey{Name: "shared"}
	oldPkg := install("v1")
	newPkg := install("v2")
	var owner v1.MiddlewarePackage
	get(client.ObjectKey{Name: newPkg.Name}, &owner)
	type identity struct {
		uid types.UID
		rv  string
	}
	shared := make(map[string]identity, len(definitions))
	for _, def := range definitions {
		get(key, def.object)
		if !metav1.IsControlledBy(def.object, &owner) || def.object.GetLabels()[v1.LabelPackageName] != newPkg.Name || def.object.GetLabels()[v1.LabelPackageVersion] != "v2" {
			t.Fatalf("unexpected owner after switch for %s: %v, %v", def.kind, def.object.GetOwnerReferences(), def.object.GetLabels())
		}
		shared[def.kind] = identity{def.object.GetUID(), def.object.GetResourceVersion()}
	}

	// A running MO for B must not prevent retiring A.
	operator := &v1.MiddlewareOperator{ObjectMeta: metav1.ObjectMeta{Name: "using-v2", Namespace: "default", Labels: map[string]string{v1.LabelPackageName: newPkg.Name}}}
	if err := cli.Create(ctx, operator); err != nil {
		t.Fatal(err)
	}
	beginUninstall := func(secret *corev1.Secret) {
		t.Helper()
		if err := cli.Delete(ctx, secret); err != nil {
			t.Fatal(err)
		}
		get(client.ObjectKeyFromObject(secret), secret)
	}
	beginUninstall(oldPkg)
	if err := HandleSecret(ctx, cli, oldPkg, consts.HandleActionUpdate); err != nil {
		t.Fatal(err)
	}
	checkGone(client.ObjectKeyFromObject(oldPkg), &corev1.Secret{})
	checkGone(client.ObjectKey{Name: oldPkg.Name}, &v1.MiddlewarePackage{})
	get(client.ObjectKeyFromObject(newPkg), newPkg)
	if newPkg.Labels[v1.LabelEnabled] != "true" {
		t.Fatal("replacement package lost enabled label")
	}
	for _, def := range definitions {
		get(key, def.object)
		if def.object.GetUID() != shared[def.kind].uid || def.object.GetResourceVersion() != shared[def.kind].rv {
			t.Fatalf("retiring A changed %s owned by B", def.kind)
		}
	}
	beginUninstall(newPkg)
	if err := HandleSecret(ctx, cli, newPkg, consts.HandleActionUpdate); err == nil || !strings.Contains(err.Error(), operator.Name) {
		t.Fatalf("expected usage block for active MO, got %v", err)
	}
	if err := cli.Delete(ctx, operator); err != nil {
		t.Fatal(err)
	}

	// A label that contradicts the controller owner blocks package finalization.
	var config v1.MiddlewareConfiguration
	get(key, &config)
	config.Labels[v1.LabelPackageName] = oldPkg.Name
	if err := cli.Update(ctx, &config); err != nil {
		t.Fatal(err)
	}
	get(client.ObjectKeyFromObject(newPkg), newPkg)
	if err := HandleSecret(ctx, cli, newPkg, consts.HandleActionUpdate); err == nil || !strings.Contains(err.Error(), "ownership") {
		t.Fatalf("expected ownership error, got %v", err)
	}
	get(client.ObjectKey{Name: newPkg.Name}, &owner)
	get(client.ObjectKeyFromObject(newPkg), newPkg)
	if len(newPkg.Finalizers) == 0 {
		t.Fatal("ambiguous owner released package finalizer")
	}
	get(key, &config)
	config.Labels[v1.LabelPackageName] = newPkg.Name
	if err := cli.Update(ctx, &config); err != nil {
		t.Fatal(err)
	}

	// Advance resourceVersion between GET and DELETE as a concurrent writer would.
	racing := &onConfigurationDelete{Client: cli, before: func(opts []client.DeleteOption) {
		options := (&client.DeleteOptions{}).ApplyOptions(opts)
		p := options.Preconditions
		if p == nil || p.UID == nil || *p.UID != config.UID || p.ResourceVersion == nil || *p.ResourceVersion != config.ResourceVersion {
			t.Fatalf("DELETE does not guard the observed object: %+v", p)
		}
		config.Annotations = map[string]string{"concurrent-write": "true"}
		if err := cli.Update(ctx, &config); err != nil {
			t.Fatal(err)
		}
	}}
	if err := HandleSecret(ctx, racing, newPkg, consts.HandleActionUpdate); !apierrors.IsConflict(err) {
		t.Fatalf("expected delete conflict, got %v", err)
	}
	get(key, &config)
	get(client.ObjectKeyFromObject(newPkg), newPkg)
	// A stale finalizer patch can fail after all the definitions and the package
	// are gone. The next reconcile must still release the Secret.
	changed := newPkg.DeepCopy()
	changed.Labels["uninstall-test"] = "concurrent-write"
	if err := cli.Update(ctx, changed); err != nil {
		t.Fatal(err)
	}
	if err := HandleSecret(ctx, cli, newPkg, consts.HandleActionUpdate); !apierrors.IsConflict(err) {
		t.Fatalf("expected stale Secret patch to conflict, got %v", err)
	}
	checkGone(client.ObjectKey{Name: newPkg.Name}, &v1.MiddlewarePackage{})
	get(client.ObjectKeyFromObject(newPkg), newPkg)
	if err := HandleSecret(ctx, cli, newPkg, consts.HandleActionUpdate); err != nil {
		t.Fatal(err)
	}
	for _, def := range definitions {
		checkGone(key, def.object)
	}
	checkGone(client.ObjectKeyFromObject(newPkg), &corev1.Secret{})
	checkGone(client.ObjectKey{Name: newPkg.Name}, &v1.MiddlewarePackage{})
}

type onConfigurationDelete struct {
	client.Client
	before func([]client.DeleteOption)
}

func (c *onConfigurationDelete) Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
	if _, ok := obj.(*v1.MiddlewareConfiguration); ok {
		c.before(opts)
	}
	return c.Client.Delete(ctx, obj, opts...)
}
