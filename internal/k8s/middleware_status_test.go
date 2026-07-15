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
	"context"
	"testing"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestUpdateMiddlewareStatusPreservesCustomResources(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := v1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}

	live := &v1.Middleware{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "middleware"},
		Status: v1.MiddlewareStatus{
			CustomResources: v1.CustomResources{
				Phase:    v1.PhaseRunning,
				Replicas: 3,
				Reason:   "all replicas are ready",
			},
			State:  v1.StateUnavailable,
			Reason: "old controller state",
		},
	}
	desired := live.DeepCopy()
	desired.Status.State = v1.StateAvailable
	desired.Status.Reason = "controller reconciliation completed"
	desired.Status.CustomResources = v1.CustomResources{Phase: v1.PhaseCreating}

	cli := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&v1.Middleware{}).
		WithObjects(live).
		Build()

	if err := UpdateMiddlewareStatus(context.Background(), cli, desired); err != nil {
		t.Fatalf("UpdateMiddlewareStatus() error = %v", err)
	}

	var stored v1.Middleware
	if err := cli.Get(context.Background(), client.ObjectKey{Name: live.Name, Namespace: live.Namespace}, &stored); err != nil {
		t.Fatalf("get stored Middleware: %v", err)
	}
	if got := stored.Status.CustomResources.Phase; got != v1.PhaseRunning {
		t.Fatalf("customResources.phase = %q, want %q", got, v1.PhaseRunning)
	}
	if got := stored.Status.CustomResources.Replicas; got != 3 {
		t.Fatalf("customResources.replicas = %d, want 3", got)
	}
	if got := stored.Status.State; got != v1.StateAvailable {
		t.Fatalf("state = %q, want %q", got, v1.StateAvailable)
	}
	if got := stored.Status.Reason; got != "controller reconciliation completed" {
		t.Fatalf("reason = %q, want controller reconciliation completed", got)
	}
}
