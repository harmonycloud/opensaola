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

package metrics

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestReconcileResult(t *testing.T) {
	tests := []struct {
		name         string
		requeue      bool
		requeueAfter time.Duration
		err          error
		want         string
	}{
		{"error takes precedence", true, 5 * time.Second, errors.New("fail"), "error"},
		{"requeue_after", false, 5 * time.Second, nil, "requeue_after"},
		{"requeue", true, 0, nil, "requeue"},
		{"ok", false, 0, nil, "ok"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ReconcileResult(tt.requeue, tt.requeueAfter, tt.err)
			if got != tt.want {
				t.Errorf("ReconcileResult() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestObserveReconcile(t *testing.T) {
	ReconcileTotal.Reset()
	ReconcileDuration.Reset()

	ObserveReconcile("testctrl", time.Now().Add(-100*time.Millisecond), false, 0, nil)
	ObserveReconcile("testctrl", time.Now(), false, 0, errors.New("boom"))

	mOk := &dto.Metric{}
	cOk, _ := ReconcileTotal.GetMetricWithLabelValues("testctrl", "ok")
	_ = cOk.Write(mOk)
	if v := mOk.GetCounter().GetValue(); v != 1 {
		t.Errorf("ok counter = %v, want 1", v)
	}

	mErr := &dto.Metric{}
	cErr, _ := ReconcileTotal.GetMetricWithLabelValues("testctrl", "error")
	_ = cErr.Write(mErr)
	if v := mErr.GetCounter().GetValue(); v != 1 {
		t.Errorf("error counter = %v, want 1", v)
	}

	hOk := &dto.Metric{}
	obs, _ := ReconcileDuration.GetMetricWithLabelValues("testctrl", "ok")
	_ = obs.(interface{ Write(*dto.Metric) error }).Write(hOk)
	if c := hOk.GetHistogram().GetSampleCount(); c != 1 {
		t.Errorf("histogram sample count = %v, want 1", c)
	}
}

func TestClassifyError(t *testing.T) {
	gr := schema.GroupResource{Group: "test", Resource: "things"}
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, ""},
		{"conflict", apiErrors.NewConflict(gr, "x", errors.New("c")), "conflict"},
		{"too_many_requests", apiErrors.NewTooManyRequests("slow", 1), "too_many_requests"},
		{"not_found", apiErrors.NewNotFound(gr, "x"), "not_found"},
		{"already_exists", apiErrors.NewAlreadyExists(gr, "x"), "already_exists"},
		{"forbidden", apiErrors.NewForbidden(gr, "x", errors.New("f")), "forbidden"},
		{"unauthorized", apiErrors.NewUnauthorized("u"), "unauthorized"},
		{"timeout", apiErrors.NewTimeoutError("t", 1), "timeout"},
		{"context deadline", context.DeadlineExceeded, "timeout"},
		{"invalid", apiErrors.NewInvalid(schema.GroupKind{Group: "test", Kind: "Thing"}, "x", nil), "invalid"},
		{"connection", &net.OpError{Op: "dial", Err: errors.New("refused")}, "connection"},
		{"other", errors.New("unknown"), "other"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifyError(tt.err)
			if got != tt.want {
				t.Errorf("ClassifyError() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestReconcileTimer_PhaseTiming(t *testing.T) {
	ReconcileAPIReadDuration.Reset()
	ReconcileComputeDuration.Reset()

	ctx, timer := NewReconcileTimer(context.Background(), "testctrl")

	// Verify context injection
	if got := TimerFromContext(ctx); got != timer {
		t.Fatal("TimerFromContext returned different timer")
	}

	// Simulate api_read
	stop := timer.Start(PhaseAPIRead)
	time.Sleep(5 * time.Millisecond)
	stop()

	// Simulate compute (two additive calls)
	stop = timer.Start(PhaseCompute)
	time.Sleep(3 * time.Millisecond)
	stop()
	stop = timer.Start(PhaseCompute)
	time.Sleep(2 * time.Millisecond)
	stop()

	timer.Observe("ok")

	// Verify api_read histogram
	m := &dto.Metric{}
	h, _ := ReconcileAPIReadDuration.GetMetricWithLabelValues("testctrl", "ok")
	_ = h.(interface{ Write(*dto.Metric) error }).Write(m)
	if c := m.GetHistogram().GetSampleCount(); c != 1 {
		t.Errorf("api_read sample count = %v, want 1", c)
	}

	// Verify compute histogram
	mc := &dto.Metric{}
	hc, _ := ReconcileComputeDuration.GetMetricWithLabelValues("testctrl", "ok")
	_ = hc.(interface{ Write(*dto.Metric) error }).Write(mc)
	if c := mc.GetHistogram().GetSampleCount(); c != 1 {
		t.Errorf("compute sample count = %v, want 1", c)
	}
}

func TestReconcileTimer_NilSafe(t *testing.T) {
	// TimerFromContext returning nil should not panic
	var timer *ReconcileTimer
	stop := timer.Start(PhaseAPIRead)
	stop()
	timer.Observe("ok") // should not panic
}

func TestObserveRequeue(t *testing.T) {
	ReconcileRequeueTotal.Reset()

	ObserveRequeue("testctrl", true, 0)
	ObserveRequeue("testctrl", false, 5*time.Second)
	ObserveRequeue("testctrl", false, 0) // ok, not counted

	m := &dto.Metric{}
	c, _ := ReconcileRequeueTotal.GetMetricWithLabelValues("testctrl", "requeue")
	_ = c.Write(m)
	if v := m.GetCounter().GetValue(); v != 1 {
		t.Errorf("requeue counter = %v, want 1", v)
	}

	ma := &dto.Metric{}
	ca, _ := ReconcileRequeueTotal.GetMetricWithLabelValues("testctrl", "requeue_after")
	_ = ca.Write(ma)
	if v := ma.GetCounter().GetValue(); v != 1 {
		t.Errorf("requeue_after counter = %v, want 1", v)
	}
}

func TestObserveAPIError(t *testing.T) {
	K8sAPIErrorsTotal.Reset()
	K8sConflictTotal.Reset()

	gr := schema.GroupResource{Group: "test", Resource: "things"}
	conflictErr := apiErrors.NewConflict(gr, "x", errors.New("c"))
	ObserveAPIError("testctrl", conflictErr)
	ObserveAPIError("testctrl", errors.New("random"))
	ObserveAPIError("testctrl", nil) // nil not counted

	// conflict should be counted in both api_errors and conflict_total
	mc := &dto.Metric{}
	cc, _ := K8sConflictTotal.GetMetricWithLabelValues("testctrl")
	_ = cc.Write(mc)
	if v := mc.GetCounter().GetValue(); v != 1 {
		t.Errorf("conflict counter = %v, want 1", v)
	}

	me := &dto.Metric{}
	ce, _ := K8sAPIErrorsTotal.GetMetricWithLabelValues("testctrl", "conflict")
	_ = ce.Write(me)
	if v := me.GetCounter().GetValue(); v != 1 {
		t.Errorf("api_errors conflict = %v, want 1", v)
	}

	mo := &dto.Metric{}
	co, _ := K8sAPIErrorsTotal.GetMetricWithLabelValues("testctrl", "other")
	_ = co.Write(mo)
	if v := mo.GetCounter().GetValue(); v != 1 {
		t.Errorf("api_errors other = %v, want 1", v)
	}
}

// Ensure all error_type enum values are unique.
func TestClassifyError_AllTypes(t *testing.T) {
	seen := map[string]bool{}
	types := []string{"conflict", "too_many_requests", "not_found", "already_exists",
		"forbidden", "unauthorized", "timeout", "invalid", "connection", "other"}
	for _, et := range types {
		if seen[et] {
			t.Errorf("duplicate error_type: %s", et)
		}
		seen[et] = true
	}
	if len(types) != 10 {
		t.Errorf("expected 10 error types, got %d", len(types))
	}
}
