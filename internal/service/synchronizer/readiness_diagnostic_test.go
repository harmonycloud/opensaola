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
