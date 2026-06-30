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
	"fmt"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func ownerIdentity(owner metav1.Object) (apiVersion, kind, name string, uid types.UID) {
	name = owner.GetName()
	uid = owner.GetUID()
	switch owner.(type) {
	case *v1.Middleware:
		return v1.GroupVersion.String(), "Middleware", name, uid
	case *v1.MiddlewareOperator:
		return v1.GroupVersion.String(), "MiddlewareOperator", name, uid
	default:
		return "", "", name, uid
	}
}

func sameControllerOwner(owner metav1.Object, ref *metav1.OwnerReference) bool {
	if owner == nil || ref == nil {
		return false
	}
	apiVersion, kind, name, uid := ownerIdentity(owner)
	if uid != "" && ref.UID != "" {
		return uid == ref.UID
	}
	if apiVersion != "" && kind != "" {
		return ref.APIVersion == apiVersion && ref.Kind == kind && ref.Name == name
	}
	return ref.Name == name
}

func resourceID(obj metav1.Object) string {
	if obj.GetNamespace() == "" {
		return obj.GetName()
	}
	return fmt.Sprintf("%s/%s", obj.GetNamespace(), obj.GetName())
}

func configurationPolicy(m *v1.MiddlewareConfiguration, obj metav1.Object, key string) string {
	if obj != nil {
		if value := obj.GetAnnotations()[key]; value != "" {
			return value
		}
	}
	if m != nil {
		return m.GetAnnotations()[key]
	}
	return ""
}

func shouldSetControllerReference(owner metav1.Object, old metav1.Object, m *v1.MiddlewareConfiguration, desired metav1.Object) (bool, error) {
	if old == nil {
		return true, nil
	}
	controller := metav1.GetControllerOf(old)
	if controller == nil || sameControllerOwner(owner, controller) {
		return true, nil
	}
	if configurationPolicy(m, desired, v1.AnnotationConfigurationOwnershipPolicy) == v1.ConfigurationOwnershipPolicyManaged {
		return false, fmt.Errorf("configuration resource %s is already controlled by %s %s/%s; refusing to take controller ownership",
			resourceID(old), controller.APIVersion, controller.Kind, controller.Name)
	}
	return false, nil
}

func isOpenSaolaManagedConfiguration(owner metav1.Object, obj metav1.Object, configurationName string) bool {
	if owner == nil || obj == nil || configurationName == "" {
		return false
	}
	labels := obj.GetLabels()
	annotations := obj.GetAnnotations()
	return labels[v1.LabelApp] == owner.GetName() && annotations[v1.LabelConfigurations] == configurationName
}

func shouldDeleteRenderedResource(owner metav1.Object, obj metav1.Object, configurationName, deletePolicy string) bool {
	if obj == nil {
		return false
	}
	if deletePolicy == v1.ConfigurationDeletePolicyOrphan {
		return false
	}
	if deletePolicy == v1.ConfigurationDeletePolicyDelete {
		return true
	}
	controller := metav1.GetControllerOf(obj)
	if controller != nil {
		return sameControllerOwner(owner, controller)
	}
	return isOpenSaolaManagedConfiguration(owner, obj, configurationName)
}
