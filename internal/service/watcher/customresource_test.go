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

package watcher

import (
	"sync"
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestCustomResourceWatcher_CounterConcurrency(t *testing.T) {
	gvk := schema.GroupVersionKind{Group: "test", Version: "v1", Kind: "Foo"}
	cw := NewCRWatcher(gvk, "ns1")

	// Initial value should be 1
	if got := cw.Counter.Load(); got != 1 {
		t.Fatalf("initial counter = %d, want 1", got)
	}

	const goroutines = 100
	var wg sync.WaitGroup

	// Concurrent increments
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			cw.Counter.Add(1)
		}()
	}
	wg.Wait()

	if got := cw.Counter.Load(); got != 1+goroutines {
		t.Fatalf("after increments counter = %d, want %d", got, 1+goroutines)
	}

	// Concurrent decrements
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			cw.Counter.Add(-1)
		}()
	}
	wg.Wait()

	if got := cw.Counter.Load(); got != 1 {
		t.Fatalf("after decrements counter = %d, want 1", got)
	}
}
