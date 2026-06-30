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
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestDeriveRuntimeDiagnostic_ImagePullTLSIncludesPodAndEvent(t *testing.T) {
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "opensaola-controller-manager",
			Namespace:  "middleware-operator",
			Generation: 4,
		},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "opensaola"}},
		},
		Status: appsv1.DeploymentStatus{
			ObservedGeneration: 3,
			Replicas:           1,
			ReadyReplicas:      0,
			AvailableReplicas:  0,
		},
	}
	replicaSet := appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "opensaola-controller-manager-8675309",
			Namespace: "middleware-operator",
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "apps/v1",
				Kind:       "Deployment",
				Name:       deployment.Name,
			}},
		},
	}
	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "opensaola-install-crds",
			Namespace: "middleware-operator",
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "apps/v1",
				Kind:       "ReplicaSet",
				Name:       replicaSet.Name,
			}},
		},
		Spec: corev1.PodSpec{
			NodeName:           "node-1",
			ServiceAccountName: "opensaola-sa",
		},
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{{
				Name:  "kubectl",
				Image: "10.10.101.172:443/middleware/kubectl:v1.30.14",
				State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{
					Reason:  "ImagePullBackOff",
					Message: "Back-off pulling image \"10.10.101.172:443/middleware/kubectl:v1.30.14\": ErrImagePull: failed to pull image: x509: certificate signed by unknown authority",
				}},
			}},
		},
	}
	event := corev1.Event{
		ObjectMeta: metav1.ObjectMeta{Name: "pull-failed", Namespace: "middleware-operator"},
		Type:       corev1.EventTypeWarning,
		Reason:     "FailedPull",
		Message:    "failed to pull image: x509: certificate signed by unknown authority",
		Count:      3,
		InvolvedObject: corev1.ObjectReference{
			APIVersion: "v1",
			Kind:       "Pod",
			Name:       pod.Name,
			Namespace:  pod.Namespace,
		},
		LastTimestamp: metav1.Now(),
	}

	got := deriveRuntimeDiagnostic("opensaola", deployment, []appsv1.ReplicaSet{replicaSet}, []corev1.Pod{pod}, nil, []corev1.Event{event})
	for _, want := range []string{
		"phase=workload-readiness",
		"resource=middleware.cn/v1/MiddlewareOperator middleware-operator/opensaola",
		"failedObject=v1/Pod middleware-operator/opensaola-install-crds",
		"ownerRef=apps/v1/Deployment middleware-operator/opensaola-controller-manager",
		"fieldPath=status.containerStatuses[name=kubectl].state.waiting",
		"actual=ImagePullBackOff",
		"generation=4",
		"observedGeneration=3",
		"staleStatus=true",
		"causeCategory=RegistryTLS",
		"nodeName=node-1",
		"serviceAccountName=opensaola-sa",
		"eventType=Warning",
		"eventReason=FailedPull",
		"eventCount=3",
		"10.10.101.172:443/middleware/kubectl:v1.30.14",
		"x509: certificate signed by unknown authority",
		"next=kubectl describe pod opensaola-install-crds -n middleware-operator",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected runtime diagnostic to contain %q, got %q", want, got)
		}
	}
	if strings.Contains(got, "status.containerStatuses[0]") {
		t.Fatalf("expected fieldPath to use container name, got %q", got)
	}
}

func TestDeriveRuntimeDiagnostic_PVCPendingIncludesPVCObject(t *testing.T) {
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "milvus", Namespace: "middleware", Generation: 8},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "milvus"}},
		},
		Status: appsv1.DeploymentStatus{ObservedGeneration: 8, Replicas: 1},
	}
	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "milvus-0",
			Namespace: "middleware",
			Labels:    map[string]string{"app": "milvus"},
		},
		Spec: corev1.PodSpec{
			Volumes: []corev1.Volume{{
				Name: "data",
				VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: "milvus-data",
				}},
			}},
		},
	}
	storageClass := "fast"
	pvc := corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "milvus-data", Namespace: "middleware"},
		Spec: corev1.PersistentVolumeClaimSpec{
			StorageClassName: &storageClass,
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("10Gi")},
			},
		},
		Status: corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimPending},
	}

	got := deriveRuntimeDiagnostic("milvus-operator", deployment, nil, []corev1.Pod{pod}, []corev1.PersistentVolumeClaim{pvc}, nil)
	for _, want := range []string{
		"phase=workload-readiness",
		"failedObject=v1/PersistentVolumeClaim middleware/milvus-data",
		"fieldPath=status.phase",
		"expected=Bound",
		"actual=Pending",
		"causeCategory=PVCPending",
		"storageClassName=fast",
		"accessModes=ReadWriteOnce",
		"requestedStorage=10Gi",
		"next=kubectl describe pvc milvus-data -n middleware",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected PVC diagnostic to contain %q, got %q", want, got)
		}
	}
}

func TestDeriveRuntimeDiagnostic_StaleReadyDeploymentIsNotReady(t *testing.T) {
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "opensaola-controller-manager", Namespace: "middleware-operator", Generation: 4},
		Status: appsv1.DeploymentStatus{
			ObservedGeneration: 3,
			Replicas:           1,
			UpdatedReplicas:    1,
			ReadyReplicas:      1,
			AvailableReplicas:  1,
		},
	}

	got := deriveRuntimeDiagnostic("opensaola", deployment, nil, nil, nil, nil)
	for _, want := range []string{
		"phase=runtime-reconcile",
		"failedObject=apps/v1/Deployment middleware-operator/opensaola-controller-manager",
		"generation=4",
		"observedGeneration=3",
		"staleStatus=true",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected stale deployment diagnostic to contain %q, got %q", want, got)
		}
	}
	if got == "Ready" || strings.Contains(got, "Runtime=Ready") {
		t.Fatalf("expected stale deployment not to be ready, got %q", got)
	}
}

func TestDeriveRuntimeDiagnostic_UpdatedReplicasRequiredForReady(t *testing.T) {
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "opensaola-controller-manager", Namespace: "middleware-operator", Generation: 4},
		Status: appsv1.DeploymentStatus{
			ObservedGeneration: 4,
			Replicas:           2,
			UpdatedReplicas:    1,
			ReadyReplicas:      2,
			AvailableReplicas:  2,
		},
	}

	if got := deriveRuntimeDiagnostic("opensaola", deployment, nil, nil, nil, nil); got == "Ready" {
		t.Fatal("expected deployment with outdated replicas not to be Ready")
	}
}

func TestDeriveRuntimeDiagnostic_ReadyDeploymentIgnoresOldWaitingReplicaSetPod(t *testing.T) {
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "opensaola-controller-manager", Namespace: "middleware-operator", Generation: 4},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "opensaola"}},
		},
		Status: appsv1.DeploymentStatus{
			ObservedGeneration:  4,
			Replicas:            1,
			UpdatedReplicas:     1,
			ReadyReplicas:       1,
			AvailableReplicas:   1,
			UnavailableReplicas: 0,
		},
	}
	oldReplicaSet := appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "opensaola-controller-manager-old",
			Namespace: "middleware-operator",
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "apps/v1",
				Kind:       "Deployment",
				Name:       deployment.Name,
			}},
		},
	}
	oldPod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "opensaola-controller-manager-old-pod",
			Namespace: "middleware-operator",
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "apps/v1",
				Kind:       "ReplicaSet",
				Name:       oldReplicaSet.Name,
			}},
		},
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{{
				Name:  "manager",
				Image: "opensaola:old",
				State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{
					Reason:  "ImagePullBackOff",
					Message: "old rollout pod failed to pull",
				}},
			}},
		},
	}

	got := deriveRuntimeDiagnostic("opensaola", deployment, []appsv1.ReplicaSet{oldReplicaSet}, []corev1.Pod{oldPod}, nil, nil)
	if got != "Ready" {
		t.Fatalf("expected ready deployment to win over stale waiting pod, got %q", got)
	}
}

func TestDeriveRuntimeDiagnostic_PodConditionIncludesNodeAndServiceAccount(t *testing.T) {
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "opensaola-controller-manager", Namespace: "middleware-operator", Generation: 4},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "opensaola"}},
		},
		Status: appsv1.DeploymentStatus{ObservedGeneration: 4, Replicas: 1},
	}
	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "opensaola-controller-manager-0",
			Namespace: "middleware-operator",
			Labels:    map[string]string{"app": "opensaola"},
		},
		Spec: corev1.PodSpec{
			NodeName:           "node-1",
			ServiceAccountName: "opensaola-sa",
		},
		Status: corev1.PodStatus{
			Conditions: []corev1.PodCondition{{
				Type:    corev1.PodScheduled,
				Status:  corev1.ConditionFalse,
				Reason:  "Unschedulable",
				Message: "0/3 nodes are available: pod has unbound immediate PersistentVolumeClaims",
			}},
		},
	}

	got := deriveRuntimeDiagnostic("opensaola", deployment, nil, []corev1.Pod{pod}, nil, nil)
	for _, want := range []string{
		"phase=workload-readiness",
		"failedObject=v1/Pod middleware-operator/opensaola-controller-manager-0",
		"fieldPath=status.conditions[type=PodScheduled]",
		"actual=False",
		"causeCategory=SchedulingUnschedulable",
		"nodeName=node-1",
		"serviceAccountName=opensaola-sa",
		"next=kubectl describe pod opensaola-controller-manager-0 -n middleware-operator",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected pod condition diagnostic to contain %q, got %q", want, got)
		}
	}
}

func TestDeriveRuntimeDiagnostic_DeploymentNotReadyIncludesConditionAndCounters(t *testing.T) {
	replicas := int32(2)
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "opensaola-controller-manager", Namespace: "middleware-operator", Generation: 4},
		Spec:       appsv1.DeploymentSpec{Replicas: &replicas},
		Status: appsv1.DeploymentStatus{
			ObservedGeneration:  4,
			Replicas:            2,
			UpdatedReplicas:     2,
			ReadyReplicas:       1,
			AvailableReplicas:   1,
			UnavailableReplicas: 1,
			Conditions: []appsv1.DeploymentCondition{{
				Type:    appsv1.DeploymentProgressing,
				Status:  corev1.ConditionFalse,
				Reason:  "ProgressDeadlineExceeded",
				Message: "Deployment exceeded its progress deadline",
			}},
		},
	}

	got := deriveRuntimeDiagnostic("opensaola", deployment, nil, nil, nil, nil)
	for _, want := range []string{
		"phase=workload-readiness",
		"failedObject=apps/v1/Deployment middleware-operator/opensaola-controller-manager",
		"fieldPath=status",
		"actual=ProgressDeadlineExceeded",
		"causeCategory=RolloutStalled",
		"readyReplicas=1",
		"availableReplicas=1",
		"unavailableReplicas=1",
		"deploymentCondition=Progressing",
		"next=kubectl describe deployment opensaola-controller-manager -n middleware-operator",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected deployment diagnostic to contain %q, got %q", want, got)
		}
	}
	if got == "Progressing" {
		t.Fatal("expected deployment diagnostic, got bare Progressing")
	}
}
