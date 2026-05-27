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

package watcher

import (
	"context"
	"sync"
	"testing"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestCustomResourceWatcher_CounterConcurrency(t *testing.T) {
	gvk := schema.GroupVersionKind{Group: "test", Version: "v1", Kind: "Foo"}
	cw := NewCRWatcher(gvk, "ns1")

	// Initial value should be 1
	if got := cw.Counter.Load(); got != 1 {
		t.Fatalf("initial counter = %d, want 1", got)
	}

	const goroutines = 100
	var wg sync.WaitGroup

	// Concurrent increments
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			cw.Counter.Add(1)
		}()
	}
	wg.Wait()

	if got := cw.Counter.Load(); got != 1+goroutines {
		t.Fatalf("after increments counter = %d, want %d", got, 1+goroutines)
	}

	// Concurrent decrements
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			cw.Counter.Add(-1)
		}()
	}
	wg.Wait()

	if got := cw.Counter.Load(); got != 1 {
		t.Fatalf("after decrements counter = %d, want 1", got)
	}
}

func TestCustomResourceUpdateNotifyDecision(t *testing.T) {
	tests := []struct {
		name           string
		oldObj         any
		newObj         any
		wantNotify     bool
		wantNamespace  string
		wantMiddleware string
	}{
		{
			name: "status resource version change uses middleware owner reference",
			oldObj: testCustomResource("ns1", "redis-a", "1", true, map[string]any{
				"phase": "Creating",
			}, "mid-a"),
			newObj: testCustomResource("ns1", "redis-a", "2", true, map[string]any{
				"phase": "Running",
			}, "mid-a"),
			wantNotify:     true,
			wantNamespace:  "ns1",
			wantMiddleware: "mid-a",
		},
		{
			name: "status resource version change falls back to custom resource name",
			oldObj: testCustomResource("ns1", "redis-a", "1", true, map[string]any{
				"phase": "Creating",
			}, ""),
			newObj: testCustomResource("ns1", "redis-a", "2", true, map[string]any{
				"phase": "Running",
			}, ""),
			wantNotify:     true,
			wantNamespace:  "ns1",
			wantMiddleware: "redis-a",
		},
		{
			name: "same resource version does not notify",
			oldObj: testCustomResource("ns1", "redis-a", "1", true, map[string]any{
				"phase": "Creating",
			}, "mid-a"),
			newObj: testCustomResource("ns1", "redis-a", "1", true, map[string]any{
				"phase": "Running",
			}, "mid-a"),
		},
		{
			name: "metadata only resource version change does not notify",
			oldObj: testCustomResource("ns1", "redis-a", "1", true, map[string]any{
				"phase": "Running",
			}, "mid-a"),
			newObj: testCustomResource("ns1", "redis-a", "2", true, map[string]any{
				"phase": "Running",
			}, "mid-a"),
		},
		{
			name: "missing package label does not notify",
			oldObj: testCustomResource("ns1", "redis-a", "1", false, map[string]any{
				"phase": "Creating",
			}, "mid-a"),
			newObj: testCustomResource("ns1", "redis-a", "2", false, map[string]any{
				"phase": "Running",
			}, "mid-a"),
		},
		{
			name:   "non custom resource objects do not notify",
			oldObj: struct{}{},
			newObj: struct{}{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []struct {
				namespace  string
				middleware string
			}
			handler := newResourceEventHandlerFuncs(context.Background(), nil, func(namespace, middlewareName string) {
				got = append(got, struct {
					namespace  string
					middleware string
				}{namespace: namespace, middleware: middlewareName})
			})

			handler.UpdateFunc(tt.oldObj, tt.newObj)

			if !tt.wantNotify {
				if len(got) != 0 {
					t.Fatalf("notifications = %#v, want none", got)
				}
				return
			}
			if len(got) != 1 {
				t.Fatalf("notifications = %#v, want exactly one", got)
			}
			if got[0].namespace != tt.wantNamespace || got[0].middleware != tt.wantMiddleware {
				t.Fatalf("notification = %s/%s, want %s/%s",
					got[0].namespace, got[0].middleware, tt.wantNamespace, tt.wantMiddleware)
			}
		})
	}
}

func testCustomResource(namespace, name, resourceVersion string, withPackageLabel bool, status map[string]any, ownerMiddlewareName string) *unstructured.Unstructured {
	cr := &unstructured.Unstructured{Object: map[string]any{
		"status": status,
	}}
	cr.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "example.io",
		Version: "v1",
		Kind:    "ExampleCluster",
	})
	cr.SetNamespace(namespace)
	cr.SetName(name)
	cr.SetResourceVersion(resourceVersion)
	if withPackageLabel {
		cr.SetLabels(map[string]string{
			v1.LabelPackageName: "example-1.0.0",
		})
	}
	if ownerMiddlewareName != "" {
		cr.SetOwnerReferences([]metav1.OwnerReference{{
			APIVersion: v1.GroupVersion.String(),
			Kind:       "Middleware",
			Name:       ownerMiddlewareName,
		}})
	}
	return cr
}
