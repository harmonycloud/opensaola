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

	// Describe the intended lifecycle; the writer checks the exact live object
	// before publishing it. A prior cleanup marker is not a prerequisite for
	// adopting a proven legacy Configuration resource.
	if resourceScopeKnown {
		tempAnnotations[v1.AnnotationConfigurationOwnerUID] = string(owner.GetUID())
		if m.GetUID() != "" {
			tempAnnotations[v1.AnnotationConfigurationUID] = string(m.GetUID())
		}
		obj.SetAnnotations(tempAnnotations)
	}

	switch act {
	case consts.HandleActionPublish, consts.HandleActionUpdate:
		disablePolicy, policyErr := configurationDisablePolicy(m, obj)
		if policyErr != nil {
			return nil, policyErr
		}
		sameNamespace := resourceIsNamespaced && obj.GetNamespace() == owner.GetNamespace()
		requireController := sameNamespace && configurationRequiresControllerReference(m, obj, disablePolicy)
		if requireController {
			if err := ctrl.SetControllerReference(owner, obj, cli.Scheme()); err != nil {
				return nil, fmt.Errorf("failed to set ControllerReference: %w", err)
			}
		} else if sameNamespace {
			// A template may itself include this owner, even as a non-controller.
			obj.SetOwnerReferences(withoutConfigurationOwner(obj.GetOwnerReferences(), owner))
		}

		writer := k8s.NewManagedResourceWriter(cli)
		writtenUID, err := writer.ReconcileWithUID(ctx, ownerObj, obj, m.Name)
		if err != nil {
			return nil, err
		}

		identity := v1.RenderedConfigurationResource{
			ConfigurationName: m.Name,
			Group:             obj.GroupVersionKind().Group,
			Version:           obj.GroupVersionKind().Version,
			Kind:              obj.GetKind(),
			Namespace:         obj.GetNamespace(),
			Name:              obj.GetName(),
		}
		live := &unstructured.Unstructured{}
		live.SetGroupVersionKind(obj.GroupVersionKind())
		if err := writer.Reader.Get(ctx, client.ObjectKeyFromObject(obj), live); err != nil {
			return nil, fmt.Errorf("get applied configuration resource: %w", err)
		}
		if writtenUID == "" || live.GetUID() != writtenUID {
			return nil, fmt.Errorf("configuration target changed after publication: expected UID %q, observed %q", writtenUID, live.GetUID())
		}
		if sameNamespace && !requireController {
			if err := stripLiveConfigurationOwnerReference(ctx, cli, live, owner); err != nil {
				return nil, err
			}
		}
		// Cleanup authority is established only by the successfully published
		// live identity, never by an adoption decision made before the write.
		if !hasConfigurationLifecycleMarkers(owner, live, m.Name, m.GetUID()) {
			return &renderedConfigurationApplyResult{identity: identity}, nil
		}
		if requireController && !metav1.IsControlledBy(live, owner) {
			// Business field reconciliation preserves lifecycle metadata. Restore
			// the reference when a previously orphaned target opts back in.
			if metav1.GetControllerOf(live) != nil {
				return nil, fmt.Errorf("configuration target %s is now controlled by another owner", client.ObjectKeyFromObject(live))
			}
			base := client.MergeFromWithOptions(live.DeepCopy(), client.MergeFromWithOptimisticLock{})
			if err := ctrl.SetControllerReference(owner, live, cli.Scheme()); err != nil {
				return nil, err
			}
			if err := cli.Patch(ctx, live, base); err != nil {
				return nil, err
			}
			if !metav1.IsControlledBy(live, owner) {
				return nil, fmt.Errorf("configuration target %s did not accept the controller reference", client.ObjectKeyFromObject(live))
			}
		}
		inventory := &v1.RenderedConfigurationResource{
			ConfigurationName:           m.Name,
			ConfigurationUID:            m.GetUID(),
			Group:                       live.GroupVersionKind().Group,
			Version:                     live.GroupVersionKind().Version,
			Kind:                        live.GetKind(),
			Namespace:                   live.GetNamespace(),
			Name:                        live.GetName(),
			OwnerUID:                    owner.GetUID(),
			ResourceUID:                 live.GetUID(),
			Namespaced:                  resourceIsNamespaced,
			DisablePolicy:               disablePolicy,
			ControllerReferenceRequired: &requireController,
		}
		return &renderedConfigurationApplyResult{identity: identity, inventory: inventory}, nil

	case consts.HandleActionDelete:
		old, err := k8s.GetCustomResource(ctx, cli, obj.GetName(), obj.GetNamespace(), obj.GroupVersionKind())
		if errors.IsNotFound(err) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		if obj.GetKind() == "CustomResourceDefinition" {
			return nil, nil
		}
		_, err = retireConfigurationResource(ctx, cli, owner, old, m)
		return nil, err
	}
	return nil, nil
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
