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
	"strings"

	middlewarecnv1 "github.com/opensaola/opensaola/api/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// // ConditionInit initializes Conditions
// func ConditionInit(ctx context.Context, c *[]metav1.Condition, og int64, size int) error {
// 	// Initialize middlewareConfiguration.Status.Conditions
// 	if *c == nil {
// 		*c = make([]metav1.Condition, size)
// 		for i := 0; i < size; i++ {
// 			(*c)[i].Status = metav1.ConditionUnknown
// 			(*c)[i].Type = middlewarecnv1.CondTypeUnknown
// 			(*c)[i].LastTransitionTime = metav1.Now()
// 			(*c)[i].ObservedGeneration = og
// 			(*c)[i].Reason = middlewarecnv1.CondReasonIniting
// 			(*c)[i].Message = "Initializing"
// 		}
// 		return nil
// 	}
// 	return errors.New("conditions is not nil")
// }

type Condition struct {
	*metav1.Condition
}

// Failed marks the condition as failed
func (c *Condition) Failed(ctx context.Context, message string, og int64) {
	c.Status = metav1.ConditionFalse
	c.LastTransitionTime = metav1.Now()
	c.ObservedGeneration = og
	c.Message = message

	switch c.Type {
	case middlewarecnv1.CondTypeChecked:
		c.Reason = middlewarecnv1.CondReasonCheckedFailed
	case middlewarecnv1.CondTypeBuildExtraResource:
		c.Reason = middlewarecnv1.CondReasonBuildExtraResourceFailed
	case middlewarecnv1.CondTypeApplyRBAC:
		c.Reason = middlewarecnv1.CondReasonApplyRBACFailed
	case middlewarecnv1.CondTypeApplyOperator:
		c.Reason = middlewarecnv1.CondReasonApplyOperatorFailed
	case middlewarecnv1.CondTypeApplyCluster:
		c.Reason = middlewarecnv1.CondReasonApplyClusterFailed
	case middlewarecnv1.CondTypeMapCueFields:
		c.Reason = middlewarecnv1.CondReasonMapCueFieldsFailed
	case middlewarecnv1.CondTypeExecuteAction:
		c.Reason = middlewarecnv1.CondReasonExecuteActionFailed
	case middlewarecnv1.CondTypeExecuteCue:
		c.Reason = middlewarecnv1.CondReasonExecuteCueFailed
	case middlewarecnv1.CondTypeExecuteCmd:
		c.Reason = middlewarecnv1.CondReasonExecuteCmdFailed
	case middlewarecnv1.CondTypeRunning:
		c.Reason = middlewarecnv1.CondReasonRunningFailed
	case middlewarecnv1.CondTypeUpdating:
		c.Reason = middlewarecnv1.CondReasonUpdatingFailed
	case middlewarecnv1.CondTypeTemplateParseWithBaseline:
		c.Reason = middlewarecnv1.CondReasonTemplateParseWithBaselineFailed
	}

	if strings.HasPrefix(c.Type, "STEP-") {
		c.Reason = middlewarecnv1.CondReasonExecuteActionFailed
	}
}

// Success marks the condition as succeeded
func (c *Condition) Success(ctx context.Context, og int64) {
	c.Status = metav1.ConditionTrue
	c.LastTransitionTime = metav1.Now()
	c.ObservedGeneration = og
	c.Message = "Succeeded"
	switch c.Type {
	case middlewarecnv1.CondTypeChecked:
		c.Reason = middlewarecnv1.CondReasonCheckedSuccess
	case middlewarecnv1.CondTypeBuildExtraResource:
		c.Reason = middlewarecnv1.CondReasonBuildExtraResourceSuccess
	case middlewarecnv1.CondTypeApplyRBAC:
		c.Reason = middlewarecnv1.CondReasonApplyRBACSuccess
	case middlewarecnv1.CondTypeApplyOperator:
		c.Reason = middlewarecnv1.CondReasonApplyOperatorSuccess
	case middlewarecnv1.CondTypeApplyCluster:
		c.Reason = middlewarecnv1.CondReasonApplyClusterSuccess
	case middlewarecnv1.CondTypeMapCueFields:
		c.Reason = middlewarecnv1.CondReasonMapCueFieldsSuccess
	case middlewarecnv1.CondTypeExecuteAction:
		c.Reason = middlewarecnv1.CondReasonExecuteActionSuccess
	case middlewarecnv1.CondTypeExecuteCue:
		c.Reason = middlewarecnv1.CondReasonExecuteCueSuccess
	case middlewarecnv1.CondTypeExecuteCmd:
		c.Reason = middlewarecnv1.CondReasonExecuteCmdSuccess
	case middlewarecnv1.CondTypeRunning:
		c.Reason = middlewarecnv1.CondReasonRunningSuccess
	case middlewarecnv1.CondTypeUpdating:
		c.Reason = middlewarecnv1.CondReasonUpdatingSuccess
	case middlewarecnv1.CondTypeTemplateParseWithBaseline:
		c.Reason = middlewarecnv1.CondReasonTemplateParseWithBaselineSuccess
	}
}

// SuccessWithMsg marks the condition as succeeded with a custom message
func (c *Condition) SuccessWithMsg(ctx context.Context, msg string, og int64) {
	c.Status = metav1.ConditionTrue
	c.LastTransitionTime = metav1.Now()
	c.ObservedGeneration = og
	c.Message = msg
	switch c.Type {
	case middlewarecnv1.CondTypeExecuteCmd:
		c.Reason = middlewarecnv1.CondReasonExecuteCmdSuccess
	case middlewarecnv1.CondTypeExecuteCue:
		c.Reason = middlewarecnv1.CondReasonExecuteCueSuccess
	}

	if strings.HasPrefix(c.Type, "STEP-") {
		c.Reason = middlewarecnv1.CondReasonExecuteActionSuccess
	}
}

// GetCondition retrieves the specified Condition, initializing it if absent
func GetCondition(ctx context.Context, conditions *[]metav1.Condition, conditionType string) *Condition {
	condition := meta.FindStatusCondition(*conditions, conditionType)
	if condition == nil {
		meta.SetStatusCondition(conditions, ConditionInit(ctx, conditionType, 1))
		condition = meta.FindStatusCondition(*conditions, conditionType)
	}
	return &Condition{Condition: condition}
}

func ConditionInit(ctx context.Context, typo string, og int64) metav1.Condition {
	return metav1.Condition{
		Type:               typo,
		Status:             metav1.ConditionUnknown,
		ObservedGeneration: og,
		LastTransitionTime: metav1.Now(),
		Reason:             middlewarecnv1.CondReasonIniting,
		Message:            "Initializing",
	}
}
