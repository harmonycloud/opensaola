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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	"github.com/harmonycloud/opensaola/internal/service/consts"
	"github.com/harmonycloud/opensaola/internal/service/middlewarebaseline"
	"github.com/harmonycloud/opensaola/internal/service/middlewareoperatorbaseline"
	"github.com/harmonycloud/opensaola/internal/service/packages"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

type packageSwitchFixture struct {
	t                                                              *testing.T
	cli                                                            client.WithWatch
	component, previous, candidate, shared, oldOnly, newOnly, last string
	initial                                                        *v1.MiddlewareConfiguration
}

func newPackageSwitchFixture(t *testing.T, cli client.WithWatch, name string, invalid bool) *packageSwitchFixture {
	t.Helper()
	f := &packageSwitchFixture{t: t, cli: cli, component: name, previous: name + "-v1", candidate: name + "-v2",
		shared: name + "-a-shared", oldOnly: name + "-b-obsolete", newOnly: name + "-c-added", last: name + "-z-last"}
	f.archive(f.previous, "v1", f.shared, f.oldOnly)
	f.check(f.reconcile(cli, f.previous))
	f.initial = f.config(f.shared)
	f.assertConfig(f.initial, f.previous, "v1", f.initial.UID)
	f.assertEnabled(f.previous, true)
	last := f.last
	if invalid {
		last = "Invalid-Name"
	}
	f.archive(f.candidate, "v2", f.shared, f.newOnly, last)
	return f
}

func (f *packageSwitchFixture) check(err error) {
	f.t.Helper()
	if err != nil {
		f.t.Fatal(err)
	}
}

func (f *packageSwitchFixture) archive(name, version string, configs ...string) {
	f.t.Helper()
	files := map[string]string{"metadata.yaml": fmt.Sprintf("name: %s\nversion: %s\n", f.component, version)}
	// Optional necessary is explicitly null in published Nacos action archives.
	files["actions/restart.yaml"] = fmt.Sprintf("apiVersion: middleware.cn/v1\nkind: MiddlewareActionBaseline\nmetadata: {name: %s-restart}\nspec: {baselineType: OpsAction, actionType: restart, necessary: null, steps: []}\n", f.component)
	for _, entry := range []struct{ kind, suffix string }{
		{"MiddlewareBaseline", "app"}, {"MiddlewareOperatorBaseline", "operator"},
	} {
		files["baselines/"+entry.suffix+".yaml"] = fmt.Sprintf("apiVersion: middleware.cn/v1\nkind: %s\nmetadata:\n  name: %s-%s\n  annotations: {archive-version: %s}\nspec: {}\n", entry.kind, f.component, entry.suffix, version)
	}
	for _, key := range configs {
		files["configurations/"+key+".yaml"] = fmt.Sprintf("apiVersion: middleware.cn/v1\nkind: MiddlewareConfiguration\nmetadata: {name: %s}\nspec: {template: %s}\n", key, version)
	}
	immutable := true
	s := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", Labels: map[string]string{
			v1.LabelProject: consts.ProjectOpenSaola, v1.LabelComponent: f.component, v1.LabelPackageName: name, v1.LabelPackageVersion: version,
		}, Annotations: map[string]string{v1.LabelInstall: "true"}, Finalizers: []string{v1.FinalizerPackageSecret}},
		Immutable: &immutable, Data: map[string][]byte{packages.Release: buildPackageRelease(f.t, name, files)},
	}
	f.check(f.cli.Create(f.t.Context(), s))
	f.t.Cleanup(func() { packages.InvalidatePackageCache(name) })
}

func (f *packageSwitchFixture) secret(name string) *corev1.Secret {
	f.t.Helper()
	var s corev1.Secret
	f.check(f.cli.Get(f.t.Context(), client.ObjectKey{Name: name, Namespace: "default"}, &s))
	return &s
}

func (f *packageSwitchFixture) config(name string) *v1.MiddlewareConfiguration {
	f.t.Helper()
	var m v1.MiddlewareConfiguration
	f.check(f.cli.Get(f.t.Context(), client.ObjectKey{Name: name}, &m))
	return &m
}

func (f *packageSwitchFixture) reconcile(cli client.Client, name string) error {
	f.t.Helper()
	return HandleSecret(f.t.Context(), cli, f.secret(name), consts.HandleActionUpdate)
}

func (f *packageSwitchFixture) assertEnabled(name string, want bool) {
	f.t.Helper()
	if s := f.secret(name); (s.Labels[v1.LabelEnabled] == "true") != want {
		f.t.Errorf("Secret %s: expected enabled=%t, labels=%v annotations=%v", name, want, s.Labels, s.Annotations)
	}
}
func (f *packageSwitchFixture) assertConfig(m *v1.MiddlewareConfiguration, name, version string, uid types.UID) {
	f.t.Helper()
	var mp v1.MiddlewarePackage
	f.check(f.cli.Get(f.t.Context(), client.ObjectKey{Name: name}, &mp))
	if !metav1.IsControlledBy(m, &mp) || m.Labels[v1.LabelPackageName] != name || m.Labels[v1.LabelPackageVersion] != version || m.Spec.Template != version || (uid != "" && m.UID != uid) {
		f.t.Fatalf("%s uid=%s owners=%v labels=%v template=%q; want uid=%s owner=%s template=%s", m.Name, m.UID, m.OwnerReferences, m.Labels, m.Spec.Template, uid, name, version)
	}
}
func (f *packageSwitchFixture) assertAbsent(key client.ObjectKey, obj client.Object) {
	f.t.Helper()
	if err := f.cli.Get(f.t.Context(), key, obj); !apierrors.IsNotFound(err) {
		f.t.Fatalf("expected %T %s gone: %v", obj, key, err)
	}
}
func (f *packageSwitchFixture) assertSwitched() {
	f.t.Helper()
	f.assertEnabled(f.previous, false)
	f.assertEnabled(f.candidate, true)
	f.assertConfig(f.config(f.shared), f.candidate, "v2", f.initial.UID)
	f.assertConfig(f.config(f.newOnly), f.candidate, "v2", "")
	f.assertConfig(f.config(f.last), f.candidate, "v2", "")
	f.assertAbsent(client.ObjectKey{Name: f.oldOnly}, &v1.MiddlewareConfiguration{})
}

func TestPackageSwitchAPI(t *testing.T) {
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
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := v1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	newClient := func(t *testing.T) client.WithWatch {
		t.Helper()
		c, err := client.NewWithWatch(cfg, client.Options{Scheme: scheme})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	oldNamespace := packages.DataNamespace
	packages.SetDataNamespace("default")
	t.Cleanup(func() { packages.SetDataNamespace(oldNamespace) })
	cli := newClient(t)
	t.Run("concurrent candidate packages", func(t *testing.T) { checkConcurrentPackageSwitch(t, cli) })
	t.Run("corrupt archive is reported without retiring A", func(t *testing.T) {
		f := newPackageSwitchFixture(t, cli, "switch-archive", false)
		bad := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "switch-invalid-tar", Namespace: "default", Labels: map[string]string{v1.LabelProject: consts.ProjectOpenSaola, v1.LabelComponent: f.component}, Annotations: map[string]string{v1.LabelInstall: "true"}}, Data: map[string][]byte{packages.Release: []byte("not a package")}}
		f.check(cli.Create(t.Context(), bad))
		f.check(f.reconcile(cli, bad.Name))
		s := f.secret(bad.Name)
		if s.Annotations[v1.AnnotationInstallError] == "" || s.Annotations[v1.AnnotationInstallDigest] != packageReleaseDigest(bad) {
			t.Fatalf("missing invalid archive diagnosis: %v", s.Annotations)
		}
		f.assertEnabled(f.previous, true)
		if f.config(f.shared).ResourceVersion != f.initial.ResourceVersion {
			t.Fatal("invalid archive changed active definition")
		}
	})
	t.Run("preflight rejects a rewritten explicit value", func(t *testing.T) {
		f := newPackageSwitchFixture(t, cli, "switch-rewrite", false)
		rewritten := false
		admission := interceptor.NewClient(cli, interceptor.Funcs{Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			err := c.Update(ctx, obj, opts...)
			o, ok := obj.(*unstructured.Unstructured)
			if err == nil && ok && obj.GetName() == f.shared && len((&client.UpdateOptions{}).ApplyOptions(opts).DryRun) > 0 {
				rewritten = true
				return unstructured.SetNestedField(o.Object, "wrong", "spec", "template")
			}
			return err
		}})
		f.check(f.reconcile(admission, f.candidate))
		if !rewritten || !strings.Contains(f.secret(f.candidate).Annotations[v1.AnnotationInstallError], "preflight changed") {
			t.Fatal("rewritten explicit field passed preflight")
		}
		f.assertEnabled(f.previous, true)
		if f.config(f.shared).ResourceVersion != f.initial.ResourceVersion {
			t.Fatal("preflight changed previous definition")
		}
	})
	t.Run("retirement waits for a terminating definition", func(t *testing.T) {
		f := newPackageSwitchFixture(t, cli, "switch-finalizer", false)
		old := f.config(f.oldOnly)
		old.Finalizers = []string{"audit.example/hold"}
		f.check(cli.Update(t.Context(), old))
		if err := f.reconcile(cli, f.candidate); err == nil || !strings.Contains(err.Error(), "waiting for finalizers") {
			t.Fatalf("expected pending retirement, got %v", err)
		}
		f.assertEnabled(f.candidate, true)
		old = f.config(f.oldOnly)
		if old.DeletionTimestamp == nil {
			t.Fatal("old definition was not queued for deletion")
		}
		old.Finalizers = nil
		f.check(cli.Update(t.Context(), old))
		f.check(f.reconcile(newClient(t), f.candidate))
		f.assertSwitched()
	})
	t.Run("preflight preserves an existing definition with disputed ownership", func(t *testing.T) {
		f := newPackageSwitchFixture(t, cli, "switch-disputed", false)
		old := f.config(f.oldOnly)
		delete(old.Labels, v1.LabelPackageName)
		f.check(cli.Update(t.Context(), old))
		if err := f.reconcile(cli, f.candidate); err == nil || !strings.Contains(err.Error(), "ownership") {
			t.Fatalf("expected ownership error, got %v", err)
		}
		f.assertEnabled(f.previous, true)
		f.assertEnabled(f.candidate, false)
		if current := f.config(f.shared); current.ResourceVersion != f.initial.ResourceVersion {
			t.Fatal("candidate touched shared definition before completing preflight")
		}
		if current := f.config(f.oldOnly); current.ResourceVersion != old.ResourceVersion {
			t.Fatal("preflight changed disputed definition")
		}
	})

	t.Run("failed reinstall restores the existing baseline", func(t *testing.T) {
		f := newPackageSwitchFixture(t, cli, "switch-reinstall", false)
		s := f.secret(f.previous)
		if s.Annotations == nil {
			s.Annotations = map[string]string{}
		}
		s.Annotations[v1.LabelInstall] = "true"
		f.check(cli.Update(t.Context(), s))
		baseKey := client.ObjectKey{Name: f.component + "-app"}
		var before, after v1.MiddlewareBaseline
		f.check(cli.Get(t.Context(), baseKey, &before))
		transient := errors.New("injected reinstall failure")
		failed := interceptor.NewClient(cli, interceptor.Funcs{Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			if obj.GetName() == f.shared && len((&client.UpdateOptions{}).ApplyOptions(opts).DryRun) == 0 {
				return transient
			}
			return c.Update(ctx, obj, opts...)
		}})
		if err := f.reconcile(failed, f.previous); !errors.Is(err, transient) {
			t.Fatalf("expected injected reinstall error, got %v", err)
		}
		f.check(cli.Get(t.Context(), baseKey, &after))
		if before.UID != after.UID || !reflect.DeepEqual(before.Annotations, after.Annotations) || !reflect.DeepEqual(before.Spec, after.Spec) {
			t.Fatal("reinstall rollback lost existing baseline")
		}
		f.assertConfig(f.config(f.shared), f.previous, "v1", f.initial.UID)
		f.assertEnabled(f.previous, true)
	})
	t.Run("large journal interrupted", func(t *testing.T) {
		checkSwitchJournalShardedPreparation(t, cli)
	})

	t.Run("admission rejects candidate before changing active resources", func(t *testing.T) {
		f := newPackageSwitchFixture(t, cli, "switch-invalid", true)
		oldOnly := f.config(f.oldOnly)
		f.check(f.reconcile(cli, f.candidate))
		f.assertEnabled(f.previous, true)
		f.assertEnabled(f.candidate, false)
		if s := f.secret(f.candidate); s.Annotations[v1.AnnotationInstallError] == "" || s.Annotations[v1.AnnotationInstallDigest] == "" {
			t.Fatalf("invalid candidate missing terminal error/digest: %v", s.Annotations)
		}
		for _, before := range []*v1.MiddlewareConfiguration{oldOnly, f.initial} {
			after := f.config(before.Name)
			if after.UID != before.UID || after.ResourceVersion != before.ResourceVersion {
				t.Fatalf("preflight changed %s", before.Name)
			}
		}
		f.assertAbsent(client.ObjectKey{Name: f.newOnly}, &v1.MiddlewareConfiguration{})
	})

	t.Run("older instances keep archive and block explicit uninstall", func(t *testing.T) {
		f := newPackageSwitchFixture(t, cli, "switch-used", false)
		archive := f.secret(f.previous).Data[packages.Release]
		meta := metav1.ObjectMeta{Name: "switch-old-mid", Namespace: "default", Labels: map[string]string{v1.LabelPackageName: f.previous}}
		mid := &v1.Middleware{ObjectMeta: meta, Spec: v1.MiddlewareSpec{Baseline: f.component + "-app"}}
		meta.Name = "switch-old-mo"
		mo := &v1.MiddlewareOperator{ObjectMeta: meta, Spec: v1.MiddlewareOperatorSpec{Baseline: f.component + "-operator"}}
		f.check(cli.Create(t.Context(), mid))
		f.check(cli.Create(t.Context(), mo))
		f.check(f.reconcile(cli, f.candidate))
		f.assertSwitched()
		for _, obj := range []client.Object{mid, mo} {
			version := obj.GetResourceVersion()
			f.check(cli.Get(t.Context(), client.ObjectKeyFromObject(obj), obj))
			if obj.GetResourceVersion() != version || obj.GetLabels()[v1.LabelPackageName] != f.previous {
				t.Fatalf("switch unexpectedly modified existing %T %s", obj, obj.GetName())
			}
		}
		// Cold reads exercise both archived baseline lookups after B took the live names.
		mb, err := middlewarebaseline.Get(t.Context(), cli, mid.Spec.Baseline, mid.Labels[v1.LabelPackageName])
		f.check(err)
		mob, err := middlewareoperatorbaseline.Get(t.Context(), cli, mo.Spec.Baseline, mo.Labels[v1.LabelPackageName])
		f.check(err)
		if mb.Annotations["archive-version"] != "v1" || mob.Annotations["archive-version"] != "v1" {
			t.Fatalf("existing instances did not resolve A's baselines: %v, %v", mb.Annotations, mob.Annotations)
		}
		old := f.secret(f.previous)
		if !bytes.Equal(old.Data[packages.Release], archive) {
			t.Fatal("retired package archive changed")
		}
		f.check(cli.Delete(t.Context(), old))
		err = f.reconcile(cli, f.previous)
		if err == nil || !strings.Contains(err.Error(), mid.Name) || !strings.Contains(err.Error(), mo.Name) {
			t.Fatalf("expected both usage blockers, got %v", err)
		}
		if len(f.secret(f.previous).Finalizers) == 0 {
			t.Fatal("package with active users released its finalizer")
		}
		for _, o := range []client.Object{mid, mo} {
			f.check(cli.Delete(t.Context(), o))
		}
		f.check(f.reconcile(cli, f.previous))
		f.assertAbsent(client.ObjectKeyFromObject(old), &corev1.Secret{})
		f.assertAbsent(client.ObjectKey{Name: f.previous}, &v1.MiddlewarePackage{})
		f.assertConfig(f.config(f.shared), f.candidate, "v2", f.initial.UID)
		f.check(cli.Delete(t.Context(), f.secret(f.candidate)))
		f.check(f.reconcile(cli, f.candidate))
		for _, name := range []string{f.shared, f.newOnly, f.last} {
			f.assertAbsent(client.ObjectKey{Name: name}, &v1.MiddlewareConfiguration{})
		}
	})

	injected := errors.New("injected API transport failure")
	for _, concurrent := range []bool{false, true} {
		name := "rollback"
		if concurrent {
			name = "concurrent"
		}
		t.Run(name+" after some writes", func(t *testing.T) {
			f := newPackageSwitchFixture(t, cli, "switch-"+name, false)
			called := false
			failed := interceptor.NewClient(cli, interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
				if obj.GetName() == f.last && len((&client.CreateOptions{}).ApplyOptions(opts).DryRun) == 0 {
					called = true
					// The preceding update and creation must both have reached the server.
					m := f.config(f.shared)
					f.assertConfig(m, f.candidate, "v2", f.initial.UID)
					f.assertConfig(f.config(f.newOnly), f.candidate, "v2", "")
					if concurrent {
						m.Spec.Template = "external change"
						f.check(c.Update(ctx, m))
					}
					return injected
				}
				return c.Create(ctx, obj, opts...)
			}})
			err := f.reconcile(failed, f.candidate)
			if err == nil || !called {
				t.Fatalf("candidate failure not observed: injected=%t err=%v", called, err)
			}
			f.assertEnabled(f.previous, true)
			f.assertEnabled(f.candidate, false)
			f.assertConfig(f.config(f.oldOnly), f.previous, "v1", "")
			if concurrent {
				// Fail closed while the template no longer matches the write being undone.
				m := f.config(f.shared)
				if m.UID != f.initial.UID || m.Spec.Template != "external change" {
					t.Fatalf("external writer lost: %+v", m.Spec)
				}
				_ = f.reconcile(newClient(t), f.candidate)
				if f.config(f.shared).Spec.Template != "external change" {
					t.Fatal("retry overwrote external writer")
				}
				return
			}
			f.assertConfig(f.config(f.shared), f.previous, "v1", f.initial.UID)
			f.assertAbsent(client.ObjectKey{Name: f.newOnly}, &v1.MiddlewareConfiguration{})
			f.assertAbsent(client.ObjectKey{Name: f.last}, &v1.MiddlewareConfiguration{})
			f.check(f.reconcile(newClient(t), f.candidate))
			f.assertSwitched()
		})
	}

	t.Run("restart after server accepts shared update", func(t *testing.T) {
		f := newPackageSwitchFixture(t, cli, "switch-restart", false)
		interrupted := interceptor.NewClient(cli, interceptor.Funcs{Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			err := c.Update(ctx, obj, opts...)
			if err == nil && obj.GetName() == f.shared && len((&client.UpdateOptions{}).ApplyOptions(opts).DryRun) == 0 {
				panic(injected)
			}
			return err
		}})
		func() {
			defer func() {
				if r := recover(); r != injected {
					t.Fatalf("expected simulated restart, got %v", r)
				}
			}()
			_ = f.reconcile(interrupted, f.candidate)
		}()
		f.assertConfig(f.config(f.shared), f.candidate, "v2", f.initial.UID)
		f.assertEnabled(f.previous, true)
		f.assertEnabled(f.candidate, false)
		packages.InvalidatePackageCache(f.candidate)
		packages.InvalidatePackageCache(f.previous)
		if err := f.reconcile(newClient(t), f.candidate); err == nil {
			t.Fatal("recovering an interrupted install must requeue the pending candidate")
		}
		f.assertConfig(f.config(f.shared), f.previous, "v1", f.initial.UID)
		f.assertEnabled(f.previous, true)
		f.assertEnabled(f.candidate, false)
		f.check(f.reconcile(newClient(t), f.candidate))
		f.assertSwitched()
	})

	t.Run("lost acknowledgement for candidate enable", func(t *testing.T) {
		f := newPackageSwitchFixture(t, cli, "switch-ack", false)
		accepted := false
		lost := interceptor.NewClient(cli, interceptor.Funcs{Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
			if _, ok := obj.(*corev1.Secret); !ok || obj.GetName() != f.candidate || accepted {
				return c.Patch(ctx, obj, patch, opts...)
			}
			data, err := patch.Data(obj)
			f.check(err)
			var delta corev1.Secret
			f.check(json.Unmarshal(data, &delta))
			if delta.Labels[v1.LabelEnabled] != "true" {
				return c.Patch(ctx, obj, patch, opts...)
			}
			if delta.ResourceVersion == "" || delta.ResourceVersion != f.secret(f.candidate).ResourceVersion {
				t.Fatalf("candidate enable patch does not guard the current Secret resourceVersion: %s", data)
			}
			f.check(c.Patch(ctx, obj, patch, opts...))
			accepted = true
			return injected
		}})
		_ = f.reconcile(lost, f.candidate)
		if !accepted {
			t.Fatal("candidate enable was not submitted to the API")
		}
		f.assertEnabled(f.candidate, true)
		shared := f.config(f.shared)
		f.assertConfig(shared, f.candidate, "v2", f.initial.UID)
		f.check(f.reconcile(newClient(t), f.candidate))
		f.assertSwitched()
		if f.config(f.shared).ResourceVersion != shared.ResourceVersion {
			t.Fatal("lost acknowledgement caused a second publication of the shared resource")
		}
	})

	t.Run("retry retirement after candidate commit", func(t *testing.T) {
		f := newPackageSwitchFixture(t, cli, "switch-retire", false)
		called := false
		block := interceptor.NewClient(cli, interceptor.Funcs{Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			if obj.GetName() == f.oldOnly && len((&client.DeleteOptions{}).ApplyOptions(opts).DryRun) == 0 {
				called = true
				f.assertEnabled(f.candidate, true)
				f.assertConfig(f.config(f.shared), f.candidate, "v2", f.initial.UID)
				return injected
			}
			return c.Delete(ctx, obj, opts...)
		}})
		if err := f.reconcile(block, f.candidate); err == nil || !called {
			t.Fatalf("expected retryable retirement failure, injected=%t err=%v", called, err)
		}
		f.assertEnabled(f.candidate, true)
		committed := f.config(f.shared)
		f.check(f.reconcile(newClient(t), f.candidate))
		f.assertSwitched()
		if f.config(f.shared).ResourceVersion != committed.ResourceVersion {
			t.Fatal("cleanup retry rewrote committed shared resource")
		}
	})
}
