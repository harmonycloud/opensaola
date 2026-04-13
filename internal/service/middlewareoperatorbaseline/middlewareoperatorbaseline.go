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

package middlewareoperatorbaseline

import (
	"context"
	"fmt"
	"strings"

	"github.com/OpenSaola/opensaola/internal/cache"
	"github.com/OpenSaola/opensaola/internal/service/packages"
	"github.com/mohae/deepcopy"
	"k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/OpenSaola/opensaola/internal/k8s"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/util/retry"

	"github.com/OpenSaola/opensaola/api/v1"
	"github.com/OpenSaola/opensaola/internal/service/status"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// Check validates a MiddlewareOperatorBaseline
func Check(ctx context.Context, cli client.Client, m *v1.MiddlewareOperatorBaseline) error {
	defer func() {
		log.FromContext(ctx).Info("finished checking MiddlewareOperatorBaseline", "name", m.Name)
	}()
	log.FromContext(ctx).Info("checking MiddlewareOperatorBaseline", "name", m.Name)

	conditionChecked := status.GetCondition(ctx, &m.Status.Conditions, v1.CondTypeChecked)
	if conditionChecked.Status != metav1.ConditionTrue || conditionChecked.ObservedGeneration < m.Generation {
		if len(m.Spec.GVKs) > 0 {
			var checkErrs []string
			for _, gvk := range m.Spec.GVKs {
				if gvk.Name == "" {
					checkErrs = append(checkErrs, "name must not be empty")
				} else if gvk.Group == "" || gvk.Version == "" || gvk.Kind == "" {
					checkErrs = append(checkErrs, "GVK must not be empty")
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
		} else if len(m.Spec.GVKs) == 0 {
			msg := "gvks must not be empty"
			conditionChecked.Failed(ctx, msg, m.Generation)
		}

		err := k8s.UpdateMiddlewareOperatorBaselineStatus(ctx, cli, m)
		if err != nil {
			return fmt.Errorf("failed to update middlewareOperatorBaseline status: %w", err)
		}
	}
	return nil
}

var OperatorBaselineCache = cache.New[string, v1.MiddlewareOperatorBaseline](0)

func Get(ctx context.Context, cli client.Client, name, pkgName string) (v1.MiddlewareOperatorBaseline, error) {
	key := fmt.Sprintf("%s/%s", pkgName, name)

	baseline, err := k8s.GetMiddlewareOperatorBaseline(ctx, cli, name)
	if err != nil && !errors.IsNotFound(err) {
		return v1.MiddlewareOperatorBaseline{}, err
	}
	if baseline != nil && baseline.GetLabels()[v1.LabelPackageName] == pkgName {
		OperatorBaselineCache.Set(key, *baseline)
		return *baseline, nil
	}

	cached, ok := OperatorBaselineCache.Get(key)
	if ok {
		result := deepcopy.Copy(cached)
		return result.(v1.MiddlewareOperatorBaseline), nil
	} else {

		var baselines []*v1.MiddlewareOperatorBaseline
		baselines, err = packages.GetMiddlewareOperatorBaselines(ctx, cli, pkgName)
		if err != nil {
			return v1.MiddlewareOperatorBaseline{}, err
		}
		for _, middlewareOperatorBaseline := range baselines {
			if middlewareOperatorBaseline.Name == name {
				var metadata *packages.Metadata
				metadata, err = packages.GetMetadata(ctx, cli, pkgName)
				if err != nil {
					return v1.MiddlewareOperatorBaseline{}, err
				}
				lbs := make(labels.Set)
				lbs[v1.LabelComponent] = metadata.Name
				lbs[v1.LabelPackageVersion] = metadata.Version
				lbs[v1.LabelPackageName] = pkgName
				middlewareOperatorBaseline.Labels = lbs
				OperatorBaselineCache.Set(key, *middlewareOperatorBaseline)
				result := deepcopy.Copy(*middlewareOperatorBaseline)
				return result.(v1.MiddlewareOperatorBaseline), nil
			}
		}
	}

	return v1.MiddlewareOperatorBaseline{}, fmt.Errorf("configuration %s not found", name)

}

// UpdateStatus updates the MiddlewareOperatorBaseline status
func UpdateStatus(ctx context.Context, cli client.Client, m *v1.MiddlewareOperatorBaseline) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		// Get the CR
		m.Status.ObservedGeneration = m.Generation
		now, err := k8s.GetMiddlewareOperatorBaseline(ctx, cli, m.Name)
		if err != nil {
			return fmt.Errorf("get middleware operator baseline error: %w", err)
		}

		log.FromContext(ctx).V(1).Info("updating MiddlewareOperatorBaseline status", "version", now.ResourceVersion)
		now.Status = m.Status

		// Retry updating the CR
		err = cli.Status().Update(ctx, now)
		if err != nil {
			return fmt.Errorf("update middleware operator baseline status error: %w", err)
		}
		return nil
	})
}

// Deploy deploys a MiddlewareOperatorBaseline
func Deploy(ctx context.Context, cli client.Client, component, pkgverison, pkgname string, dryrun bool, m *v1.MiddlewareOperatorBaseline, owner metav1.Object) error {
	lbs := make(labels.Set)
	lbs[v1.LabelComponent] = component
	lbs[v1.LabelPackageVersion] = pkgverison
	lbs[v1.LabelPackageName] = pkgname
	m.Labels = lbs

	if owner != nil {
		// Create MiddlewareOperatorBaseline
		err := ctrl.SetControllerReference(owner, m, cli.Scheme())
		if err != nil {
			return err
		}
	}

	if !dryrun {
		return k8s.CreateMiddlewareOperatorBaseline(ctx, cli, m)
	}
	return nil
}
