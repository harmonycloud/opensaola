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

package synchronizer

import (
	"strings"
	"testing"
	"time"

	zeusv1 "github.com/harmonycloud/opensaola/api/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestDeriveMiddlewareReadinessDiagnostic_PodImagePullTLS(t *testing.T) {
	mid := &zeusv1.Middleware{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "demo-milvus",
			Namespace:  "middleware",
			Generation: 7,
		},
		Status: zeusv1.MiddlewareStatus{ObservedGeneration: 6},
	}
	cr := &unstructured.Unstructured{}
	cr.SetAPIVersion("apps/v1")
	cr.SetKind("Deployment")
	cr.SetNamespace("middleware")
	cr.SetName("demo-milvus")
	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "demo-milvus-0", Namespace: "middleware"},
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{{
				Name:  "milvus",
				Image: "10.10.101.172:443/middleware/milvus:v2.4.0",
				State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{
					Reason:  "ErrImagePull",
					Message: "failed to pull image: x509: certificate signed by unknown authority",
				}},
			}},
		},
	}

	got := deriveMiddlewareReadinessDiagnostic(mid, cr, map[string]corev1.Pod{pod.Name: pod}, nil)
	for _, want := range []string{
		"phase=workload-readiness",
		"controller=middleware-synchronizer",
		"resource=middleware.cn/v1/Middleware middleware/demo-milvus",
		"failedObject=v1/Pod middleware/demo-milvus-0",
		"ownerRef=apps/v1/Deployment middleware/demo-milvus",
		"fieldPath=status.containerStatuses[name=milvus].state.waiting",
		"actual=ErrImagePull",
		"generation=7",
		"observedGeneration=6",
		"staleStatus=true",
		"causeCategory=RegistryTLS",
		"10.10.101.172:443/middleware/milvus:v2.4.0",
		"next=kubectl describe pod demo-milvus-0 -n middleware",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected middleware readiness diagnostic to contain %q, got %q", want, got)
		}
	}
}

func TestApplyLocalReadinessDiagnostic_RespectsOperatorStatusOwnership(t *testing.T) {
	t.Parallel()

	cr := &unstructured.Unstructured{}
	cr.SetAPIVersion("psmdb.percona.com/v1")
	cr.SetKind("PerconaServerMongoDB")
	cr.SetNamespace("middleware")
	cr.SetName("demo-mongodb")
	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "demo-mongodb-rs0-0", Namespace: "middleware"},
		Status: corev1.PodStatus{Conditions: []corev1.PodCondition{{
			Type:    corev1.PodReady,
			Status:  corev1.ConditionFalse,
			Reason:  "ContainersNotReady",
			Message: "containers with unready status: [mongod]",
		}}},
	}

	tests := []struct {
		name           string
		mid            *zeusv1.Middleware
		wantPhase      zeusv1.Phase
		wantReason     string
		wantDiagnostic bool
	}{
		{
			name: "operator CR status remains authoritative",
			mid: &zeusv1.Middleware{
				Spec: zeusv1.MiddlewareSpec{OperatorBaseline: zeusv1.OperatorBaseline{
					Name:    "mongodb-operator-standard",
					GvkName: "v1",
				}},
				Status: zeusv1.MiddlewareStatus{CustomResources: zeusv1.CustomResources{
					Phase:  zeusv1.Phase("initializing"),
					Reason: "operator is initializing replica set",
				}},
			},
			wantPhase:  zeusv1.Phase("initializing"),
			wantReason: "operator is initializing replica set",
		},
		{
			name: "operator ready status remains authoritative",
			mid: &zeusv1.Middleware{
				Spec: zeusv1.MiddlewareSpec{OperatorBaseline: zeusv1.OperatorBaseline{
					Name:    "mongodb-operator-standard",
					GvkName: "v1",
				}},
				Status: zeusv1.MiddlewareStatus{CustomResources: zeusv1.CustomResources{
					Phase: zeusv1.Phase("ready"),
				}},
			},
			wantPhase: zeusv1.Phase("ready"),
		},
		{
			name: "partial operator baseline remains CR authoritative",
			mid: &zeusv1.Middleware{
				Spec: zeusv1.MiddlewareSpec{OperatorBaseline: zeusv1.OperatorBaseline{
					Name: "mongodb-operator-standard",
				}},
				Status: zeusv1.MiddlewareStatus{CustomResources: zeusv1.CustomResources{
					Phase:  zeusv1.Phase("initializing"),
					Reason: "operator is initializing replica set",
				}},
			},
			wantPhase:  zeusv1.Phase("initializing"),
			wantReason: "operator is initializing replica set",
		},
		{
			name: "no operator middleware derives a readiness failure",
			mid: &zeusv1.Middleware{
				Status: zeusv1.MiddlewareStatus{CustomResources: zeusv1.CustomResources{
					Phase: zeusv1.PhaseCreating,
				}},
			},
			wantPhase:      zeusv1.PhaseFailed,
			wantDiagnostic: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mid := tt.mid.DeepCopy()
			applyLocalReadinessDiagnostic(mid, cr, map[string]corev1.Pod{pod.Name: pod}, nil)

			if got := mid.Status.CustomResources.Phase; got != tt.wantPhase {
				t.Fatalf("phase = %q, want %q", got, tt.wantPhase)
			}
			if tt.wantDiagnostic {
				if !strings.Contains(mid.Status.CustomResources.Reason, "phase=workload-readiness") {
					t.Fatalf("expected local readiness diagnostic, got %q", mid.Status.CustomResources.Reason)
				}
				return
			}
			if got := mid.Status.CustomResources.Reason; got != tt.wantReason {
				t.Fatalf("reason = %q, want %q", got, tt.wantReason)
			}
		})
	}
}

func TestDeriveMiddlewareReadinessDiagnostic_ContainerCreatingIsTransient(t *testing.T) {
	mid := &zeusv1.Middleware{
		ObjectMeta: metav1.ObjectMeta{Name: "demo-minio", Namespace: "middleware", Generation: 2},
		Status:     zeusv1.MiddlewareStatus{ObservedGeneration: 2},
	}
	cr := &unstructured.Unstructured{}
	cr.SetAPIVersion("apps/v1")
	cr.SetKind("StatefulSet")
	cr.SetNamespace("middleware")
	cr.SetName("demo-minio")
	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "demo-minio-0", Namespace: "middleware"},
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{{
				Name:  "minio",
				Image: "registry.local/middleware/minio:latest",
				State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{
					Reason: "ContainerCreating",
				}},
			}},
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodReady, Status: corev1.ConditionFalse, Reason: "ContainersNotReady"},
				{Type: corev1.ContainersReady, Status: corev1.ConditionFalse, Reason: "ContainersNotReady"},
				{Type: corev1.PodScheduled, Status: corev1.ConditionTrue},
			},
		},
	}

	got := deriveMiddlewareReadinessDiagnostic(mid, cr, map[string]corev1.Pod{pod.Name: pod}, nil)
	if got != "" {
		t.Fatalf("expected ContainerCreating startup to remain transient, got %q", got)
	}
}

func TestDeriveMiddlewareReadinessDiagnostic_PodInitializingIsTransient(t *testing.T) {
	mid := &zeusv1.Middleware{ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "middleware"}}
	cr := &unstructured.Unstructured{}
	cr.SetAPIVersion("apps/v1")
	cr.SetKind("StatefulSet")
	cr.SetNamespace("middleware")
	cr.SetName("demo")
	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "demo-0", Namespace: "middleware"},
		Status: corev1.PodStatus{
			InitContainerStatuses: []corev1.ContainerStatus{{
				Name:  "init",
				Image: "registry.local/init:latest",
				State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{
					Reason: "PodInitializing",
				}},
			}},
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodReady, Status: corev1.ConditionFalse, Reason: "ContainersNotReady"},
				{Type: corev1.ContainersReady, Status: corev1.ConditionFalse, Reason: "ContainersNotReady"},
			},
		},
	}

	got := deriveMiddlewareReadinessDiagnostic(mid, cr, map[string]corev1.Pod{pod.Name: pod}, nil)
	if got != "" {
		t.Fatalf("expected PodInitializing startup to remain transient, got %q", got)
	}
}

func TestDeriveMiddlewareReadinessDiagnostic_TransientWaitDoesNotHideUnschedulable(t *testing.T) {
	mid := &zeusv1.Middleware{ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "middleware"}}
	cr := &unstructured.Unstructured{}
	cr.SetAPIVersion("apps/v1")
	cr.SetKind("StatefulSet")
	cr.SetNamespace("middleware")
	cr.SetName("demo")
	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "demo-0", Namespace: "middleware"},
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{{
				Name:  "demo",
				Image: "registry.local/demo:latest",
				State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{
					Reason: "ContainerCreating",
				}},
			}},
			Conditions: []corev1.PodCondition{{
				Type:    corev1.PodScheduled,
				Status:  corev1.ConditionFalse,
				Reason:  "Unschedulable",
				Message: "0/3 nodes are available",
			}},
		},
	}

	got := deriveMiddlewareReadinessDiagnostic(mid, cr, map[string]corev1.Pod{pod.Name: pod}, nil)
	for _, want := range []string{
		"fieldPath=status.conditions[type=PodScheduled]",
		"causeCategory=SchedulingUnschedulable",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected transient wait not to hide %q, got %q", want, got)
		}
	}
}

func TestDeriveMiddlewareReadinessDiagnostic_TransientWaitDoesNotHideActionableWait(t *testing.T) {
	mid := &zeusv1.Middleware{ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "middleware"}}
	cr := &unstructured.Unstructured{}
	cr.SetAPIVersion("apps/v1")
	cr.SetKind("StatefulSet")
	cr.SetNamespace("middleware")
	cr.SetName("demo")
	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "demo-0", Namespace: "middleware"},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{
			{
				Name:  "starting",
				Image: "registry.local/starting:latest",
				State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{
					Reason: "ContainerCreating",
				}},
			},
			{
				Name:  "broken",
				Image: "registry.local/broken:latest",
				State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{
					Reason:  "CreateContainerConfigError",
					Message: "secret not found",
				}},
			},
		}},
	}

	got := deriveMiddlewareReadinessDiagnostic(mid, cr, map[string]corev1.Pod{pod.Name: pod}, nil)
	for _, want := range []string{
		"fieldPath=status.containerStatuses[name=broken].state.waiting",
		"actual=CreateContainerConfigError",
		"secret not found",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected transient wait not to hide %q, got %q", want, got)
		}
	}
}

func TestDeriveMiddlewareReadinessDiagnostic_InitialReadinessGrace(t *testing.T) {
	now := time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC)
	pendingPVC := corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "export-demo-minio-0", Namespace: "middleware"},
		Status:     corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimPending},
	}

	tests := []struct {
		name           string
		createdAt      time.Time
		midCreatedAt   time.Time
		midGeneration  int64
		crGeneration   int64
		runtimePhase   zeusv1.Phase
		pods           map[string]corev1.Pod
		pvcs           map[string]corev1.PersistentVolumeClaim
		wantEmpty      bool
		wantDiagnostic string
	}{
		{
			name:      "pending PVC remains transient during first-start window",
			createdAt: now.Add(-time.Minute),
			pvcs:      map[string]corev1.PersistentVolumeClaim{pendingPVC.Name: pendingPVC},
			wantEmpty: true,
		},
		{
			name:          "Middleware update before first workload ready remains transient",
			createdAt:     now.Add(-time.Minute),
			midGeneration: 2,
			pvcs:          map[string]corev1.PersistentVolumeClaim{pendingPVC.Name: pendingPVC},
			wantEmpty:     true,
		},
		{
			name:      "pod ready false without container status remains transient during first-start window",
			createdAt: now.Add(-time.Minute),
			pods: map[string]corev1.Pod{
				"demo-minio-0": {
					ObjectMeta: metav1.ObjectMeta{Name: "demo-minio-0", Namespace: "middleware"},
					Status: corev1.PodStatus{Conditions: []corev1.PodCondition{
						{Type: corev1.PodReady, Status: corev1.ConditionFalse, Reason: "ContainersNotReady"},
						{Type: corev1.ContainersReady, Status: corev1.ConditionFalse, Reason: "ContainersNotReady"},
					}},
				},
			},
			wantEmpty: true,
		},
		{
			name:      "PVC binding unschedulable remains transient during first-start window",
			createdAt: now.Add(-time.Minute),
			pods: map[string]corev1.Pod{
				"demo-minio-0": {
					ObjectMeta: metav1.ObjectMeta{Name: "demo-minio-0", Namespace: "middleware"},
					Status: corev1.PodStatus{Conditions: []corev1.PodCondition{{
						Type:    corev1.PodScheduled,
						Status:  corev1.ConditionFalse,
						Reason:  "Unschedulable",
						Message: "0/3 nodes are available: pod has unbound immediate PersistentVolumeClaims",
					}}},
				},
			},
			wantEmpty: true,
		},
		{
			name:      "resource unschedulable stays actionable during first-start window",
			createdAt: now.Add(-time.Minute),
			pods: map[string]corev1.Pod{
				"demo-minio-0": {
					ObjectMeta: metav1.ObjectMeta{Name: "demo-minio-0", Namespace: "middleware"},
					Status: corev1.PodStatus{Conditions: []corev1.PodCondition{{
						Type:    corev1.PodScheduled,
						Status:  corev1.ConditionFalse,
						Reason:  "Unschedulable",
						Message: "0/3 nodes are available: 1 Insufficient cpu",
					}}},
				},
			},
			wantDiagnostic: "causeCategory=SchedulingUnschedulable",
		},
		{
			name:      "mixed PVC binding and resource unschedulable stays actionable during first-start window",
			createdAt: now.Add(-time.Minute),
			pods: map[string]corev1.Pod{
				"demo-minio-0": {
					ObjectMeta: metav1.ObjectMeta{Name: "demo-minio-0", Namespace: "middleware"},
					Status: corev1.PodStatus{Conditions: []corev1.PodCondition{{
						Type:    corev1.PodScheduled,
						Status:  corev1.ConditionFalse,
						Reason:  "Unschedulable",
						Message: "0/3 nodes are available: 1 Insufficient cpu, 2 pod has unbound immediate PersistentVolumeClaims",
					}}},
				},
			},
			wantDiagnostic: "causeCategory=SchedulingUnschedulable",
		},
		{
			name:      "actionable container wait stays actionable during first-start window",
			createdAt: now.Add(-time.Minute),
			pods: map[string]corev1.Pod{
				"demo-minio-0": {
					ObjectMeta: metav1.ObjectMeta{Name: "demo-minio-0", Namespace: "middleware"},
					Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
						Name: "minio",
						State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{
							Reason:  "ErrImagePull",
							Message: "registry unavailable",
						}},
					}}},
				},
			},
			wantDiagnostic: "actual=ErrImagePull",
		},
		{
			name:           "pending PVC reports after first-start window",
			createdAt:      now.Add(-initialReadinessGracePeriod),
			pvcs:           map[string]corev1.PersistentVolumeClaim{pendingPVC.Name: pendingPVC},
			wantDiagnostic: "causeCategory=PVCPending",
		},
		{
			name:           "old primary CR is not extended by a fresh Middleware timestamp",
			createdAt:      now.Add(-initialReadinessGracePeriod),
			midCreatedAt:   now,
			pvcs:           map[string]corev1.PersistentVolumeClaim{pendingPVC.Name: pendingPVC},
			wantDiagnostic: "causeCategory=PVCPending",
		},
		{
			name:           "missing primary CR timestamp does not suppress diagnostics",
			pvcs:           map[string]corev1.PersistentVolumeClaim{pendingPVC.Name: pendingPVC},
			wantDiagnostic: "causeCategory=PVCPending",
		},
		{
			name:           "updated primary CR does not use the first-start window",
			createdAt:      now.Add(-time.Minute),
			crGeneration:   2,
			pvcs:           map[string]corev1.PersistentVolumeClaim{pendingPVC.Name: pendingPVC},
			wantDiagnostic: "causeCategory=PVCPending",
		},
		{
			name:           "pending PVC remains actionable after the runtime has already failed",
			createdAt:      now.Add(-time.Minute),
			runtimePhase:   zeusv1.PhaseFailed,
			pvcs:           map[string]corev1.PersistentVolumeClaim{pendingPVC.Name: pendingPVC},
			wantDiagnostic: "causeCategory=PVCPending",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			midGeneration := tt.midGeneration
			if midGeneration == 0 {
				midGeneration = 1
			}
			crGeneration := tt.crGeneration
			if crGeneration == 0 {
				crGeneration = 1
			}
			phase := tt.runtimePhase
			if phase == "" {
				phase = zeusv1.PhaseCreating
			}
			mid := &zeusv1.Middleware{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "demo-minio",
					Namespace:         "middleware",
					Generation:        midGeneration,
					CreationTimestamp: metav1.NewTime(tt.midCreatedAt),
				},
				Status: zeusv1.MiddlewareStatus{CustomResources: zeusv1.CustomResources{
					Phase: phase,
				}},
			}
			cr := &unstructured.Unstructured{}
			cr.SetAPIVersion("apps/v1")
			cr.SetKind("StatefulSet")
			cr.SetNamespace("middleware")
			cr.SetName(mid.Name)
			cr.SetGeneration(crGeneration)
			cr.SetCreationTimestamp(metav1.NewTime(tt.createdAt))

			got := deriveMiddlewareReadinessDiagnosticAt(mid, cr, tt.pods, tt.pvcs, now)
			if tt.wantEmpty {
				if got != "" {
					t.Fatalf("expected no diagnostic, got %q", got)
				}
				return
			}
			if !strings.Contains(got, tt.wantDiagnostic) {
				t.Fatalf("expected diagnostic to contain %q, got %q", tt.wantDiagnostic, got)
			}
		})
	}
}

func TestDeriveMiddlewareReadinessDiagnostic_PVCPending(t *testing.T) {
	mid := &zeusv1.Middleware{ObjectMeta: metav1.ObjectMeta{Name: "demo-milvus", Namespace: "middleware"}}
	cr := &unstructured.Unstructured{}
	cr.SetAPIVersion("apps/v1")
	cr.SetKind("StatefulSet")
	cr.SetNamespace("middleware")
	cr.SetName("demo-milvus")
	pvc := corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "data-demo-milvus-0", Namespace: "middleware"},
		Status:     corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimPending},
	}

	got := deriveMiddlewareReadinessDiagnostic(mid, cr, nil, map[string]corev1.PersistentVolumeClaim{pvc.Name: pvc})
	for _, want := range []string{
		"failedObject=v1/PersistentVolumeClaim middleware/data-demo-milvus-0",
		"ownerRef=apps/v1/StatefulSet middleware/demo-milvus",
		"fieldPath=status.phase",
		"expected=Bound",
		"actual=Pending",
		"causeCategory=PVCPending",
		"next=kubectl describe pvc data-demo-milvus-0 -n middleware",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected middleware PVC diagnostic to contain %q, got %q", want, got)
		}
	}
}

func TestDeriveMiddlewareReadinessDiagnostic_PodUnschedulable(t *testing.T) {
	mid := &zeusv1.Middleware{
		ObjectMeta: metav1.ObjectMeta{Name: "demo-milvus", Namespace: "middleware", Generation: 9},
		Status:     zeusv1.MiddlewareStatus{ObservedGeneration: 9},
	}
	cr := &unstructured.Unstructured{}
	cr.SetAPIVersion("apps/v1")
	cr.SetKind("Deployment")
	cr.SetNamespace("middleware")
	cr.SetName("demo-milvus")
	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "demo-milvus-0", Namespace: "middleware"},
		Spec:       corev1.PodSpec{ServiceAccountName: "milvus-sa"},
		Status: corev1.PodStatus{
			Conditions: []corev1.PodCondition{{
				Type:    corev1.PodScheduled,
				Status:  corev1.ConditionFalse,
				Reason:  "Unschedulable",
				Message: "0/3 nodes are available: pod has unbound immediate PersistentVolumeClaims",
			}},
		},
	}

	got := deriveMiddlewareReadinessDiagnostic(mid, cr, map[string]corev1.Pod{pod.Name: pod}, nil)
	for _, want := range []string{
		"phase=workload-readiness",
		"failedObject=v1/Pod middleware/demo-milvus-0",
		"fieldPath=status.conditions[type=PodScheduled]",
		"actual=False",
		"causeCategory=SchedulingUnschedulable",
		"serviceAccountName=milvus-sa",
		"Unschedulable",
		"next=kubectl describe pod demo-milvus-0 -n middleware",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected middleware pod condition diagnostic to contain %q, got %q", want, got)
		}
	}
}
