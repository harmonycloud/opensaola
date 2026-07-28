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
	"testing"
)

func TestSyncCustomResourceSession_OldReleasePreservesReplacement(t *testing.T) {
	key := t.Name()
	t.Cleanup(func() {
		StopSyncCustomResource(key)
	})

	first, owner, err := acquireSyncCustomResourceSession(context.Background(), key)
	if err != nil || !owner {
		t.Fatalf("first acquire = owner:%v err:%v, want owner", owner, err)
	}

	StopSyncCustomResource(key)
	second, owner, err := acquireSyncCustomResourceSession(context.Background(), key)
	if err != nil || !owner {
		t.Fatalf("replacement acquire = owner:%v err:%v, want owner", owner, err)
	}
	if first.lease == second.lease {
		t.Fatal("replacement session reused the old lease")
	}

	first.release(key)
	actual, ok := SyncCustomResourceStopChanMap.Load(key)
	if !ok || actual != second {
		t.Fatalf("session registry = %#v, want replacement session", actual)
	}

	second.release(key)
	if _, ok := SyncCustomResourceStopChanMap.Load(key); ok {
		t.Fatal("released replacement session remained in registry")
	}
	select {
	case <-first.done:
	default:
		t.Fatal("first session did not report completion")
	}
	select {
	case <-second.done:
	default:
		t.Fatal("replacement session did not report completion")
	}
}

func TestStopAllSyncCustomResources_StopsAndRemovesSessions(t *testing.T) {
	key := t.Name()
	t.Cleanup(func() {
		StopSyncCustomResource(key)
	})

	session, owner, err := acquireSyncCustomResourceSession(context.Background(), key)
	if err != nil || !owner {
		t.Fatalf("acquire = owner:%v err:%v, want owner", owner, err)
	}

	StopAllSyncCustomResources()
	select {
	case <-session.stopCh:
	default:
		t.Fatal("StopAllSyncCustomResources did not stop the active session")
	}
	if _, ok := SyncCustomResourceStopChanMap.Load(key); ok {
		t.Fatal("StopAllSyncCustomResources left the stopped session in registry")
	}
	session.release(key)
}

func TestSyncCustomResourceSession_RegisterIfActiveRejectsStoppedSession(t *testing.T) {
	session := newSyncCustomResourceSession(context.Background())
	t.Cleanup(func() {
		session.release(t.Name())
	})

	called := false
	if !session.registerIfActive(func() { called = true }) {
		t.Fatal("active session rejected registration")
	}
	if !called {
		t.Fatal("active session did not execute registration")
	}

	session.stop()
	called = false
	if session.registerIfActive(func() { called = true }) {
		t.Fatal("stopped session accepted registration")
	}
	if called {
		t.Fatal("stopped session executed registration")
	}
}

func TestAcquireSyncCustomResourceSession_ReplacesStoppedEntry(t *testing.T) {
	key := t.Name()
	t.Cleanup(func() {
		StopSyncCustomResource(key)
	})

	stopped, owner, err := acquireSyncCustomResourceSession(context.Background(), key)
	if err != nil || !owner {
		t.Fatalf("first acquire = owner:%v err:%v, want owner", owner, err)
	}
	// Deliberately leave the stopped session in the map to model the handover
	// window between Stop signaling and registry cleanup.
	stopped.stop()

	replacement, owner, err := acquireSyncCustomResourceSession(context.Background(), key)
	if err != nil || !owner {
		t.Fatalf("replacement acquire = owner:%v err:%v, want owner", owner, err)
	}
	if replacement == stopped {
		t.Fatal("acquire reused a stopped session")
	}
	actual, ok := SyncCustomResourceStopChanMap.Load(key)
	if !ok || actual != replacement {
		t.Fatalf("session registry = %#v, want replacement session", actual)
	}
}
