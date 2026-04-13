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
	"errors"
	"fmt"
	"strings"

	"github.com/OpenSaola/opensaola/pkg/service/middlewareaction"

	"github.com/OpenSaola/opensaola/pkg/service/customresource"

	"github.com/OpenSaola/opensaola/pkg/service/middlewarebaseline"
	"github.com/OpenSaola/opensaola/pkg/service/synchronizer"
	"github.com/OpenSaola/opensaola/pkg/tools"
	"github.com/OpenSaola/opensaola/pkg/tools/ctxkeys"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"

	v1 "github.com/OpenSaola/opensaola/api/v1"
	"github.com/OpenSaola/opensaola/pkg/k8s"
	"github.com/OpenSaola/opensaola/pkg/resource/logger"
	"github.com/OpenSaola/opensaola/pkg/service/consts"
	"github.com/OpenSaola/opensaola/pkg/service/middlewareconfiguration"
	"github.com/OpenSaola/opensaola/pkg/service/status"
	"github.com/OpenSaola/opensaola/pkg/service/watcher"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func hydrateDeleteContext(ctx context.Context, cli client.Client, m *v1.Middleware) error {
	logger.Log.Infoj(map[string]interface{}{
		"amsg":                "start hydrating Middleware delete context",
		"name":                m.Name,
		"namespace":           m.Namespace,
		"packageName":         m.Labels[v1.LabelPackageName],
		"baseline":            m.Spec.Baseline,
		"configurationsCount": len(m.Spec.Configurations),
		"operatorBaseline":    m.Spec.OperatorBaseline.Name,
		"gvkName":             m.Spec.OperatorBaseline.GvkName,
		"hasNecessary":        len(m.Spec.Necessary.Raw) > 0,
	})

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

	logger.Log.Infoj(map[string]interface{}{
		"amsg":                     "finished hydrating Middleware delete context",
		"name":                     m.Name,
		"namespace":                m.Namespace,
		"packageName":              m.Labels[v1.LabelPackageName],
		"baseline":                 m.Spec.Baseline,
		"configurationsBefore":     configurationsBefore,
		"configurationsAfter":      len(m.Spec.Configurations),
		"labelsBefore":             labelsBefore,
		"labelsAfter":              len(m.Labels),
		"annotationsBefore":        annotationsBefore,
		"annotationsAfter":         len(m.Annotations),
		"operatorBaselineBefore":   operatorBaselineNameBefore,
		"operatorBaselineAfter":    m.Spec.OperatorBaseline.Name,
		"gvkNameBefore":            gvkNameBefore,
		"gvkNameAfter":             m.Spec.OperatorBaseline.GvkName,
		"operatorBaselineHydrated": operatorBaselineNameBefore == "" && m.Spec.OperatorBaseline.Name != "",
		"gvkNameHydrated":          gvkNameBefore == "" && m.Spec.OperatorBaseline.GvkName != "",
		"necessaryHydrated":        necessaryHydrated,
		"hasNecessaryAfterHydrate": len(m.Spec.Necessary.Raw) > 0,
		"configurationsHydrated":   len(m.Spec.Configurations) > configurationsBefore,
		"labelsHydrated":           len(m.Labels) > labelsBefore,
		"annotationsHydrated":      len(m.Annotations) > annotationsBefore,
	})

	return nil
}

func HandleResource(ctx context.Context, cli client.Client, action consts.HandleAction, m *v1.Middleware) error {
	if m == nil {
		return fmt.Errorf("middleware is nil")
	}
	// Delete path: do not rely on full template rendering (only need to locate name/namespace)
	if action == consts.HandleActionDelete {
		if err := hydrateDeleteContext(ctx, cli, m); err != nil {
			return fmt.Errorf("hydrate delete context error: %w", err)
		}
		logger.Log.Infoj(map[string]interface{}{
			"amsg":                "start executing Middleware delete cleanup",
			"name":                m.Name,
			"namespace":           m.Namespace,
			"packageName":         m.GetLabels()[v1.LabelPackageName],
			"configurationsCount": len(m.Spec.Configurations),
			"baseline":            m.Spec.Baseline,
			"operatorBaseline":    m.Spec.OperatorBaseline.Name,
			"gvkName":             m.Spec.OperatorBaseline.GvkName,
			"deletionTimestamp":   m.GetDeletionTimestamp(),
		})
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

// handleExtraResource handles extra resources
func handleExtraResource(ctx context.Context, cli client.Client, act consts.HandleAction, m *v1.Middleware) (err error) {
	conditionBuildExtraResource := status.GetCondition(ctx, &m.Status.Conditions, v1.CondTypeBuildExtraResource)
	defer func() {
		if act != consts.HandleActionDelete {
			if err != nil {
				logger.Log.Errorf("%s extra resource error: %v", act, err)
				conditionBuildExtraResource.Failed(ctx, err.Error(), m.Generation)
			} else {
				logger.Log.Infof("%s extra resource finished", act)
				conditionBuildExtraResource.Success(ctx, m.Generation)
			}
			if updateErr := k8s.UpdateMiddlewareStatus(ctx, cli, m); updateErr != nil {
				logger.Log.Errorf("update middleware status error: %v", updateErr)
				if err == nil {
					err = updateErr
				}
			}
		}
	}()

	var (
		errorList []string
		mcs       []*v1.MiddlewareConfiguration
	)

	switch act {
	case consts.HandleActionDelete:
		// Delete path: skip full template rendering (avoid nil pointer in template body blocking cleanup)
		logger.Log.Infoj(map[string]interface{}{
			"amsg":                "Middleware delete path starting extra resources cleanup",
			"name":                m.Name,
			"namespace":           m.Namespace,
			"packageName":         m.GetLabels()[v1.LabelPackageName],
			"configurationsCount": len(m.Spec.Configurations),
		})
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
				logger.Log.Errorf("update middleware status error: %v", updateErr)
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
	}

	switch action {
	case consts.HandleActionPublish, consts.HandleActionUpdate:
		scheme, schemeErr := ctxkeys.SchemeFrom(ctx)
		if schemeErr != nil {
			return fmt.Errorf("get scheme from context: %w", schemeErr)
		}
		err = ctrl.SetControllerReference(m, cr, scheme)
		if err != nil {
			logger.Log.Errorf("CustomResource set controller reference error: %v", err)
			return err
		}
		err = k8s.CreateOrPatchCustomResource(ctx, cli, cr)
		if err != nil && !apiErrors.IsAlreadyExists(err) {
			// Stop watching
			logger.Log.Error(err)
			watcher.CloseCRWatcher(ctx, cr)
			return err
		}

		// Start watching the CR resource
		cw := watcher.NewCRWatcher(cr.GroupVersionKind(), cr.GetNamespace())
		cwCache, ok := watcher.CustomResourceWatcherMap.Load(cw.GetKey())
		if !ok {
			// If the watcher does not exist, create a new one
			logger.Log.Infoj(map[string]interface{}{
				"amsg":      "create watcher",
				"gvk":       cr.GroupVersionKind(),
				"namespace": cr.GetNamespace(),
			})
			watcher.CustomResourceWatcherMap.Store(cw.GetKey(), cw)
			go k8s.NewInformerOptUnit(ctx, cli, cw.StopChan, cw.GVK, cw.Namespace, watcher.NewResourceEventHandlerFuncs(ctx, cli, m.Name, m.Namespace))
		} else {
			crList, err := k8s.ListCustomResources(ctx, cli, cr.GetNamespace(), cr.GroupVersionKind(), client.MatchingLabels{
				v1.LabelComponent: cr.GetLabels()[v1.LabelComponent],
			})
			if err != nil {
				return err
			}
			cw = cwCache.(*watcher.CustomResourceWatcher)
			cw.Counter.Store(int32(len(crList)))

			// If the watcher exists, re-query CR count and calibrate the counter
			logger.Log.Infoj(map[string]interface{}{
				"amsg":      "sync watcher counter",
				"gvk":       cr.GroupVersionKind(),
				"namespace": cr.GetNamespace(),
				"counter":   cw.Counter.Load(),
			})
		}

		go synchronizer.SyncCustomResourceV2(ctx, cli, cr, m)
	case consts.HandleActionDelete:
		// Stop watching/syncing: after middleware deletion, CR delete events may not be received (or may be filtered by label).
		// This is a fallback to close the in-process watcher & sync goroutines.
		// Note: this is in-process state (Map + chan); each operator replica must execute this independently.
		cw := watcher.NewCRWatcher(cr.GroupVersionKind(), cr.GetNamespace())
		if _, ok := watcher.CustomResourceWatcherMap.Load(cw.GetKey()); ok {
			watcher.CloseCRWatcher(ctx, cr)
		}
		stopKey := fmt.Sprintf(synchronizer.SyncCustomResourceStopChanMapKey, cr.GroupVersionKind().String(), cr.GetNamespace(), cr.GetName())
		if resourceStop, ok := synchronizer.SyncCustomResourceStopChanMap.Load(stopKey); ok {
			func() {
				defer func() { _ = recover() }()
				close(resourceStop.(chan struct{}))
			}()
			synchronizer.SyncCustomResourceStopChanMap.Delete(stopKey)
		}

		// Delete CR
		err = k8s.DeleteCustomResource(ctx, cli, cr)
		if err != nil && !apiErrors.IsNotFound(err) {
			logger.Log.Errorf("delete custom resource error: %v", err)
			return err
		}
	}

	return nil
}
