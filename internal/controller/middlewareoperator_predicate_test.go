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
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

// TestGenerationChangedPredicate_FiltersStatusOnlyUpdate verifies that GenerationChangedPredicate
// correctly filters status-only updates (generation unchanged)
func TestGenerationChangedPredicate_FiltersStatusOnlyUpdate(t *testing.T) {
	p := predicate.GenerationChangedPredicate{}

	// status-only update: generation unchanged
	oldDeploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "d1", Namespace: "ns1", Generation: 1},
	}
	newDeploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "d1", Namespace: "ns1", Generation: 1},
	}

	e := event.UpdateEvent{ObjectOld: oldDeploy, ObjectNew: newDeploy}
	if p.Update(e) {
		t.Error("expected GenerationChangedPredicate to filter status-only update (same generation)")
	}

	// spec change: generation changed
	newDeploy2 := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "d1", Namespace: "ns1", Generation: 2},
	}
	e2 := event.UpdateEvent{ObjectOld: oldDeploy, ObjectNew: newDeploy2}
	if !p.Update(e2) {
		t.Error("expected GenerationChangedPredicate to allow spec change (generation changed)")
	}
}

// Gate removed: MiddlewareOperator/Deployment event filtering is always enabled in the controller
