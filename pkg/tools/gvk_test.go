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
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestGvkToString_Valid(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		gvk  schema.GroupVersionKind
		want string
	}{
		{
			name: "standard GVK",
			gvk:  schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"},
			want: "apps/v1/Deployment",
		},
		{
			name: "custom resource GVK",
			gvk:  schema.GroupVersionKind{Group: "opensaola.io", Version: "v1alpha1", Kind: "Middleware"},
			want: "opensaola.io/v1alpha1/Middleware",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := GvkToString(tt.gvk)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("GvkToString() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestGvkToString_EmptyField(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		gvk  schema.GroupVersionKind
	}{
		{
			name: "empty group",
			gvk:  schema.GroupVersionKind{Group: "", Version: "v1", Kind: "Pod"},
		},
		{
			name: "empty version",
			gvk:  schema.GroupVersionKind{Group: "apps", Version: "", Kind: "Pod"},
		},
		{
			name: "empty kind",
			gvk:  schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: ""},
		},
		{
			name: "all empty",
			gvk:  schema.GroupVersionKind{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := GvkToString(tt.gvk)
			if err == nil {
				t.Fatal("expected error for empty field, got nil")
			}
		})
	}
}

func TestStringToGvk_Valid(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  schema.GroupVersionKind
	}{
		{
			name:  "standard GVK string",
			input: "apps/v1/Deployment",
			want:  schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"},
		},
		{
			name:  "custom resource GVK string",
			input: "opensaola.io/v1alpha1/Middleware",
			want:  schema.GroupVersionKind{Group: "opensaola.io", Version: "v1alpha1", Kind: "Middleware"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := StringToGvk(tt.input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("StringToGvk() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestStringToGvk_InvalidFormat(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
	}{
		{
			name:  "too few parts",
			input: "apps/v1",
		},
		{
			name:  "too many parts",
			input: "apps/v1/Deployment/extra",
		},
		{
			name:  "single part",
			input: "Deployment",
		},
		{
			name:  "empty string",
			input: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := StringToGvk(tt.input)
			if err == nil {
				t.Fatalf("expected error for input %q, got nil", tt.input)
			}
		})
	}
}
