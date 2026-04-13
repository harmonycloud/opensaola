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

package controller

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

func TestDeriveRuntimePhase(t *testing.T) {
	tests := []struct {
		name   string
		status *appsv1.DeploymentStatus
		want   string
	}{
		{
			name: "scaled to zero",
			status: &appsv1.DeploymentStatus{
				Replicas: 0,
			},
			want: "Scaled to Zero",
		},
		{
			name: "ready",
			status: &appsv1.DeploymentStatus{
				Replicas:          3,
				ReadyReplicas:     3,
				AvailableReplicas: 3,
			},
			want: "Ready",
		},
		{
			name: "replica failure",
			status: &appsv1.DeploymentStatus{
				Replicas:      3,
				ReadyReplicas: 1,
				Conditions: []appsv1.DeploymentCondition{
					{
						Type:   appsv1.DeploymentReplicaFailure,
						Status: corev1.ConditionTrue,
					},
				},
			},
			want: "ReplicaFailure",
		},
		{
			name: "unavailable",
			status: &appsv1.DeploymentStatus{
				Replicas:      3,
				ReadyReplicas: 0,
				Conditions: []appsv1.DeploymentCondition{
					{
						Type:   appsv1.DeploymentProgressing,
						Status: corev1.ConditionFalse,
					},
				},
			},
			want: "Unavailable",
		},
		{
			name: "progressing",
			status: &appsv1.DeploymentStatus{
				Replicas:        3,
				UpdatedReplicas: 2,
				ReadyReplicas:   2,
			},
			want: "Progressing",
		},
		{
			name: "unknown",
			status: &appsv1.DeploymentStatus{
				Replicas:          3,
				UpdatedReplicas:   3,
				ReadyReplicas:     3,
				AvailableReplicas: 0,
			},
			want: "Unknown",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := deriveRuntimePhase(tt.status)
			if got != tt.want {
				t.Errorf("deriveRuntimePhase() = %v, want %v", got, tt.want)
			}
		})
	}
}
