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
