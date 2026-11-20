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

	"github.com/distribution/reference"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/structured-merge-diff/v6/fieldpath"
)

// Only independently recognized defaults participate in capture and deletion.
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
