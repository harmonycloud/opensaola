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

package middlewarepackage

import (
	"fmt"
	"testing"

	"github.com/opensaola/opensaola/internal/service/packages"
	corev1 "k8s.io/api/core/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// ---------------------------------------------------------------------------
// packageReleaseDigest
// ---------------------------------------------------------------------------

func TestPackageReleaseDigest_NilSecret(t *testing.T) {
	t.Parallel()
	// packageReleaseDigest(nil) should return empty string.
	// nil Secret 应该返回空字符串。
	got := packageReleaseDigest(nil)
	if got != "" {
		t.Errorf("expected empty string for nil secret, got %q", got)
	}
}

func TestPackageReleaseDigest_EmptyData(t *testing.T) {
	t.Parallel()
	// Secret with nil Data map should return empty string.
	// Data 为 nil 的 Secret 应返回空字符串。
	secret := &corev1.Secret{}
	got := packageReleaseDigest(secret)
	if got != "" {
		t.Errorf("expected empty string for secret with nil Data, got %q", got)
	}
}

func TestPackageReleaseDigest_MissingReleaseKey(t *testing.T) {
	t.Parallel()
	// Secret with Data that does not contain the Release key should return empty string.
	// Data 中不包含 Release key 时应返回空字符串。
	secret := &corev1.Secret{
		Data: map[string][]byte{
			"other-key": []byte("some-value"),
		},
	}
	got := packageReleaseDigest(secret)
	if got != "" {
		t.Errorf("expected empty string when Release key is missing, got %q", got)
	}
}

func TestPackageReleaseDigest_EmptyReleaseValue(t *testing.T) {
	t.Parallel()
	// Secret with empty byte slice for Release key should return empty string.
	// Release key 对应空 []byte 时应返回空字符串。
	secret := &corev1.Secret{
		Data: map[string][]byte{
			packages.Release: {},
		},
	}
	got := packageReleaseDigest(secret)
	if got != "" {
		t.Errorf("expected empty string for empty Release value, got %q", got)
	}
}

func TestPackageReleaseDigest_ValidData(t *testing.T) {
	t.Parallel()
	// A secret with valid Release data should return a 64-char hex SHA256 digest.
	// 有效 Release 数据应返回 64 字符的十六进制 SHA256 摘要。
	secret := &corev1.Secret{
		Data: map[string][]byte{
			packages.Release: []byte("hello world"),
		},
	}
	got := packageReleaseDigest(secret)
	if len(got) != 64 {
		t.Fatalf("expected 64-char hex string, got %d chars: %q", len(got), got)
	}

	// Verify deterministic: same input produces same output.
	// 验证确定性：相同输入产生相同输出。
	got2 := packageReleaseDigest(secret)
	if got != got2 {
		t.Errorf("digest is not deterministic: %q != %q", got, got2)
	}
}

func TestPackageReleaseDigest_DifferentData(t *testing.T) {
	t.Parallel()
	// Different Release data must produce different digests.
	// 不同 Release 数据必须产生不同摘要。
	s1 := &corev1.Secret{
		Data: map[string][]byte{packages.Release: []byte("data-a")},
	}
	s2 := &corev1.Secret{
		Data: map[string][]byte{packages.Release: []byte("data-b")},
	}
	d1 := packageReleaseDigest(s1)
	d2 := packageReleaseDigest(s2)
	if d1 == d2 {
		t.Errorf("different inputs produced the same digest: %q", d1)
	}
}

// ---------------------------------------------------------------------------
// truncateBytes
// ---------------------------------------------------------------------------

func TestTruncateBytes_Short(t *testing.T) {
	t.Parallel()
	// String shorter than max should be returned as-is.
	// 短于 max 的字符串应原样返回。
	got := truncateBytes("short", 100)
	if got != "short" {
		t.Errorf("expected %q, got %q", "short", got)
	}
}

func TestTruncateBytes_ExactLength(t *testing.T) {
	t.Parallel()
	// String exactly at max length should be returned as-is.
	// 长度恰好等于 max 的字符串应原样返回。
	got := truncateBytes("abcde", 5)
	if got != "abcde" {
		t.Errorf("expected %q, got %q", "abcde", got)
	}
}

func TestTruncateBytes_Long(t *testing.T) {
	t.Parallel()
	// String longer than max should be truncated to max bytes.
	// 超过 max 的字符串应被截断。
	got := truncateBytes("hello world", 5)
	if got != "hello" {
		t.Errorf("expected %q, got %q", "hello", got)
	}
}

func TestTruncateBytes_ZeroMax(t *testing.T) {
	t.Parallel()
	// max <= 0 should return the original string (no truncation).
	// max <= 0 时应返回原始字符串。
	got := truncateBytes("anything", 0)
	if got != "anything" {
		t.Errorf("expected %q, got %q", "anything", got)
	}
}

func TestTruncateBytes_NegativeMax(t *testing.T) {
	t.Parallel()
	// Negative max should return the original string.
	// 负数 max 应返回原始字符串。
	got := truncateBytes("anything", -1)
	if got != "anything" {
		t.Errorf("expected %q, got %q", "anything", got)
	}
}

func TestTruncateBytes_EmptyString(t *testing.T) {
	t.Parallel()
	// Empty string should always return empty.
	// 空字符串始终返回空。
	got := truncateBytes("", 10)
	if got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// isTerminalInstallError
// ---------------------------------------------------------------------------

func TestIsTerminalInstallError_Nil(t *testing.T) {
	t.Parallel()
	// nil error should return false.
	// nil 错误应返回 false。
	if isTerminalInstallError(nil) {
		t.Error("expected false for nil error")
	}
}

func TestIsTerminalInstallError_InvalidRequest(t *testing.T) {
	t.Parallel()
	// K8s Invalid error should be terminal.
	// K8s Invalid 错误应被识别为终端错误。
	err := apiErrors.NewInvalid(
		schema.GroupKind{Group: "middleware.cn", Kind: "MiddlewareBaseline"},
		"test-baseline",
		nil,
	)
	if !isTerminalInstallError(err) {
		t.Error("expected true for apiErrors.IsInvalid error")
	}
}

func TestIsTerminalInstallError_BadRequest(t *testing.T) {
	t.Parallel()
	// K8s BadRequest error should be terminal.
	// K8s BadRequest 错误应被识别为终端错误。
	err := apiErrors.NewBadRequest("invalid spec")
	if !isTerminalInstallError(err) {
		t.Error("expected true for apiErrors.IsBadRequest error")
	}
}

func TestIsTerminalInstallError_TransientError(t *testing.T) {
	t.Parallel()
	// Generic transient errors should not be terminal.
	// 一般性瞬态错误不应被视为终端错误。
	err := fmt.Errorf("network timeout")
	if isTerminalInstallError(err) {
		t.Error("expected false for transient error")
	}
}

func TestIsTerminalInstallError_YAMLConversionError(t *testing.T) {
	t.Parallel()
	// Error containing "error converting YAML to JSON" should be terminal.
	// 包含 "error converting YAML to JSON" 的错误应为终端错误。
	err := fmt.Errorf("error converting YAML to JSON: some details")
	if !isTerminalInstallError(err) {
		t.Error("expected true for YAML conversion error")
	}
}

func TestIsTerminalInstallError_YAMLParseError(t *testing.T) {
	t.Parallel()
	// Error containing "yaml:" should be terminal.
	// 包含 "yaml:" 的错误应为终端错误。
	err := fmt.Errorf("yaml: line 5: mapping values are not allowed")
	if !isTerminalInstallError(err) {
		t.Error("expected true for yaml parse error")
	}
}

func TestIsTerminalInstallError_InvalidMapKey(t *testing.T) {
	t.Parallel()
	// Error containing "invalid map key" should be terminal.
	// 包含 "invalid map key" 的错误应为终端错误。
	err := fmt.Errorf("invalid map key: found non-string key")
	if !isTerminalInstallError(err) {
		t.Error("expected true for invalid map key error")
	}
}

func TestIsTerminalInstallError_NotFoundIsNotTerminal(t *testing.T) {
	t.Parallel()
	// K8s NotFound error should NOT be terminal (it's transient / retryable).
	// K8s NotFound 错误不应被识别为终端错误。
	err := apiErrors.NewNotFound(
		schema.GroupResource{Group: "middleware.cn", Resource: "middlewarebaselines"},
		"test-baseline",
	)
	if isTerminalInstallError(err) {
		t.Error("expected false for NotFound error")
	}
}

func TestIsTerminalInstallError_ConflictIsNotTerminal(t *testing.T) {
	t.Parallel()
	// K8s Conflict error should NOT be terminal.
	// K8s Conflict 错误不应被识别为终端错误。
	err := apiErrors.NewConflict(
		schema.GroupResource{Group: "middleware.cn", Resource: "middlewarebaselines"},
		"test-baseline",
		fmt.Errorf("object has been modified"),
	)
	if isTerminalInstallError(err) {
		t.Error("expected false for Conflict error")
	}
}

// ---------------------------------------------------------------------------
// NOTE: Check, HandleSecret, and HandleResource require a real or fake
// controller-runtime client and are tightly coupled to K8s API calls.
// They need integration tests (envtest) rather than pure unit tests.
//
// 注意：Check、HandleSecret、HandleResource 需要真实或 fake 的
// controller-runtime client，与 K8s API 调用紧耦合，
// 适合用集成测试（envtest）而非纯单元测试。
// ---------------------------------------------------------------------------
