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

package packages

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestList_NilClient(t *testing.T) {
	SetDataNamespace("test-ns")

	_, err := List(context.Background(), nil, Option{})
	if err == nil {
		t.Fatal("expected error when calling List with nil client, got nil")
	}
}

func TestList_EmptyResult(t *testing.T) {
	SetDataNamespace("test-ns")

	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	cli := fake.NewClientBuilder().
		WithScheme(scheme).
		Build()

	pkgs, err := List(context.Background(), cli, Option{})
	if err != nil {
		t.Fatalf("List() returned unexpected error: %v", err)
	}
	if len(pkgs) != 0 {
		t.Errorf("expected 0 packages, got %d", len(pkgs))
	}
}
