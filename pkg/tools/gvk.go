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

package tools

import (
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

// GvkToString converts a GroupVersionKind to a string.
func GvkToString(gvk schema.GroupVersionKind) (string, error) {
	// Field validation
	if gvk.Group == "" || gvk.Version == "" || gvk.Kind == "" {
		return "", fmt.Errorf("gvk is invalid")
	}
	return fmt.Sprintf("%s/%s/%s", gvk.Group, gvk.Version, gvk.Kind), nil
}

// StringToGvk converts a string to a GroupVersionKind.
func StringToGvk(gvkString string) (schema.GroupVersionKind, error) {
	gvk := schema.GroupVersionKind{}
	gvkSlice := strings.Split(gvkString, "/")
	if len(gvkSlice) != 3 {
		return schema.GroupVersionKind{}, fmt.Errorf("gvkString %s is invalid", gvkString)
	}
	gvk.Group = gvkSlice[0]
	gvk.Version = gvkSlice[1]
	gvk.Kind = gvkSlice[2]
	return gvk, nil
}
