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

// Package middlewarebaseline provides operations for MiddlewareBaseline resources
package middlewarebaseline

import (
	"context"
	"fmt"
	"sync"

	"github.com/OpenSaola/opensaola/api/v1"
	"github.com/OpenSaola/opensaola/pkg/k8s"
	"github.com/OpenSaola/opensaola/pkg/resource/logger"
	"github.com/OpenSaola/opensaola/pkg/service/packages"
	"github.com/OpenSaola/opensaola/pkg/service/status"
	"github.com/mohae/deepcopy"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Check validates a MiddlewareBaseline
func Check(ctx context.Context, cli client.Client, m *v1.MiddlewareBaseline) error {
	defer func() {
		logger.Log.Infoj(map[string]interface{}{
			"amsg": "finished checking MiddlewareBaseline",
			"name": m.Name,
		})
	}()
	logger.Log.Infoj(map[string]interface{}{
		"amsg": "checking MiddlewareBaseline",
		"name": m.Name,
	})

	conditionChecked := status.GetCondition(ctx, &m.Status.Conditions, v1.CondTypeChecked)
	if conditionChecked.Status != metav1.ConditionTrue || conditionChecked.ObservedGeneration < m.Generation {
		conditionChecked.Success(ctx, m.Generation)
		m.Status.State = v1.StateAvailable
		if err := k8s.UpdateMiddlewareBaselineStatus(ctx, cli, m); err != nil {
			return fmt.Errorf("failed to update middlewareBaseline status: %w", err)
		}
	}
	return nil
}

var Cache sync.Map

func Get(ctx context.Context, cli client.Client, name, pkgName string) (v1.MiddlewareBaseline, error) {
	var result interface{}

	key := fmt.Sprintf("%s/%s", pkgName, name)

	baseline, err := k8s.GetMiddlewareBaseline(ctx, cli, name)
	if err != nil && !errors.IsNotFound(err) {
		return v1.MiddlewareBaseline{}, err
	}
	if baseline != nil && baseline.GetLabels()[v1.LabelPackageName] == pkgName {
		Cache.Store(key, *baseline)
		return *baseline, nil
	}

	cache, ok := Cache.Load(key)
	if ok {
		result = deepcopy.Copy(cache)
		return result.(v1.MiddlewareBaseline), nil
	} else {

		var baselines []*v1.MiddlewareBaseline
		baselines, err = packages.GetMiddlewareBaselines(ctx, cli, pkgName)
		if err != nil {
			return v1.MiddlewareBaseline{}, err
		}
		for _, middlewareBaseline := range baselines {
			if middlewareBaseline.Name == name {
				var metadata *packages.Metadata
				metadata, err = packages.GetMetadata(ctx, cli, pkgName)
				if err != nil {
					return v1.MiddlewareBaseline{}, err
				}
				lbs := make(labels.Set)
				lbs[v1.LabelComponent] = metadata.Name
				lbs[v1.LabelPackageVersion] = metadata.Version
				lbs[v1.LabelPackageName] = pkgName
				middlewareBaseline.Labels = lbs
				Cache.Store(key, *middlewareBaseline)
				result = deepcopy.Copy(*middlewareBaseline)
				return result.(v1.MiddlewareBaseline), nil
			}
		}
	}

	return v1.MiddlewareBaseline{}, fmt.Errorf("middlewarebaseline %s not found", name)

}

// Deploy deploys a MiddlewareBaseline
func Deploy(ctx context.Context, cli client.Client, component, pkgverison, pkgname string, dryrun bool, m *v1.MiddlewareBaseline, owner metav1.Object) error {
	lbs := make(labels.Set)
	lbs[v1.LabelComponent] = component
	lbs[v1.LabelPackageVersion] = pkgverison
	lbs[v1.LabelPackageName] = pkgname
	m.Labels = lbs

	// Create MiddlewareBaseline
	if owner != nil {
		err := ctrl.SetControllerReference(owner, m, cli.Scheme())
		if err != nil {
			return err
		}
	}
	if !dryrun {
		return k8s.CreateMiddlewareBaseline(ctx, cli, m)
	}
	return nil
}
