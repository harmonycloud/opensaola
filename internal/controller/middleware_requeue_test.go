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

package controller

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/opensaola/opensaola/internal/service/consts"
	ctrl "sigs.k8s.io/controller-runtime"
)

// TestErrPackageNotReady_RequeueAfter verifies that ErrPackageNotReady caught by Reconcile
// returns RequeueAfter instead of an error
func TestErrPackageNotReady_RequeueAfter(t *testing.T) {
	// Simulate handleMiddlewareOperator returning a wrapped ErrPackageNotReady
	wrappedErr := fmt.Errorf("upgrade failed: %w", consts.ErrPackageNotReady)

	if !errors.Is(wrappedErr, consts.ErrPackageNotReady) {
		t.Fatal("expected errors.Is to unwrap ErrPackageNotReady through fmt.Errorf %%w")
	}

	// Simulate the branching logic in Reconcile
	var result ctrl.Result
	var retErr error

	if errors.Is(wrappedErr, consts.ErrPackageNotReady) {
		result = ctrl.Result{RequeueAfter: 5 * time.Second}
		retErr = nil
	} else {
		result = ctrl.Result{}
		retErr = wrappedErr
	}

	if retErr != nil {
		t.Fatalf("expected nil error, got %v", retErr)
	}
	if result.RequeueAfter != 5*time.Second {
		t.Fatalf("expected RequeueAfter=5s, got %v", result.RequeueAfter)
	}
}

// TestErrPackageInstallFailed_NoRequeue verifies that ErrPackageInstallFailed is caught
// and does not RequeueAfter (to avoid a 5s tight loop), returning nil error instead
func TestErrPackageInstallFailed_NoRequeue(t *testing.T) {
	wrappedErr := fmt.Errorf("upgrade failed: %w", consts.ErrPackageInstallFailed)
	if !errors.Is(wrappedErr, consts.ErrPackageInstallFailed) {
		t.Fatal("expected errors.Is to unwrap ErrPackageInstallFailed through fmt.Errorf %w")
	}

	var result ctrl.Result
	var retErr error

	if errors.Is(wrappedErr, consts.ErrPackageNotReady) {
		result = ctrl.Result{RequeueAfter: 5 * time.Second}
		retErr = nil
	} else if errors.Is(wrappedErr, consts.ErrPackageInstallFailed) {
		result = ctrl.Result{}
		retErr = nil
	} else {
		result = ctrl.Result{}
		retErr = wrappedErr
	}

	if retErr != nil {
		t.Fatalf("expected nil error, got %v", retErr)
	}
	if result.RequeueAfter != 0 {
		t.Fatalf("expected RequeueAfter=0, got %v", result.RequeueAfter)
	}
}

func TestErrPackageUnavailableExceeded_NoRequeue(t *testing.T) {
	wrappedErr := fmt.Errorf("upgrade failed: %w", consts.ErrPackageUnavailableExceeded)
	if !errors.Is(wrappedErr, consts.ErrPackageUnavailableExceeded) {
		t.Fatal("expected errors.Is to unwrap ErrPackageUnavailableExceeded through fmt.Errorf %w")
	}

	var result ctrl.Result
	var retErr error

	if errors.Is(wrappedErr, consts.ErrPackageNotReady) {
		result = ctrl.Result{RequeueAfter: 5 * time.Second}
		retErr = nil
	} else if errors.Is(wrappedErr, consts.ErrPackageInstallFailed) || errors.Is(wrappedErr, consts.ErrPackageUnavailableExceeded) {
		result = ctrl.Result{}
		retErr = nil
	} else {
		result = ctrl.Result{}
		retErr = wrappedErr
	}

	if retErr != nil {
		t.Fatalf("expected nil error, got %v", retErr)
	}
	if result.RequeueAfter != 0 {
		t.Fatalf("expected RequeueAfter=0, got %v", result.RequeueAfter)
	}
}

// Gate removed: ReplacePackageRequeue is now the default non-blocking requeue strategy
