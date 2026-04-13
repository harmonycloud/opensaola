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

package config

import (
	"testing"

	"github.com/spf13/viper"
)

// initializeForTest resets viper and adds the current directory as a config path
// so that config.yaml can be found when tests run from the pkg/config directory.
func initializeForTest(t *testing.T) {
	t.Helper()
	viper.Reset()
	// When tests run, the working directory is pkg/config/ where config.yaml lives.
	// Initialize() adds "./pkg/config" which resolves to "pkg/config/pkg/config" from
	// within the test directory. Adding "." ensures viper finds config.yaml in cwd.
	viper.AddConfigPath(".")
}

// TestInitialize_DefaultConfig verifies that Initialize loads the default config.yaml
// and populates viper with expected keys.
func TestInitialize_DefaultConfig(t *testing.T) {
	initializeForTest(t)

	err := Initialize()
	if err != nil {
		t.Fatalf("Initialize() returned unexpected error: %v", err)
	}

	// Verify expected keys from config.yaml
	if ns := viper.GetString("data_namespace"); ns == "" {
		t.Error("expected 'data_namespace' to be set, got empty string")
	}

	if logLevel := viper.GetInt("log.level"); logLevel < 0 {
		t.Errorf("expected 'log.level' >= 0, got %d", logLevel)
	}

	if format := viper.GetString("log.format"); format == "" {
		t.Error("expected 'log.format' to be set, got empty string")
	}
}

// TestInitialize_DefaultValues verifies that Initialize sets the expected default
// values for keys not present in the config file.
func TestInitialize_DefaultValues(t *testing.T) {
	initializeForTest(t)

	err := Initialize()
	if err != nil {
		t.Fatalf("Initialize() returned unexpected error: %v", err)
	}

	// cache_cleanup_interval default
	interval := viper.GetInt("cache_cleanup_interval")
	if interval != 1800 {
		t.Errorf("expected cache_cleanup_interval=1800, got %d", interval)
	}

	// sync_customresource_interval_seconds default
	syncInterval := viper.GetInt("sync_customresource_interval_seconds")
	if syncInterval != 10 {
		t.Errorf("expected sync_customresource_interval_seconds=10, got %d", syncInterval)
	}

	// Alias: cache_clenup_interval should resolve to cache_cleanup_interval
	aliasVal := viper.GetInt("cache_clenup_interval")
	if aliasVal != interval {
		t.Errorf("expected alias cache_clenup_interval=%d, got %d", interval, aliasVal)
	}
}

// TestInitialize_ConfigFileValues verifies that specific values from the config.yaml
// file are loaded correctly.
func TestInitialize_ConfigFileValues(t *testing.T) {
	initializeForTest(t)

	err := Initialize()
	if err != nil {
		t.Fatalf("Initialize() returned unexpected error: %v", err)
	}

	// Verify data_namespace matches what's in config.yaml
	ns := viper.GetString("data_namespace")
	if ns != "middleware-operator" {
		t.Errorf("expected data_namespace='middleware-operator', got %q", ns)
	}

	// Verify cache_cleanup_interval from config.yaml (overrides default)
	interval := viper.GetInt("cache_cleanup_interval")
	if interval != 1800 {
		t.Errorf("expected cache_cleanup_interval=1800 from config.yaml, got %d", interval)
	}
}
