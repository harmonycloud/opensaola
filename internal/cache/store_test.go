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

package cache

import (
	"sync"
	"testing"
	"time"
)

func TestGetSet(t *testing.T) {
	s := New[string, int](0)
	s.Set("a", 1)
	v, ok := s.Get("a")
	if !ok {
		t.Fatal("expected key 'a' to exist")
	}
	if v != 1 {
		t.Fatalf("expected 1, got %d", v)
	}
}

func TestDelete(t *testing.T) {
	s := New[string, string](0)
	s.Set("key", "val")
	s.Delete("key")
	_, ok := s.Get("key")
	if ok {
		t.Fatal("expected key to be deleted")
	}
}

func TestGetMiss(t *testing.T) {
	s := New[string, int](0)
	v, ok := s.Get("nonexistent")
	if ok {
		t.Fatal("expected miss for nonexistent key")
	}
	if v != 0 {
		t.Fatalf("expected zero value, got %d", v)
	}
}

func TestTTLExpiration(t *testing.T) {
	s := New[string, int](50 * time.Millisecond)
	s.Set("x", 42)

	// Should be available immediately.
	v, ok := s.Get("x")
	if !ok || v != 42 {
		t.Fatalf("expected 42 before expiry, got %d, ok=%v", v, ok)
	}

	// Wait for expiration.
	time.Sleep(100 * time.Millisecond)

	_, ok = s.Get("x")
	if ok {
		t.Fatal("expected key to be expired")
	}
}

func TestNoTTL(t *testing.T) {
	s := New[string, int](0)
	s.Set("persist", 99)

	// Even after a short sleep, entries with TTL=0 should remain.
	time.Sleep(50 * time.Millisecond)

	v, ok := s.Get("persist")
	if !ok || v != 99 {
		t.Fatalf("expected 99 with no TTL, got %d, ok=%v", v, ok)
	}
}

func TestClear(t *testing.T) {
	s := New[string, int](0)
	s.Set("a", 1)
	s.Set("b", 2)
	s.Set("c", 3)
	s.Clear()

	for _, k := range []string{"a", "b", "c"} {
		if _, ok := s.Get(k); ok {
			t.Fatalf("expected key %q to be cleared", k)
		}
	}
}

func TestRange(t *testing.T) {
	s := New[string, int](0)
	s.Set("a", 1)
	s.Set("b", 2)
	s.Set("c", 3)

	visited := make(map[string]int)
	s.Range(func(k string, v int) bool {
		visited[k] = v
		return true
	})

	if len(visited) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(visited))
	}
	for _, k := range []string{"a", "b", "c"} {
		if _, ok := visited[k]; !ok {
			t.Fatalf("expected key %q to be visited", k)
		}
	}
}

func TestRangeSkipsExpired(t *testing.T) {
	s := New[string, int](50 * time.Millisecond)
	s.Set("expired1", 1)
	s.Set("expired2", 2)

	time.Sleep(100 * time.Millisecond)

	// Add a fresh entry after the expired ones.
	s2 := New[string, int](time.Hour)
	s2.Set("fresh", 3)
	// We need to use the same store; set fresh entry with a long TTL manually.
	// Re-do: set fresh on the original store with a direct data.Store to bypass the short TTL.
	// Actually, the store TTL is fixed at creation. So all entries in s get 50ms TTL.
	// Instead, we verify that Range visits nothing from the expired store.
	var count int
	s.Range(func(_ string, _ int) bool {
		count++
		return true
	})
	if count != 0 {
		t.Fatalf("expected 0 entries after expiry, got %d", count)
	}
}

func TestConcurrentAccess(t *testing.T) {
	s := New[int, int](0)
	var wg sync.WaitGroup
	const goroutines = 100
	const ops = 100

	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func(id int) {
			defer wg.Done()
			for i := 0; i < ops; i++ {
				key := id*ops + i
				s.Set(key, key)
				s.Get(key)
				if i%3 == 0 {
					s.Delete(key)
				}
			}
		}(g)
	}
	wg.Wait()
}
