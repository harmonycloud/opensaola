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

package status

import (
	"context"
	"testing"

	v1 "github.com/opensaola/opensaola/api/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestConditionInit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cond := ConditionInit(ctx, "TestType", 5)

	if cond.Type != "TestType" {
		t.Errorf("expected type TestType, got %v", cond.Type)
	}
	if cond.Status != metav1.ConditionUnknown {
		t.Errorf("expected ConditionUnknown, got %v", cond.Status)
	}
	if cond.ObservedGeneration != 5 {
		t.Errorf("expected ObservedGeneration 5, got %v", cond.ObservedGeneration)
	}
	if cond.Reason != v1.CondReasonIniting {
		t.Errorf("expected reason %q, got %q", v1.CondReasonIniting, cond.Reason)
	}
	if cond.Message != "Initializing" {
		t.Errorf("expected message Initializing, got %q", cond.Message)
	}
	if cond.LastTransitionTime.IsZero() {
		t.Error("expected LastTransitionTime to be set")
	}
}

func TestCondition_Success(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	conds := []metav1.Condition{}

	cond := GetCondition(ctx, &conds, v1.CondTypeChecked)
	cond.Success(ctx, 3)

	if cond.Status != metav1.ConditionTrue {
		t.Errorf("expected ConditionTrue, got %v", cond.Status)
	}
	if cond.ObservedGeneration != 3 {
		t.Errorf("expected ObservedGeneration 3, got %v", cond.ObservedGeneration)
	}
	if cond.Message != "Succeeded" {
		t.Errorf("expected message Succeeded, got %q", cond.Message)
	}
	if cond.Reason != v1.CondReasonCheckedSuccess {
		t.Errorf("expected reason %q, got %q", v1.CondReasonCheckedSuccess, cond.Reason)
	}
}

func TestCondition_Success_ApplyRBAC(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	conds := []metav1.Condition{}

	cond := GetCondition(ctx, &conds, v1.CondTypeApplyRBAC)
	cond.Success(ctx, 1)

	if cond.Reason != v1.CondReasonApplyRBACSuccess {
		t.Errorf("expected reason %q, got %q", v1.CondReasonApplyRBACSuccess, cond.Reason)
	}
}

func TestCondition_Failed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	conds := []metav1.Condition{}

	cond := GetCondition(ctx, &conds, v1.CondTypeChecked)
	cond.Failed(ctx, "something went wrong", 2)

	if cond.Status != metav1.ConditionFalse {
		t.Errorf("expected ConditionFalse, got %v", cond.Status)
	}
	if cond.Message != "something went wrong" {
		t.Errorf("expected message 'something went wrong', got %q", cond.Message)
	}
	if cond.Reason != v1.CondReasonCheckedFailed {
		t.Errorf("expected reason %q, got %q", v1.CondReasonCheckedFailed, cond.Reason)
	}
	if cond.ObservedGeneration != 2 {
		t.Errorf("expected ObservedGeneration 2, got %v", cond.ObservedGeneration)
	}
}

func TestCondition_Failed_ExecuteAction(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	conds := []metav1.Condition{}

	cond := GetCondition(ctx, &conds, v1.CondTypeExecuteAction)
	cond.Failed(ctx, "action error", 1)

	if cond.Reason != v1.CondReasonExecuteActionFailed {
		t.Errorf("expected reason %q, got %q", v1.CondReasonExecuteActionFailed, cond.Reason)
	}
}

func TestCondition_Failed_StepPrefix(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	conds := []metav1.Condition{}

	cond := GetCondition(ctx, &conds, "STEP-1")
	cond.Failed(ctx, "step failed", 1)

	if cond.Reason != v1.CondReasonExecuteActionFailed {
		t.Errorf("expected reason %q for STEP- prefix, got %q", v1.CondReasonExecuteActionFailed, cond.Reason)
	}
}

func TestCondition_SuccessWithMsg(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	conds := []metav1.Condition{}

	cond := GetCondition(ctx, &conds, v1.CondTypeExecuteCmd)
	cond.SuccessWithMsg(ctx, "custom output", 4)

	if cond.Status != metav1.ConditionTrue {
		t.Errorf("expected ConditionTrue, got %v", cond.Status)
	}
	if cond.Message != "custom output" {
		t.Errorf("expected message 'custom output', got %q", cond.Message)
	}
	if cond.Reason != v1.CondReasonExecuteCmdSuccess {
		t.Errorf("expected reason %q, got %q", v1.CondReasonExecuteCmdSuccess, cond.Reason)
	}
}

func TestCondition_SuccessWithMsg_StepPrefix(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	conds := []metav1.Condition{}

	cond := GetCondition(ctx, &conds, "STEP-2")
	cond.SuccessWithMsg(ctx, "step done", 1)

	if cond.Reason != v1.CondReasonExecuteActionSuccess {
		t.Errorf("expected reason %q for STEP- prefix, got %q", v1.CondReasonExecuteActionSuccess, cond.Reason)
	}
}

func TestGetCondition_Existing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	conds := []metav1.Condition{
		{
			Type:               v1.CondTypeChecked,
			Status:             metav1.ConditionTrue,
			Reason:             v1.CondReasonCheckedSuccess,
			Message:            "Succeeded",
			LastTransitionTime: metav1.Now(),
		},
	}

	cond := GetCondition(ctx, &conds, v1.CondTypeChecked)
	if cond.Status != metav1.ConditionTrue {
		t.Errorf("expected ConditionTrue for existing condition, got %v", cond.Status)
	}
	if cond.Reason != v1.CondReasonCheckedSuccess {
		t.Errorf("expected reason %q, got %q", v1.CondReasonCheckedSuccess, cond.Reason)
	}
}

func TestGetCondition_NotFound_Initializes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	conds := []metav1.Condition{}

	cond := GetCondition(ctx, &conds, "NewType")

	// GetCondition should initialize the condition when not found
	if cond.Status != metav1.ConditionUnknown {
		t.Errorf("expected ConditionUnknown for new condition, got %v", cond.Status)
	}
	if cond.Type != "NewType" {
		t.Errorf("expected type NewType, got %v", cond.Type)
	}
	if cond.Reason != v1.CondReasonIniting {
		t.Errorf("expected reason %q, got %q", v1.CondReasonIniting, cond.Reason)
	}

	// Should have been added to the conditions slice
	if len(conds) != 1 {
		t.Errorf("expected conditions slice length 1, got %d", len(conds))
	}
}

func TestGetCondition_MultipleCalls_SamePointer(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	conds := []metav1.Condition{}

	cond1 := GetCondition(ctx, &conds, v1.CondTypeChecked)
	cond1.Success(ctx, 1)

	// Getting the same condition again should reflect the update
	cond2 := GetCondition(ctx, &conds, v1.CondTypeChecked)
	if cond2.Status != metav1.ConditionTrue {
		t.Errorf("expected ConditionTrue after Success, got %v", cond2.Status)
	}
}
