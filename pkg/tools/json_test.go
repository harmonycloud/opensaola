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
)

func TestJsonToMap_Valid(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		wantKey string
		wantVal interface{}
	}{
		{
			name:    "simple object",
			input:   `{"name":"test","count":42}`,
			wantKey: "name",
			wantVal: "test",
		},
		{
			name:    "nested object",
			input:   `{"metadata":{"labels":{"app":"web"}}}`,
			wantKey: "metadata",
			wantVal: nil, // just check key exists
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := JsonToMap([]byte(tt.input))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if _, ok := got[tt.wantKey]; !ok {
				t.Errorf("expected key %q not found in result", tt.wantKey)
			}
			if tt.wantVal != nil && got[tt.wantKey] != tt.wantVal {
				t.Errorf("got[%q] = %v, want %v", tt.wantKey, got[tt.wantKey], tt.wantVal)
			}
		})
	}
}

func TestJsonToMap_Invalid(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
	}{
		{
			name:  "invalid JSON",
			input: `{not json}`,
		},
		{
			name:  "empty string",
			input: ``,
		},
		{
			name:  "JSON array instead of object",
			input: `[1,2,3]`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := JsonToMap([]byte(tt.input))
			if err == nil {
				t.Fatal("expected error for invalid JSON, got nil")
			}
		})
	}
}

func TestIsExistInStringSlice_Found(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		slice  []string
		target string
	}{
		{
			name:   "first element",
			slice:  []string{"a", "b", "c"},
			target: "a",
		},
		{
			name:   "last element",
			slice:  []string{"a", "b", "c"},
			target: "c",
		},
		{
			name:   "single element",
			slice:  []string{"only"},
			target: "only",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if !IsExistInStringSlice(tt.slice, tt.target) {
				t.Errorf("IsExistInStringSlice(%v, %q) = false, want true", tt.slice, tt.target)
			}
		})
	}
}

func TestIsExistInStringSlice_NotFound(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		slice  []string
		target string
	}{
		{
			name:   "not in slice",
			slice:  []string{"a", "b", "c"},
			target: "d",
		},
		{
			name:   "empty slice",
			slice:  []string{},
			target: "a",
		},
		{
			name:   "nil slice",
			slice:  nil,
			target: "a",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if IsExistInStringSlice(tt.slice, tt.target) {
				t.Errorf("IsExistInStringSlice(%v, %q) = true, want false", tt.slice, tt.target)
			}
		})
	}
}
