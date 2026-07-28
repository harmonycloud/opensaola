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
	"time"

	"k8s.io/client-go/kubernetes/fake"
)

func TestNsInformerManager_FinalUnregisterRejectsStaleRegistration(t *testing.T) {
	canceled := make(chan struct{})
	entry := &nsEntry{
		cancel: func() { close(canceled) },
		midKeys: map[string]struct{}{
			"ns/mid#old": {},
		},
		refCount: 1,
	}
	manager := &NsInformerManager{}
	manager.entries.Store("ns", entry)

	manager.Unregister("ns", "ns/mid#old")
	select {
	case <-canceled:
	default:
		t.Fatal("final unregister did not cancel the stale namespace entry")
	}
	if _, ok := manager.entries.Load("ns"); ok {
		t.Fatal("final unregister did not remove the stale namespace entry")
	}

	replacement := &nsEntry{
		cancel:  func() {},
		midKeys: make(map[string]struct{}),
	}
	manager.entries.Store("ns", replacement)
	if added, active, _ := entry.addRegistration("ns/mid#new"); added || active {
		t.Fatal("stopped entry accepted a replacement registration")
	}
	actual, ok := manager.entries.Load("ns")
	if !ok || actual != replacement {
		t.Fatal("stale entry altered the replacement namespace entry")
	}
}

func TestNsInformerManager_RegisterReplacesStoppedEntry(t *testing.T) {
	rootCtx, rootCancel := context.WithCancel(context.Background())
	manager := &NsInformerManager{
		clientset:  fake.NewSimpleClientset(),
		rootCtx:    rootCtx,
		rootCancel: rootCancel,
	}
	t.Cleanup(manager.stopAll)

	stale := &nsEntry{
		stopped:  true,
		cancel:   func() {},
		midKeys:  map[string]struct{}{"ns/mid#old": {}},
		refCount: 1,
	}
	manager.entries.Store("ns", stale)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := manager.Register(ctx, "ns", "ns/mid#new"); err != nil {
		t.Fatalf("Register returned error: %v", err)
	}

	raw, ok := manager.entries.Load("ns")
	if !ok {
		t.Fatal("Register did not install a replacement entry")
	}
	actual, ok := raw.(*nsEntry)
	if !ok || actual == stale {
		t.Fatalf("Register kept stale entry: %#v", raw)
	}
	if actual.isStopped() {
		t.Fatal("replacement entry is stopped")
	}
	actual.mu.Lock()
	_, registered := actual.midKeys["ns/mid#new"]
	actual.mu.Unlock()
	if !registered {
		t.Fatal("replacement entry did not retain the new registration")
	}
}

func TestStopNsInformerManagerIfCurrent_PreservesReplacement(t *testing.T) {
	globalManagerMu.Lock()
	previous := globalManager
	globalManager = &NsInformerManager{}
	replacement := globalManager
	globalManagerMu.Unlock()
	t.Cleanup(func() {
		globalManagerMu.Lock()
		globalManager = previous
		globalManagerMu.Unlock()
	})

	StopNsInformerManagerIfCurrent(&NsInformerManager{})
	if got := GetNsInformerManager(); got != replacement {
		t.Fatalf("global manager = %p, want replacement %p", got, replacement)
	}
}
