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
	"fmt"

	"k8s.io/apimachinery/pkg/api/errors"

	"github.com/OpenSaola/opensaola/pkg/k8s"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/util/retry"

	v1 "github.com/OpenSaola/opensaola/api/v1"
	"github.com/OpenSaola/opensaola/pkg/resource/logger"
	"github.com/OpenSaola/opensaola/pkg/service/consts"
	"github.com/OpenSaola/opensaola/pkg/tools"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

// Handle handles a MiddlewareConfiguration
func Handle(ctx context.Context, cli client.Client, owner metav1.Object, act consts.HandleAction, m *v1.MiddlewareConfiguration) (err error) {
	obj := new(unstructured.Unstructured)
	err = yaml.Unmarshal([]byte(m.Spec.Template), obj)
	if err != nil {
		return fmt.Errorf("failed to unmarshal CR: %w", err)
	}
	// Ignore empty template
	if obj.Object == nil {
		return nil
	}

	if ok, _ := k8s.IsNamespaced(obj); ok {
		if obj.GetNamespace() == "" {
			obj.SetNamespace(owner.GetNamespace())
		}
		err = ctrl.SetControllerReference(owner, obj, cli.Scheme())
		if err != nil {
			return fmt.Errorf("failed to set ControllerReference: %w", err)
		}
	}

	tempLabels := make(map[string]string)
	for k, v := range owner.GetLabels() {
		tempLabels[k] = v
	}
	for k, v := range m.GetLabels() {
		tempLabels[k] = v
	}
	for k, v := range obj.GetLabels() {
		tempLabels[k] = v
	}
	tempLabels[v1.LabelApp] = owner.GetName()
	obj.SetLabels(tempLabels)

	tempAnnotations := obj.GetAnnotations()
	if tempAnnotations == nil {
		tempAnnotations = make(map[string]string)
	}
	tempAnnotations[v1.LabelConfigurations] = m.Name
	obj.SetAnnotations(tempAnnotations)

	var isExists bool
	old, err := k8s.GetCustomResource(ctx, cli, obj.GetName(), obj.GetNamespace(), obj.GroupVersionKind())
	if err != nil && !errors.IsNotFound(err) {
		return err
	}
	if old == nil {
		oldLists, err := k8s.ListCustomResources(ctx, cli, obj.GetNamespace(), obj.GroupVersionKind(), client.MatchingLabels{
			v1.LabelApp:       m.GetLabels()[owner.GetName()],
			v1.LabelComponent: m.GetLabels()[v1.LabelComponent],
		})
		if err != nil && !errors.IsNotFound(err) {
			return err
		}
		for _, item := range oldLists {
			if item.GetAnnotations()[v1.LabelConfigurations] == m.Name {
				old = &item
				break
			}
		}
		if old != nil {
			isExists = true
		}
	} else {
		isExists = true
	}

	switch act {
	case consts.HandleActionPublish, consts.HandleActionUpdate:
		if isExists {
			obj.SetName(old.GetName())
			err = k8s.PatchCustomResource(ctx, cli, obj)
			if err != nil {
				return err
			}
			logger.Log.Debugj(map[string]interface{}{
				"amsg":      fmt.Sprintf("updated %s successfully", obj.GetKind()),
				"name":      obj.GetName(),
				"namespace": obj.GetNamespace(),
			})
		} else {
			err = k8s.CreateCustomResource(ctx, cli, obj)
			if err != nil && !errors.IsAlreadyExists(err) {
				logger.Log.Debugj(map[string]interface{}{
					"amsg": fmt.Sprintf("failed to create %s", obj.GetKind()),
					"obj":  obj,
				})
				return fmt.Errorf("failed to create CR: %w", err)
			}
			logger.Log.Debugj(map[string]interface{}{
				"amsg":      fmt.Sprintf("created %s successfully", obj.GetKind()),
				"name":      obj.GetName(),
				"namespace": obj.GetNamespace(),
			})
			if err == nil {
				return
			}
		}
	case consts.HandleActionDelete:
		if isExists {
			// If it is a CRD, return
			if obj.GroupVersionKind().Kind == "CustomResourceDefinition" {
				return nil
			}
			obj.SetName(old.GetName())
			err = k8s.DeleteCustomResource(ctx, cli, obj)
			if err != nil && !errors.IsNotFound(err) {
				logger.Log.Errorj(map[string]interface{}{
					"amsg":      fmt.Sprintf("failed to delete %s", obj.GetKind()),
					"name":      obj.GetName(),
					"namespace": obj.GetNamespace(),
				})
				return fmt.Errorf("failed to delete CR: %w", err)
			}
			logger.Log.Debugj(map[string]interface{}{
				"amsg":      fmt.Sprintf("deleted %s successfully", obj.GetKind()),
				"name":      obj.GetName(),
				"namespace": obj.GetNamespace(),
			})
		}
	}
	return nil
}

// handleTemplate processes the template
func handleTemplate(ctx context.Context, templateValues *tools.TemplateValues, m *v1.MiddlewareConfiguration) (string, error) {
	return tools.TemplateParse(ctx, m.Spec.Template, templateValues)
}

// UpdateStatus updates the MiddlewareConfiguration status
func UpdateStatus(ctx context.Context, cli client.Client, m *v1.MiddlewareConfiguration) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		// Get the CR
		m.Status.ObservedGeneration = m.Generation
		now, err := k8s.GetMiddlewareConfiguration(ctx, cli, m.Name)
		if err != nil {
			return fmt.Errorf("get middleware configuration error: %w", err)
		}

		logger.Log.Debugj(map[string]interface{}{
			"amsg":    "updating MiddlewareConfiguration status",
			"version": now.ResourceVersion,
		})
		now.Status = m.Status

		// Retry updating the CR
		err = cli.Status().Update(ctx, now)
		if err != nil {
			return fmt.Errorf("update middleware configuration status error: %w", err)
		}
		return nil
	})
}

// Deploy deploys a MiddlewareConfiguration
func Deploy(ctx context.Context, cli client.Client, component, pkgverison, pkgname string, dryrun bool, m *v1.MiddlewareConfiguration, owner metav1.Object) error {
	lbs := make(labels.Set)
	lbs[v1.LabelComponent] = component
	lbs[v1.LabelPackageVersion] = pkgverison
	lbs[v1.LabelPackageName] = pkgname
	m.Labels = lbs

	if owner != nil {
		// Create MiddlewareConfiguration
		err := ctrl.SetControllerReference(owner, m, cli.Scheme())
		if err != nil {
			return err
		}
	}
	if !dryrun {
		return k8s.CreateMiddlewareConfiguration(ctx, cli, m)
	}
	return nil
}
