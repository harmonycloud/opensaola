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

package middlewareoperator

import (
	"testing"

	v1 "github.com/harmonycloud/opensaola/api/v1"
)

func TestTargetUpgradeBaseline(t *testing.T) {
	tests := []struct {
		name        string
		operator    *v1.MiddlewareOperator
		want        string
		description string
	}{
		{
			name: "annotation wins",
			operator: &v1.MiddlewareOperator{
				Spec: v1.MiddlewareOperatorSpec{Baseline: "current-baseline"},
			},
			want:        "target-baseline",
			description: "explicit upgrade baseline should select the target package baseline",
		},
		{
			name: "fallback to spec baseline",
			operator: &v1.MiddlewareOperator{
				Spec: v1.MiddlewareOperatorSpec{Baseline: "current-baseline"},
			},
			want:        "current-baseline",
			description: "update-only upgrades should keep the existing baseline",
		},
		{
			name:        "nil operator",
			operator:    nil,
			want:        "",
			description: "nil input should be safe",
		},
	}

	tests[0].operator.SetAnnotations(map[string]string{v1.LabelBaseline: "target-baseline"})

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := targetUpgradeBaseline(tt.operator); got != tt.want {
				t.Fatalf("%s: got %q want %q", tt.description, got, tt.want)
			}
		})
	}
}
