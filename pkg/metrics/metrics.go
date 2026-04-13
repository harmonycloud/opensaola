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
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

const (
	PhaseAPIRead     = "api_read"
	PhaseCompute     = "compute"
	PhaseAPIWrite    = "api_write"
	PhaseStatusWrite = "status_write"
)

func init() {
	metrics.Registry.MustRegister(
		ReconcileDuration, ReconcileTotal,
		ReconcileAPIReadDuration, ReconcileComputeDuration,
		ReconcileAPIWriteDuration, ReconcileStatusWriteDuration,
		ReconcileRequeueTotal, K8sConflictTotal, K8sAPIErrorsTotal,
		FinalizerBackfillTotal, LegacyDeleteTotal, LegacyDeleteDuration,
	)
}

// --- PR-01 metrics ---

var (
	ReconcileDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "opensaola_reconcile_duration_seconds",
			Help:    "Total wall-clock time of a single Reconcile call.",
			Buckets: []float64{0.005, 0.01, 0.02, 0.05, 0.1, 0.2, 0.5, 1, 2, 5, 10, 20, 30, 60},
		},
		[]string{"controller", "result"},
	)

	ReconcileTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "opensaola_reconcile_total",
			Help: "Total number of Reconcile calls.",
		},
		[]string{"controller", "result"},
	)
)

// --- PR-02 metrics ---

var (
	ReconcileAPIReadDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "opensaola_reconcile_api_read_duration_seconds",
			Help:    "Wall-clock time spent in API read (Get/List) during a Reconcile.",
			Buckets: []float64{0.001, 0.002, 0.005, 0.01, 0.02, 0.05, 0.1, 0.2, 0.5, 1, 2, 5, 10},
		},
		[]string{"controller", "result"},
	)
	ReconcileComputeDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "opensaola_reconcile_compute_duration_seconds",
			Help:    "Wall-clock time spent in compute (template/parse/diff) during a Reconcile.",
			Buckets: []float64{0.001, 0.002, 0.005, 0.01, 0.02, 0.05, 0.1, 0.2, 0.5, 1, 2, 5, 10, 20},
		},
		[]string{"controller", "result"},
	)
	ReconcileAPIWriteDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "opensaola_reconcile_api_write_duration_seconds",
			Help:    "Wall-clock time spent in API write (Create/Update/Patch/Delete) during a Reconcile.",
			Buckets: []float64{0.005, 0.01, 0.02, 0.05, 0.1, 0.2, 0.5, 1, 2, 5, 10, 20, 30},
		},
		[]string{"controller", "result"},
	)
	ReconcileStatusWriteDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "opensaola_reconcile_status_write_duration_seconds",
			Help:    "Wall-clock time spent in status write during a Reconcile.",
			Buckets: []float64{0.005, 0.01, 0.02, 0.05, 0.1, 0.2, 0.5, 1, 2, 5, 10, 20, 30},
		},
		[]string{"controller", "result"},
	)

	ReconcileRequeueTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "opensaola_reconcile_requeue_total",
			Help: "Total number of Reconcile requeue events.",
		},
		[]string{"controller", "result"},
	)
	K8sConflictTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "opensaola_k8s_conflict_total",
			Help: "Total number of 409 Conflict errors from Kubernetes API.",
		},
		[]string{"controller"},
	)
	K8sAPIErrorsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "opensaola_k8s_api_errors_total",
			Help: "Total number of Kubernetes API errors by type.",
		},
		[]string{"controller", "error_type"},
	)
)

// --- PR-03 metrics ---

var (
	FinalizerBackfillTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "opensaola_finalizer_backfill_total",
			Help: "Total number of finalizer backfill operations.",
		},
		[]string{"controller", "result"},
	)
	LegacyDeleteTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "opensaola_legacy_delete_total",
			Help: "Total number of legacy delete operations.",
		},
		[]string{"controller", "result"},
	)
	LegacyDeleteDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "opensaola_legacy_delete_duration_seconds",
			Help:    "Wall-clock time spent in legacy delete operations.",
			Buckets: []float64{0.005, 0.01, 0.02, 0.05, 0.1, 0.2, 0.5, 1, 2, 5, 10, 20, 30},
		},
		[]string{"controller", "result"},
	)
)

// ObserveFinalizerBackfill records a finalizer backfill metric.
func ObserveFinalizerBackfill(controller, result string) {
	FinalizerBackfillTotal.WithLabelValues(controller, result).Inc()
}

// ObserveLegacyDelete records a legacy delete metric.
func ObserveLegacyDelete(controller, result string, start time.Time) {
	LegacyDeleteTotal.WithLabelValues(controller, result).Inc()
	LegacyDeleteDuration.WithLabelValues(controller, result).Observe(time.Since(start).Seconds())
}

// ReconcileResult maps Reconcile return values to a result label value.
func ReconcileResult(requeue bool, requeueAfter time.Duration, err error) string {
	if err != nil {
		return "error"
	}
	if requeueAfter > 0 {
		return "requeue_after"
	}
	if requeue {
		return "requeue"
	}
	return "ok"
}

// ObserveReconcile records reconcile metrics.
func ObserveReconcile(controller string, startTime time.Time, requeue bool, requeueAfter time.Duration, err error) {
	result := ReconcileResult(requeue, requeueAfter, err)
	elapsed := time.Since(startTime).Seconds()
	ReconcileDuration.WithLabelValues(controller, result).Observe(elapsed)
	ReconcileTotal.WithLabelValues(controller, result).Inc()
}

// --- Error classification ---

// ClassifyError maps Kubernetes API errors to a low-cardinality error_type enum.
func ClassifyError(err error) string {
	if err == nil {
		return ""
	}
	switch {
	case apiErrors.IsConflict(err):
		return "conflict"
	case apiErrors.IsTooManyRequests(err):
		return "too_many_requests"
	case apiErrors.IsNotFound(err):
		return "not_found"
	case apiErrors.IsAlreadyExists(err):
		return "already_exists"
	case apiErrors.IsForbidden(err):
		return "forbidden"
	case apiErrors.IsUnauthorized(err):
		return "unauthorized"
	case apiErrors.IsTimeout(err), apiErrors.IsServerTimeout(err),
		err == context.DeadlineExceeded:
		return "timeout"
	case apiErrors.IsInvalid(err):
		return "invalid"
	case isConnectionError(err):
		return "connection"
	default:
		return "other"
	}
}

func isConnectionError(err error) bool {
	var netErr net.Error
	if ok := errors.As(err, &netErr); ok {
		return true
	}
	var opErr *net.OpError
	return errors.As(err, &opErr)
}

// ObserveAPIError records API error metrics (including a dedicated conflict counter).
func ObserveAPIError(controller string, err error) {
	if err == nil {
		return
	}
	errType := ClassifyError(err)
	K8sAPIErrorsTotal.WithLabelValues(controller, errType).Inc()
	if errType == "conflict" {
		K8sConflictTotal.WithLabelValues(controller).Inc()
	}
}

// --- ReconcileTimer: phased timer ---

type reconcileTimerKey struct{}

// ReconcileTimer accumulates the duration of each phase within a single Reconcile call.
type ReconcileTimer struct {
	controller string
	mu         sync.Mutex
	durations  map[string]time.Duration
}

// NewReconcileTimer creates a phased timer and injects it into the context.
func NewReconcileTimer(ctx context.Context, controller string) (context.Context, *ReconcileTimer) {
	t := &ReconcileTimer{
		controller: controller,
		durations:  make(map[string]time.Duration, 4),
	}
	return context.WithValue(ctx, reconcileTimerKey{}, t), t
}

// TimerFromContext retrieves the ReconcileTimer from context (may be nil).
func TimerFromContext(ctx context.Context) *ReconcileTimer {
	t, _ := ctx.Value(reconcileTimerKey{}).(*ReconcileTimer)
	return t
}

// Start begins timing a phase and returns a stop function. Can be called multiple times for the same phase (additive).
// nil-safe: returns a no-op when t is nil.
func (t *ReconcileTimer) Start(phase string) func() {
	if t == nil {
		return func() {}
	}
	start := time.Now()
	return func() {
		elapsed := time.Since(start)
		t.mu.Lock()
		t.durations[phase] += elapsed
		t.mu.Unlock()
	}
}

// Observe writes the accumulated phased durations to corresponding histograms.
// nil-safe: no-op when t is nil.
func (t *ReconcileTimer) Observe(result string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	phaseMetrics := map[string]*prometheus.HistogramVec{
		PhaseAPIRead:     ReconcileAPIReadDuration,
		PhaseCompute:     ReconcileComputeDuration,
		PhaseAPIWrite:    ReconcileAPIWriteDuration,
		PhaseStatusWrite: ReconcileStatusWriteDuration,
	}
	for phase, h := range phaseMetrics {
		if d, ok := t.durations[phase]; ok {
			h.WithLabelValues(t.controller, result).Observe(d.Seconds())
		}
	}
}

// ObserveRequeue records a requeue event.
func ObserveRequeue(controller string, requeue bool, requeueAfter time.Duration) {
	if requeue {
		ReconcileRequeueTotal.WithLabelValues(controller, "requeue").Inc()
	} else if requeueAfter > 0 {
		ReconcileRequeueTotal.WithLabelValues(controller, "requeue_after").Inc()
	}
}
