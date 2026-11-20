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

package controller

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	"github.com/harmonycloud/opensaola/internal/k8s"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	controllerconfig "sigs.k8s.io/controller-runtime/pkg/config"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

type actionFixture struct {
	client client.WithWatch
	req    ctrl.Request
}

func newActionFixture(t *testing.T, steps []v1.Step) actionFixture {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := v1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	api, err := client.NewWithWatch(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	baseline := &v1.MiddlewareActionBaseline{
		ObjectMeta: metav1.ObjectMeta{GenerateName: "action-test-"},
		Spec:       v1.MiddlewareActionBaselineSpec{BaselineType: v1.WorkflowNormalAction, ActionType: "test", Steps: steps},
	}
	if err := api.Create(t.Context(), baseline); err != nil {
		t.Fatal(err)
	}
	action := &v1.MiddlewareAction{
		ObjectMeta: metav1.ObjectMeta{GenerateName: "action-test-", Namespace: "default"},
		Spec:       v1.MiddlewareActionSpec{Baseline: baseline.Name, MiddlewareName: "test"},
	}
	if err := api.Create(t.Context(), action); err != nil {
		t.Fatal(err)
	}
	return actionFixture{api, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(action)}}
}

func (f actionFixture) reconcile(t *testing.T, api client.Client) error {
	t.Helper()
	r := MiddlewareActionReconciler{Client: api, Recorder: record.NewFakeRecorder(10)}
	_, err := r.Reconcile(t.Context(), f.req)
	return err
}

func (f actionFixture) read(t *testing.T) *v1.MiddlewareAction {
	t.Helper()
	var action v1.MiddlewareAction
	if err := f.client.Get(t.Context(), f.req.NamespacedName, &action); err != nil {
		t.Fatal(err)
	}
	return &action
}

func TestActionHTTPReceipts(t *testing.T) {
	for _, code := range []int{200, 204, 299, 300, 404, 503} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			var first, next atomic.Int32
			handler := http.NewServeMux()
			handler.HandleFunc("/first", func(w http.ResponseWriter, _ *http.Request) { first.Add(1); w.WriteHeader(code) })
			handler.HandleFunc("/next", func(http.ResponseWriter, *http.Request) { next.Add(1) })
			server := httptest.NewServer(handler)
			defer server.Close()
			f := newActionFixture(t, []v1.Step{
				{Name: "first", HTTP: v1.Http{Method: "POST", URL: server.URL + "/first?token=private-value"}},
				{Name: "next", HTTP: v1.Http{Method: "POST", URL: server.URL + "/next"}},
			})
			success := code >= 200 && code < 300
			err := f.reconcile(t, f.client)
			if (err == nil) != success {
				t.Fatalf("HTTP %d returned %v", code, err)
			}
			if err := f.reconcile(t, f.client); err != nil {
				t.Fatal(err)
			}
			got := f.read(t).Status
			receipt := meta.FindStatusCondition(got.Conditions, "STEP-first")
			if receipt == nil || !strings.Contains(receipt.Message, fmt.Sprintf("status=%d", code)) || strings.Contains(fmt.Sprint(got), "private-value") {
				t.Fatalf("unexpected receipt: %+v", got)
			}
			if first.Load() != 1 {
				t.Fatalf("first received %d requests", first.Load())
			}
			if success {
				if next.Load() != 1 || got.State != v1.StateAvailable || receipt.Status != metav1.ConditionTrue || !meta.IsStatusConditionTrue(got.Conditions, "STEP-next") {
					t.Fatalf("next=%d, status=%+v", next.Load(), got)
				}
			} else if next.Load() != 0 || got.State != v1.StateUnavailable || receipt.Status != metav1.ConditionFalse || meta.FindStatusCondition(got.Conditions, "STEP-next") != nil {
				t.Fatalf("next=%d, status=%+v", next.Load(), got)
			}
		})
	}
}

func TestActionKubectlEditTarget(t *testing.T) {
	resolver, err := k8s.NewIntentSchemaResolver(cfg)
	if err != nil {
		t.Fatal(err)
	}
	k8s.ConfigureResourceIntents(resolver)
	t.Cleanup(func() { k8s.ConfigureResourceIntents(nil) })
	const deployment = `{spec: {selector: {matchLabels: {app: "fixture"}}, template: {metadata: {labels: {app: "fixture"}}, spec: {containers: [{name: "fixture", image: "fixture"}]}}}}`
	for _, tc := range []struct {
		name, version, kind, output string
		targetLabels                string
		existing, success, present  bool
	}{
		{name: "config-data", version: "v1", kind: "ConfigMap", output: `{data: {mode: "on"}}`, success: true, present: true},
		{name: "update-config", version: "v1", kind: "ConfigMap", output: `{data: {mode: "on"}}`, existing: true, success: true, present: true},
		{name: "empty-data", version: "v1", kind: "ConfigMap", output: "{data: {}}", success: true, present: true},
		{name: "invalid-data", version: "v1", kind: "ConfigMap", output: "{data: {mode: true}}"},
		{name: "secret-string-data", version: "v1", kind: "Secret", output: `{stringData: {mode: "on"}}`, success: true, present: true},
		{name: "identity-only", version: "v1", kind: "ConfigMap", output: `{apiVersion:"v1",kind:"ConfigMap",metadata:{namespace:"default"}}`, success: true},
		{name: "changed-identity", version: "v1", kind: "ConfigMap", output: `{metadata:{name:"different"},data:{mode:"on"}}`},
		{name: "empty-output", version: "v1", kind: "ConfigMap", output: "{}", success: true},
		{name: "label-only", version: "v1", kind: "ConfigMap", output: "{}", targetLabels: `, labels: {app: "fixture"}`},
		{name: "annotation-only", version: "v1", kind: "ConfigMap", output: `{metadata: {annotations: {app: "fixture"}}}`},
		{name: "status-only", version: "apps/v1", kind: "Deployment", output: "{status: {replicas: 1}}"},
		{name: "empty-spec", version: "apps/v1", kind: "Deployment", output: "{spec: {}}"},
		{name: "deployment", version: "apps/v1", kind: "Deployment", output: deployment, success: true, present: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var next atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { next.Add(1) }))
			defer server.Close()
			name := fmt.Sprintf("edit-%s-%x", tc.name, time.Now().UnixNano())
			cue := fmt.Sprintf("parameters: resource: {apiversion: %q, kind: %q, namespace: %q, name: %q%s}\noutput: %s", tc.version, tc.kind, "default", name, tc.targetLabels, tc.output)
			f := newActionFixture(t, []v1.Step{
				{Name: "edit", Type: "KubectlEdit", CUE: cue},
				{Name: "next", HTTP: v1.Http{Method: "POST", URL: server.URL}},
			})
			obj := &unstructured.Unstructured{}
			obj.SetAPIVersion(tc.version)
			obj.SetKind(tc.kind)
			obj.SetNamespace("default")
			obj.SetName(name)
			if tc.existing {
				obj.Object["data"] = map[string]any{"mode": "off", "keep": "external"}
				if err := f.client.Create(t.Context(), obj); err != nil {
					t.Fatal(err)
				}
			}
			err := f.reconcile(t, f.client)
			status := f.read(t).Status
			if tc.success {
				if err != nil || status.State != v1.StateAvailable || !meta.IsStatusConditionTrue(status.Conditions, "STEP-edit") || next.Load() != 1 {
					t.Fatalf("err=%v status=%+v next=%d", err, status, next.Load())
				}
			} else if err == nil || status.State != v1.StateUnavailable || !meta.IsStatusConditionFalse(status.Conditions, "STEP-edit") || next.Load() != 0 {
				t.Fatalf("err=%v status=%+v next=%d", err, status, next.Load())
			}
			if err := f.client.Get(t.Context(), client.ObjectKeyFromObject(obj), obj); !tc.present {
				if !apierrors.IsNotFound(err) {
					t.Fatalf("expected missing target, got %v", err)
				}
				return
			} else if err != nil {
				t.Fatal(err)
			}
			data, _, err := unstructured.NestedStringMap(obj.Object, "data")
			if err != nil {
				t.Fatal(err)
			}
			switch tc.name {
			case "update-config", "config-data":
				if data["mode"] != "on" || tc.existing && data["keep"] != "external" {
					t.Fatalf("unexpected ConfigMap data %v", data)
				}
			case "secret-string-data":
				if data["mode"] != "b24=" {
					t.Fatal("Secret did not store stringData")
				}
			}
		})
	}
}

func TestActionCMDPreparationRetry(t *testing.T) {
	output := filepath.Join(t.TempDir(), "commands")
	cmd := func(line string) v1.CMD { return v1.CMD{Command: []string{fmt.Sprintf("echo %s >> %q", line, output)}} }
	f := newActionFixture(t, []v1.Step{{Name: "first", CMD: cmd("first")}, {Name: "next", CMD: cmd("next")}})
	unavailable := errors.New("injected temporary baseline read failure")
	api := interceptor.NewClient(f.client, interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*v1.MiddlewareActionBaseline); ok {
				return unavailable
			}
			return c.Get(ctx, key, obj, opts...)
		},
	})
	if err := f.reconcile(t, api); !errors.Is(err, unavailable) || f.read(t).Status.State != "" {
		t.Fatalf("expected retryable preparation error, got %v, status %+v", err, f.read(t).Status)
	}
	if _, err := os.Stat(output); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("CMD ran before preparation completed: %v", err)
	}
	for i := 0; i < 2; i++ {
		if err := f.reconcile(t, f.client); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(output)
	if err != nil || string(got) != "first\nnext\n" || f.read(t).Status.State != v1.StateAvailable {
		t.Fatalf("commands=%q, err=%v, status=%+v", got, err, f.read(t).Status)
	}
}

func TestActionCMDExitFailure(t *testing.T) {
	f := newActionFixture(t, []v1.Step{{Name: "first", CMD: v1.CMD{Command: []string{"exit 7"}}}, {Name: "next", CMD: v1.CMD{Command: []string{"true"}}}})
	if err := f.reconcile(t, f.client); err == nil {
		t.Fatal("command with exit code 7 was accepted")
	}
	got := f.read(t).Status
	if !strings.Contains(string(got.Reason), "exit status 7") || got.State != v1.StateUnavailable || !meta.IsStatusConditionFalse(got.Conditions, "STEP-first") || meta.FindStatusCondition(got.Conditions, "STEP-next") != nil {
		t.Fatalf("unexpected status: %+v", got)
	}
}

func TestActionLostHTTPResponse(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			t.Error("HTTP/1 test server must support hijacking")
			return
		}
		conn, _, err := hijacker.Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		_ = conn.Close()
	}))
	defer server.Close()
	f := newActionFixture(t, []v1.Step{
		{Name: "switch", HTTP: v1.Http{Method: "POST", URL: server.URL}},
		{Name: "next", HTTP: v1.Http{Method: "POST", URL: server.URL}},
	})
	if err := f.reconcile(t, f.client); err == nil {
		t.Fatal("missing response was accepted")
	}
	if err := f.reconcile(t, f.client); err != nil {
		t.Fatal(err)
	}
	got := f.read(t).Status
	c := meta.FindStatusCondition(got.Conditions, "STEP-switch")
	if requests.Load() != 1 || got.State != v1.StateUnavailable || c == nil || c.Status != metav1.ConditionUnknown || c.Reason != "ExecutionOutcomeUnknown" {
		t.Fatalf("requests=%d, status=%+v", requests.Load(), got)
	}
}

// Interrupt status writes on both sides of dispatch and observe the next reconciliation.
func TestActionStatusWriteRecovery(t *testing.T) {
	for _, tc := range []struct {
		name      string
		interrupt v1.State
		ackLost   bool
		want      v1.State
	}{
		{"before-dispatch", v1.StateUpdating, false, v1.StateAvailable},
		{"after-dispatch", v1.StateAvailable, false, v1.StateUnavailable},
		{"terminal-ack-lost", v1.StateAvailable, true, v1.StateAvailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
			defer server.Close()
			f := newActionFixture(t, []v1.Step{{Name: "only", HTTP: v1.Http{Method: "POST", URL: server.URL}}})
			interrupted := errors.New("interrupted status write")
			api := interceptor.NewClient(f.client, interceptor.Funcs{
				SubResourceUpdate: func(ctx context.Context, c client.Client, sub string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
					action, ok := obj.(*v1.MiddlewareAction)
					if ok && sub == "status" && action.Status.State == tc.interrupt {
						if tc.ackLost {
							if err := c.SubResource(sub).Update(ctx, obj, opts...); err != nil {
								return err
							}
						}
						return interrupted
					}
					return c.SubResource(sub).Update(ctx, obj, opts...)
				},
			})
			if err := f.reconcile(t, api); !errors.Is(err, interrupted) {
				t.Fatalf("expected injected interruption, got %v", err)
			}
			if tc.interrupt == v1.StateUpdating && requests.Load() != 0 {
				t.Fatal("request dispatched before start was recorded")
			}
			recoveryErr := f.reconcile(t, f.client)
			if (recoveryErr != nil) != (tc.want == v1.StateUnavailable) {
				t.Fatalf("recovery returned %v", recoveryErr)
			}
			if err := f.reconcile(t, f.client); err != nil {
				t.Fatal(err)
			}
			got := f.read(t).Status
			if got.State != tc.want || requests.Load() != 1 {
				t.Fatalf("requests=%d, status=%+v", requests.Load(), got)
			}
			if tc.want == v1.StateUnavailable {
				c := meta.FindStatusCondition(got.Conditions, v1.CondTypeExecuteAction)
				if c == nil || c.Reason != "ExecutionOutcomeUnknown" || c.Status != metav1.ConditionUnknown {
					t.Fatalf("expected unknown execution outcome, got %+v", got)
				}
			}
		})
	}
}

func TestActionBaselineStepIdentity(t *testing.T) {
	for _, tc := range []struct {
		name  string
		names []string
		valid bool
	}{
		{"empty", []string{}, true},
		{"duplicate", []string{"same", "same"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
			defer srv.Close()
			steps := make([]v1.Step, 0, len(tc.names))
			for _, name := range tc.names {
				steps = append(steps, v1.Step{Name: name, HTTP: v1.Http{Method: "POST", URL: srv.URL}})
			}
			f := newActionFixture(t, steps)
			baselineReq := ctrl.Request{NamespacedName: client.ObjectKey{Name: f.read(t).Spec.Baseline}}
			br := MiddlewareActionBaselineReconciler{Client: f.client, Recorder: record.NewFakeRecorder(10)}
			if _, err := br.Reconcile(t.Context(), baselineReq); err != nil {
				t.Fatal(err)
			}
			var baseline v1.MiddlewareActionBaseline
			if err := f.client.Get(t.Context(), baselineReq.NamespacedName, &baseline); err != nil {
				t.Fatal(err)
			}
			actionErr := f.reconcile(t, f.client)
			got := f.read(t).Status
			if tc.valid {
				if actionErr != nil || baseline.Status.State != v1.StateAvailable || got.State != v1.StateAvailable || !meta.IsStatusConditionTrue(got.Conditions, v1.CondTypeChecked) {
					t.Fatalf("empty action: err=%v, baseline=%+v, action=%+v", actionErr, baseline.Status, got)
				}
			} else if actionErr == nil || baseline.Status.State != v1.StateUnavailable || !meta.IsStatusConditionFalse(baseline.Status.Conditions, v1.CondTypeChecked) || got.State != v1.StateUnavailable || !strings.Contains(string(got.Reason), "duplicate step name") {
				t.Fatalf("duplicate steps: err=%v, baseline=%+v, action=%+v", actionErr, baseline.Status, got)
			}
			if calls.Load() != 0 {
				t.Fatalf("unexpectedly executed %d requests", calls.Load())
			}
		})
	}
}

func TestActionStatusConflicts(t *testing.T) {
	for _, tc := range []struct {
		name    string
		replace bool
	}{{"metadata-edit", false}, {"replacement-action", true}} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
			defer server.Close()
			f := newActionFixture(t, []v1.Step{{Name: "once", HTTP: v1.Http{Method: "POST", URL: server.URL}}})
			initial := f.read(t)
			modified := false
			api := interceptor.NewClient(f.client, interceptor.Funcs{
				SubResourceUpdate: func(ctx context.Context, c client.Client, sub string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
					action, ok := obj.(*v1.MiddlewareAction)
					if ok && !modified && sub == "status" && action.Status.State == v1.StateAvailable {
						modified = true
						fresh := f.read(t)
						if tc.replace {
							if err := c.Delete(ctx, fresh); err != nil {
								return err
							}
							replacement := &v1.MiddlewareAction{ObjectMeta: metav1.ObjectMeta{Name: fresh.Name, Namespace: fresh.Namespace}, Spec: fresh.Spec}
							if err := c.Create(ctx, replacement); err != nil {
								return err
							}
						} else {
							fresh.Annotations = map[string]string{"observer": "present"}
							if err := c.Update(ctx, fresh); err != nil {
								return err
							}
						}
					}
					return c.SubResource(sub).Update(ctx, obj, opts...)
				},
			})
			err := f.reconcile(t, api)
			got := f.read(t)
			if requests.Load() != 1 || !modified {
				t.Fatalf("requests=%d, modified=%t", requests.Load(), modified)
			}
			if tc.replace {
				if err == nil || got.UID == initial.UID || got.Status.State != "" || len(got.Status.Conditions) != 0 {
					t.Fatalf("old execution affected replacement: err=%v, status=%+v", err, got.Status)
				}
			} else if err != nil || got.UID != initial.UID || got.Status.State != v1.StateAvailable || got.Annotations["observer"] != "present" {
				t.Fatalf("metadata-only conflict lost receipt: err=%v, action=%+v", err, got)
			}
		})
	}
}

// Exercise workqueue retry and informer reads with no manual second reconcile.
func TestActionManagerRetriesPreparation(t *testing.T) {
	var calls, reads atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer srv.Close()
	f := newActionFixture(t, []v1.Step{{Name: "only", HTTP: v1.Http{Method: "POST", URL: srv.URL}}})
	scheme := f.client.Scheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme: scheme, Metrics: metricsserver.Options{BindAddress: "0"}, HealthProbeBindAddress: "0",
		Controller: controllerconfig.Controller{SkipNameValidation: ptr.To(true)},
		Cache: cache.Options{ByObject: map[client.Object]cache.ByObject{
			&v1.MiddlewareAction{}: {Field: fields.OneTermEqualSelector("metadata.name", f.req.Name)},
		}},
		NewClient: func(config *rest.Config, opts client.Options) (client.Client, error) {
			cached, err := client.NewWithWatch(config, opts)
			if err != nil {
				return nil, err
			}
			return interceptor.NewClient(cached, interceptor.Funcs{
				Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					if _, ok := obj.(*v1.MiddlewareActionBaseline); ok && reads.Add(1) == 1 {
						return errors.New("first baseline read unavailable")
					}
					return c.Get(ctx, key, obj, opts...)
				},
			}), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	r := MiddlewareActionReconciler{Client: mgr.GetClient(), Scheme: scheme, Recorder: record.NewFakeRecorder(10)}
	if err := r.SetupWithManager(mgr); err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- mgr.Start(ctx) }()
	defer func() {
		stop()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(10 * time.Second):
			t.Error("manager shutdown timed out")
		}
	}()
	err = wait.PollUntilContextTimeout(ctx, 50*time.Millisecond, 15*time.Second, true, func(context.Context) (bool, error) {
		got := f.read(t).Status
		if got.State == v1.StateUnavailable {
			return false, fmt.Errorf("unexpected terminal state: %+v", got)
		}
		return got.State == v1.StateAvailable, nil
	})
	if err != nil || reads.Load() < 2 || calls.Load() != 1 {
		t.Fatalf("err=%v, reads=%d, requests=%d", err, reads.Load(), calls.Load())
	}
}

func TestActionLegacyReceipts(t *testing.T) {
	for _, tc := range []struct {
		name  string
		state v1.State
		want  v1.State
	}{{"partial", "", v1.StateUnavailable}, {"completed", v1.StateAvailable, v1.StateAvailable}, {"failed", v1.StateUnavailable, v1.StateUnavailable}} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
			defer server.Close()
			f := newActionFixture(t, []v1.Step{{Name: "first", HTTP: v1.Http{Method: "POST", URL: server.URL}}, {Name: "second", HTTP: v1.Http{Method: "POST", URL: server.URL}}})
			action := f.read(t)
			action.Status.State = tc.state
			meta.SetStatusCondition(&action.Status.Conditions, metav1.Condition{Type: "STEP-first", Status: metav1.ConditionTrue, Reason: "ExecuteActionSuccess", ObservedGeneration: action.Generation})
			if err := f.client.Status().Update(t.Context(), action); err != nil {
				t.Fatal(err)
			}
			err := f.reconcile(t, f.client)
			got := f.read(t)
			if got.Status.State != tc.want || calls.Load() != 0 {
				t.Fatalf("requests=%d, status=%+v", calls.Load(), got.Status)
			}
			if tc.state == "" {
				c := meta.FindStatusCondition(got.Status.Conditions, v1.CondTypeExecuteAction)
				if err == nil || c == nil || c.Reason != "ExecutionOutcomeUnknown" {
					t.Fatalf("expected uncertain outcome, got %v, %+v", err, got.Status)
				}
			} else if err != nil || got.ResourceVersion != action.ResourceVersion {
				t.Fatalf("terminal Action changed: %v, %+v", err, got.Status)
			}
		})
	}
}
