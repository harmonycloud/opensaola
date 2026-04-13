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

package synchronizer

// debouncer.go implements a per-Middleware debounce mechanism for informer event-driven status updates.
//
// When a rolling update triggers many Pod events in rapid succession, a plain event handler
// would cause a status write storm. This file provides:
//   - Debouncer     — per-Middleware debounce with configurable window and forced-fire ceiling.
//   - NsDebounceRegistry — namespace-scoped registry that manages all Debouncers.
//   - Package-level singleton helpers for easy use from informer EventHandlers.

import (
	"sync"
	"time"

	"github.com/spf13/viper"
	ctrl "sigs.k8s.io/controller-runtime"
)

// defaultDebounceWindowMs is the default debounce window in milliseconds.
const defaultDebounceWindowMs = 200

// defaultDebounceMaxDelayMs is the default maximum delay before a forced fire, in milliseconds.
const defaultDebounceMaxDelayMs = 5000

// debounceWindow returns the configured debounce window duration.
// Falls back to defaultDebounceWindowMs if not set or non-positive.
func debounceWindow() time.Duration {
	ms := viper.GetInt("debounce_window_ms")
	if ms <= 0 {
		ms = defaultDebounceWindowMs
	}
	return time.Duration(ms) * time.Millisecond
}

// debounceMaxDelay returns the configured maximum delay duration.
// Falls back to defaultDebounceMaxDelayMs if not set or non-positive.
func debounceMaxDelay() time.Duration {
	ms := viper.GetInt("debounce_max_delay_ms")
	if ms <= 0 {
		ms = defaultDebounceMaxDelayMs
	}
	return time.Duration(ms) * time.Millisecond
}

// ---------------------------------------------------------------------------
// Debouncer
// ---------------------------------------------------------------------------

// Debouncer coalesces high-frequency informer events for a single Middleware into
// at most one triggerFn call per debounce window.
//
// Behaviour:
//   - If a pending timer already exists, subsequent Notify() calls are silently dropped.
//   - If no timer is pending, a new timer is armed for window duration.
//   - If time since last fire exceeds maxDelay, the trigger fires immediately to prevent
//     status from being stale during sustained high-frequency events.
type Debouncer struct {
	mu        sync.Mutex
	timer     *time.Timer
	window    time.Duration
	maxDelay  time.Duration
	pending   bool
	lastFire  time.Time
	triggerFn func()
}

// NewDebouncer creates a Debouncer with the given trigger callback.
// window and maxDelay are read from viper configuration at creation time.
func NewDebouncer(triggerFn func()) *Debouncer {
	return &Debouncer{
		window:    debounceWindow(),
		maxDelay:  debounceMaxDelay(),
		triggerFn: triggerFn,
	}
}

// Notify signals that a relevant informer event has occurred.
// It either arms a new timer or, if the max delay ceiling is breached, fires immediately.
func (d *Debouncer) Notify() {
	d.mu.Lock()
	defer d.mu.Unlock()

	// If the last fire was too long ago (or never), fire immediately to avoid stale status.
	if !d.lastFire.IsZero() && time.Since(d.lastFire) >= d.maxDelay {
		// Cancel any pending timer before firing inline.
		if d.timer != nil {
			d.timer.Stop()
			d.timer = nil
		}
		d.pending = false
		d.lastFire = time.Now()
		go d.triggerFn()
		return
	}

	// A timer is already armed; silently drop this event.
	if d.pending {
		return
	}

	// Arm a new timer for the debounce window.
	d.pending = true
	d.timer = time.AfterFunc(d.window, func() {
		d.mu.Lock()
		d.pending = false
		d.timer = nil
		d.lastFire = time.Now()
		d.mu.Unlock()

		d.triggerFn()
	})
}

// Stop cancels any pending timer and clears the debouncer state.
// It is safe to call Stop multiple times.
func (d *Debouncer) Stop() {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.timer != nil {
		d.timer.Stop()
		d.timer = nil
	}
	d.pending = false
}

// ---------------------------------------------------------------------------
// NsDebounceRegistry
// ---------------------------------------------------------------------------

// NsDebounceRegistry is a namespace-scoped registry that manages Debouncers
// keyed by "ns/midName". It is safe for concurrent use.
type NsDebounceRegistry struct {
	mu         sync.RWMutex
	debouncers map[string]*Debouncer
}

// registryKey returns the canonical map key for a given namespace and middleware name.
func registryKey(ns, midName string) string {
	return ns + "/" + midName
}

// Register creates and stores a new Debouncer for the given ns/midName pair.
// If one already exists it is stopped before being replaced.
func (r *NsDebounceRegistry) Register(ns, midName string, triggerFn func()) {
	key := registryKey(ns, midName)

	r.mu.Lock()
	defer r.mu.Unlock()

	if existing, ok := r.debouncers[key]; ok {
		existing.Stop()
		ctrl.Log.WithName("synchronizer").V(1).Info("debouncer replaced", "key", key)
		// Replaced existing Debouncer
	}
	r.debouncers[key] = NewDebouncer(triggerFn)
	ctrl.Log.WithName("synchronizer").V(1).Info("debouncer registered", "key", key)
	// Registered Debouncer for %s
}

// Unregister stops and removes the Debouncer for the given ns/midName.
// It is a no-op if no such Debouncer exists.
func (r *NsDebounceRegistry) Unregister(ns, midName string) {
	key := registryKey(ns, midName)

	r.mu.Lock()
	defer r.mu.Unlock()

	if d, ok := r.debouncers[key]; ok {
		d.Stop()
		delete(r.debouncers, key)
		ctrl.Log.WithName("synchronizer").V(1).Info("debouncer unregistered", "key", key)
		// Unregistered Debouncer for %s
	}
}

// NotifyNamespace notifies every Debouncer registered under the given namespace.
// This is called by an informer EventHandler when any Pod/StatefulSet/etc. changes
// within that namespace.
func (r *NsDebounceRegistry) NotifyNamespace(ns string) {
	prefix := ns + "/"

	// Use a read lock for iteration; Notify() uses its own per-Debouncer mutex.
	r.mu.RLock()
	defer r.mu.RUnlock()

	for key, d := range r.debouncers {
		if len(key) > len(prefix) && key[:len(prefix)] == prefix {
			d.Notify()
		}
	}
}

// StopAll stops every registered Debouncer and clears the registry.
// Typically called on controller shutdown.
func (r *NsDebounceRegistry) StopAll() {
	r.mu.Lock()
	defer r.mu.Unlock()

	for key, d := range r.debouncers {
		d.Stop()
		delete(r.debouncers, key)
		ctrl.Log.WithName("synchronizer").V(1).Info("debouncer stopped", "key", key)
		// Stopped Debouncer for %s
	}
}

// ---------------------------------------------------------------------------
// Package-level singleton
// ---------------------------------------------------------------------------

// globalDebounceRegistry is the package-level singleton registry.
var globalDebounceRegistry = &NsDebounceRegistry{
	debouncers: make(map[string]*Debouncer),
}

// RegisterDebouncer registers a Debouncer for the given ns/midName in the global registry.
// triggerFn is typically the recomputeAndUpdateStatus callback for that Middleware.
func RegisterDebouncer(ns, midName string, triggerFn func()) {
	globalDebounceRegistry.Register(ns, midName, triggerFn)
}

// UnregisterDebouncer removes the Debouncer for the given ns/midName from the global registry.
func UnregisterDebouncer(ns, midName string) {
	globalDebounceRegistry.Unregister(ns, midName)
}

// NotifyNamespace notifies all Debouncers in the given namespace via the global registry.
func NotifyNamespace(ns string) {
	globalDebounceRegistry.NotifyNamespace(ns)
}

// StopAllDebouncers stops every Debouncer in the global registry.
func StopAllDebouncers() {
	globalDebounceRegistry.StopAll()
}
