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

package customresource

import (
	"context"
	"encoding/json"
	"fmt"

	v1 "github.com/opensaola/opensaola/api/v1"
	"github.com/opensaola/opensaola/internal/service/middlewarebaseline"
	"github.com/opensaola/opensaola/internal/service/middlewareoperatorbaseline"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// HandleGvk resolves the GVK for a Middleware
func HandleGvk(ctx context.Context, cli client.Client, m *v1.Middleware) (
	gvk *schema.GroupVersionKind, err error) {
	var (
		gvkName              string
		operatorBaselineName string
	)

	middlewareBaseline, err := middlewarebaseline.Get(ctx, cli, m.Spec.Baseline, m.Labels[v1.LabelPackageName])
	if err != nil {
		return nil, fmt.Errorf("get middleware baseline error: %w", err)
	}

	// Fields from the published file take precedence
	if m.Spec.OperatorBaseline.GvkName != "" {
		gvkName = m.Spec.OperatorBaseline.GvkName
	} else {
		gvkName = middlewareBaseline.Spec.OperatorBaseline.GvkName
	}

	if m.Spec.OperatorBaseline.Name != "" {
		operatorBaselineName = m.Spec.OperatorBaseline.Name
	} else {
		operatorBaselineName = middlewareBaseline.Spec.OperatorBaseline.Name
	}

	if operatorBaselineName != "" {
		// Get the associated operator baseline
		operatorBaseline, err := middlewareoperatorbaseline.Get(ctx, cli, operatorBaselineName, m.Labels[v1.LabelPackageName])
		if err != nil {
			return nil, fmt.Errorf("get operatorBaseline error: %w", err)
		}

		for _, gvkBaseline := range operatorBaseline.Spec.GVKs {
			if gvkBaseline.Name == gvkName {
				gvk = &schema.GroupVersionKind{
					Group:   gvkBaseline.Group,
					Version: gvkBaseline.Version,
					Kind:    gvkBaseline.Kind,
				}
				return gvk, nil
			}
		}
	} else {
		gvk = &schema.GroupVersionKind{
			Group:   middlewareBaseline.Spec.GVK.Group,
			Version: middlewareBaseline.Spec.GVK.Version,
			Kind:    middlewareBaseline.Spec.GVK.Kind,
		}
		return gvk, nil
	}

	return nil, fmt.Errorf("gvkName %s not exist", gvkName)
}

// RestoreIfIllegalUpdate checks if an update event is legitimate, and reverts if not
func RestoreIfIllegalUpdate(ctx context.Context, cli client.Client, oldObj, newObj *unstructured.Unstructured) error {
	// Get managedFields from the new object
	newManagedFields := newObj.GetManagedFields()

	// Check if new managedFields contains any owner
	isIllegal := false
	for _, field := range newManagedFields {
		if field.Manager == "kubectl" {
			// If the owner is kubectl, do not revert
			isIllegal = true
			break
		}
	}

	// If new managedFields does not contain any expected owner, revert the resource
	if isIllegal {
		log.FromContext(ctx).Error(nil, "illegal update event for resource, reverting", "namespace", newObj.GetNamespace(), "name", newObj.GetName())

		// Revert the resource using oldObj
		err := cli.Update(ctx, oldObj)
		if err != nil {
			return fmt.Errorf("failed to revert resource: %w", err)
		}

		log.FromContext(ctx).Info("resource successfully reverted", "namespace", oldObj.GetNamespace(), "name", oldObj.GetName())
		return nil
	}

	log.FromContext(ctx).Info("resource update is legitimate, no revert needed", "namespace", newObj.GetNamespace(), "name", newObj.GetName())
	return nil
}

// GetNeedPublishCustomResource builds the CustomResource that should be published
func GetNeedPublishCustomResource(ctx context.Context, cli client.Client, m *v1.Middleware) (*unstructured.Unstructured, error) {
	err := json.Unmarshal(m.Spec.Parameters.Raw, new(make(map[string]interface{})))
	if err != nil {
		return nil, fmt.Errorf("unmarshal parameters error: %w", err)
	}

	// Resolve GVK
	gvk, err := HandleGvk(ctx, cli, m)
	if err != nil {
		log.FromContext(ctx).Error(err, "HandleGvk error")
		return nil, err
	}

	customResource := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"spec": m.Spec.Parameters,
		},
	}

	customResource.SetLabels(m.Labels)
	customResource.SetGroupVersionKind(*gvk)
	customResource.SetName(m.Name)
	customResource.SetNamespace(m.Namespace)
	customResource.SetAnnotations(m.Annotations)
	return customResource, nil
}
