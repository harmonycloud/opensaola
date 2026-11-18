/*
Copyright 2026 The OpenSaola Authors.

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
	"fmt"
	"runtime"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Each owned member contains one nested named env member; live also has an
// external child on every member and one unrelated sidecar. Update changes one
// image in every ten members; noop uses the already converged result. Converter
// construction and fixture preparation are outside the measured loop.
func BenchmarkDeclaredNamedList(b *testing.B) {
	converter := declaredTestConverter(b)
	for _, size := range []int{100, 1000} {
		b.Run(fmt.Sprintf("members_%d", size), func(b *testing.B) {
			entries := make([]any, 0, size)
			for i := 0; i < size; i++ {
				entries = append(entries, map[string]any{"name": fmt.Sprintf("member-%04d", i), "image": "before", "env": []any{map[string]any{"name": "MODE", "value": "steady"}}})
			}
			before := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "test.invalid/v1", "kind": "Thing", "metadata": map[string]any{"name": "benchmark", "namespace": "benchmark"}, "spec": map[string]any{"containers": entries}}}
			live, desired := before.DeepCopy(), before.DeepCopy()
			liveEntries, _, err := unstructured.NestedSlice(live.Object, "spec", "containers")
			if err != nil {
				b.Fatal(err)
			}
			desiredEntries, _, err := unstructured.NestedSlice(desired.Object, "spec", "containers")
			if err != nil {
				b.Fatal(err)
			}
			for i := 0; i < size; i++ {
				liveMember, ok := liveEntries[i].(map[string]any)
				if !ok {
					b.Fatal("invalid live fixture")
				}
				liveMember["external"] = "preserve"
				if i%10 == 0 {
					desiredMember, ok := desiredEntries[i].(map[string]any)
					if !ok {
						b.Fatal("invalid desired fixture")
					}
					desiredMember["image"] = "after"
				}
			}
			liveEntries = append(liveEntries, map[string]any{"name": "external-sidecar", "image": "external"})
			if err := unstructured.SetNestedSlice(live.Object, liveEntries, "spec", "containers"); err != nil {
				b.Fatal(err)
			}
			if err := unstructured.SetNestedSlice(desired.Object, desiredEntries, "spec", "containers"); err != nil {
				b.Fatal(err)
			}
			plan, err := PlanDeclaredResourceIntent(converter, before, live, desired, desired, nil)
			if err != nil {
				b.Fatal(err)
			}
			b.Run("verify_plan", func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					if err := plan.Validate(converter, plan.Object); err != nil {
						b.Fatal(err)
					}
				}
				b.ReportMetric(float64(size), "members/op")
			})
			for _, mode := range []string{"update", "noop"} {
				b.Run(mode, func(b *testing.B) {
					history, actual := before, live
					if mode == "noop" {
						history, actual = desired, plan.Object
					}
					b.ReportAllocs()
					b.ResetTimer()
					var result *unstructured.Unstructured
					for i := 0; i < b.N; i++ {
						var err error
						result, _, err = MergeDeclaredResourceIntent(converter, history, actual, desired, desired, nil)
						if err != nil {
							b.Fatal(err)
						}
					}
					runtime.KeepAlive(result)
					b.ReportMetric(float64(size), "members/op")
				})
			}
		})
	}
}

// Omit all historical members, each with an image and a nested named env item.
// An external child keeps each outer member alive. The accepted result also
// contains unrelated new env members, exercising keyed lookup without repeated
// scans. Plan, verification, and already-absent retry are measured separately;
// this does not measure API calls, compression or full reconciliation.
func BenchmarkDeclaredNamedDeletion(b *testing.B) {
	converter := declaredTestConverter(b)
	for _, size := range []int{100, 1000} {
		b.Run(fmt.Sprintf("members_%d", size), func(b *testing.B) {
			beforeItems, liveItems := make([]any, 0, size), make([]any, 0, size)
			for i := 0; i < size; i++ {
				name := fmt.Sprintf("member-%04d", i)
				beforeItems = append(beforeItems, map[string]any{"name": name, "image": "owned", "env": []any{map[string]any{"name": "OWN", "value": "owned"}}})
				liveItems = append(liveItems, map[string]any{"name": name, "image": "owned", "external": map[string]any{}, "env": []any{map[string]any{"name": "OWN", "value": "owned"}, map[string]any{"name": "EXTERNAL", "value": "keep"}}})
			}
			before := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "test.invalid/v1", "kind": "Thing", "metadata": map[string]any{"name": "benchmark", "namespace": "benchmark"}, "spec": map[string]any{"containers": beforeItems}}}
			live, desired := before.DeepCopy(), before.DeepCopy()
			if err := unstructured.SetNestedSlice(live.Object, liveItems, "spec", "containers"); err != nil {
				b.Fatal(err)
			}
			if err := unstructured.SetNestedSlice(desired.Object, []any{}, "spec", "containers"); err != nil {
				b.Fatal(err)
			}
			planned, err := PlanDeclaredResourceIntent(converter, before, live, desired, desired, nil)
			if err != nil || planned.DeletionConflictError() != nil {
				b.Fatalf("invalid fixture: %v %+v", err, planned)
			}
			if err := ValidateResourceIntentDeletions(converter, planned, planned.Object); err != nil {
				b.Fatal(err)
			}
			for _, mode := range []string{"plan", "verify", "retry_absent"} {
				b.Run(mode, func(b *testing.B) {
					b.ReportAllocs()
					b.ResetTimer()
					var result *DeclaredIntentMergeResult
					for i := 0; i < b.N; i++ {
						var err error
						switch mode {
						case "plan":
							result, err = PlanDeclaredResourceIntent(converter, before, live, desired, desired, nil)
						case "verify":
							err = planned.Validate(converter, planned.Object)
						case "retry_absent":
							result, err = PlanDeclaredResourceIntent(converter, before, planned.Object, desired, desired, nil)
						}
						if err != nil {
							b.Fatal(err)
						}
					}
					runtime.KeepAlive(result)
					b.ReportMetric(float64(size), "members/op")
				})
			}
		})
	}
}
