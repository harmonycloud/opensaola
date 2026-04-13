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

package ctxkeys

import (
	"context"
	"testing"
)

func TestWithStep_SetsLegacyKey(t *testing.T) {
	ctx := context.Background()
	step := map[string]interface{}{
		"s1": map[string]interface{}{"output": "ok"},
	}

	ctx = WithStep(ctx, step)

	got := StepFrom(ctx)
	if got["s1"] == nil {
		t.Fatalf("expected step data to be present via StepFrom")
	}
}

func TestStepFrom_EmptyContext(t *testing.T) {
	got := StepFrom(context.Background())
	if got == nil {
		t.Fatalf("expected non-nil map, got nil")
	}
	if len(got) != 0 {
		t.Fatalf("expected empty map, got len %d", len(got))
	}
}

func TestStepFrom_DoesNotReadStringKey(t *testing.T) {
	ctx := context.WithValue(context.Background(), "step", map[string]interface{}{
		"s1": map[string]interface{}{"output": "ok"},
	})

	got := StepFrom(ctx)
	if got == nil || len(got) != 0 {
		t.Fatalf("expected non-nil empty map, got nil=%v len=%d", got == nil, len(got))
	}
	if _, exists := got["s1"]; exists {
		t.Fatalf("StepFrom must not read string key \"step\", but found key \"s1\"")
	}
}
