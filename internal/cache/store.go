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
	"time"
)

// Store is a type-safe, concurrency-safe cache with optional TTL.
// It wraps sync.Map to provide generic key-value storage.
type Store[K comparable, V any] struct {
	data sync.Map
	ttl  time.Duration
}

type entry[V any] struct {
	value     V
	expiresAt time.Time
}

// New creates a new Store with the given TTL.
// A TTL of 0 means entries never expire.
func New[K comparable, V any](ttl time.Duration) *Store[K, V] {
	return &Store[K, V]{ttl: ttl}
}

// Get retrieves a value by key. Returns the value and true if found and not expired.
// Expired entries are automatically evicted on access.
func (s *Store[K, V]) Get(key K) (V, bool) {
	raw, ok := s.data.Load(key)
	if !ok {
		var zero V
		return zero, false
	}
	e := raw.(entry[V])
	if !e.expiresAt.IsZero() && time.Now().After(e.expiresAt) {
		s.data.Delete(key)
		var zero V
		return zero, false
	}
	return e.value, true
}

// Set stores a value with the configured TTL.
func (s *Store[K, V]) Set(key K, val V) {
	var exp time.Time
	if s.ttl > 0 {
		exp = time.Now().Add(s.ttl)
	}
	s.data.Store(key, entry[V]{value: val, expiresAt: exp})
}

// Delete removes a key from the store.
func (s *Store[K, V]) Delete(key K) {
	s.data.Delete(key)
}

// Clear removes all entries from the store.
func (s *Store[K, V]) Clear() {
	s.data.Range(func(key, _ any) bool {
		s.data.Delete(key)
		return true
	})
}

// Range iterates over all non-expired entries. The callback returns false to stop iteration.
func (s *Store[K, V]) Range(fn func(key K, val V) bool) {
	s.data.Range(func(k, v any) bool {
		e := v.(entry[V])
		if !e.expiresAt.IsZero() && time.Now().After(e.expiresAt) {
			s.data.Delete(k)
			return true // skip expired, continue iteration
		}
		return fn(k.(K), e.value)
	})
}
