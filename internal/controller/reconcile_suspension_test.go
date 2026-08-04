/*
Copyright 2026 The OpenSaola Authors.

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
	"context"
	"testing"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestReconcilePausedConditionLifecycle(t *testing.T) {
	t.Parallel()

	conditions := []metav1.Condition{{Type: v1.CondTypeChecked, Status: metav1.ConditionTrue}}
	if changed := markReconcilePaused(context.Background(), &conditions, 7); !changed {
		t.Fatal("first pause mark should change conditions")
	}
	pauseStartedAt := pausedCondition(t, conditions).LastTransitionTime
	if !hasReconcilePaused(conditions) {
		t.Fatal("pause marker was not recorded")
	}
	if changed := markReconcilePaused(context.Background(), &conditions, 7); changed {
		t.Fatal("identical pause mark should be idempotent")
	}
	// Refreshing the paused generation must not imply the pause began again.
	if changed := markReconcilePaused(context.Background(), &conditions, 8); !changed {
		t.Fatal("new paused generation should refresh the condition")
	}
	if got := pausedCondition(t, conditions).LastTransitionTime; !got.Equal(&pauseStartedAt) {
		t.Fatalf("pause transition time changed from %v to %v", pauseStartedAt, got)
	}
	if removed := clearReconcilePaused(&conditions); !removed {
		t.Fatal("pause marker was not removed")
	}
	if hasReconcilePaused(conditions) {
		t.Fatal("pause marker remained after clearing")
	}
	if len(conditions) != 1 || conditions[0].Type != v1.CondTypeChecked {
		t.Fatalf("unrelated conditions changed: %#v", conditions)
	}
}

func pausedCondition(t *testing.T, conditions []metav1.Condition) *metav1.Condition {
	t.Helper()
	for i := range conditions {
		if conditions[i].Type == v1.CondTypeReconcilePaused {
			return &conditions[i]
		}
	}
	t.Fatalf("%s condition not found", v1.CondTypeReconcilePaused)
	return nil
}
