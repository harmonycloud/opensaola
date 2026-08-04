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

	"github.com/harmonycloud/opensaola/api/v1"
	"github.com/harmonycloud/opensaola/internal/service/status"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const reconcilePausedMessage = "reconciliation is suspended by middleware.cn/suspend-reconcile=true"

// markReconcilePaused records a durable pause marker without advancing the
// owning resource's observedGeneration. The marker remains until an explicit
// resume policy has completed the corresponding desired-state apply.
func markReconcilePaused(ctx context.Context, conditions *[]metav1.Condition, generation int64) bool {
	condition := status.GetCondition(ctx, conditions, v1.CondTypeReconcilePaused)
	wasPaused := condition.Status == metav1.ConditionTrue
	if condition.Status == metav1.ConditionTrue &&
		condition.ObservedGeneration == generation &&
		condition.Reason == v1.CondReasonReconcilePaused &&
		condition.Message == reconcilePausedMessage {
		return false
	}

	condition.Status = metav1.ConditionTrue
	condition.ObservedGeneration = generation
	// LastTransitionTime describes a Condition status transition, rather than
	// each paused-generation refresh. Keep it stable while reconciliation
	// remains paused so consumers can identify when the pause began.
	if !wasPaused {
		condition.LastTransitionTime = metav1.Now()
	}
	condition.Reason = v1.CondReasonReconcilePaused
	condition.Message = reconcilePausedMessage
	return true
}

func hasReconcilePaused(conditions []metav1.Condition) bool {
	for _, condition := range conditions {
		if condition.Type == v1.CondTypeReconcilePaused && condition.Status == metav1.ConditionTrue {
			return true
		}
	}
	return false
}

// clearReconcilePaused removes the recovery marker only after a successful
// desired-state apply. Keeping it on errors guarantees that retries resume
// reconciliation even when no new generation is produced.
func clearReconcilePaused(conditions *[]metav1.Condition) bool {
	if conditions == nil {
		return false
	}
	filtered := (*conditions)[:0]
	removed := false
	for _, condition := range *conditions {
		if condition.Type == v1.CondTypeReconcilePaused {
			removed = true
			continue
		}
		filtered = append(filtered, condition)
	}
	if removed {
		*conditions = filtered
	}
	return removed
}

func setReconcileAdoptionCondition(ctx context.Context, conditions *[]metav1.Condition, conditionStatus metav1.ConditionStatus, reason, message string, generation int64) bool {
	condition := status.GetCondition(ctx, conditions, v1.CondTypeReconcileAdoption)
	if condition.Status == conditionStatus &&
		condition.ObservedGeneration == generation &&
		condition.Reason == reason &&
		condition.Message == message {
		return false
	}
	if condition.Status != conditionStatus {
		condition.LastTransitionTime = metav1.Now()
	}
	condition.Status = conditionStatus
	condition.ObservedGeneration = generation
	condition.Reason = reason
	condition.Message = message
	return true
}

func clearReconcileAdoptionCondition(conditions *[]metav1.Condition) bool {
	if conditions == nil {
		return false
	}
	filtered := (*conditions)[:0]
	removed := false
	for _, condition := range *conditions {
		if condition.Type == v1.CondTypeReconcileAdoption {
			removed = true
			continue
		}
		filtered = append(filtered, condition)
	}
	if removed {
		*conditions = filtered
	}
	return removed
}
