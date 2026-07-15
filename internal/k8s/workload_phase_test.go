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
	"testing"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestDeriveDeploymentPhase(t *testing.T) {
	t.Parallel()

	three := int32(3)
	zero := int32(0)
	tests := []struct {
		name     string
		object   *appsv1.Deployment
		previous v1.Phase
		want     v1.Phase
	}{
		{name: "nil object", want: v1.PhaseUnknown},
		{
			name:   "initial empty status",
			object: &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Generation: 1}},
			want:   v1.PhaseCreating,
		},
		{
			name: "initial partial replicas remain creating",
			object: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{Generation: 1},
				Spec:       appsv1.DeploymentSpec{Replicas: &three},
				Status: appsv1.DeploymentStatus{
					ObservedGeneration:  1,
					Replicas:            1,
					UpdatedReplicas:     1,
					ReadyReplicas:       1,
					AvailableReplicas:   1,
					UnavailableReplicas: 2,
				},
			},
			want: v1.PhaseCreating,
		},
		{
			name: "fully converged",
			object: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{Generation: 1},
				Spec:       appsv1.DeploymentSpec{Replicas: &three},
				Status: appsv1.DeploymentStatus{
					ObservedGeneration: 1,
					Replicas:           3,
					UpdatedReplicas:    3,
					ReadyReplicas:      3,
					AvailableReplicas:  3,
				},
			},
			previous: v1.PhaseCreating,
			want:     v1.PhaseRunning,
		},
		{
			name: "new generation not observed",
			object: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{Generation: 2},
				Spec:       appsv1.DeploymentSpec{Replicas: &three},
				Status: appsv1.DeploymentStatus{
					ObservedGeneration: 1,
					Replicas:           3,
					UpdatedReplicas:    3,
					ReadyReplicas:      3,
					AvailableReplicas:  3,
				},
			},
			previous: v1.PhaseRunning,
			want:     v1.PhaseUpdating,
		},
		{
			name: "current replica failure",
			object: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{Generation: 1},
				Status: appsv1.DeploymentStatus{
					ObservedGeneration: 1,
					Conditions: []appsv1.DeploymentCondition{{
						Type: appsv1.DeploymentReplicaFailure, Status: corev1.ConditionTrue,
					}},
				},
			},
			want: v1.PhaseFailed,
		},
		{
			name: "current progress deadline exceeded",
			object: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{Generation: 1},
				Status: appsv1.DeploymentStatus{
					ObservedGeneration: 1,
					Conditions: []appsv1.DeploymentCondition{{
						Type: appsv1.DeploymentProgressing, Status: corev1.ConditionFalse, Reason: "ProgressDeadlineExceeded",
					}},
				},
			},
			want: v1.PhaseFailed,
		},
		{
			name: "stale failure does not fail new generation",
			object: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{Generation: 2},
				Status: appsv1.DeploymentStatus{
					ObservedGeneration: 1,
					Conditions: []appsv1.DeploymentCondition{{
						Type: appsv1.DeploymentProgressing, Status: corev1.ConditionFalse, Reason: "ProgressDeadlineExceeded",
					}},
				},
			},
			previous: v1.PhaseRunning,
			want:     v1.PhaseUpdating,
		},
		{
			name: "scaled to zero is converged",
			object: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{Generation: 1},
				Spec:       appsv1.DeploymentSpec{Replicas: &zero},
				Status:     appsv1.DeploymentStatus{ObservedGeneration: 1},
			},
			want: v1.PhaseRunning,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := DeriveDeploymentPhase(tt.object, tt.previous); got != tt.want {
				t.Fatalf("DeriveDeploymentPhase() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDeriveStatefulSetPhase(t *testing.T) {
	t.Parallel()

	four := int32(4)
	zero := int32(0)
	partition := int32(2)
	tests := []struct {
		name     string
		object   *appsv1.StatefulSet
		previous v1.Phase
		want     v1.Phase
	}{
		{name: "nil object", want: v1.PhaseUnknown},
		{
			name: "initial partial replicas remain creating",
			object: &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{Generation: 1},
				Spec:       appsv1.StatefulSetSpec{Replicas: &four},
				Status: appsv1.StatefulSetStatus{
					ObservedGeneration: 1,
					Replicas:           1,
					CurrentReplicas:    1,
					UpdatedReplicas:    1,
				},
			},
			want: v1.PhaseCreating,
		},
		{
			name: "fully converged",
			object: &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{Generation: 1},
				Spec: appsv1.StatefulSetSpec{
					Replicas:       &four,
					UpdateStrategy: appsv1.StatefulSetUpdateStrategy{Type: appsv1.RollingUpdateStatefulSetStrategyType},
				},
				Status: appsv1.StatefulSetStatus{
					ObservedGeneration: 1,
					Replicas:           4,
					CurrentReplicas:    4,
					UpdatedReplicas:    4,
					ReadyReplicas:      4,
					AvailableReplicas:  4,
					CurrentRevision:    "revision-a",
					UpdateRevision:     "revision-a",
				},
			},
			previous: v1.PhaseCreating,
			want:     v1.PhaseRunning,
		},
		{
			name: "rolling update remains updating",
			object: &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{Generation: 2},
				Spec:       appsv1.StatefulSetSpec{Replicas: &four},
				Status: appsv1.StatefulSetStatus{
					ObservedGeneration: 2,
					Replicas:           4,
					CurrentReplicas:    3,
					UpdatedReplicas:    1,
					ReadyReplicas:      3,
					AvailableReplicas:  3,
					CurrentRevision:    "revision-a",
					UpdateRevision:     "revision-b",
				},
			},
			previous: v1.PhaseRunning,
			want:     v1.PhaseUpdating,
		},
		{
			name: "partitioned rollout is converged",
			object: &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{Generation: 2},
				Spec: appsv1.StatefulSetSpec{
					Replicas: &four,
					UpdateStrategy: appsv1.StatefulSetUpdateStrategy{
						Type:          appsv1.RollingUpdateStatefulSetStrategyType,
						RollingUpdate: &appsv1.RollingUpdateStatefulSetStrategy{Partition: &partition},
					},
				},
				Status: appsv1.StatefulSetStatus{
					ObservedGeneration: 2,
					Replicas:           4,
					CurrentReplicas:    2,
					UpdatedReplicas:    2,
					ReadyReplicas:      4,
					AvailableReplicas:  4,
					CurrentRevision:    "revision-a",
					UpdateRevision:     "revision-b",
				},
			},
			previous: v1.PhaseUpdating,
			want:     v1.PhaseRunning,
		},
		{
			name: "scaled to zero is converged",
			object: &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{Generation: 1},
				Spec:       appsv1.StatefulSetSpec{Replicas: &zero},
				Status:     appsv1.StatefulSetStatus{ObservedGeneration: 1},
			},
			want: v1.PhaseRunning,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := DeriveStatefulSetPhase(tt.object, tt.previous); got != tt.want {
				t.Fatalf("DeriveStatefulSetPhase() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDeriveDaemonSetPhase(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		object   *appsv1.DaemonSet
		previous v1.Phase
		want     v1.Phase
	}{
		{name: "nil object", want: v1.PhaseUnknown},
		{
			name:   "initial status not observed",
			object: &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Generation: 1}},
			want:   v1.PhaseCreating,
		},
		{
			name: "initial partial scheduling remains creating",
			object: &appsv1.DaemonSet{
				ObjectMeta: metav1.ObjectMeta{Generation: 1},
				Status: appsv1.DaemonSetStatus{
					ObservedGeneration:     1,
					DesiredNumberScheduled: 3,
					CurrentNumberScheduled: 1,
					UpdatedNumberScheduled: 1,
				},
			},
			want: v1.PhaseCreating,
		},
		{
			name: "fully converged",
			object: &appsv1.DaemonSet{
				ObjectMeta: metav1.ObjectMeta{Generation: 1},
				Status: appsv1.DaemonSetStatus{
					ObservedGeneration:     1,
					DesiredNumberScheduled: 3,
					CurrentNumberScheduled: 3,
					UpdatedNumberScheduled: 3,
					NumberReady:            3,
					NumberAvailable:        3,
				},
			},
			previous: v1.PhaseCreating,
			want:     v1.PhaseRunning,
		},
		{
			name: "rolling update remains updating",
			object: &appsv1.DaemonSet{
				ObjectMeta: metav1.ObjectMeta{Generation: 2},
				Status: appsv1.DaemonSetStatus{
					ObservedGeneration:     2,
					DesiredNumberScheduled: 3,
					CurrentNumberScheduled: 3,
					UpdatedNumberScheduled: 1,
					NumberReady:            2,
					NumberAvailable:        2,
					NumberUnavailable:      1,
				},
			},
			previous: v1.PhaseRunning,
			want:     v1.PhaseUpdating,
		},
		{
			name: "no eligible nodes is converged",
			object: &appsv1.DaemonSet{
				ObjectMeta: metav1.ObjectMeta{Generation: 1},
				Status:     appsv1.DaemonSetStatus{ObservedGeneration: 1},
			},
			want: v1.PhaseRunning,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := DeriveDaemonSetPhase(tt.object, tt.previous); got != tt.want {
				t.Fatalf("DeriveDaemonSetPhase() = %q, want %q", got, tt.want)
			}
		})
	}
}
