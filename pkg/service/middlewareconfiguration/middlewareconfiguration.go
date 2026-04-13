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

package middlewareconfiguration

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/OpenSaola/opensaola/pkg/service/packages"
	"k8s.io/apimachinery/pkg/api/errors"

	"github.com/OpenSaola/opensaola/pkg/k8s"
	"k8s.io/apimachinery/pkg/labels"

	v1 "github.com/OpenSaola/opensaola/api/v1"
	"github.com/OpenSaola/opensaola/pkg/resource/logger"
	"github.com/OpenSaola/opensaola/pkg/service/consts"
	"github.com/OpenSaola/opensaola/pkg/service/status"
	"github.com/OpenSaola/opensaola/pkg/tools"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Check validates a MiddlewareConfiguration
func Check(ctx context.Context, cli client.Client, m *v1.MiddlewareConfiguration) error {
	defer func() {
		logger.Log.Infoj(map[string]interface{}{
			"amsg": "finished validating MiddlewareConfiguration",
			"name": m.Name,
		})
	}()
	logger.Log.Infoj(map[string]interface{}{
		"amsg": "validating MiddlewareConfiguration",
		"name": m.Name,
	})

	conditionChecked := status.GetCondition(ctx, &m.Status.Conditions, v1.CondTypeChecked)
	if conditionChecked.Status != metav1.ConditionTrue || conditionChecked.ObservedGeneration < m.Generation {
		conditionChecked.Success(ctx, m.Generation)
		m.Status.State = v1.StateAvailable
		if err := k8s.UpdateMiddlewareConfigurationStatus(ctx, cli, m); err != nil {
			return fmt.Errorf("failed to update MiddlewareConfiguration status: %w", err)
		}
	}

	return nil
}

var Cache sync.Map

func Get(ctx context.Context, cli client.Client, name, pkgName string) (v1.MiddlewareConfiguration, error) {
	key := fmt.Sprintf("%s/%s", pkgName, name)

	configuration, err := k8s.GetMiddlewareConfiguration(ctx, cli, name)
	if err != nil && !errors.IsNotFound(err) {
		return v1.MiddlewareConfiguration{}, err
	}
	if configuration != nil && configuration.GetLabels()[v1.LabelPackageName] == pkgName {
		Cache.Store(key, *configuration)
		return *configuration, nil
	}

	cache, ok := Cache.Load(key)
	if ok {
		return cache.(v1.MiddlewareConfiguration), nil
	} else {
		var configurations map[string]*v1.MiddlewareConfiguration
		configurations, err = packages.GetConfigurations(ctx, cli, pkgName)
		if err != nil {
			return v1.MiddlewareConfiguration{}, err
		}
		if configuration, ok = configurations[name]; ok {
			var metadata *packages.Metadata
			metadata, err = packages.GetMetadata(ctx, cli, pkgName)
			if err != nil {
				return v1.MiddlewareConfiguration{}, err
			}
			lbs := make(labels.Set)
			lbs[v1.LabelComponent] = metadata.Name
			lbs[v1.LabelPackageVersion] = metadata.Version
			lbs[v1.LabelPackageName] = pkgName
			configuration.Labels = lbs
			Cache.Store(key, *configuration)
			return *configuration, nil
		}
	}

	return v1.MiddlewareConfiguration{}, fmt.Errorf("configuration %s not found", name)

}

func GetTemplateParsedMiddlewareConfigurations(ctx context.Context, cli client.Client, act consts.HandleAction, quoter tools.Quoter) ([]*v1.MiddlewareConfiguration, error) {
	var middlewareConfigurations []*v1.MiddlewareConfiguration
	templateValues, err := tools.GetTemplateValues(ctx, quoter)
	if err != nil {
		return nil, fmt.Errorf("failed to get template values: %w", err)
	}

	for _, cfg := range quoter.GetConfigurations() {
		var mc v1.MiddlewareConfiguration

		mc, err = Get(ctx, cli, cfg.Name, quoter.GetLabels()[v1.LabelPackageName])
		if err != nil {
			return nil, fmt.Errorf("get configuration %s error: %w", cfg.Name, err)
		}

		// Assign Values
		if len(cfg.Values.Raw) > 0 {
			var data []byte
			data, err = cfg.Values.MarshalJSON()
			if err != nil {
				return nil, err
			}

			var parse string
			parse, err = tools.TemplateParse(ctx, string(data), templateValues)
			if err != nil {
				return nil, err
			}
			err = json.Unmarshal([]byte(parse), &templateValues.Values)
			if err != nil {
				return nil, err
			}
		}

		var template string
		template, err = handleTemplate(ctx, templateValues, &mc)
		if err != nil {
			return nil, fmt.Errorf("failed to handle template: %s %w", cfg.Name, err)
		}
		mc.Spec.Template = template
		middlewareConfigurations = append(middlewareConfigurations, &mc)
	}

	return middlewareConfigurations, nil
}
