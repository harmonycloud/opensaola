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
	"fmt"
	"strings"

	"github.com/harmonycloud/opensaola/internal/service/middlewareaction"

	"github.com/harmonycloud/opensaola/internal/service/customresource"

	"github.com/harmonycloud/opensaola/internal/service/middlewarebaseline"
	"github.com/harmonycloud/opensaola/internal/service/synchronizer"
	"github.com/harmonycloud/opensaola/pkg/tools"
	"github.com/harmonycloud/opensaola/pkg/tools/ctxkeys"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	"github.com/harmonycloud/opensaola/internal/k8s"
	"github.com/harmonycloud/opensaola/internal/service/consts"
	"github.com/harmonycloud/opensaola/internal/service/middlewareconfiguration"
	"github.com/harmonycloud/opensaola/internal/service/status"
	"github.com/harmonycloud/opensaola/internal/service/watcher"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

func hydrateDeleteContext(ctx context.Context, cli client.Client, m *v1.Middleware) error {
	log.FromContext(ctx).Info("start hydrating Middleware delete context",
		"name", m.Name,
		"namespace", m.Namespace,
		"packageName", m.Labels[v1.LabelPackageName],
		"baseline", m.Spec.Baseline,
		"configurationsCount", len(m.Spec.Configurations),
		"operatorBaseline", m.Spec.OperatorBaseline.Name,
		"gvkName", m.Spec.OperatorBaseline.GvkName,
		"hasNecessary", len(m.Spec.Necessary.Raw) > 0,
	)

	baseline, err := middlewarebaseline.Get(ctx, cli, m.Spec.Baseline, m.Labels[v1.LabelPackageName])
	if err != nil {
		return fmt.Errorf("get baseline error: %w", err)
	}

	configurationsBefore := len(m.Spec.Configurations)
	labelsBefore := len(m.Labels)
	annotationsBefore := len(m.Annotations)
	operatorBaselineNameBefore := m.Spec.OperatorBaseline.Name
	gvkNameBefore := m.Spec.OperatorBaseline.GvkName
	necessaryHydrated := false

	if err = tools.StructMerge(&baseline.Spec.Configurations, &m.Spec.Configurations, tools.StructMergeArrayType); err != nil {
		return fmt.Errorf("struct merge configurations error: %w", err)
	}

	if err = tools.StructMerge(&baseline.Labels, &m.Labels, tools.StructMergeMapType); err != nil {
		return fmt.Errorf("struct merge labels error: %w", err)
	}

	if err = tools.StructMerge(&baseline.Annotations, &m.Annotations, tools.StructMergeMapType); err != nil {
		return fmt.Errorf("struct merge annotations error: %w", err)
	}

	if m.Spec.OperatorBaseline.Name == "" {
		m.Spec.OperatorBaseline.Name = baseline.Spec.OperatorBaseline.Name
	}
	if m.Spec.OperatorBaseline.GvkName == "" {
		m.Spec.OperatorBaseline.GvkName = baseline.Spec.OperatorBaseline.GvkName
	}
	if len(m.Spec.Necessary.Raw) == 0 && len(baseline.Spec.Necessary.Raw) > 0 {
		m.Spec.Necessary = *baseline.Spec.Necessary.DeepCopy()
		necessaryHydrated = true
	}

	log.FromContext(ctx).Info("finished hydrating Middleware delete context",
		"name", m.Name,
		"namespace", m.Namespace,
		"packageName", m.Labels[v1.LabelPackageName],
		"baseline", m.Spec.Baseline,
		"configurationsBefore", configurationsBefore,
		"configurationsAfter", len(m.Spec.Configurations),
		"labelsBefore", labelsBefore,
		"labelsAfter", len(m.Labels),
		"annotationsBefore", annotationsBefore,
		"annotationsAfter", len(m.Annotations),
		"operatorBaselineBefore", operatorBaselineNameBefore,
		"operatorBaselineAfter", m.Spec.OperatorBaseline.Name,
		"gvkNameBefore", gvkNameBefore,
		"gvkNameAfter", m.Spec.OperatorBaseline.GvkName,
		"operatorBaselineHydrated", operatorBaselineNameBefore == "" && m.Spec.OperatorBaseline.Name != "",
		"gvkNameHydrated", gvkNameBefore == "" && m.Spec.OperatorBaseline.GvkName != "",
		"necessaryHydrated", necessaryHydrated,
		"hasNecessaryAfterHydrate", len(m.Spec.Necessary.Raw) > 0,
		"configurationsHydrated", len(m.Spec.Configurations) > configurationsBefore,
		"labelsHydrated", len(m.Labels) > labelsBefore,
		"annotationsHydrated", len(m.Annotations) > annotationsBefore,
	)

	return nil
}

func HandleResource(ctx context.Context, cli client.Client, action consts.HandleAction, m *v1.Middleware) error {
	if m == nil {
		return fmt.Errorf("middleware is nil")
	}
	if action != consts.HandleActionDelete && v1.IsMiddlewareReconcileWriteSuspended(m.GetAnnotations(), m.Status.Conditions) {
		log.FromContext(ctx).Info("skipping Middleware child-resource reconciliation because it is suspended",
			"name", m.Name,
			"namespace", m.Namespace,
			"annotation", v1.AnnotationSuspendReconcile,
		)
		return nil
	}
	// Delete path: do not rely on full template rendering (only need to locate name/namespace)
	if action == consts.HandleActionDelete {
		if err := hydrateDeleteContext(ctx, cli, m); err != nil {
			return fmt.Errorf("hydrate delete context error: %w", err)
		}
		log.FromContext(ctx).Info("start executing Middleware delete cleanup",
			"name", m.Name,
			"namespace", m.Namespace,
			"packageName", m.GetLabels()[v1.LabelPackageName],
			"configurationsCount", len(m.Spec.Configurations),
			"baseline", m.Spec.Baseline,
			"operatorBaseline", m.Spec.OperatorBaseline.Name,
			"gvkName", m.Spec.OperatorBaseline.GvkName,
			"deletionTimestamp", m.GetDeletionTimestamp(),
		)
		// Clean up extra resources (delete-only: prefer rendering metadata.name only; fall back to label-based list deletion on failure)
		if err := handleExtraResource(ctx, cli, action, m); err != nil {
			return fmt.Errorf("build extra resource error: %w", err)
		}
		// Delete CR (delete-only: only need gvk + name + namespace)
		if err := buildCustomResource(ctx, cli, action, m); err != nil {
			return fmt.Errorf("build cr error: %w", err)
		}
		return nil
	}

	conditionChecked := status.GetCondition(ctx, &m.Status.Conditions, v1.CondTypeChecked)
	if conditionChecked.Status == metav1.ConditionTrue {
		// Parse and merge templates
		if err := TemplateParseWithBaseline(ctx, cli, m); err != nil {
			return err
		}

		// Handle preActions
		if err := middlewareaction.HandlePreActions(ctx, cli, m); err != nil {
			return fmt.Errorf("handle preActions error: %w", err)
		}

		// Publish extra resources
		if err := handleExtraResource(ctx, cli, action, m); err != nil {
			return fmt.Errorf("build extra resource error: %w", err)
		}

		// Publish CR
		if err := buildCustomResource(ctx, cli, action, m); err != nil {
			return fmt.Errorf("build cr error: %w", err)
		}
	}
	return nil
}

// RenderPrimaryCustomResource renders the effective primary custom resource
// without writing it. It is used by the pause/recovery state machine to take a
// durable desired-state snapshot and to calculate B/L/D merges safely.
func RenderPrimaryCustomResource(ctx context.Context, cli client.Client, m *v1.Middleware) (*unstructured.Unstructured, error) {
	return renderPrimaryCustomResource(ctx, cli, m, true)
}

// RenderPrimaryCustomResourceWithoutOverrides renders the current Baseline,
// template, and pure PreAction output before persistent reconcile overrides
// are applied. It is the base against which a new complete override patch is
// generated.
func RenderPrimaryCustomResourceWithoutOverrides(ctx context.Context, cli client.Client, m *v1.Middleware) (*unstructured.Unstructured, error) {
	return renderPrimaryCustomResource(ctx, cli, m, false)
}

func renderPrimaryCustomResource(ctx context.Context, cli client.Client, m *v1.Middleware, applyOverrides bool) (*unstructured.Unstructured, error) {
	if m == nil {
		return nil, fmt.Errorf("middleware is nil")
	}
	rendered := m.DeepCopy()
	if err := renderMiddlewareWithBaseline(ctx, cli, rendered); err != nil {
		return nil, err
	}
	if err := middlewareaction.RenderPreActions(ctx, cli, rendered); err != nil {
		return nil, fmt.Errorf("render pre actions: %w", err)
	}
	cr, err := customresource.GetNeedPublishCustomResource(ctx, cli, rendered)
	if err != nil {
		return nil, err
	}
	if applyOverrides {
		if err := ApplyReconcileOverrides(rendered, cr); err != nil {
			return nil, err
		}
	}
	return cr, nil
}

// handleExtraResource handles extra resources
func handleExtraResource(ctx context.Context, cli client.Client, act consts.HandleAction, m *v1.Middleware) (err error) {
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
			if updateErr := k8s.UpdateMiddlewareStatus(ctx, cli, m); updateErr != nil {
				log.FromContext(ctx).Error(updateErr, "update middleware status error")
				if err == nil {
					err = updateErr
				}
			}
		}
	}()

	var mcs []*v1.MiddlewareConfiguration

	switch act {
	case consts.HandleActionDelete:
		// Delete path: skip full template rendering (avoid nil pointer in template body blocking cleanup)
		log.FromContext(ctx).Info("Middleware delete path starting extra resources cleanup",
			"name", m.Name,
			"namespace", m.Namespace,
			"packageName", m.GetLabels()[v1.LabelPackageName],
			"configurationsCount", len(m.Spec.Configurations),
		)
		return middlewareconfiguration.DeleteTemplateRenderedResources(ctx, cli, m, m)

	case consts.HandleActionPublish, consts.HandleActionUpdate:
		mcs, err = middlewareconfiguration.GetTemplateParsedMiddlewareConfigurations(ctx, cli, act, m)
		if err != nil {
			return err
		}
		var inventory []v1.RenderedConfigurationResource
		inventory, err = middlewareconfiguration.ReconcileRenderedConfigurationResources(ctx, cli, m, act, mcs, m.Status.RenderedConfigurationResources)
		if err != nil {
			return err
		}
		m.Status.RenderedConfigurationResources = inventory
		m.Status.RenderedConfigurationResourcesGeneration = m.Generation
	}

	return nil
}

// seedInitialCustomResourcePhase persists Creating immediately after the first
// successful primary custom resource apply. SyncCustomResourceV2 takes over
// from this seed once the target resource exposes a derived or native phase.
func seedInitialCustomResourcePhase(ctx context.Context, cli client.Client, action consts.HandleAction, m *v1.Middleware) error {
	if m == nil || action != consts.HandleActionPublish || m.Status.CustomResources.Phase != v1.PhaseUnknown {
		return nil
	}

	effectivePhase := m.Status.CustomResources.Phase
	if err := k8s.PatchMiddlewareStatusFields(ctx, cli, m.Name, m.Namespace, func(s *v1.MiddlewareStatus) {
		// A synchronizer update may have won the race after the primary resource
		// was applied. Do not replace an observed runtime phase with Creating.
		if s.CustomResources.Phase == v1.PhaseUnknown {
			s.CustomResources.Phase = v1.PhaseCreating
		}
		effectivePhase = s.CustomResources.Phase
	}); err != nil {
		return fmt.Errorf("seed initial custom resource phase: %w", err)
	}

	// Keep the reconcile copy consistent with the value that was actually
	// persisted. Later controller status updates deliberately preserve
	// the runtime-owned customResources field.
	m.Status.CustomResources.Phase = effectivePhase
	return nil
}

// buildCustomResource builds the custom resource
func buildCustomResource(ctx context.Context, cli client.Client, action consts.HandleAction, m *v1.Middleware) (err error) {
	conditionApplyCluster := status.GetCondition(ctx, &m.Status.Conditions, v1.CondTypeApplyCluster)
	defer func() {
		if action != consts.HandleActionDelete {
			if err != nil {
				conditionApplyCluster.Failed(ctx, err.Error(), m.Generation)
			} else {
				conditionApplyCluster.Success(ctx, m.Generation)
			}
			if updateErr := k8s.UpdateMiddlewareStatus(ctx, cli, m); updateErr != nil {
				log.FromContext(ctx).Error(updateErr, "update middleware status error")
				if err == nil {
					err = updateErr
				}
			}
		}
	}()

	var cr *unstructured.Unstructured
	if action == consts.HandleActionDelete {
		// Delete path only needs gvk + name + namespace, avoiding extra dependencies like parameters/baseline parsing
		gvk, gvkErr := customresource.HandleGvk(ctx, cli, m)
		if gvkErr != nil {
			return fmt.Errorf("handle gvk error: %w", gvkErr)
		}
		cr = new(unstructured.Unstructured)
		cr.SetGroupVersionKind(*gvk)
		cr.SetName(m.Name)
		cr.SetNamespace(m.Namespace)
	} else {
		// Get the CR that should be published
		cr, err = customresource.GetNeedPublishCustomResource(ctx, cli, m)
		if err != nil {
			return fmt.Errorf("parse cr error: %w", err)
		}
		if err = ApplyReconcileOverrides(m, cr); err != nil {
			return fmt.Errorf("apply reconcile overrides: %w", err)
		}
	}

	switch action {
	case consts.HandleActionPublish, consts.HandleActionUpdate:
		if policy, ok := v1.ReconcileResumePolicyFor(m.GetAnnotations()); ok && policy == v1.ReconcileResumePolicyMerge && m.Status.ReconcilePause != nil {
			if err = VerifyPausedPrimaryCustomResourceForApply(ctx, cli, m.Status.ReconcilePause, cr); err != nil {
				return fmt.Errorf("verify paused primary custom resource before resume apply: %w", err)
			}
		}
		scheme, schemeErr := ctxkeys.SchemeFrom(ctx)
		if schemeErr != nil {
			return fmt.Errorf("get scheme from context: %w", schemeErr)
		}
		err = ctrl.SetControllerReference(m, cr, scheme)
		if err != nil {
			err = status.WrapDiagnostic(err, status.Diagnostic{
				Phase:              status.PhaseRuntimeReconcile,
				Controller:         "middleware",
				Resource:           middlewareObjectRef(m),
				FailedObject:       status.ObjectRefFromObject(cr, cr.GroupVersionKind()),
				Owner:              middlewareObjectRef(m),
				Generation:         m.Generation,
				ObservedGeneration: m.Status.ObservedGeneration,
				Next:               "check the rendered custom resource metadata.ownerReferences and Middleware controller scheme registration",
			})
			log.FromContext(ctx).Error(err, "CustomResource set controller reference error", status.DiagnosticLogValues(err)...)
			return err
		}
		err = k8s.NewManagedResourceWriter(cli).Reconcile(ctx, m, cr, "", false)
		if err != nil && !apiErrors.IsAlreadyExists(err) {
			// Stop watching
			err = status.WrapDiagnostic(err, status.Diagnostic{
				Phase:              status.PhaseRuntimeReconcile,
				Controller:         "middleware",
				Resource:           middlewareObjectRef(m),
				FailedObject:       status.ObjectRefFromObject(cr, cr.GroupVersionKind()),
				Owner:              middlewareObjectRef(m),
				Generation:         m.Generation,
				ObservedGeneration: m.Status.ObservedGeneration,
				Next: fmt.Sprintf(
					"run kubectl describe %s %s -n %s and kubectl get events -n %s --field-selector involvedObject.name=%s",
					strings.ToLower(cr.GetKind()),
					cr.GetName(),
					cr.GetNamespace(),
					cr.GetNamespace(),
					cr.GetName(),
				),
			})
			log.FromContext(ctx).Error(err, "create or patch custom resource error", status.DiagnosticLogValues(err)...)
			// Do not release a shared watcher here: this reconcile may be a retry
			// after the CR was already published and registered. Membership is
			// released only by the explicit Middleware delete path.
			return err
		}
		if err = seedInitialCustomResourcePhase(ctx, cli, action, m); err != nil {
			return err
		}

		// Start or join the shared namespace/GVK watcher. Membership is tracked by
		// CR name, avoiding component-wide counter resets that could stop another
		// Middleware's watcher.
		if _, _, watcherErr := watcher.EnsureCRWatcher(ctx, cli, cr, m.Name, m.Namespace); watcherErr != nil {
			return status.WrapDiagnostic(watcherErr, status.Diagnostic{
				Phase:              status.PhaseRuntimeReconcile,
				Controller:         "middleware",
				Resource:           middlewareObjectRef(m),
				FailedObject:       status.ObjectRefFromObject(cr, cr.GroupVersionKind()),
				Generation:         m.Generation,
				ObservedGeneration: m.Status.ObservedGeneration,
				Next:               "check the dynamic custom-resource watcher registration and informer logs",
			})
		}

		go func() {
			if syncErr := synchronizer.SyncCustomResourceV2(ctx, cli, cr, m); syncErr != nil {
				log.FromContext(ctx).Error(syncErr, "custom resource sync exited with error", "gvk", cr.GroupVersionKind(), "namespace", cr.GetNamespace(), "name", cr.GetName())
			}
		}()
	case consts.HandleActionDelete:
		// Stop watching/syncing: after middleware deletion, CR delete events may not be received (or may be filtered by label).
		// This is a fallback to close the in-process watcher & sync goroutines.
		// Note: this is in-process state (Map + chan); each operator replica must execute this independently.
		watcher.ReleaseCRWatcher(ctx, cr)
		stopKey := fmt.Sprintf(synchronizer.SyncCustomResourceStopChanMapKey, cr.GroupVersionKind().String(), cr.GetNamespace(), cr.GetName())
		synchronizer.StopSyncCustomResource(stopKey)

		// Delete CR
		err = k8s.DeleteCustomResource(ctx, cli, cr)
		if err != nil && !apiErrors.IsNotFound(err) {
			log.FromContext(ctx).Error(err, "delete custom resource error")
			return err
		}
	}

	return nil
}
