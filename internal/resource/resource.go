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

// Package resource handles resource initialization.
package resource

import (
	"context"
	"time"

	"github.com/OpenSaola/opensaola/internal/k8s"
	"github.com/OpenSaola/opensaola/internal/resource/logger"
	"github.com/OpenSaola/opensaola/internal/service/middlewareactionbaseline"
	"github.com/OpenSaola/opensaola/internal/service/middlewarebaseline"
	"github.com/OpenSaola/opensaola/internal/service/middlewareconfiguration"
	"github.com/OpenSaola/opensaola/internal/service/middlewareoperatorbaseline"
	"github.com/rs/zerolog"
	"github.com/spf13/viper"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

/*
resource.go handles resource package operations.
*/

// Initialize initializes resources.
func Initialize() {
	lv := viper.GetInt("log.level")
	logger.Initialize(zerolog.Level(lv))
}

func InitCacheCleanupTimer(ctx context.Context) {
	ticker := time.NewTicker(time.Duration(viper.GetInt64("cache_cleanup_interval")) * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			middlewareconfiguration.ConfigCache.Clear()
			middlewarebaseline.BaselineCache.Clear()
			middlewareoperatorbaseline.OperatorBaselineCache.Clear()
			middlewareactionbaseline.ActionBaselineCache.Clear()
		}
	}
}

func InitActionsCleanupTimer(ctx context.Context, cli client.Client) {
	ticker := time.NewTicker(600 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			actions, err := k8s.ListMiddlewareActions(ctx, cli, "", nil)
			if err != nil {
				logger.Log.Errorf("list middleware actions error: %v", err)
				continue
			}
			for _, action := range actions {
				if -action.GetCreationTimestamp().Sub(time.Now()) > (time.Duration(viper.GetInt64("cache_cleanup_interval")) * time.Second) {
					_ = k8s.DeleteMiddlewareAction(ctx, cli, &action)
				}
			}
		}
	}
}
