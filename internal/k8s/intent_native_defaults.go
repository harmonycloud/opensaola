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

package k8s

import (
	"reflect"
	"time"

	"github.com/distribution/reference"
	admissionv1 "k8s.io/api/admissionregistration/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"sigs.k8s.io/structured-merge-diff/v6/fieldpath"
)

// nativeAdmissionDefault predicts an omitted field from the versioned native
// Go type and the declaration. It never uses an admission response as evidence.
// The field rules come from Kubernetes v1.35.0 pkg/apis/<group>/v1/defaults.go
// for core, networking, rbac and admissionregistration. The StatefulSet TypeMeta
// conversion is in pkg/apis/apps/v1/conversion.go.
func nativeAdmissionDefault(root reflect.Type, path fieldpath.Path, declaredParent map[string]any) (any, bool) {
	if len(path) == 0 || path[len(path)-1].FieldName == nil {
		return nil, false
	}
	field := *path[len(path)-1].FieldName
	if _, declared := declaredParent[field]; declared {
		return nil, false
	}
	parent := nativeDefaultParent(root, path)
	switch parent {
	case reflect.TypeFor[rbacv1.Subject]():
		if field == "apiGroup" {
			kind, _ := declaredParent["kind"].(string)
			switch kind {
			case rbacv1.GroupKind, rbacv1.UserKind:
				return rbacv1.GroupName, true
			}
		}
	case reflect.TypeFor[admissionv1.MatchResources]():
		switch field {
		case "matchPolicy":
			return string(admissionv1.Equivalent), true
		case "namespaceSelector", "objectSelector":
			return map[string]any{}, true
		}
	case reflect.TypeFor[admissionv1.Rule](), reflect.TypeFor[admissionv1.RuleWithOperations](), reflect.TypeFor[admissionv1.NamedRuleWithOperations]():
		if field == "scope" {
			return string(admissionv1.AllScopes), true
		}
	case reflect.TypeFor[corev1.ObjectFieldSelector]():
		if field == "apiVersion" {
			return corev1.SchemeGroupVersion.String(), true
		}
	case reflect.TypeFor[corev1.ServiceAccountTokenProjection]():
		if field == "expirationSeconds" {
			return int64(time.Hour / time.Second), true
		}
	case reflect.TypeFor[networkingv1.NetworkPolicyPort]():
		if field == "protocol" {
			return string(corev1.ProtocolTCP), true
		}
	case reflect.TypeFor[corev1.PersistentVolumeClaimSpec]():
		if field == "volumeMode" {
			return string(corev1.PersistentVolumeFilesystem), true
		}
	case reflect.TypeFor[corev1.PersistentVolumeClaimStatus]():
		// SetDefaults_PersistentVolumeClaim operates on the containing claim.
		if field == "phase" && len(path) >= 2 && nativeDefaultParent(root, path[:len(path)-1]) == reflect.TypeFor[corev1.PersistentVolumeClaim]() {
			return string(corev1.ClaimPending), true
		}
	case reflect.TypeFor[corev1.PersistentVolumeClaim]():
		// Convert_apps_StatefulSetSpec_To_v1_StatefulSetSpec populates
		// TypeMeta on nested claims when converting back to the wire API.
		if len(path) >= 3 && declaredNativePathType(root, path[:len(path)-3]) == reflect.TypeFor[appsv1.StatefulSetSpec]() {
			switch field {
			case "kind":
				return "PersistentVolumeClaim", true
			case "apiVersion":
				return corev1.SchemeGroupVersion.String(), true
			}
		}
	}
	return nativeCaptureDefault(parent, field, declaredParent)
}

// Capture and historical deletion checks retain their original set of defaults.
// Admission equivalence can recognize additional defaults without turning them
// into fields that old cleanup history can later remove.
func (l *declaredDeletionLookup) nativeDefaultAt(object map[string]any, root intentDeclaredType, path fieldpath.Path, actual any) bool {
	if len(path) < 2 || path[len(path)-1].FieldName == nil {
		return false
	}
	parent := nativeDefaultParent(l.merger.nativeType, path)
	field := *path[len(path)-1].FieldName
	var fields map[string]any
	if parent == reflect.TypeFor[corev1.Container]() && field == "imagePullPolicy" {
		container, _, err := l.find(object, root, path[:len(path)-1])
		if err != nil {
			return false
		}
		fields, _ = container.value.(map[string]any)
	}
	v, found := nativeCaptureDefault(parent, field, fields)
	return found && reflect.DeepEqual(actual, v)
}

func nativeDefaultParent(root reflect.Type, path fieldpath.Path) reflect.Type {
	parent := declaredNativePathType(root, path[:len(path)-1])
	for parent != nil && parent.Kind() == reflect.Pointer {
		parent = parent.Elem()
	}
	return parent
}

func nativeCaptureDefault(parent reflect.Type, field string, fields map[string]any) (any, bool) {
	if parent == nil {
		return nil, false
	}
	switch parent {
	case reflect.TypeFor[corev1.Probe]():
		switch field {
		case "timeoutSeconds", "successThreshold":
			return int64(1), true
		case "periodSeconds":
			return int64(10), true
		case "failureThreshold":
			return int64(3), true
		}
	case reflect.TypeFor[corev1.HTTPGetAction]():
		switch field {
		case "path":
			return "/", true
		case "scheme":
			return string(corev1.URISchemeHTTP), true
		}
	case reflect.TypeFor[corev1.GRPCAction]():
		if field == "service" {
			return "", true
		}
	case reflect.TypeFor[corev1.Container]():
		switch field {
		case "terminationMessagePath":
			return corev1.TerminationMessagePathDefault, true
		case "terminationMessagePolicy":
			return string(corev1.TerminationMessageReadFile), true
		case "imagePullPolicy":
			image, _ := fields["image"].(string)
			ref, err := reference.ParseNormalizedNamed(image)
			if err != nil {
				return nil, false
			}
			if tagged, ok := reference.TagNameOnly(ref).(reference.Tagged); ok && tagged.Tag() == "latest" {
				return string(corev1.PullAlways), true
			}
			return string(corev1.PullIfNotPresent), true
		}
	case reflect.TypeFor[corev1.SecretVolumeSource](), reflect.TypeFor[corev1.ConfigMapVolumeSource](), reflect.TypeFor[corev1.DownwardAPIVolumeSource](), reflect.TypeFor[corev1.ProjectedVolumeSource]():
		if field == "defaultMode" {
			return int64(corev1.SecretVolumeSourceDefaultMode), true
		}
	}
	// Optional children can be nested inside a required empty JSON object
	// such as a PVC's status. Use the shape emitted by the native wire type.
	empty, ok := canonicalDeclaredNativeValue(map[string]any{}, parent)
	object, isObject := empty.(map[string]any)
	if !ok || !isObject {
		return nil, false
	}
	v, found := object[field]
	return v, found
}
