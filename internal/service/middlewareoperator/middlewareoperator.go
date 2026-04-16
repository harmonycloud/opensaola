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

package middlewareoperator

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/harmonycloud/opensaola/internal/service/middlewareaction"
	"github.com/harmonycloud/opensaola/internal/service/middlewareoperatorbaseline"
	"github.com/harmonycloud/opensaola/internal/service/packages"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/yaml"

	"github.com/harmonycloud/opensaola/api/v1"
	"github.com/harmonycloud/opensaola/internal/k8s"
	"github.com/harmonycloud/opensaola/internal/service/consts"
	"github.com/harmonycloud/opensaola/internal/service/middlewareconfiguration"
	"github.com/harmonycloud/opensaola/internal/service/status"
	"github.com/harmonycloud/opensaola/pkg/tools"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const defaultUpgradePackageUnavailableTimeout = 50 * time.Second

/*
File: middlewareOperator.go defines MiddlewareOperator-related operations
including but not limited to: resource fetching, validation, status updates, RBAC generation, deployment generation, field mapping, etc.
*/

func skipNoOperatorResource(ctx context.Context, cli client.Client, m *v1.MiddlewareOperator) error {
	if IsNoOperatorResource(m) {
		m.Status.State = v1.StateAvailable
		return consts.NoOperator
	}
	return nil
}

func IsNoOperatorResource(m *v1.MiddlewareOperator) bool {
	if m == nil {
		return false
	}
	return m.GetAnnotations()[v1.LabelNoOperator] == "true"
}

// Step1: MiddlewareOperator validation

// Check validates MiddlewareOperator
func Check(ctx context.Context, cli client.Client, m *v1.MiddlewareOperator) error {
	conditionChecked := status.GetCondition(ctx, &m.Status.Conditions, v1.CondTypeChecked)
	defer func() {
		log.FromContext(ctx).Info("finished validating MiddlewareOperator", "name", m.Name, "namespace", m.Namespace)
	}()
	log.FromContext(ctx).Info("validating MiddlewareOperator", "name", m.Name, "namespace", m.Namespace)

	if err := skipNoOperatorResource(ctx, cli, m); err != nil {
		conditionChecked.Success(ctx, m.Generation)
	} else {
		if m.Spec.Baseline == "" {
			msg := "baseline must not be empty"
			conditionChecked.Failed(ctx, msg, m.Generation)
		} else {
			conditionChecked.Success(ctx, m.Generation)
		}
	}

	err := k8s.UpdateMiddlewareOperatorStatus(ctx, cli, m)
	if err != nil {
		return fmt.Errorf("failed to update MiddlewareOperator status: %w", err)
	}
	// }
	return nil
}

var updatingLocker sync.Mutex

func clearUpgradeAnnotation(ctx context.Context, cli client.Client, live, current *v1.MiddlewareOperator) {
	if live != nil && live.Annotations != nil {
		delete(live.Annotations, v1.LabelUpdate)
		if err := k8s.UpdateMiddlewareOperator(ctx, cli, live); err != nil {
			log.FromContext(ctx).Info("failed to clear upgrade annotation on MiddlewareOperator", "warning", true, "namespace", live.Namespace, "name", live.Name, "err", err)
		}
	}
	if current != nil && current.Annotations != nil {
		delete(current.Annotations, v1.LabelUpdate)
	}
}

func packageUnavailableMessage(targetVersion, reason string) string {
	return fmt.Sprintf("waiting for upgrade target package to be ready: version=%s, reason=%s", targetVersion, reason)
}

func markPackageUnavailable(ctx context.Context, conditionUpdating *status.Condition, targetVersion, reason string, og int64) error {
	if conditionUpdating == nil || conditionUpdating.Condition == nil {
		return consts.ErrPackageNotReady
	}
	message := packageUnavailableMessage(targetVersion, reason)
	if conditionUpdating.Status != metav1.ConditionUnknown || !strings.Contains(conditionUpdating.Message, fmt.Sprintf("version=%s", targetVersion)) {
		conditionUpdating.Status = metav1.ConditionUnknown
		conditionUpdating.LastTransitionTime = metav1.Now()
	}
	conditionUpdating.ObservedGeneration = og
	conditionUpdating.Reason = v1.CondReasonIniting
	conditionUpdating.Message = message
	if time.Since(conditionUpdating.LastTransitionTime.Time) < defaultUpgradePackageUnavailableTimeout {
		log.FromContext(ctx).Info("upgrade target package temporarily unavailable", "warning", true, "message", message)
		return consts.ErrPackageNotReady
	}
	return fmt.Errorf("%w: %s, timeout=%s", consts.ErrPackageUnavailableExceeded, message, defaultUpgradePackageUnavailableTimeout)
}

func ReplacePackage(ctx context.Context, cli client.Client, m *v1.MiddlewareOperator) error {
	// If the upgrade annotation exists, start the upgrade flow
	_, ok := m.GetAnnotations()[v1.LabelUpdate]
	if ok {
		updatingLocker.Lock()
		defer updatingLocker.Unlock()
		mo, err := k8s.GetMiddlewareOperator(ctx, cli, m.Name, m.Namespace)
		if err != nil {
			return fmt.Errorf("failed to get MiddlewareOperator: %w", err)
		}
		if _, ok = mo.GetAnnotations()[v1.LabelUpdate]; !ok {
			return errors.New("no upgrade annotation found, skipping")
		}
		conditionChecked := status.GetCondition(ctx, &m.Status.Conditions, v1.CondTypeChecked)
		conditionUpdating := status.GetCondition(ctx, &m.Status.Conditions, v1.CondTypeUpdating)
		m.Status.State = v1.StateUpdating
		err = k8s.UpdateMiddlewareOperatorStatus(ctx, cli, m)
		if err != nil {
			return err
		}
		defer func() {
			if err != nil && !errors.Is(err, consts.ErrPackageNotReady) {
				log.FromContext(ctx).Error(err, "failed to upgrade MiddlewareOperator")
				conditionUpdating.Failed(ctx, err.Error(), m.Generation)
			} else if err == nil {
				log.FromContext(ctx).Info("MiddlewareOperator upgrade succeeded")
				conditionUpdating.Success(ctx, m.Generation)
			}
			if updateErr := k8s.UpdateMiddlewareOperatorStatus(ctx, cli, m); updateErr != nil {
				log.FromContext(ctx).Error(updateErr, "failed to update MiddlewareOperator status during upgrade")
				if err == nil {
					err = updateErr
				}
			}
		}()

		log.FromContext(ctx).Info("upgrading MiddlewareOperator", "name", m.Name, "namespace", m.Namespace)
		if conditionChecked.Status == metav1.ConditionTrue {
			targetVersion := m.Annotations[v1.LabelUpdate]

			// Get the new package
			var mp []v1.MiddlewarePackage
			mp, err = k8s.ListMiddlewarePackages(ctx, cli, client.MatchingLabels{
				v1.LabelComponent:      m.Labels[v1.LabelComponent],
				v1.LabelPackageVersion: targetVersion,
			})
			if err != nil {
				return err
			}

			if len(mp) != 1 {
				err = markPackageUnavailable(
					ctx,
					conditionUpdating,
					targetVersion,
					fmt.Sprintf("failed to get new package: expected 1 package but got %d", len(mp)),
					m.Generation,
				)
				if errors.Is(err, consts.ErrPackageUnavailableExceeded) {
					clearUpgradeAnnotation(ctx, cli, mo, m)
				}
				return err
			}

			// Do not continue parsing/switching baseline when package is not ready: avoid tight loop stuck in Updating state.
			enabled, installErr, statusErr := packages.GetInstallStatus(ctx, cli, mp[0].Name)
			if statusErr != nil || !enabled {
				if statusErr == nil && installErr != "" {
					// Terminal failure: package content installation failed (e.g., invalid resource naming). Mark upgrade as failed and clear upgrade annotation to avoid 5s requeue spam.
					err = fmt.Errorf("%w: %s", consts.ErrPackageInstallFailed, installErr)
					clearUpgradeAnnotation(ctx, cli, mo, m)
					return err
				}
				reason := fmt.Sprintf("target package not ready: enabled=%t", enabled)
				if statusErr != nil {
					reason = fmt.Sprintf("failed to get target package status: %v", statusErr)
				}
				err = markPackageUnavailable(ctx, conditionUpdating, targetVersion, reason, m.Generation)
				if errors.Is(err, consts.ErrPackageUnavailableExceeded) {
					clearUpgradeAnnotation(ctx, cli, mo, m)
				}
				return err
			}

			// Parse the new package
			if err := skipNoOperatorResource(ctx, cli, m); err == nil {
				_, err = packages.GetMiddlewareOperatorBaseline(ctx, cli, m.Annotations[v1.LabelBaseline], mp[0].Name)
				if err != nil {
					return err
				}
			}

			// Save original state; roll back in-memory changes if subsequent cluster write fails,
			// preventing the controller defer from reading polluted Annotations/Labels/Spec.
			origSpec := m.Spec
			origLabels := m.Labels
			origAnnotations := m.Annotations
			rollback := func() {
				m.Spec = origSpec
				m.Labels = origLabels
				m.Annotations = origAnnotations
			}

			oldGlobe := m.Spec.Globe
			oldPreActions := m.Spec.PreActions
			m.Spec = v1.MiddlewareOperatorSpec{}
			m.Spec.Globe = oldGlobe
			m.Spec.PreActions = oldPreActions
			m.Spec.Baseline = m.Annotations[v1.LabelBaseline]
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
				reason := "validation failed after switching to new package"
				if pkgErr != nil {
					reason = fmt.Sprintf("failed to re-fetch package after switching: %v", pkgErr)
				} else if pkg == nil {
					reason = "package is nil after switching to new package"
				} else if !pkg.Enabled {
					reason = fmt.Sprintf("package still not enabled after switching: %s", m.Labels[v1.LabelPackageName])
				}
				rollback()
				err = markPackageUnavailable(ctx, conditionUpdating, targetVersion, reason, m.Generation)
				if errors.Is(err, consts.ErrPackageUnavailableExceeded) {
					clearUpgradeAnnotation(ctx, cli, mo, m)
				}
				return err
			}

			err = k8s.UpdateMiddlewareOperator(ctx, cli, m)
			if err != nil {
				rollback()
				return err
			}
			// Refresh cache after successful upgrade
			if _, err := k8s.GetMiddlewareOperator(ctx, cli, m.Name, m.Namespace); err != nil {
				log.FromContext(ctx).Info("failed to refresh MiddlewareOperator cache", "warning", true, "namespace", m.Namespace, "name", m.Name, "err", err)
			}
		}
	}
	return nil
}

// Step3: MiddlewareOperator extra resource publishing
// handleExtraResource handles extra resources
func handleExtraResource(ctx context.Context, cli client.Client, act consts.HandleAction, m *v1.MiddlewareOperator) (err error) {
	conditionBuildExtraResource := status.GetCondition(ctx, &m.Status.Conditions, v1.CondTypeBuildExtraResource)
	defer func() {
		if act != consts.HandleActionDelete {
			if err != nil {
				log.FromContext(ctx).Error(err, "extra resource error", "action", act)
				conditionBuildExtraResource.Failed(ctx, err.Error(), m.Generation)
			} else {
				log.FromContext(ctx).Info("extra resource finished", "action", act)
				conditionBuildExtraResource.Success(ctx, m.Generation)
			}
			updateErr := k8s.UpdateMiddlewareOperatorStatus(ctx, cli, m)
			if updateErr != nil {
				log.FromContext(ctx).Error(updateErr, "update middleware operator status error")
			}
		}
	}()

	var (
		errorList []string
		mcs       []*v1.MiddlewareConfiguration
	)

	switch act {
	case consts.HandleActionDelete:
		return middlewareconfiguration.DeleteTemplateRenderedResources(ctx, cli, m, m)

	case consts.HandleActionPublish, consts.HandleActionUpdate:
		mcs, err = middlewareconfiguration.GetTemplateParsedMiddlewareConfigurations(ctx, cli, act, m)
		if err != nil {
			return err
		}
		for _, mc := range mcs {
			err = middlewareconfiguration.Handle(ctx, cli, m, act, mc)
			if err != nil {
				errorList = append(errorList, fmt.Sprintf("%s middleware configuration %s error: %v", act, mc.Name, err))
			}
		}
	}
	if len(errorList) > 0 {
		err = errors.New(strings.Join(errorList, ";"))
		return err
	}

	return nil
}

// HandleResource handles resources
func HandleResource(ctx context.Context, cli client.Client, action consts.HandleAction, m *v1.MiddlewareOperator) error {
	if m == nil {
		return fmt.Errorf("middleware operator is nil")
	}
	if action == consts.HandleActionDelete && IsNoOperatorResource(m) {
		return nil
	}
	if err := skipNoOperatorResource(ctx, cli, m); err != nil {
		return err
	}

	if action == consts.HandleActionDelete {
		if err := hydrateDeleteContext(ctx, cli, m); err != nil {
			return fmt.Errorf("hydrate delete context error: %w", err)
		}
		// Delete path: avoid relying on full template rendering (only need to locate name/namespace)
		if err := handleExtraResource(ctx, cli, action, m); err != nil {
			return err
		}
		// RBAC deletion is best-effort (historical behavior: delete does not report condition)
		if err := handleRBAC(ctx, cli, action, m); err != nil {
			log.FromContext(ctx).Info("MiddlewareOperator failed to delete RBAC, continuing with subsequent cleanup",
				"warning", true,
				"name", m.Name,
				"namespace", m.Namespace,
				"packageName", m.GetLabels()[v1.LabelPackageName],
				"permissionScope", m.Spec.PermissionScope,
				"permissions", len(m.Spec.Permissions),
				"err", err.Error(),
			)
		}
		// Deployment deletion only needs name/namespace
		if err := k8s.DeleteDeployment(ctx, cli, m.Name, m.Namespace); err != nil && !apiErrors.IsNotFound(err) {
			return fmt.Errorf("delete deployment error: %w", err)
		}
		return nil
	}

	conditionChecked := status.GetCondition(ctx, &m.Status.Conditions, v1.CondTypeChecked)
	if conditionChecked.Status == metav1.ConditionTrue {

		// Parse and merge templates
		err := TemplateParseWithBaseline(ctx, cli, m)
		if err != nil {
			log.FromContext(ctx).Error(err, "template parse with baseline error")
			return err
		}

		// Publish extra resources
		if err = handleExtraResource(ctx, cli, action, m); err != nil {
			log.FromContext(ctx).Error(err, "publish extra resource error")
			if action != consts.HandleActionDelete {
				return err
			}
		}

		// Generate RBAC
		if err = handleRBAC(ctx, cli, action, m); err != nil {
			log.FromContext(ctx).Error(err, "build rbac error")
			if action != consts.HandleActionDelete {
				return err
			}
		}

		// Generate Deployment
		if err = buildDeployment(ctx, cli, action, m); err != nil {
			return fmt.Errorf("build deployment error: %w", err)
		}
	}
	return nil
}

func hydrateDeleteContext(ctx context.Context, cli client.Client, m *v1.MiddlewareOperator) error {
	log.FromContext(ctx).Info("start hydrating MiddlewareOperator delete context",
		"name", m.Name,
		"namespace", m.Namespace,
		"packageName", m.Labels[v1.LabelPackageName],
		"baseline", m.Spec.Baseline,
		"configurationsCount", len(m.Spec.Configurations),
		"permissionsCount", len(m.Spec.Permissions),
		"permissionScope", m.Spec.PermissionScope,
		"hasGlobe", m.Spec.Globe != nil && len(m.Spec.Globe.Raw) > 0,
	)

	baseline, err := middlewareoperatorbaseline.Get(ctx, cli, m.Spec.Baseline, m.Labels[v1.LabelPackageName])
	if err != nil {
		return fmt.Errorf("get baseline error: %w", err)
	}

	configurationsBefore := len(m.Spec.Configurations)
	permissionsBefore := len(m.Spec.Permissions)
	labelsBefore := len(m.Labels)
	annotationsBefore := len(m.Annotations)
	scopeBefore := m.Spec.PermissionScope
	globeHydrated := false

	if err = tools.StructMerge(&baseline.Spec.Configurations, &m.Spec.Configurations, tools.StructMergeArrayType); err != nil {
		return fmt.Errorf("struct merge configurations error: %w", err)
	}

	if err = tools.StructMerge(&baseline.Spec.Permissions, &m.Spec.Permissions, tools.StructMergeArrayType); err != nil {
		return fmt.Errorf("struct merge permissions error: %w", err)
	}

	// Render template expressions in permission ServiceAccountName.
	// Baseline stores templates like "{{ .Globe.Name }}-xxx", which must be
	// resolved to actual names (e.g. "nacos-hc-operator-xxx") before deletion.
	// ServiceAccountName in baseline is a template expression; it must be rendered to actual names before deletion.
	templateValues, tvErr := tools.GetTemplateValues(ctx, m)
	if tvErr != nil {
		return fmt.Errorf("get template values for permission rendering: %w", tvErr)
	}
	for i, perm := range m.Spec.Permissions {
		if strings.Contains(perm.ServiceAccountName, "{{") {
			rendered, renderErr := tools.TemplateParse(ctx, perm.ServiceAccountName, templateValues)
			if renderErr != nil {
				log.FromContext(ctx).Info("render permission ServiceAccountName failed", "warning", true, "err", renderErr)
				continue
			}
			m.Spec.Permissions[i].ServiceAccountName = strings.TrimSpace(rendered)
		}
	}

	if err = tools.StructMerge(&baseline.Labels, &m.Labels, tools.StructMergeMapType); err != nil {
		return fmt.Errorf("struct merge labels error: %w", err)
	}

	if err = tools.StructMerge(&baseline.Annotations, &m.Annotations, tools.StructMergeMapType); err != nil {
		return fmt.Errorf("struct merge annotations error: %w", err)
	}

	if m.Spec.PermissionScope == v1.PermissionScopeUnknown || m.Spec.PermissionScope == "" {
		m.Spec.PermissionScope = baseline.Spec.PermissionScope
	}

	if (m.Spec.Globe == nil || len(m.Spec.Globe.Raw) == 0) && baseline.Spec.Globe != nil {
		m.Spec.Globe = baseline.Spec.Globe.DeepCopy()
		globeHydrated = true
	}

	log.FromContext(ctx).Info("finished hydrating MiddlewareOperator delete context",
		"name", m.Name,
		"namespace", m.Namespace,
		"packageName", m.Labels[v1.LabelPackageName],
		"baseline", m.Spec.Baseline,
		"configurationsBefore", configurationsBefore,
		"configurationsAfter", len(m.Spec.Configurations),
		"permissionsBefore", permissionsBefore,
		"permissionsAfter", len(m.Spec.Permissions),
		"labelsBefore", labelsBefore,
		"labelsAfter", len(m.Labels),
		"annotationsBefore", annotationsBefore,
		"annotationsAfter", len(m.Annotations),
		"permissionScopeBefore", scopeBefore,
		"permissionScopeAfter", m.Spec.PermissionScope,
		"permissionScopeHydrated", (scopeBefore == v1.PermissionScopeUnknown || scopeBefore == "") && m.Spec.PermissionScope != "",
		"globeHydrated", globeHydrated,
		"hasGlobeAfterHydrate", m.Spec.Globe != nil && len(m.Spec.Globe.Raw) > 0,
		"configurationsHydrated", len(m.Spec.Configurations) > configurationsBefore,
		"permissionsHydrated", len(m.Spec.Permissions) > permissionsBefore,
		"labelsHydrated", len(m.Labels) > labelsBefore,
		"annotationsHydrated", len(m.Annotations) > annotationsBefore,
	)

	return nil
}

func TemplateParseWithBaseline(ctx context.Context, cli client.Client, m *v1.MiddlewareOperator) error {
	// Get the associated baseline template
	baseline, err := middlewareoperatorbaseline.Get(ctx, cli, m.Spec.Baseline, m.Labels[v1.LabelPackageName])
	if err != nil {
		return fmt.Errorf("get baseline error: %w", err)
	}

	err = tools.StructMerge(&baseline.Spec, &m.Spec, tools.StructMergeMapType)
	if err != nil {
		return fmt.Errorf("struct merge error: %w", err)
	}

	err = tools.StructMerge(&baseline.Labels, &m.Labels, tools.StructMergeMapType)
	if err != nil {
		return fmt.Errorf("struct merge error: %w", err)
	}

	for _, baselinePre := range baseline.Spec.PreActions {
		if !baselinePre.Fixed {
			for idx, pre := range m.Spec.PreActions {
				if pre.Name == baselinePre.Name {
					err = tools.StructMerge(&baselinePre, &pre, tools.StructMergeMapType)
					if err != nil {
						return fmt.Errorf("struct merge error: %w", err)
					}
					m.Spec.PreActions[idx] = pre
					break
				}
			}
		}
	}

	var templateValues *tools.TemplateValues
	templateValues, err = tools.GetTemplateValues(ctx, m)
	if err != nil {
		return fmt.Errorf("get template values error: %w", err)
	}

	var specBytes []byte
	specBytes, err = yaml.Marshal(m.Spec)
	if err != nil {
		return fmt.Errorf("marshal spec error: %w", err)
	}

	specBytes = tools.ProcessYAMLGoTemp(specBytes)

	var parse string
	parse, err = tools.TemplateParse(ctx, string(specBytes), templateValues)
	if err != nil {
		return fmt.Errorf("template parse error: %w", err)
	}

	err = yaml.Unmarshal([]byte(parse), &m.Spec)
	if err != nil {
		return fmt.Errorf("unmarshal spec error: %w", err)
	}

	// Handle preActions
	err = middlewareaction.HandlePreActions(ctx, cli, m)
	if err != nil {
		return fmt.Errorf("handle preActions error: %w", err)
	}

	return nil
}
