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
	"errors"
	"testing"
	"time"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestCustomResourceWatcher_MembersRemainIsolated(t *testing.T) {
	gvk := schema.GroupVersionKind{Group: "test", Version: "v1", Kind: "Foo"}
	cw := NewCRWatcher(gvk, "ns1")

	if !cw.addMember("first") || !cw.addMember("second") {
		t.Fatal("expected watcher to accept both members")
	}
	if removed, empty := cw.removeMember("first"); !removed || empty {
		t.Fatalf("release first member = removed:%v empty:%v, want true:false", removed, empty)
	}
	select {
	case <-cw.StopChan:
		t.Fatal("watcher stopped after releasing one of two members")
	default:
	}
	if removed, empty := cw.removeMember("second"); !removed || !empty {
		t.Fatalf("release final member = removed:%v empty:%v, want true:true", removed, empty)
	}
	select {
	case <-cw.StopChan:
	default:
		t.Fatal("watcher did not stop after final member release")
	}
}

func TestEnsureCRWatcher_RetriesInformerFailure(t *testing.T) {
	StopAllCRWatchers()
	t.Cleanup(StopAllCRWatchers)

	cr := testCustomResource("ns1", "redis-a", "1", true, map[string]any{"phase": "Creating"}, "mid-a")
	firstAttempt := make(chan struct{}, 1)
	secondAttempt := make(chan struct{}, 1)
	attempt := 0
	runner := func(_ context.Context, _ client.Client, stop <-chan struct{}, _ schema.GroupVersionKind, _ string, _ cache.ResourceEventHandlerFuncs) error {
		attempt++
		if attempt == 1 {
			firstAttempt <- struct{}{}
			return errors.New("transient dynamic client failure")
		}
		secondAttempt <- struct{}{}
		<-stop
		return nil
	}

	cw, created, err := ensureCRWatcher(context.Background(), nil, cr, cache.ResourceEventHandlerFuncs{}, runner, func(int) time.Duration { return time.Millisecond })
	if err != nil || !created {
		t.Fatalf("ensureCRWatcher() = created:%v err:%v, want created watcher", created, err)
	}

	select {
	case <-firstAttempt:
	case <-time.After(time.Second):
		t.Fatal("first informer attempt did not run")
	}
	select {
	case <-secondAttempt:
	case <-time.After(time.Second):
		t.Fatal("watcher did not retry after informer failure")
	}

	cw.Stop()
	select {
	case <-cw.Done():
	case <-time.After(time.Second):
		t.Fatal("watcher supervisor did not exit after stop")
	}
	if _, ok := CustomResourceWatcherMap.Load(cw.GetKey()); ok {
		t.Fatal("stopped watcher remained in registry")
	}
}

func TestCustomResourceWatcher_CleanupPreservesReplacement(t *testing.T) {
	StopAllCRWatchers()
	t.Cleanup(StopAllCRWatchers)

	gvk := schema.GroupVersionKind{Group: "test", Version: "v1", Kind: "Foo"}
	old := NewCRWatcher(gvk, "ns1")
	if !old.addMember("old") {
		t.Fatal("expected old watcher member registration")
	}
	CustomResourceWatcherMap.Store(old.GetKey(), old)
	started := make(chan struct{}, 1)
	runner := func(_ context.Context, _ client.Client, stop <-chan struct{}, _ schema.GroupVersionKind, _ string, _ cache.ResourceEventHandlerFuncs) error {
		started <- struct{}{}
		<-stop
		return nil
	}
	go old.run(context.Background(), nil, cache.ResourceEventHandlerFuncs{}, runner, func(int) time.Duration { return time.Millisecond })
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("old watcher did not start")
	}

	replacement := NewCRWatcher(gvk, "ns1")
	if !replacement.addMember("replacement") {
		t.Fatal("expected replacement watcher member registration")
	}
	CustomResourceWatcherMap.Store(old.GetKey(), replacement)
	old.Stop()
	select {
	case <-old.Done():
	case <-time.After(time.Second):
		t.Fatal("old watcher did not exit")
	}

	actual, ok := CustomResourceWatcherMap.Load(old.GetKey())
	if !ok || actual != replacement {
		t.Fatalf("watcher registry = %#v, want replacement", actual)
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

func TestCustomResourceAddNotifyDecision(t *testing.T) {
	tests := []struct {
		name           string
		obj            any
		wantNotify     bool
		wantNamespace  string
		wantMiddleware string
	}{
		{
			name:           "initial list add notifies middleware owner",
			obj:            testCustomResource("ns1", "redis-a", "2", true, map[string]any{"phase": "Running"}, "mid-a"),
			wantNotify:     true,
			wantNamespace:  "ns1",
			wantMiddleware: "mid-a",
		},
		{
			name:           "initial list add falls back to custom resource name",
			obj:            testCustomResource("ns1", "redis-a", "2", true, map[string]any{"phase": "Running"}, ""),
			wantNotify:     true,
			wantNamespace:  "ns1",
			wantMiddleware: "redis-a",
		},
		{
			name: "missing package label does not notify",
			obj:  testCustomResource("ns1", "redis-a", "2", false, map[string]any{"phase": "Running"}, "mid-a"),
		},
		{
			name: "non custom resource object does not notify",
			obj:  struct{}{},
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

			handler.AddFunc(tt.obj)

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
				t.Fatalf("notification = %s/%s, want %s/%s", got[0].namespace, got[0].middleware, tt.wantNamespace, tt.wantMiddleware)
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
