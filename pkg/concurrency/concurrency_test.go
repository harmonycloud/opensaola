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

package concurrency

import (
	"os"
	"testing"
)

func TestControllerOptions_Default(t *testing.T) {
	opts := ControllerOptions("TEST", 3)
	if opts.MaxConcurrentReconciles != 3 {
		t.Errorf("default: MaxConcurrentReconciles = %d, want 3", opts.MaxConcurrentReconciles)
	}
	if opts.RateLimiter == nil {
		t.Error("default: RateLimiter should not be nil")
	}
}

func TestControllerOptions_EnvOverride(t *testing.T) {
	os.Setenv("MYCTRL_MAX_CONCURRENT", "10")
	defer os.Unsetenv("MYCTRL_MAX_CONCURRENT")

	opts := ControllerOptions("MYCTRL", 3)
	if opts.MaxConcurrentReconciles != 10 {
		t.Errorf("env override: MaxConcurrentReconciles = %d, want 10", opts.MaxConcurrentReconciles)
	}
}

func TestControllerOptions_InvalidEnv(t *testing.T) {
	os.Setenv("BAD_MAX_CONCURRENT", "notanumber")
	defer os.Unsetenv("BAD_MAX_CONCURRENT")

	opts := ControllerOptions("BAD", 5)
	if opts.MaxConcurrentReconciles != 5 {
		t.Errorf("invalid env: MaxConcurrentReconciles = %d, want 5 (fallback)", opts.MaxConcurrentReconciles)
	}
}

func TestControllerOptions_ZeroEnv(t *testing.T) {
	os.Setenv("ZERO_MAX_CONCURRENT", "0")
	defer os.Unsetenv("ZERO_MAX_CONCURRENT")

	opts := ControllerOptions("ZERO", 5)
	if opts.MaxConcurrentReconciles != 5 {
		t.Errorf("zero env: MaxConcurrentReconciles = %d, want 5 (fallback)", opts.MaxConcurrentReconciles)
	}
}
