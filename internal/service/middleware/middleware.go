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

package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"sigs.k8s.io/yaml"

	"github.com/opensaola/opensaola/internal/service/middlewarebaseline"
	"github.com/opensaola/opensaola/internal/service/packages"
	"github.com/opensaola/opensaola/pkg/tools"

	v1 "github.com/opensaola/opensaola/api/v1"
	"github.com/opensaola/opensaola/internal/k8s"
	"github.com/opensaola/opensaola/internal/service/consts"
	"github.com/opensaola/opensaola/internal/service/status"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// Check validates Middleware
func Check(ctx context.Context, cli client.Client, m *v1.Middleware) error {
	defer func() {
		log.FromContext(ctx).Info("finished validating Middleware", "name", m.Name, "namespace", m.Namespace)
	}()
	log.FromContext(ctx).Info("validating Middleware", "name", m.Name, "namespace", m.Namespace)

	conditionChecked := status.GetCondition(ctx, &m.Status.Conditions, v1.CondTypeChecked)
	if conditionChecked.Status != metav1.ConditionTrue || conditionChecked.ObservedGeneration < m.Generation {
		conditionChecked.Success(ctx, m.Generation)
		err := k8s.UpdateMiddlewareStatus(ctx, cli, m)
		if err != nil {
			return fmt.Errorf("failed to update Middleware status: %w", err)
		}
	}
	return nil
}

var updatingLocker sync.Mutex

func ReplacePackage(ctx context.Context, cli client.Client, m *v1.Middleware) error {
	// If the upgrade annotation exists, start the upgrade flow
	_, ok := m.GetAnnotations()[v1.LabelUpdate]
	if ok {
		updatingLocker.Lock()
		defer updatingLocker.Unlock()
		mo, err := k8s.GetMiddleware(ctx, cli, m.Name, m.Namespace)
		if err != nil {
			return fmt.Errorf("failed to get Middleware: %w", err)
		}
		if _, ok = mo.GetAnnotations()[v1.LabelUpdate]; !ok {
			return errors.New("no upgrade annotation found, skipping")
		}
		conditionChecked := status.GetCondition(ctx, &m.Status.Conditions, v1.CondTypeChecked)
		conditionUpdating := status.GetCondition(ctx, &m.Status.Conditions, v1.CondTypeUpdating)
		m.Status.State = v1.StateUpdating
		err = k8s.UpdateMiddlewareStatus(ctx, cli, m)
		if err != nil {
			return err
		}
		defer func() {
			if err != nil {
				log.FromContext(ctx).Error(err, "failed to upgrade Middleware")
				conditionUpdating.Failed(ctx, err.Error(), m.Generation)
			} else {
				log.FromContext(ctx).Info("Middleware upgrade succeeded")
				conditionUpdating.Success(ctx, m.Generation)
			}
			if updateErr := k8s.UpdateMiddlewareStatus(ctx, cli, m); updateErr != nil {
				log.FromContext(ctx).Error(updateErr, "failed to update Middleware status during upgrade")
				if err == nil {
					err = updateErr
				}
			}
		}()

		log.FromContext(ctx).Info("upgrading Middleware", "name", m.Name, "namespace", m.Namespace)
		if conditionChecked.Status == metav1.ConditionTrue {
			// Delete already published resources
			// err = HandleResource(ctx, cli, consts.HandleActionDelete, m)
			// if err != nil {
			// 	logger.Log.Errorf("failed to delete resources: %v", err)
			// 	return err
			// }

			// Get the new package
			var mp []v1.MiddlewarePackage
			mp, err = k8s.ListMiddlewarePackages(ctx, cli, client.MatchingLabels{
				v1.LabelComponent:      m.Labels[v1.LabelComponent],
				v1.LabelPackageVersion: m.Annotations[v1.LabelUpdate],
			})
			if err != nil {
				return err
			}

			if len(mp) != 1 {
				err = fmt.Errorf("failed to get new package: expected 1 package but got %d", len(mp))
				return err
			}

			log.FromContext(ctx).Info("upgrading in progress", "name", m.Name, "namespace", m.Namespace, "package", mp[0].Name, "version", m.Annotations[v1.LabelUpdate], "baseline", m.Annotations[v1.LabelBaseline])

			// Do not continue parsing/switching baseline when package is not ready: avoid tight loop stuck in Updating state.
			enabled, installErr, statusErr := packages.GetInstallStatus(ctx, cli, mp[0].Name)
			if statusErr != nil || !enabled {
				if statusErr == nil && installErr != "" {
					// Terminal failure: package content installation failed (e.g., invalid resource naming). Mark upgrade as failed and clear upgrade annotation to avoid 5s requeue spam.
					err = fmt.Errorf("%w: %s", consts.ErrPackageInstallFailed, installErr)
					if mo != nil {
						if mo.Annotations != nil {
							delete(mo.Annotations, v1.LabelUpdate)
						}
						if updateErr := k8s.UpdateMiddleware(ctx, cli, mo); updateErr != nil {
						log.FromContext(ctx).Info("failed to clear upgrade annotation on Middleware", "warning", true, "namespace", mo.Namespace, "name", mo.Name, "err", updateErr)
					}
					}
					if m.Annotations != nil {
						delete(m.Annotations, v1.LabelUpdate)
					}
					return err
				}
				return consts.ErrPackageNotReady
			}

			// Parse the new package
			var baseline *v1.MiddlewareBaseline
			baseline, err = packages.GetMiddlewareBaseline(ctx, cli, m.Annotations[v1.LabelBaseline], mp[0].Name)
			if err != nil {
				return err
			}
			log.FromContext(ctx).Info("upgrading [querying baseline]", "name", m.Name, "namespace", m.Namespace, "package", mp[0].Name, "version", m.Annotations[v1.LabelUpdate], "baseline", baseline.Name)

			// Save original state; roll back in-memory changes if subsequent cluster write fails,
			// preventing the controller defer from reading polluted Annotations/Labels/Spec.
			origBaseline := m.Spec.Baseline
			origLabels := m.Labels
			origAnnotations := m.Annotations
			rollback := func() {
				m.Spec.Baseline = origBaseline
				m.Labels = origLabels
				m.Annotations = origAnnotations
			}

			m.Spec.Baseline = baseline.Name
			// Copy maps before modification to avoid mutating origLabels/origAnnotations references
			newLabels := make(map[string]string, len(m.Labels))
			for k, v := range m.Labels {
				newLabels[k] = v
			}
			newLabels[v1.LabelPackageVersion] = m.Annotations[v1.LabelUpdate]
			newLabels[v1.LabelPackageName] = mp[0].Name
			m.Labels = newLabels
			newAnnotations := make(map[string]string, len(m.Annotations))
			for k, v := range m.Annotations {
				if k != v1.LabelUpdate {
					newAnnotations[k] = v
				}
			}
			m.Annotations = newAnnotations

			// Check once; if not satisfied, return a sentinel error for controller RequeueAfter
			pkg, pkgErr := packages.Get(ctx, cli, m.Labels[v1.LabelPackageName])
			if pkgErr != nil || pkg == nil || !pkg.Enabled {
				rollback()
				return consts.ErrPackageNotReady
			}
			err = k8s.UpdateMiddleware(ctx, cli, m)
			if err != nil {
				rollback()
				return err
			}
			// Refresh cache after successful upgrade
			if _, err := k8s.GetMiddleware(ctx, cli, m.Name, m.Namespace); err != nil {
				log.FromContext(ctx).Info("failed to refresh Middleware cache", "warning", true, "namespace", m.Namespace, "name", m.Name, "err", err)
			}
		}
	}
	return nil
}

var NecessaryIgnore = []string{
	"repository",
}

// TemplateParseWithBaseline parses and merges templates with the baseline
func TemplateParseWithBaseline(ctx context.Context, cli client.Client, m *v1.Middleware) (err error) {
	conditionTemplateParseWithBaseline := status.GetCondition(ctx, &m.Status.Conditions, v1.CondTypeTemplateParseWithBaseline)
	defer func() {
		if err != nil {
			log.FromContext(ctx).Error(err, "TemplateParseWithBaseline error")
			conditionTemplateParseWithBaseline.Failed(ctx, err.Error(), m.Generation)
		} else {
			log.FromContext(ctx).Info("TemplateParseWithBaseline finished")
			conditionTemplateParseWithBaseline.Success(ctx, m.Generation)
		}
		if updateErr := k8s.UpdateMiddlewareStatus(ctx, cli, m); updateErr != nil {
			log.FromContext(ctx).Error(updateErr, "update middleware status error")
			if err == nil {
				err = updateErr
			}
		}
	}()

	// Get the baseline template
	baseline, err := middlewarebaseline.Get(ctx, cli, m.Spec.Baseline, m.Labels[v1.LabelPackageName])
	if err != nil {
		return fmt.Errorf("get baseline error: %w", err)
	}

	err = tools.StructMerge(&baseline.Spec.Parameters, &m.Spec.Parameters, tools.StructMergeMapType)
	if err != nil {
		return fmt.Errorf("struct merge error: %w", err)
	}

	err = tools.StructMerge(&baseline.Spec.Configurations, &m.Spec.Configurations, tools.StructMergeArrayType)
	if err != nil {
		return fmt.Errorf("struct merge error: %w", err)
	}

	err = tools.StructMerge(&baseline.Labels, &m.Labels, tools.StructMergeMapType)
	if err != nil {
		return fmt.Errorf("struct merge error: %w", err)
	}

	err = tools.StructMerge(&baseline.Annotations, &m.Annotations, tools.StructMergeMapType)
	if err != nil {
		return fmt.Errorf("struct merge error: %w", err)
	}

	for idx, baselinePre := range baseline.Spec.PreActions {
		if !baselinePre.Fixed {
			for _, pre := range m.Spec.PreActions {
				if pre.Name == baselinePre.Name {
					err = tools.StructMerge(&baselinePre, &pre, tools.StructMergeMapType)
					if err != nil {
						return fmt.Errorf("struct merge error: %w", err)
					}
					baseline.Spec.PreActions[idx] = pre
					break
				}
			}
		}
	}

	m.Spec.PreActions = baseline.Spec.PreActions

	// Check whether required parameters are present
	var (
		necessaryMap, necessaryBaselineMap     map[string]any
		necessaryBytes, necessaryBaselineBytes []byte
	)

	necessaryMap = make(map[string]any)
	necessaryBytes, err = m.Spec.Necessary.MarshalJSON()
	if err != nil {
		return fmt.Errorf("marshal necessary error: %w", err)
	}
	err = json.Unmarshal(necessaryBytes, &necessaryMap)
	if err != nil {
		return fmt.Errorf("unmarshal necessary error: %w", err)
	}

	necessaryBaselineMap = make(map[string]any)
	necessaryBaselineBytes, err = baseline.Spec.Necessary.MarshalJSON()
	if err != nil {
		return fmt.Errorf("marshal necessary error: %w", err)
	}
	err = json.Unmarshal(necessaryBaselineBytes, &necessaryBaselineMap)
	if err != nil {
		return fmt.Errorf("unmarshal necessary error: %w", err)
	}

	for k := range necessaryBaselineMap {
		var isIgnore bool
		for _, ignore := range NecessaryIgnore {
			if k == ignore {
				isIgnore = true
				break
			}
		}
		if isIgnore {
			continue
		}
		if _, ok := necessaryMap[k]; !ok {
			return errors.New("required parameter missing: " + k)
		}
	}

	var parametersYamlBytes []byte

	parametersYamlBytes, err = yaml.Marshal(m.Spec.Parameters)
	if err != nil {
		return err
	}

	parametersYamlBytes = tools.ProcessYAMLGoTemp(parametersYamlBytes)

	var templateValues *tools.TemplateValues
	templateValues, err = tools.GetTemplateValues(ctx, m)
	if err != nil {
		return fmt.Errorf("get template values error: %w", err)
	}

	var parametersParse string
	parametersParse, err = tools.TemplateParse(ctx, string(parametersYamlBytes), templateValues)
	if err != nil {
		return fmt.Errorf("template parse error: %w", err)
	}

	err = yaml.Unmarshal([]byte(parametersParse), &m.Spec.Parameters)
	if err != nil {
		return fmt.Errorf("unmarshal mid error: %w", err)
	}

	var preActionsYamlBytes []byte
	preActionsYamlBytes, err = yaml.Marshal(m.Spec.PreActions)
	if err != nil {
		return fmt.Errorf("marshal pre actions error: %w", err)
	}

	var preActionsParse string
	preActionsParse, err = tools.TemplateParse(ctx, string(preActionsYamlBytes), templateValues)
	if err != nil {
		return fmt.Errorf("template parse error: %w", err)
	}

	err = yaml.Unmarshal([]byte(preActionsParse), &m.Spec.PreActions)
	if err != nil {
		return fmt.Errorf("unmarshal pre actions error: %w", err)
	}

	// Then process configurations
	var configurationsYamlBytes []byte
	configurationsYamlBytes, err = yaml.Marshal(m.Spec.Configurations)
	if err != nil {
		return err
	}

	configurationsYamlBytes = tools.ProcessYAMLGoTemp(configurationsYamlBytes)

	parameters := make(map[string]any)
	err = yaml.Unmarshal([]byte(parametersParse), &parameters)
	if err != nil {
		return err
	}
	templateValues.Parameters = parameters

	var configurationsParse string
	configurationsParse, err = tools.TemplateParse(ctx, string(configurationsYamlBytes), templateValues)
	if err != nil {
		return fmt.Errorf("template parse error: %w", err)
	}

	err = yaml.Unmarshal([]byte(configurationsParse), &m.Spec.Configurations)
	if err != nil {
		return fmt.Errorf("unmarshal mid error: %w", err)
	}

	// Process metadata
	var metadataYamlBytes []byte
	metadataYamlBytes, err = yaml.Marshal(m.ObjectMeta)
	if err != nil {
		return err
	}
	metadataYamlBytes = tools.ProcessYAMLGoTemp(metadataYamlBytes)

	var metadataParse string
	metadataParse, err = tools.TemplateParse(ctx, string(metadataYamlBytes), templateValues)
	if err != nil {
		return fmt.Errorf("template parse error: %w", err)
	}
	err = yaml.Unmarshal([]byte(metadataParse), &m.ObjectMeta)
	if err != nil {
		return fmt.Errorf("unmarshal metadata error: %w", err)
	}

	return nil
}
