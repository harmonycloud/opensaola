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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestMiddlewareOperatorNameFromDeployment(t *testing.T) {
	t.Run("returns error when ownerReferences missing", func(t *testing.T) {
		deployment := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "default"},
		}

		_, err := middlewareOperatorNameFromDeployment(deployment)
		if err == nil {
			t.Fatal("expected error when ownerReferences are missing")
		}
	})

	t.Run("returns owner name when ownerReference valid", func(t *testing.T) {
		deployment := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "demo",
				Namespace: "default",
				OwnerReferences: []metav1.OwnerReference{{
					Kind: "MiddlewareOperator",
					Name: "demo-mo",
				}},
			},
		}

		got, err := middlewareOperatorNameFromDeployment(deployment)
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if got != "demo-mo" {
			t.Fatalf("middlewareOperatorNameFromDeployment() = %q, want %q", got, "demo-mo")
		}
	})

	// Verifies observability: hasMiddlewareOperatorOwner passes this through the predicate,
	// but middlewareOperatorNameFromDeployment must still reject it as anomalous.
	t.Run("returns error when multiple ownerReferences even if one is MiddlewareOperator", func(t *testing.T) {
		deployment := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "demo",
				Namespace: "default",
				OwnerReferences: []metav1.OwnerReference{
					{Kind: "StatefulSet", Name: "foo"},
					{Kind: "MiddlewareOperator", Name: "demo-mo"},
				},
			},
		}
		_, err := middlewareOperatorNameFromDeployment(deployment)
		if err == nil {
			t.Fatal("expected error when multiple ownerReferences present")
		}
	})
}

func TestDeploymentRuntimeStatusChanged(t *testing.T) {
	oldDeployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "demo",
			Namespace: "default",
			OwnerReferences: []metav1.OwnerReference{{
				Kind: "MiddlewareOperator",
				Name: "demo-mo",
			}},
		},
		Status: appsv1.DeploymentStatus{Replicas: 1, ReadyReplicas: 0},
	}

	newDeployment := oldDeployment.DeepCopy()
	newDeployment.Status.ReadyReplicas = 1
	if !deploymentRuntimeStatusChanged(oldDeployment, newDeployment) {
		t.Fatal("expected runtime status change to be detected")
	}

	newDeployment = oldDeployment.DeepCopy()
	if deploymentRuntimeStatusChanged(oldDeployment, newDeployment) {
		t.Fatal("expected identical deployment runtime state to be ignored")
	}
}

func TestDeploymentRuntimeStatusChanged_OnOwnerReferenceChange(t *testing.T) {
	oldDeployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "demo",
			Namespace: "default",
			OwnerReferences: []metav1.OwnerReference{{
				Kind: "MiddlewareOperator",
				Name: "demo-mo",
			}},
		},
		Status: appsv1.DeploymentStatus{Replicas: 1, ReadyReplicas: 0},
	}
	newDeployment := oldDeployment.DeepCopy()
	newDeployment.OwnerReferences[0].Name = "demo-mo-2"

	if !deploymentRuntimeStatusChanged(oldDeployment, newDeployment) {
		t.Fatal("expected owner reference change to be handled")
	}
}

func TestHasMiddlewareOperatorOwner(t *testing.T) {
	t.Run("returns false when no ownerReferences", func(t *testing.T) {
		d := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "demo"}}
		if hasMiddlewareOperatorOwner(d) {
			t.Fatal("expected false for deployment with no ownerReferences")
		}
	})

	t.Run("returns false when owner kind is not MiddlewareOperator", func(t *testing.T) {
		d := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				OwnerReferences: []metav1.OwnerReference{{Kind: "StatefulSet", Name: "foo"}},
			},
		}
		if hasMiddlewareOperatorOwner(d) {
			t.Fatal("expected false for non-MiddlewareOperator owner")
		}
	})

	t.Run("returns true when single owner is MiddlewareOperator", func(t *testing.T) {
		d := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				OwnerReferences: []metav1.OwnerReference{{Kind: "MiddlewareOperator", Name: "demo-mo"}},
			},
		}
		if !hasMiddlewareOperatorOwner(d) {
			t.Fatal("expected true for MiddlewareOperator owner")
		}
	})

	// Multiple owners with one being MiddlewareOperator: predicate lets it through,
	// Reconcile will log the anomaly via middlewareOperatorNameFromDeployment.
	t.Run("returns true when one of multiple owners is MiddlewareOperator", func(t *testing.T) {
		d := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				OwnerReferences: []metav1.OwnerReference{
					{Kind: "StatefulSet", Name: "foo"},
					{Kind: "MiddlewareOperator", Name: "demo-mo"},
				},
			},
		}
		if !hasMiddlewareOperatorOwner(d) {
			t.Fatal("expected true when one owner is MiddlewareOperator")
		}
	})
}
