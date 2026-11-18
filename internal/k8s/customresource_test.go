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
	"testing"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var testGVK = schema.GroupVersionKind{Group: "test.io", Version: "v1", Kind: "TestResource"}

func newTestCR(name, namespace string, labels, annotations map[string]string) *unstructured.Unstructured {
	cr := &unstructured.Unstructured{}
	cr.SetGroupVersionKind(testGVK)
	cr.SetName(name)
	cr.SetNamespace(namespace)
	if labels != nil {
		cr.SetLabels(labels)
	}
	if annotations != nil {
		cr.SetAnnotations(annotations)
	}
	return cr
}

// TestIsImmutableResource verifies immutable resource detection.
func TestIsImmutableResource(t *testing.T) {
	cr := newTestCR("obj1", "ns1", nil, nil)
	if isImmutableResource(cr) {
		t.Error("test resource should not be immutable")
	}
}

// TestFieldOwnerConstant verifies the FieldOwner constant is set.
func TestFieldOwnerConstant(t *testing.T) {
	if v1.FieldOwner == "" {
		t.Error("FieldOwner constant should not be empty")
	}
}
