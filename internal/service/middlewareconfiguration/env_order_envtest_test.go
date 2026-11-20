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
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	"github.com/harmonycloud/opensaola/internal/service/consts"
	admissionv1 "k8s.io/api/admission/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func testEnvironmentOrder(t *testing.T, ctx context.Context, cli client.Client, writes *atomic.Int64) {
	const deployment = `apiVersion: apps/v1
kind: Deployment
metadata: {name: database}
spec:
  selector: {matchLabels: {app: database}}
  template:
    metadata: {labels: {app: database}}
    spec:
      containers:
      - name: db-client
        image: registry.k8s.io/pause:3.10
        env: [%s]
`
	host := corev1.EnvVar{Name: "DB_HOST", Value: "private-test-host"}
	endpoint := corev1.EnvVar{Name: "ENDPOINT", Value: "$(DB_HOST):5432"}
	trace := corev1.EnvVar{Name: "TRACE_ID", Value: "keep-me"}
	template := func(env ...corev1.EnvVar) string {
		items := make([]string, 0, len(env))
		for _, item := range env {
			items = append(items, fmt.Sprintf("{name: %q, value: %q}", item.Name, item.Value))
		}
		return fmt.Sprintf(deployment, strings.Join(items, ", "))
	}
	fixture := func(t *testing.T, initial string) (*v1.MiddlewareOperator, *v1.MiddlewareConfiguration) {
		t.Helper()
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "env-order-"}}
		if err := cli.Create(ctx, ns); err != nil {
			t.Fatal(err)
		}
		owner := &v1.MiddlewareOperator{ObjectMeta: metav1.ObjectMeta{Name: "operator", Namespace: ns.Name}}
		if err := cli.Create(ctx, owner); err != nil {
			t.Fatal(err)
		}
		cfg := &v1.MiddlewareConfiguration{ObjectMeta: metav1.ObjectMeta{Name: ns.Name}}
		cfg.Spec.Template = initial
		if err := cli.Create(ctx, cfg); err != nil {
			t.Fatal(err)
		}
		return owner, cfg
	}
	assertLive := func(t *testing.T, ns string, want ...corev1.EnvVar) {
		t.Helper()
		var live appsv1.Deployment
		if err := cli.Get(ctx, client.ObjectKey{Namespace: ns, Name: "database"}, &live); err != nil {
			t.Fatal(err)
		}
		containers := live.Spec.Template.Spec.Containers
		if len(containers) != 1 || containers[0].Name != "db-client" || !slices.Equal(containers[0].Env, want) {
			t.Fatalf("unexpected container env: got %+v, want %+v", containers, want)
		}
	}

	t.Run("reorder_slots_with_external_injection", func(t *testing.T) {
		owner, cfg := fixture(t, template(endpoint, host))
		installConfigurationMutationWebhook(t, ctx, cli, owner.Namespace, cfg.Spec.Template,
			`[{"op":"add","path":"/spec/template/spec/containers/0/env/1","value":{"name":"TRACE_ID","value":"keep-me"}}]`)
		if _, err := Handle(ctx, cli, owner, consts.HandleActionPublish, cfg); err != nil {
			t.Fatal(err)
		}
		assertLive(t, owner.Namespace, endpoint, trace, host)

		cfg.Spec.Template = template(host, endpoint)
		if _, err := Handle(ctx, cli, owner, consts.HandleActionUpdate, cfg); err != nil {
			t.Fatal(err)
		}
		assertLive(t, owner.Namespace, host, trace, endpoint)

		updatedHost := corev1.EnvVar{Name: host.Name, Value: "new-db"}
		cfg.Spec.Template = template(updatedHost, endpoint)
		if _, err := Handle(ctx, cli, owner, consts.HandleActionUpdate, cfg); err != nil {
			t.Fatal(err)
		}
		assertLive(t, owner.Namespace, updatedHost, trace, endpoint)

		beforeReplay := writes.Load()
		if _, err := Handle(ctx, cli, owner, consts.HandleActionUpdate, cfg); err != nil {
			t.Fatal(err)
		}
		if writes.Load() != beforeReplay {
			t.Fatal("already ordered environment caused a write on replay")
		}
	})
	for _, tc := range []struct {
		name          string
		operation     admissionv1.Operation
		rewriteDryRun bool
	}{
		{"preflight_create_reversal", admissionv1.Create, true},
		{"real_create_response_reversal", admissionv1.Create, false},
		{"dry_run_update_reversal", admissionv1.Update, true},
		{"real_update_response_reversal", admissionv1.Update, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			initial := template(host, endpoint)
			if tc.operation == admissionv1.Update {
				initial = template(endpoint, host)
			}
			owner, cfg := fixture(t, initial)
			var mutations atomic.Int32
			var reverse atomic.Bool
			calls := installConfigurationWebhook(t, ctx, cli, owner.Namespace, cfg.Spec.Template, func(req *admissionv1.AdmissionRequest) string {
				dryRun := req.DryRun != nil && *req.DryRun
				if reverse.Load() && req.Operation == tc.operation && dryRun == tc.rewriteDryRun {
					mutations.Add(1)
					return `[{"op":"move","from":"/spec/template/spec/containers/0/env/0","path":"/spec/template/spec/containers/0/env/1"}]`
				}
				return ""
			})
			action := consts.HandleActionPublish
			if tc.operation == admissionv1.Update {
				if _, err := Handle(ctx, cli, owner, consts.HandleActionPublish, cfg); err != nil {
					t.Fatal(err)
				}
				cfg.Spec.Template = template(host, endpoint)
				action = consts.HandleActionUpdate
			}
			reverse.Store(true)
			result, err := Handle(ctx, cli, owner, action, cfg)
			if err == nil || result != nil || calls.Load() == 0 || mutations.Load() == 0 {
				t.Fatalf("expected failed publication, got result=%v err=%v dryRuns=%d mutations=%d", result, err, calls.Load(), mutations.Load())
			}
			wantDiagnostic := `.spec.template.spec.containers[name="db-client"].env: expected order ["DB_HOST" "ENDPOINT"], observed ["ENDPOINT" "DB_HOST"]`
			if !strings.Contains(err.Error(), wantDiagnostic) {
				t.Fatalf("wanted %q in publication error, got %v", wantDiagnostic, err)
			}
			if strings.Contains(err.Error(), host.Value) || strings.Contains(err.Error(), endpoint.Value) {
				t.Fatalf("order diagnostic exposed variable values: %v", err)
			}
			if tc.operation == admissionv1.Create && tc.rewriteDryRun {
				if err := cli.Get(ctx, client.ObjectKey{Namespace: owner.Namespace, Name: "database"}, &appsv1.Deployment{}); !apierrors.IsNotFound(err) {
					t.Fatalf("preflight rejection should prevent creation, got %v", err)
				}
			} else {
				assertLive(t, owner.Namespace, endpoint, host)
			}
			reverse.Store(false)
			result, err = Handle(ctx, cli, owner, action, cfg)
			if err != nil || result == nil || result.inventory == nil {
				t.Fatalf("retry after removing the admission mutation: result=%v err=%v", result, err)
			}
			assertLive(t, owner.Namespace, host, endpoint)
		})
	}
}
