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

	"github.com/harmonycloud/opensaola/internal/k8s"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/util/retry"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	"github.com/harmonycloud/opensaola/internal/service/consts"
	"github.com/harmonycloud/opensaola/pkg/tools"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/yaml"
)

// renderedConfigurationApplyResult separates the physical identity rendered
// in this reconcile from the subset that is safe to put in the lifecycle
// inventory. A rendered-but-unmanaged identity still prevents an older status
// entry with the same target from being deleted.
type renderedConfigurationApplyResult struct {
	identity  v1.RenderedConfigurationResource
	inventory *v1.RenderedConfigurationResource
}

// Handle applies one rendered MiddlewareConfiguration. An empty template is a
// deliberate no-op; stale-object cleanup is performed by the caller from the
// prior status inventory.
func Handle(ctx context.Context, cli client.Client, owner metav1.Object, act consts.HandleAction, m *v1.MiddlewareConfiguration) (*renderedConfigurationApplyResult, error) {
	ownerObj, ok := owner.(client.Object)
	if !ok {
		return nil, fmt.Errorf("unsupported owner type %T", owner)
	}
	obj := new(unstructured.Unstructured)
	if err := yaml.Unmarshal([]byte(m.Spec.Template), obj); err != nil {
		return nil, fmt.Errorf("failed to unmarshal CR: %w", err)
	}
	if obj.Object == nil {
		return nil, nil
	}

	var (
		resourceIsNamespaced bool
		resourceScopeKnown   bool
	)
	mapping, nsErr := cli.RESTMapper().RESTMapping(obj.GroupVersionKind().GroupKind(), obj.GroupVersionKind().Version)
	namespaced := nsErr == nil && mapping.Scope.Name() == meta.RESTScopeNameNamespace
	if nsErr != nil {
		if meta.IsNoMatchError(nsErr) || k8s.IsCRDNotInstalled(nsErr) {
			log.FromContext(ctx).Info("CRD not installed in cluster, skipping namespace/owner-ref setup",
				"kind", obj.GetKind(),
				"apiVersion", obj.GetAPIVersion(),
			)
		} else {
			return nil, fmt.Errorf("failed to check if resource is namespaced: %w", nsErr)
		}
	} else {
		resourceScopeKnown = true
		resourceIsNamespaced = namespaced
		if resourceIsNamespaced && obj.GetNamespace() == "" {
			obj.SetNamespace(owner.GetNamespace())
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

	old, err := k8s.GetCustomResource(ctx, cli, obj.GetName(), obj.GetNamespace(), obj.GroupVersionKind())
	if err != nil && !errors.IsNotFound(err) {
		return nil, err
	}
	isExists := old != nil
	lifecycleManaged := isLifecycleManagedForApply(owner, old, m, resourceIsNamespaced, resourceScopeKnown)
	if lifecycleManaged {
		tempAnnotations[v1.AnnotationConfigurationOwnerUID] = string(owner.GetUID())
		if m.GetUID() != "" {
			tempAnnotations[v1.AnnotationConfigurationUID] = string(m.GetUID())
		}
		obj.SetAnnotations(tempAnnotations)
	}

	if act == consts.HandleActionSSACompatibility {
		if resourceIsNamespaced && obj.GetNamespace() == owner.GetNamespace() {
			setOwnerRef, ownerErr := shouldSetControllerReference(owner, old, m, obj)
			if ownerErr != nil {
				return nil, ownerErr
			}
			if setOwnerRef {
				if err = ctrl.SetControllerReference(owner, obj, cli.Scheme()); err != nil {
					return nil, fmt.Errorf("failed to set ControllerReference: %w", err)
				}
			} else {
				obj.SetOwnerReferences(old.GetOwnerReferences())
			}
		}
		return nil, k8s.NewManagedResourceWriter(cli).Reconcile(ctx, ownerObj, obj, m.Name, true)
	}

	switch act {
	case consts.HandleActionPublish, consts.HandleActionUpdate:
		disablePolicy, policyErr := configurationDisablePolicy(m, obj)
		if policyErr != nil {
			return nil, policyErr
		}

		if isExists {
			if resourceIsNamespaced && obj.GetNamespace() == owner.GetNamespace() {
				setOwnerRef, ownerErr := shouldSetControllerReference(owner, old, m, obj)
				if ownerErr != nil {
					return nil, ownerErr
				}
				if setOwnerRef {
					if err = ctrl.SetControllerReference(owner, obj, cli.Scheme()); err != nil {
						return nil, fmt.Errorf("failed to set ControllerReference: %w", err)
					}
				} else {
					obj.SetOwnerReferences(old.GetOwnerReferences())
					log.FromContext(ctx).Info("patching configuration resource without taking controller ownership",
						"configuration", m.Name,
						"gvk", obj.GroupVersionKind().String(),
						"namespace", obj.GetNamespace(),
						"name", obj.GetName(),
						"controllerOwner", metav1.GetControllerOf(old),
					)
				}
			}
			if err = k8s.NewManagedResourceWriter(cli).Reconcile(ctx, ownerObj, obj, m.Name, false); err != nil {
				return nil, err
			}
			log.FromContext(ctx).V(1).Info(fmt.Sprintf("updated %s successfully", obj.GetKind()), "name", obj.GetName(), "namespace", obj.GetNamespace())
		} else {
			if resourceIsNamespaced && obj.GetNamespace() == owner.GetNamespace() {
				if err = ctrl.SetControllerReference(owner, obj, cli.Scheme()); err != nil {
					return nil, fmt.Errorf("failed to set ControllerReference: %w", err)
				}
			}
			err = k8s.NewManagedResourceWriter(cli).Reconcile(ctx, ownerObj, obj, m.Name, false)
			if err != nil && !errors.IsAlreadyExists(err) {
				log.FromContext(ctx).V(1).Info(fmt.Sprintf("failed to create %s", obj.GetKind()), "obj", obj)
				return nil, fmt.Errorf("failed to create CR: %w", err)
			}
			if errors.IsAlreadyExists(err) {
				// A race-created object was not proven to be under this lifecycle.
				lifecycleManaged = false
			}
			log.FromContext(ctx).V(1).Info(fmt.Sprintf("created %s successfully", obj.GetKind()), "name", obj.GetName(), "namespace", obj.GetNamespace())
		}

		identity := v1.RenderedConfigurationResource{
			ConfigurationName: m.Name,
			Group:             obj.GroupVersionKind().Group,
			Version:           obj.GroupVersionKind().Version,
			Kind:              obj.GetKind(),
			Namespace:         obj.GetNamespace(),
			Name:              obj.GetName(),
		}
		if !lifecycleManaged {
			return &renderedConfigurationApplyResult{identity: identity}, nil
		}
		live, getErr := k8s.GetCustomResource(ctx, cli, obj.GetName(), obj.GetNamespace(), obj.GroupVersionKind())
		if getErr != nil {
			return nil, fmt.Errorf("get applied configuration resource: %w", getErr)
		}
		inventory := &v1.RenderedConfigurationResource{
			ConfigurationName: m.Name,
			ConfigurationUID:  m.GetUID(),
			Group:             live.GroupVersionKind().Group,
			Version:           live.GroupVersionKind().Version,
			Kind:              live.GetKind(),
			Namespace:         live.GetNamespace(),
			Name:              live.GetName(),
			OwnerUID:          owner.GetUID(),
			ResourceUID:       live.GetUID(),
			Namespaced:        resourceIsNamespaced,
			DisablePolicy:     disablePolicy,
		}
		return &renderedConfigurationApplyResult{identity: identity, inventory: inventory}, nil

	case consts.HandleActionDelete:
		if isExists {
			if obj.GroupVersionKind().Kind == "CustomResourceDefinition" {
				return nil, nil
			}
			deletePolicy := configurationPolicy(m, obj, v1.AnnotationConfigurationDeletePolicy)
			if !shouldDeleteRenderedResource(owner, old, m.Name, deletePolicy) {
				log.FromContext(ctx).Info("skipping configuration rendered resource delete because it is not owned by OpenSaola lifecycle",
					"configuration", m.Name,
					"gvk", obj.GroupVersionKind().String(),
					"namespace", old.GetNamespace(),
					"name", old.GetName(),
					"controllerOwner", metav1.GetControllerOf(old),
				)
				return nil, nil
			}
			if err = k8s.DeleteCustomResource(ctx, cli, obj); err != nil && !errors.IsNotFound(err) {
				log.FromContext(ctx).Error(err, fmt.Sprintf("failed to delete %s", obj.GetKind()), "name", obj.GetName(), "namespace", obj.GetNamespace())
				return nil, fmt.Errorf("failed to delete CR: %w", err)
			}
			log.FromContext(ctx).V(1).Info(fmt.Sprintf("deleted %s successfully", obj.GetKind()), "name", obj.GetName(), "namespace", obj.GetNamespace())
		}
	}
	return nil, nil
}

func isLifecycleManagedForApply(owner metav1.Object, old *unstructured.Unstructured, configuration *v1.MiddlewareConfiguration, resourceIsNamespaced, resourceScopeKnown bool) bool {
	if owner == nil || owner.GetUID() == "" || !resourceScopeKnown {
		return false
	}
	if old == nil {
		return true
	}
	if resourceIsNamespaced {
		controller := metav1.GetControllerOf(old)
		return controller == nil || sameControllerOwner(owner, controller)
	}
	return hasConfigurationLifecycleMarkers(owner, old, configuration.Name, configuration.GetUID())
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

		log.FromContext(ctx).V(1).Info("updating MiddlewareConfiguration status", "version", now.ResourceVersion)
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
