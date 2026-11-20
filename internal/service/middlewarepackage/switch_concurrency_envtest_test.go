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
	"strings"
	"sync"
	"testing"
	"time"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	"github.com/harmonycloud/opensaola/internal/service/consts"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// Stop a real installation between its prepared snapshot and first shared update.
func pausePackageSwitch(ctx context.Context, cli client.WithWatch, secret *corev1.Secret, shared string) (prepared <-chan struct{}, resume func(), done <-chan error) {
	ready := make(chan struct{})
	proceed := make(chan struct{})
	finish := make(chan error, 1)
	markReady := sync.OnceFunc(func() { close(ready) })
	pausing := interceptor.NewClient(cli, interceptor.Funcs{
		Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			if obj.GetName() == shared && len((&client.UpdateOptions{}).ApplyOptions(opts).DryRun) == 0 {
				markReady()
				select {
				case <-proceed:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			return c.Update(ctx, obj, opts...)
		},
	})
	go func() { finish <- HandleSecret(ctx, pausing, secret, consts.HandleActionUpdate) }()
	return ready, sync.OnceFunc(func() { close(proceed) }), finish
}

func checkConcurrentPackageSwitch(t *testing.T, cli client.WithWatch) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	f := newPackageSwitchFixture(t, cli, "switch-compete", false)
	third := f.component + "-v3"
	thirdOnly := f.component + "-v3-only"
	f.archive(third, "v3", f.shared, thirdOnly)
	b, c := f.secret(f.candidate), f.secret(third)
	bReady, resumeB, bDone := pausePackageSwitch(ctx, cli, b, f.shared)
	defer resumeB()
	select {
	case <-bReady:
	case err := <-bDone:
		t.Fatalf("v2 exited before preparation: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}

	bRecord, j, err := readSwitchJournal(ctx, cli, f.component)
	f.check(err)
	if j == nil || j.TargetUID != b.UID || j.Phase != "prepared" || len(j.Sources) != 1 || j.Sources[0].Name != f.previous {
		t.Fatalf("unexpected v2 journal: %#v", j)
	}
	// An independent caller without the local handler lock also has to obey the journal.
	if err := activatePackage(ctx, cli, c); err == nil || !strings.Contains(err.Error(), b.Name) {
		t.Fatalf("competing v3 activation ignored the v2 journal: %v", err)
	}
	current, _, err := readSwitchJournal(ctx, cli, f.component)
	f.check(err)
	if current == nil || current.UID != bRecord.UID || current.ResourceVersion != bRecord.ResourceVersion {
		t.Fatal("v3 changed v2's in-flight record")
	}

	cReady, resumeC, cDone := pausePackageSwitch(ctx, cli, c, f.shared)
	defer resumeC()
	select {
	case <-cReady:
		t.Fatal("v3 prepared while v2 still owned the switch")
	case err := <-cDone:
		t.Fatalf("the v3 installation should queue while v2 runs: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	resumeB()
	select {
	case err := <-bDone:
		f.check(err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case <-cReady:
	case err := <-cDone:
		t.Fatalf("v3 exited before preparation: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	cRecord, j, err := readSwitchJournal(ctx, cli, f.component)
	f.check(err)
	if j == nil || cRecord.UID == bRecord.UID || j.TargetUID != c.UID || j.Phase != "prepared" || len(j.Sources) != 1 || j.Sources[0].SecretUID != b.UID {
		t.Fatalf("v3 did not switch from committed v2: %#v", j)
	}
	f.assertSwitched()
	f.assertEnabled(third, false)
	resumeC()
	select {
	case err := <-cDone:
		f.check(err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	f.assertEnabled(f.previous, false)
	f.assertEnabled(f.candidate, false)
	f.assertEnabled(third, true)
	f.assertConfig(f.config(f.shared), third, "v3", f.initial.UID)
	f.assertConfig(f.config(thirdOnly), third, "v3", "")
	for _, retired := range []string{f.oldOnly, f.newOnly, f.last} {
		f.assertAbsent(client.ObjectKey{Name: retired}, &v1.MiddlewareConfiguration{})
	}
	for _, name := range []string{f.previous, f.candidate, third} {
		secret := f.secret(name)
		if secret.Annotations[pendingSwitch] != "" || secret.Annotations[v1.LabelInstall] != "" {
			t.Errorf("%s still has an install request: %v", name, secret.Annotations)
		}
	}
	if record, j, err := readSwitchJournal(ctx, cli, f.component); err != nil || record != nil || j != nil {
		t.Fatalf("leftover switch journal: %v %v", record, err)
	}
}
