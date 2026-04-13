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
	"testing"
	"time"
)

func TestDebouncer_SingleNotify(t *testing.T) {
	fired := make(chan struct{}, 1)
	d := &Debouncer{
		window:    50 * time.Millisecond,
		maxDelay:  5 * time.Second,
		triggerFn: func() { fired <- struct{}{} },
	}

	d.Notify()

	select {
	case <-fired:
		// success
	case <-time.After(500 * time.Millisecond):
		t.Fatal("callback did not fire within timeout")
	}
}

func TestDebouncer_RapidNotify_Coalesced(t *testing.T) {
	count := make(chan struct{}, 10)
	d := &Debouncer{
		window:    100 * time.Millisecond,
		maxDelay:  5 * time.Second,
		triggerFn: func() { count <- struct{}{} },
	}

	// Send multiple rapid notifications.
	for i := 0; i < 5; i++ {
		d.Notify()
	}

	// Wait for one fire.
	select {
	case <-count:
		// expected
	case <-time.After(500 * time.Millisecond):
		t.Fatal("callback did not fire within timeout")
	}

	// Give extra time to see if additional fires occur.
	time.Sleep(200 * time.Millisecond)

	// Drain channel and count.
	close(count)
	total := 1
	for range count {
		total++
	}
	if total != 1 {
		t.Errorf("expected exactly 1 callback fire, got %d", total)
	}
}

func TestDebouncer_Stop(t *testing.T) {
	fired := make(chan struct{}, 1)
	d := &Debouncer{
		window:    50 * time.Millisecond,
		maxDelay:  5 * time.Second,
		triggerFn: func() { fired <- struct{}{} },
	}

	d.Notify()
	d.Stop()

	select {
	case <-fired:
		t.Fatal("callback should not fire after Stop")
	case <-time.After(200 * time.Millisecond):
		// success — no fire
	}
}
