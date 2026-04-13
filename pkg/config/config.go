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

// Package config handles configuration file operations.
package config

import (
	"fmt"
	"github.com/spf13/viper"
	"os"
	yamlv2 "sigs.k8s.io/yaml/goyaml.v2"
)

/*
config.go handles configuration file operations.
*/

// Initialize initializes the configuration.
func Initialize() error {
	viper.SetConfigType("yaml")
	// Prefer the in-container mounted config directory (e.g. ConfigMap mounted at /pkg/config/config.yaml)
	// to ensure environment configs (feature gates, log level, etc.) take effect; fall back to built-in defaults.
	viper.AddConfigPath("/pkg/config")
	viper.AddConfigPath("./pkg/config")
	switch os.Getenv("opensaola_env") {
	case "dev":
		viper.SetConfigName("config-dev")
	case "prd":
		viper.SetConfigName("config-prd")
	default:
		viper.SetConfigName("config")
	}
	if err := viper.ReadInConfig(); err != nil {
		return fmt.Errorf("failed to read config file: %w", err)
	}

	if used := viper.ConfigFileUsed(); used != "" {
		fmt.Printf("Using config file: %s\n", used)
	}
	viper.SetDefault("cache_cleanup_interval", 1800)
	// SyncCustomResource default poll interval (seconds). Historical default was 1s, which put pressure on apiserver; changed to a gentler default, overridable via config.
	viper.SetDefault("sync_customresource_interval_seconds", 10)
	// Backward compatibility: cache_clenup_interval as an alias for cache_cleanup_interval
	viper.RegisterAlias("cache_clenup_interval", "cache_cleanup_interval")

	yamlv2.FutureLineWrap()
	fmt.Println("Configuration initialized successfully")
	return nil
}
