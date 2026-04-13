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

package resource

import (
	"context"
	"testing"
	"time"

	"github.com/opensaola/opensaola/internal/resource/logger"
	"github.com/spf13/viper"
)

// TestInitialize_SetsLogger verifies that Initialize sets the global logger.Log.
// 验证 Initialize 能正确设置全局 logger.Log。
func TestInitialize_SetsLogger(t *testing.T) {
	t.Cleanup(func() {
		viper.Reset()
		logger.Log = nil
	})

	// Set log level to info (1 = zerolog.InfoLevel).
	// 设置日志级别为 info（1 = zerolog.InfoLevel）。
	viper.Set("log.level", 1)

	Initialize()

	if logger.Log == nil {
		t.Fatal("expected logger.Log to be non-nil after Initialize")
	}
}

// TestInitialize_DefaultLogLevel verifies Initialize works with default (zero) log level.
// 验证 Initialize 在默认（零值）日志级别下也能正常工作。
func TestInitialize_DefaultLogLevel(t *testing.T) {
	t.Cleanup(func() {
		viper.Reset()
		logger.Log = nil
	})

	// Do not set log.level — viper returns 0 (zerolog.DebugLevel).
	// 不设置 log.level — viper 返回 0（zerolog.DebugLevel）。
	Initialize()

	if logger.Log == nil {
		t.Fatal("expected logger.Log to be non-nil after Initialize with default level")
	}
}

// TestInitCacheCleanupTimer_ContextCancel verifies that InitCacheCleanupTimer
// exits promptly when its context is cancelled.
// 验证当 context 被取消时，InitCacheCleanupTimer 能及时退出。
func TestInitCacheCleanupTimer_ContextCancel(t *testing.T) {
	t.Cleanup(func() {
		viper.Reset()
	})

	// Use a very long interval so the ticker does not fire during the test.
	// 使用很长的间隔，确保 ticker 在测试期间不会触发。
	viper.Set("cache_cleanup_interval", 3600)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go func() {
		InitCacheCleanupTimer(ctx)
		close(done)
	}()

	// Cancel the context and verify the goroutine exits within 1 second.
	// 取消 context 并验证 goroutine 在 1 秒内退出。
	cancel()

	select {
	case <-done:
		// Success: goroutine exited.
		// 成功：goroutine 已退出。
	case <-time.After(1 * time.Second):
		t.Fatal("InitCacheCleanupTimer did not exit within 1 second after context cancel")
	}
}
