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

package consts

import (
	"strings"
	"testing"
)

// TestErrorSentinels_NonNil verifies all error sentinel variables are non-nil.
// 验证所有哨兵错误变量都不为 nil。
func TestErrorSentinels_NonNil(t *testing.T) {
	t.Parallel()
	errs := map[string]error{
		"SameTypeMiddlewareExists":         SameTypeMiddlewareExists,
		"SameTypeMiddlewareOperatorExists": SameTypeMiddlewareOperatorExists,
		"NoOperator":                       NoOperator,
		"ErrPackageNotReady":               ErrPackageNotReady,
		"ErrPackageInstallFailed":          ErrPackageInstallFailed,
		"ErrPackageUnavailableExceeded":    ErrPackageUnavailableExceeded,
	}
	for name, e := range errs {
		if e == nil {
			t.Errorf("%s should not be nil", name)
		}
	}
}

// TestErrorSentinels_ContainGuidance verifies error messages contain actionable guidance.
// 验证错误消息包含可操作的指导信息。
func TestErrorSentinels_ContainGuidance(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		err      error
		contains string
	}{
		{
			name:     "NoOperator mentions MiddlewareOperator",
			err:      NoOperator,
			contains: "MiddlewareOperator",
		},
		{
			name:     "SameTypeMiddlewareExists mentions delete",
			err:      SameTypeMiddlewareExists,
			contains: "delete",
		},
		{
			name:     "SameTypeMiddlewareOperatorExists mentions kubectl",
			err:      SameTypeMiddlewareOperatorExists,
			contains: "kubectl",
		},
		{
			name:     "ErrPackageNotReady mentions package",
			err:      ErrPackageNotReady,
			contains: "package",
		},
		{
			name:     "ErrPackageInstallFailed mentions annotations",
			err:      ErrPackageInstallFailed,
			contains: "annotations",
		},
		{
			name:     "ErrPackageUnavailableExceeded mentions Secret",
			err:      ErrPackageUnavailableExceeded,
			contains: "Secret",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if !strings.Contains(tt.err.Error(), tt.contains) {
				t.Errorf("%s error message should contain %q, got %q", tt.name, tt.contains, tt.err.Error())
			}
		})
	}
}

// TestErrorSentinels_Distinct verifies all error sentinels have distinct messages.
// 验证所有哨兵错误的消息互不相同。
func TestErrorSentinels_Distinct(t *testing.T) {
	t.Parallel()
	errs := []error{
		SameTypeMiddlewareExists,
		SameTypeMiddlewareOperatorExists,
		NoOperator,
		ErrPackageNotReady,
		ErrPackageInstallFailed,
		ErrPackageUnavailableExceeded,
	}
	seen := make(map[string]bool)
	for _, e := range errs {
		msg := e.Error()
		if seen[msg] {
			t.Errorf("duplicate error message: %s", msg)
		}
		seen[msg] = true
	}
}

// TestHandleActionConstants_Distinct verifies all HandleAction constants are distinct non-empty strings.
// 验证所有 HandleAction 常量都是互不相同的非空字符串。
func TestHandleActionConstants_Distinct(t *testing.T) {
	t.Parallel()
	actions := map[string]HandleAction{
		"HandleActionPublish": HandleActionPublish,
		"HandleActionDelete":  HandleActionDelete,
		"HandleActionUpdate":  HandleActionUpdate,
	}
	seen := make(map[HandleAction]bool)
	for name, a := range actions {
		if a == "" {
			t.Errorf("%s should not be empty", name)
		}
		if seen[a] {
			t.Errorf("duplicate HandleAction value %q from %s", a, name)
		}
		seen[a] = true
	}
}

// TestProjectConstant verifies ProjectOpenSaola is set to "opensaola".
// 验证 ProjectOpenSaola 设置为 "opensaola"。
func TestProjectConstant(t *testing.T) {
	t.Parallel()
	if ProjectOpenSaola == "" {
		t.Error("ProjectOpenSaola should not be empty")
	}
	if ProjectOpenSaola != "opensaola" {
		t.Errorf("ProjectOpenSaola should be 'opensaola', got %q", ProjectOpenSaola)
	}
}

// TestStatusCondTypeCheck_NonEmpty verifies StatusCondTypeCheck is non-empty.
// 验证 StatusCondTypeCheck 不为空。
func TestStatusCondTypeCheck_NonEmpty(t *testing.T) {
	t.Parallel()
	if StatusCondTypeCheck == "" {
		t.Error("StatusCondTypeCheck should not be empty")
	}
}

// TestActionIotaConstants_Distinct verifies iota-based action constants have expected sequential values.
// 验证基于 iota 的 action 常量具有预期的顺序值。
func TestActionIotaConstants_Distinct(t *testing.T) {
	t.Parallel()
	if ActionNone != 0 {
		t.Errorf("ActionNone should be 0, got %d", ActionNone)
	}
	if ActionCreate != 1 {
		t.Errorf("ActionCreate should be 1, got %d", ActionCreate)
	}
	if ActionUpdate != 2 {
		t.Errorf("ActionUpdate should be 2, got %d", ActionUpdate)
	}
}

// TestGenerationIotaConstants_Distinct verifies iota-based generation constants have expected sequential values.
// 验证基于 iota 的 generation 常量具有预期的顺序值。
func TestGenerationIotaConstants_Distinct(t *testing.T) {
	t.Parallel()
	if GenerationZero != 0 {
		t.Errorf("GenerationZero should be 0, got %d", GenerationZero)
	}
	if GenerationCreate != 1 {
		t.Errorf("GenerationCreate should be 1, got %d", GenerationCreate)
	}
}
