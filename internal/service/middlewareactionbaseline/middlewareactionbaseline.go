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

package middlewareactionbaseline

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/OpenSaola/opensaola/internal/service/packages"
	"k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"

	"cuelang.org/go/cue/cuecontext"
	"github.com/OpenSaola/opensaola/api/v1"
	"github.com/OpenSaola/opensaola/internal/k8s"
	"github.com/OpenSaola/opensaola/internal/resource/logger"
	"github.com/OpenSaola/opensaola/internal/service/status"
	"github.com/mohae/deepcopy"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Check validates a MiddlewareActionBaseline
func Check(ctx context.Context, cli client.Client, m *v1.MiddlewareActionBaseline) error {
	defer func() {
		logger.Log.Infoj(map[string]interface{}{
			"amsg": "finished validating MiddlewareActionBaseline",
			"name": m.Name,
		})
	}()
	logger.Log.Infoj(map[string]interface{}{
		"amsg": "validating MiddlewareActionBaseline",
		"name": m.Name,
	})

	conditionChecked := status.GetCondition(ctx, &m.Status.Conditions, v1.CondTypeChecked)
	if conditionChecked.Status != metav1.ConditionTrue || conditionChecked.ObservedGeneration < m.Generation {
		if len(m.Spec.Steps) > 0 {
			var checkErrs []string
			for _, step := range m.Spec.Steps {
				if err := checkStep(ctx, step, m); err != nil {
					checkErrs = append(checkErrs, fmt.Sprintf("%s validation failed: %s", step.Name, err.Error()))
				}
			}
			if len(checkErrs) > 0 {
				msg := strings.Join(checkErrs, ";")
				conditionChecked.Failed(ctx, msg, m.Generation)
				m.Status.State = v1.StateUnavailable
			} else {
				conditionChecked.Success(ctx, m.Generation)
				m.Status.State = v1.StateAvailable
			}
		} else if len(m.Spec.Steps) == 0 {
			msg := "steps must not be empty"
			conditionChecked.Failed(ctx, msg, m.Generation)
		}
		err := k8s.UpdateMiddlewareActionBaselineStatus(ctx, cli, m)
		if err != nil {
			return fmt.Errorf("failed to update MiddlewareActionBaseline status: %w", err)
		}

	}
	return nil
}

var Cache sync.Map

func Get(ctx context.Context, cli client.Client, name, pkgName string) (v1.MiddlewareActionBaseline, error) {
	var result interface{}
	key := fmt.Sprintf("%s/%s", pkgName, name)

	baseline, err := k8s.GetMiddlewareActionBaseline(ctx, cli, name)
	if err != nil && !errors.IsNotFound(err) {
		return v1.MiddlewareActionBaseline{}, err
	}
	if baseline != nil && baseline.GetLabels()[v1.LabelPackageName] == pkgName {
		Cache.Store(key, *baseline)
		return *baseline, nil
	}

	cache, ok := Cache.Load(key)
	if ok {
		// cacheStruct := cache.(v1.MiddlewareActionBaseline)
		result = deepcopy.Copy(cache)
		return result.(v1.MiddlewareActionBaseline), nil
	} else {

		var baselines []*v1.MiddlewareActionBaseline
		baselines, err = packages.GetMiddlewareActionBaselines(ctx, cli, pkgName)
		if err != nil {
			return v1.MiddlewareActionBaseline{}, err
		}
		for _, actionBaseline := range baselines {
			if actionBaseline.Name == name {
				var metadata *packages.Metadata
				metadata, err = packages.GetMetadata(ctx, cli, pkgName)
				if err != nil {
					return v1.MiddlewareActionBaseline{}, err
				}
				lbs := make(labels.Set)
				lbs[v1.LabelComponent] = metadata.Name
				lbs[v1.LabelPackageVersion] = metadata.Version
				lbs[v1.LabelPackageName] = pkgName
				actionBaseline.Labels = lbs
				Cache.Store(key, *actionBaseline)
				result = deepcopy.Copy(*actionBaseline)
				return result.(v1.MiddlewareActionBaseline), nil
			}
		}
	}

	return v1.MiddlewareActionBaseline{}, fmt.Errorf("configuration %s not found", name)

}

// checkStep validates a Step
func checkStep(ctx context.Context, step v1.Step, m *v1.MiddlewareActionBaseline) error {
	if step.Name == "" {
		return fmt.Errorf("name must not be empty")
	} else {
		if step.CUE != "" {
			// check cue
			// err := checkCUE(ctx, step.CUE, m)
			// if err != nil {
			// 	return fmt.Errorf("failed to validate CUE: %w", err)
			// }
		}
		// TODO: check http
		// TODO: check cmd
	}
	return nil
}

// checkCUE validates a CUE template
func checkCUE(ctx context.Context, cueString string, m *v1.MiddlewareActionBaseline) error {
	cueCompiler := cuecontext.New().CompileString(cueString)
	if cueCompiler.Err() != nil {
		return fmt.Errorf("failed to compile CUE: %w", cueCompiler.Err())
	}
	fields, err := cueCompiler.Fields()
	if err != nil {
		return fmt.Errorf("failed to get CUE fields: %w", err)
	}
	fieldsMap := make(map[string]struct{})
	for fields.Next() {
		fieldsMap[fields.Label()] = struct{}{}
	}
	if _, ok := fieldsMap["output"]; !ok {
		return fmt.Errorf("output must not be empty")
	}
	if _, ok := fieldsMap["resource"]; !ok {
		return fmt.Errorf("resource must not be empty")
	}
	if _, ok := fieldsMap["parameter"]; !ok {
		return fmt.Errorf("parameter must not be empty")
	}
	return nil
}

// Deploy deploys a MiddlewareActionBaseline
func Deploy(ctx context.Context, cli client.Client, component, pkgverison, pkgname string, dryrun bool, m *v1.MiddlewareActionBaseline, owner metav1.Object) error {
	lbs := make(labels.Set)
	lbs[v1.LabelComponent] = component
	lbs[v1.LabelPackageVersion] = pkgverison
	lbs[v1.LabelPackageName] = pkgname
	m.Labels = lbs
	m.CreationTimestamp = metav1.Now()

	if owner != nil {
		// Create MiddlewareActionBaseline
		err := ctrl.SetControllerReference(owner, m, cli.Scheme())
		if err != nil {
			return err
		}
	}
	if !dryrun {
		return k8s.CreateMiddlewareActionBaseline(ctx, cli, m)
	}
	return nil
}
