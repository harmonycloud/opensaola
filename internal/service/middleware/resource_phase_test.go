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

package middleware

import (
	"context"
	"testing"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	"github.com/harmonycloud/opensaola/internal/service/consts"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestSeedInitialCustomResourcePhase(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		action         consts.HandleAction
		reconcilePhase v1.Phase
		livePhase      v1.Phase
		want           v1.Phase
	}{
		{
			name:           "publish unknown phase seeds creating",
			action:         consts.HandleActionPublish,
			reconcilePhase: v1.PhaseUnknown,
			livePhase:      v1.PhaseUnknown,
			want:           v1.PhaseCreating,
		},
		{
			name:           "publish preserves runtime phase that won the race",
			action:         consts.HandleActionPublish,
			reconcilePhase: v1.PhaseUnknown,
			livePhase:      v1.PhaseRunning,
			want:           v1.PhaseRunning,
		},
		{
			name:           "publish does not reset existing phase",
			action:         consts.HandleActionPublish,
			reconcilePhase: v1.PhaseRunning,
			livePhase:      v1.PhaseRunning,
			want:           v1.PhaseRunning,
		},
		{
			name:           "update does not seed creating",
			action:         consts.HandleActionUpdate,
			reconcilePhase: v1.PhaseUnknown,
			livePhase:      v1.PhaseUnknown,
			want:           v1.PhaseUnknown,
		},
		{
			name:           "delete does not seed creating",
			action:         consts.HandleActionDelete,
			reconcilePhase: v1.PhaseUnknown,
			livePhase:      v1.PhaseUnknown,
			want:           v1.PhaseUnknown,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			scheme := runtime.NewScheme()
			if err := v1.AddToScheme(scheme); err != nil {
				t.Fatalf("add scheme: %v", err)
			}

			live := &v1.Middleware{
				ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "middleware"},
				Status: v1.MiddlewareStatus{
					CustomResources: v1.CustomResources{Phase: tt.livePhase},
				},
			}
			reconcile := live.DeepCopy()
			reconcile.Status.CustomResources.Phase = tt.reconcilePhase
			cli := fake.NewClientBuilder().
				WithScheme(scheme).
				WithStatusSubresource(&v1.Middleware{}).
				WithObjects(live).
				Build()

			if err := seedInitialCustomResourcePhase(context.Background(), cli, tt.action, reconcile); err != nil {
				t.Fatalf("seedInitialCustomResourcePhase() error = %v", err)
			}
			if got := reconcile.Status.CustomResources.Phase; got != tt.want {
				t.Fatalf("reconcile phase = %q, want %q", got, tt.want)
			}

			var stored v1.Middleware
			if err := cli.Get(context.Background(), client.ObjectKey{Name: live.Name, Namespace: live.Namespace}, &stored); err != nil {
				t.Fatalf("get stored Middleware: %v", err)
			}
			if got := stored.Status.CustomResources.Phase; got != tt.want {
				t.Fatalf("stored phase = %q, want %q", got, tt.want)
			}
		})
	}
}
