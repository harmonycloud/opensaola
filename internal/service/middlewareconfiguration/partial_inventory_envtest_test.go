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

package middlewareconfiguration

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	"github.com/harmonycloud/opensaola/internal/service/consts"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type partialInventoryFixture struct {
	t     *testing.T
	ctx   context.Context
	cli   client.Client
	owner client.Object
	a, b  *v1.MiddlewareConfiguration
}

func newPartialInventoryFixture(t *testing.T, ctx context.Context, cli client.Client, ownerKind, policy string) *partialInventoryFixture {
	t.Helper()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "partial-inventory-"}}
	if err := cli.Create(ctx, ns); err != nil {
		t.Fatal(err)
	}
	cfg := func(name, namespace, policy string) *v1.MiddlewareConfiguration {
		c := &v1.MiddlewareConfiguration{ObjectMeta: metav1.ObjectMeta{Name: ns.Name + "-" + name, Annotations: map[string]string{v1.AnnotationConfigurationDisablePolicy: policy}}}
		c.Spec.Template = fmt.Sprintf("apiVersion: v1\nkind: ConfigMap\nmetadata: {name: %s, namespace: %s}\ndata: {setting: published}\n", name, namespace)
		if err := cli.Create(ctx, c); err != nil {
			t.Fatal(err)
		}
		return c
	}
	a, b := cfg("a", ns.Name, policy), cfg("b", ns.Name+"-missing", "delete")
	m := metav1.ObjectMeta{Name: "owner", Namespace: ns.Name}
	refs := []v1.Configuration{{Name: a.Name}, {Name: b.Name}}
	var o client.Object = &v1.Middleware{ObjectMeta: m, Spec: v1.MiddlewareSpec{Configurations: refs, Baseline: "fixture"}}
	if ownerKind == "MO" {
		o = &v1.MiddlewareOperator{ObjectMeta: m, Spec: v1.MiddlewareOperatorSpec{Configurations: refs}}
	}
	if err := cli.Create(ctx, o); err != nil {
		t.Fatal(err)
	}
	return &partialInventoryFixture{t, ctx, cli, o, a, b}
}

// Reload from persistent status before every entry, including recovery after
// the isolated client that injected the previous failure is discarded.
func (f *partialInventoryFixture) reload() client.Object {
	f.t.Helper()
	var o client.Object
	switch f.owner.(type) {
	case *v1.Middleware:
		o = &v1.Middleware{}
	default:
		o = &v1.MiddlewareOperator{}
	}
	if err := f.cli.Get(f.ctx, client.ObjectKeyFromObject(f.owner), o); err != nil {
		f.t.Fatal(err)
	}
	return o
}

func partialInventoryStatus(owner client.Object) ([]v1.RenderedConfigurationResource, *v1.PendingConfigurationWrite, int64) {
	switch m := owner.(type) {
	case *v1.Middleware:
		return m.Status.RenderedConfigurationResources, m.Status.PendingConfigurationWrite, m.Status.RenderedConfigurationResourcesGeneration
	case *v1.MiddlewareOperator:
		return m.Status.RenderedConfigurationResources, m.Status.PendingConfigurationWrite, m.Status.RenderedConfigurationResourcesGeneration
	default:
		panic("unexpected owner kind")
	}
}

func (f *partialInventoryFixture) apply(cli client.Client, action consts.HandleAction, configs ...*v1.MiddlewareConfiguration) ([]v1.RenderedConfigurationResource, error) {
	owner := f.reload()
	return ReconcileRenderedConfigurationResources(f.ctx, cli, owner, action, configs)
}

func (f *partialInventoryFixture) getA() *corev1.ConfigMap {
	f.t.Helper()
	var cm corev1.ConfigMap
	if err := f.cli.Get(f.ctx, client.ObjectKey{Namespace: f.owner.GetNamespace(), Name: "a"}, &cm); err != nil {
		f.t.Fatal(err)
	}
	return &cm
}

func (f *partialInventoryFixture) withdraw() {
	f.t.Helper()
	o := f.reload()
	switch m := o.(type) {
	case *v1.Middleware:
		m.Spec.Configurations = nil
	case *v1.MiddlewareOperator:
		m.Spec.Configurations = nil
	}
	if err := f.cli.Update(f.ctx, o); err != nil {
		f.t.Fatal(err)
	}
}

// Inject failure precisely in the owner status write following the successful
// target commit. The GET uses a separate client and observes actual API state.
type partialInventoryStatusClient struct {
	client.Client
	target    client.ObjectKey
	stopped   bool
	loseReply bool
}

func (c *partialInventoryStatusClient) Status() client.SubResourceWriter {
	return partialInventoryStatusWriter{c.Client.Status(), c}
}

type partialInventoryStatusWriter struct {
	client.SubResourceWriter
	c *partialInventoryStatusClient
}

var errInventoryCrash = errors.New("injected crash before configuration inventory confirmation")

func (w partialInventoryStatusWriter) Patch(ctx context.Context, o client.Object, p client.Patch, opts ...client.SubResourcePatchOption) error {
	var cm corev1.ConfigMap
	if err := w.c.Client.Get(ctx, w.c.target, &cm); err == nil && cm.Data["setting"] == "published" {
		if w.c.loseReply {
			if err := w.SubResourceWriter.Patch(ctx, o, p, opts...); err != nil {
				return err
			}
		}
		w.c.stopped = true
		return errInventoryCrash
	}
	return w.SubResourceWriter.Patch(ctx, o, p, opts...)
}

func testPartialConfigurationInventory(t *testing.T, ctx context.Context, cli client.Client, writes *atomic.Int64) {
	for _, kind := range []string{"MID", "MO"} {
		for _, scenario := range []string{"repair_B", "withdraw", "withdraw_orphan", "replace_confirmed"} {
			t.Run(kind+"/"+scenario, func(t *testing.T) {
				policy := "delete"
				if scenario == "withdraw_orphan" {
					policy = "orphan"
				}
				f := newPartialInventoryFixture(t, ctx, cli, kind, policy)
				_, err := f.apply(cli, consts.HandleActionPublish, f.a, f.b)
				if err == nil {
					t.Fatal("missing target namespace for B should fail publication")
				}
				a := f.getA()
				records, _, completedGen := partialInventoryStatus(f.reload())
				if len(records) != 1 || records[0].Name != "a" || records[0].OwnerUID != f.owner.GetUID() || records[0].ResourceUID != a.UID || records[0].ConfigurationUID != f.a.UID || records[0].DisablePolicy != policy || completedGen != 0 {
					t.Fatalf("partial batch must persist only A's confirmed receipt without completing its generation: records=%+v gen=%d", records, completedGen)
				}
				if scenario == "repair_B" {
					f.b.Spec.Template = "apiVersion: v1\nkind: ConfigMap\nmetadata: {name: b}\ndata: {setting: repaired}\n"
					if err := cli.Update(ctx, f.b); err != nil {
						t.Fatal(err)
					}
					result, err := f.apply(cli, consts.HandleActionPublish, f.a, f.b)
					if err != nil || len(result) != 2 {
						t.Fatalf("retry failed: records=%v err=%v", result, err)
					}
					var b corev1.ConfigMap
					if err := cli.Get(ctx, client.ObjectKey{Namespace: f.owner.GetNamespace(), Name: "b"}, &b); err != nil {
						t.Fatal(err)
					}
					confirmed, pending, completeGen := partialInventoryStatus(f.reload())
					if completeGen != f.owner.GetGeneration() || len(confirmed) != 2 || pending != nil || result[0].ResourceUID != a.UID || result[1].ResourceUID != b.UID || b.Data["setting"] != "repaired" {
						t.Fatal("retry did not durably confirm A and repaired B with their actual UIDs")
					}
					before := writes.Load()
					if _, err := f.apply(cli, consts.HandleActionUpdate, f.a, f.b); err != nil {
						t.Fatal(err)
					}
					if n := writes.Load() - before; n != 0 {
						t.Fatalf("unchanged retry produced %d persistent writes", n)
					}
					if token := f.getA().Annotations[v1.AnnotationConfigurationWriteID]; token == "" || token != a.Annotations[v1.AnnotationConfigurationWriteID] {
						t.Fatal("unchanged retry replaced A's publication token")
					}
					return
				}
				if scenario == "replace_confirmed" {
					if err := cli.Delete(ctx, a); err != nil {
						t.Fatal(err)
					}
					replacement := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: a.Name, Namespace: a.Namespace}, Data: map[string]string{"external": "preserve"}}
					if err := cli.Create(ctx, replacement); err != nil {
						t.Fatal(err)
					}
					a = replacement
				}
				f.withdraw()
				// The initial owner generation never completed; real controllers
				// still use Publish here even after the user withdrew the batch.
				result, err := f.apply(cli, consts.HandleActionPublish)
				if err != nil || len(result) != 0 {
					t.Fatalf("withdraw: %v / %v", result, err)
				}
				if scenario == "withdraw" {
					if err := cli.Get(ctx, client.ObjectKeyFromObject(a), &corev1.ConfigMap{}); !apierrors.IsNotFound(err) {
						t.Fatalf("withdrawing partial first publication left A: %v", err)
					}
				} else if got := f.getA(); got.UID != a.UID || got.ResourceVersion != a.ResourceVersion {
					t.Fatal("withdrawal modified orphan or external replacement")
				}
			})
		}
		for _, scenario := range []string{"recover", "confirmation_reply_lost", "lost_token", "missing_source_marker", "different_uid", "copied_create_token"} {
			t.Run(kind+"/restart_"+scenario, func(t *testing.T) {
				f := newPartialInventoryFixture(t, ctx, cli, kind, "delete")
				target := client.ObjectKey{Namespace: f.owner.GetNamespace(), Name: "a"}
				if scenario == "different_uid" {
					// A valid retained object exists before prepare, so the pending
					// record has an observed UID even if its confirmation fails.
					if _, err := Handle(ctx, cli, f.reload(), consts.HandleActionPublish, f.a); err != nil {
						t.Fatal(err)
					}
					existing := f.getA()
					existing.Data["setting"] = "before"
					if err := cli.Update(ctx, existing); err != nil {
						t.Fatal(err)
					}
				}
				fault := &partialInventoryStatusClient{Client: cli, target: target, loseReply: scenario == "confirmation_reply_lost"}
				if _, err := f.apply(fault, consts.HandleActionUpdate, f.a); !errors.Is(err, errInventoryCrash) || !fault.stopped {
					t.Fatalf("confirmation fault missed: %v", err)
				}
				saved, pending, _ := partialInventoryStatus(f.reload())
				if scenario == "confirmation_reply_lost" {
					if len(saved) != 1 || saved[0].ResourceUID != f.getA().UID || pending != nil {
						t.Fatal("persisted confirmation was lost together with its response")
					}
					f.withdraw()
					if _, err := f.apply(cli, consts.HandleActionUpdate); err != nil {
						t.Fatal(err)
					}
					if err := cli.Get(ctx, target, &corev1.ConfigMap{}); !apierrors.IsNotFound(err) {
						t.Fatalf("confirmed object leaked after response loss: %v", err)
					}
					return
				}
				if pending == nil || pending.ID == "" || pending.Resource.Name != "a" {
					t.Fatalf("crash left no identifiable pending status: %+v", pending)
				}
				a := f.getA()
				switch scenario {
				case "lost_token", "missing_source_marker":
					if scenario == "lost_token" {
						if a.Annotations[v1.AnnotationConfigurationWriteID] != pending.ID {
							t.Fatal("published target lacks the pending operation ID")
						}
						delete(a.Annotations, v1.AnnotationConfigurationWriteID)
					} else {
						delete(a.Annotations, v1.LabelConfigurations)
					}
					if err := cli.Update(ctx, a); err != nil {
						t.Fatal(err)
					}
				case "different_uid", "copied_create_token":
					if scenario == "copied_create_token" && pending.ObservedUID != "" {
						t.Fatal("create recorded a pre-existing UID")
					}
					if scenario == "different_uid" && pending.ObservedUID != a.UID {
						t.Fatalf("pending lost prior UID: %q != %q", pending.ObservedUID, a.UID)
					}
					if err := cli.Delete(ctx, a); err != nil {
						t.Fatal(err)
					}
					replacement := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: a.Name, Namespace: a.Namespace, Labels: a.Labels, Annotations: a.Annotations, OwnerReferences: a.OwnerReferences}, Data: a.Data}
					if err := cli.Create(ctx, replacement); err != nil {
						t.Fatal(err)
					}
					if replacement.UID == a.UID {
						t.Fatal("replacement kept original UID")
					}
					a = replacement
				}
				f.withdraw()
				_, err := f.apply(cli, consts.HandleActionUpdate)
				if scenario == "copied_create_token" || scenario == "missing_source_marker" || scenario == "lost_token" {
					if err == nil {
						t.Fatal("inconsistent live evidence should give a visible error")
					}
					if got := f.getA(); got.UID != a.UID || got.ResourceVersion != a.ResourceVersion {
						t.Fatal("invalid receipt permitted a target write")
					}
					saved, stillPending, _ := partialInventoryStatus(f.reload())
					if len(saved) != 0 || stillPending == nil || stillPending.ID != pending.ID {
						t.Fatal("invalid receipt was lost or promoted")
					}
					if scenario == "copied_create_token" {
						// Remove the copied token from the replacement and retry. Its
						// remaining owner/source annotations alone never authorize deletion.
						delete(a.Annotations, v1.AnnotationConfigurationWriteID)
					} else if scenario == "lost_token" {
						a.Annotations[v1.AnnotationConfigurationWriteID] = pending.ID
					} else {
						a.Annotations[v1.LabelConfigurations] = f.a.Name
					}
					if err := cli.Update(ctx, a); err != nil {
						t.Fatal(err)
					}
					_, err = f.apply(cli, consts.HandleActionUpdate)
				}
				if err != nil {
					t.Fatal(err)
				}
				records, pending, _ := partialInventoryStatus(f.reload())
				if pending != nil {
					t.Fatalf("recovery left pending status %+v (records=%+v)", pending, records)
				}
				if scenario == "recover" || scenario == "missing_source_marker" || scenario == "lost_token" {
					if err := cli.Get(ctx, target, &corev1.ConfigMap{}); !apierrors.IsNotFound(err) {
						t.Fatalf("recovery did not retire its committed target: %v", err)
					}
				} else if got := f.getA(); got.UID != a.UID || got.ResourceVersion != a.ResourceVersion {
					t.Fatal("unproven target was modified during restart")
				}
			})
		}
	}
}
