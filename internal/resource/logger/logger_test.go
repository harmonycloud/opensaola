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

package logger

import (
	"testing"

	"github.com/rs/zerolog"
	"github.com/spf13/viper"
)

// TestInitialize_SetsLogLevel verifies that Initialize sets the global Log
// variable and configures the requested log level.
// 验证 Initialize 能正确设置全局 Log 变量并配置指定的日志级别。
func TestInitialize_SetsLogLevel(t *testing.T) {
	// Clean up viper state and global Log after test.
	// 测试结束后清理 viper 状态和全局 Log。
	t.Cleanup(func() {
		viper.Reset()
		Log = nil
	})

	Initialize(zerolog.InfoLevel)

	if Log == nil {
		t.Fatal("expected Log to be non-nil after Initialize")
	}

	got := Log.Zlog.GetLevel()
	if got != zerolog.InfoLevel {
		t.Errorf("expected log level %v, got %v", zerolog.InfoLevel, got)
	}
}

// TestInitialize_MultipleCalls verifies that calling Initialize more than once
// does not panic and leaves Log in a valid state.
// 验证多次调用 Initialize 不会 panic，且 Log 仍然有效。
func TestInitialize_MultipleCalls(t *testing.T) {
	t.Cleanup(func() {
		viper.Reset()
		Log = nil
	})

	Initialize(zerolog.DebugLevel)
	Initialize(zerolog.WarnLevel)

	if Log == nil {
		t.Fatal("expected Log to be non-nil after multiple Initialize calls")
	}

	// The second call should overwrite the first.
	// 第二次调用应覆盖第一次的配置。
	got := Log.Zlog.GetLevel()
	if got != zerolog.WarnLevel {
		t.Errorf("expected log level %v after second Initialize, got %v", zerolog.WarnLevel, got)
	}
}

// TestFileWriter_EmptyPath verifies that fileWriter returns nil when the
// log file path is empty.
// 验证当日志文件路径为空时，fileWriter 返回 nil。
func TestFileWriter_EmptyPath(t *testing.T) {
	t.Cleanup(func() {
		viper.Reset()
	})

	viper.Set("log.file_path", "")

	w := fileWriter()
	if w != nil {
		t.Errorf("expected fileWriter to return nil for empty path, got %v", w)
	}
}

// TestFileWriter_InvalidPath verifies that fileWriter returns nil gracefully
// when the log directory cannot be created (e.g., a path under /proc on Linux
// or a non-writable root on macOS).
// 验证当日志目录无法创建时，fileWriter 能优雅地返回 nil。
func TestFileWriter_InvalidPath(t *testing.T) {
	t.Cleanup(func() {
		viper.Reset()
	})

	// Use a path under /dev/null which is not a directory — MkdirAll will fail.
	// 使用 /dev/null 下的路径，因为它不是目录，MkdirAll 会失败。
	viper.Set("log.file_path", "/dev/null/nonexistent/test.log")

	w := fileWriter()
	if w != nil {
		t.Errorf("expected fileWriter to return nil for invalid path, got %v", w)
	}
}

// TestFileWriter_ValidPath verifies that fileWriter returns a non-nil writer
// when given a valid temporary directory path.
// 验证当给定有效的临时目录路径时，fileWriter 返回非 nil 的 writer。
func TestFileWriter_ValidPath(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := tmpDir + "/test.log"

	t.Cleanup(func() {
		viper.Reset()
	})

	viper.Set("log.file_path", logPath)

	w := fileWriter()
	if w == nil {
		t.Error("expected fileWriter to return a non-nil writer for a valid path")
	}
}

// TestStdWriter_DefaultFormat verifies that stdWriter returns a non-nil writer
// when no format is configured (defaults to console).
// 验证未配置格式时（默认为 console），stdWriter 返回非 nil 的 writer。
func TestStdWriter_DefaultFormat(t *testing.T) {
	t.Cleanup(func() {
		viper.Reset()
	})

	w := stdWriter()
	if w == nil {
		t.Error("expected stdWriter to return a non-nil writer with default format")
	}
}

// TestStdWriter_JSONFormat verifies that stdWriter returns a non-nil writer
// when the format is set to "json".
// 验证格式设置为 "json" 时，stdWriter 返回非 nil 的 writer。
func TestStdWriter_JSONFormat(t *testing.T) {
	t.Cleanup(func() {
		viper.Reset()
	})

	viper.Set("log.format", "json")

	w := stdWriter()
	if w == nil {
		t.Error("expected stdWriter to return a non-nil writer with json format")
	}
}
