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

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
)

// syncCustomResourceSession is the ownership record for one running SyncV2
// instance. Keeping the session identity in the map lets an old goroutine clean
// up only its own state after a leader handover or explicit restart.
type syncCustomResourceSession struct {
	ctx    context.Context
	cancel context.CancelFunc

	stopCh chan struct{}
	done   chan struct{}

	mu          sync.Mutex
	stopped     bool
	releaseOnce sync.Once
	lease       uint64
}

var nextSyncCustomResourceLease atomic.Uint64

func newSyncCustomResourceSession(parent context.Context) *syncCustomResourceSession {
	ctx, cancel := context.WithCancel(parent)
	return &syncCustomResourceSession{
		ctx:    ctx,
		cancel: cancel,
		stopCh: make(chan struct{}),
		done:   make(chan struct{}),
		lease:  nextSyncCustomResourceLease.Add(1),
	}
}

func (s *syncCustomResourceSession) stop() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return
	}
	s.stopped = true
	s.cancel()
	close(s.stopCh)
}

// registerIfActive serializes the final liveness check with Stop. A stopped
// session must never register a Debouncer after a replacement is allowed to
// enter the registry.
func (s *syncCustomResourceSession) registerIfActive(register func()) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped || s.ctx.Err() != nil {
		return false
	}
	register()
	return true
}

// isStopped observes the session state under the same lock used by stop. It is
// used while acquiring the registry key so a newly requested sync does not get
// discarded behind an already-stopped session that has not completed cleanup.
func (s *syncCustomResourceSession) isStopped() bool {
	if s == nil {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stopped || s.ctx.Err() != nil
}

func (s *syncCustomResourceSession) release(key string) {
	if s == nil {
		return
	}
	s.releaseOnce.Do(func() {
		SyncCustomResourceStopChanMap.CompareAndDelete(key, s)
		s.cancel()
		close(s.done)
	})
}

var SyncCustomResourceStopChanMap sync.Map
var SyncCustomResourceStopChanMapKey = "%s/%s/%s" // gvk/ns/name

func acquireSyncCustomResourceSession(ctx context.Context, key string) (*syncCustomResourceSession, bool, error) {
	for {
		candidate := newSyncCustomResourceSession(ctx)
		actual, loaded := SyncCustomResourceStopChanMap.LoadOrStore(key, candidate)
		if !loaded {
			return candidate, true, nil
		}

		existing, ok := actual.(*syncCustomResourceSession)
		if !ok {
			candidate.cancel()
			return nil, false, fmt.Errorf("sync custom resource session %s has unexpected type %T", key, actual)
		}
		candidate.cancel()
		if !existing.isStopped() {
			return existing, false, nil
		}

		// StopSyncCustomResource removes the current session after signaling it.
		// A concurrent SyncV2 must not treat that stopped entry as a live duplicate;
		// delete only this identity and retry to install a replacement.
		SyncCustomResourceStopChanMap.CompareAndDelete(key, existing)
	}
}

// StopSyncCustomResource stops and removes the current session for key. Its
// deferred cleanup remains identity-scoped, so it cannot remove a replacement.
func StopSyncCustomResource(key string) {
	value, ok := SyncCustomResourceStopChanMap.Load(key)
	if !ok {
		return
	}
	session, ok := value.(*syncCustomResourceSession)
	if !ok {
		return
	}
	session.stop()
	SyncCustomResourceStopChanMap.CompareAndDelete(key, session)
}

func StopAllSyncCustomResources() {
	SyncCustomResourceStopChanMap.Range(func(key, value any) bool {
		if session, ok := value.(*syncCustomResourceSession); ok {
			session.stop()
			SyncCustomResourceStopChanMap.CompareAndDelete(key, session)
		}
		return true
	})
}
