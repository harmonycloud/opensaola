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

package v1

import (
	"strings"
	"testing"
)

// TestStateConstants_NonEmpty verifies all State constants are non-empty strings.
// 验证所有 State 常量都是非空字符串。
func TestStateConstants_NonEmpty(t *testing.T) {
	t.Parallel()
	states := map[string]State{
		"StateAvailable":   StateAvailable,
		"StateUnavailable": StateUnavailable,
		"StateUpdating":    StateUpdating,
	}
	for name, s := range states {
		if s == "" {
			t.Errorf("%s should not be empty", name)
		}
	}
}

// TestStateConstants_Distinct verifies all State constants have distinct values.
// 验证所有 State 常量的值互不相同。
func TestStateConstants_Distinct(t *testing.T) {
	t.Parallel()
	states := []State{StateAvailable, StateUnavailable, StateUpdating}
	seen := make(map[State]bool)
	for _, s := range states {
		if seen[s] {
			t.Errorf("duplicate State value: %s", s)
		}
		seen[s] = true
	}
}

// TestPhaseConstants_NonEmpty verifies all non-unknown Phase constants are non-empty strings.
// 验证所有非 Unknown 的 Phase 常量都是非空字符串。
func TestPhaseConstants_NonEmpty(t *testing.T) {
	t.Parallel()
	phases := map[string]Phase{
		"PhaseChecking":                PhaseChecking,
		"PhaseChecked":                 PhaseChecked,
		"PhaseCreating":                PhaseCreating,
		"PhaseUpdating":                PhaseUpdating,
		"PhaseRunning":                 PhaseRunning,
		"PhaseFailed":                  PhaseFailed,
		"PhaseUpdatingCustomResources": PhaseUpdatingCustomResources,
		"PhaseBuildingRBAC":            PhaseBuildingRBAC,
		"PhaseBuildingDeployment":      PhaseBuildingDeployment,
		"PhaseFinished":                PhaseFinished,
		"PhaseMappingFields":           PhaseMappingFields,
		"PhaseExecuting":               PhaseExecuting,
	}
	for name, p := range phases {
		if p == "" {
			t.Errorf("%s should not be empty", name)
		}
	}
}

// TestPhaseUnknown_IsEmpty verifies PhaseUnknown is the zero value (empty string).
// 验证 PhaseUnknown 是零值（空字符串）。
func TestPhaseUnknown_IsEmpty(t *testing.T) {
	t.Parallel()
	if PhaseUnknown != "" {
		t.Errorf("PhaseUnknown should be empty, got %q", PhaseUnknown)
	}
}

// TestPhaseConstants_Distinct verifies all Phase constants have distinct values.
// 验证所有 Phase 常量的值互不相同。
func TestPhaseConstants_Distinct(t *testing.T) {
	t.Parallel()
	phases := []Phase{
		PhaseUnknown, PhaseChecking, PhaseChecked, PhaseCreating,
		PhaseUpdating, PhaseRunning, PhaseFailed, PhaseUpdatingCustomResources,
		PhaseBuildingRBAC, PhaseBuildingDeployment, PhaseFinished,
		PhaseMappingFields, PhaseExecuting,
	}
	seen := make(map[Phase]bool)
	for _, p := range phases {
		if seen[p] {
			t.Errorf("duplicate Phase value: %q", p)
		}
		seen[p] = true
	}
}

// TestLabelConstants_ContainDomain verifies all label constants contain the expected domain prefix.
// 验证所有标签常量包含预期的域名前缀。
func TestLabelConstants_ContainDomain(t *testing.T) {
	t.Parallel()
	labels := map[string]string{
		"LabelPackageVersion": LabelPackageVersion,
		"LabelComponent":      LabelComponent,
		"LabelProject":        LabelProject,
		"LabelPackageName":    LabelPackageName,
		"LabelApp":            LabelApp,
		"LabelConfigurations": LabelConfigurations,
		"LabelBaseline":       LabelBaseline,
		"LabelUpdate":         LabelUpdate,
		"LabelEnabled":        LabelEnabled,
		"LabelInstall":        LabelInstall,
		"LabelUnInstall":      LabelUnInstall,
		"LabelSource":         LabelSource,
		"LabelSourceName":     LabelSourceName,
		"LabelNoOperator":     LabelNoOperator,
	}
	for name, l := range labels {
		if !strings.Contains(l, "middleware.cn/") {
			t.Errorf("%s = %q should contain 'middleware.cn/' domain prefix", name, l)
		}
	}
}

// TestAnnotationConstants_ContainDomain verifies all annotation constants contain the expected domain prefix.
// 验证所有注解常量包含预期的域名前缀。
func TestAnnotationConstants_ContainDomain(t *testing.T) {
	t.Parallel()
	annotations := map[string]string{
		"AnnotationInstallDigest":     AnnotationInstallDigest,
		"AnnotationInstallError":      AnnotationInstallError,
		"AnnotationDisasterSyncer":    AnnotationDisasterSyncer,
		"AnnotationDataSyncer":        AnnotationDataSyncer,
		"AnnotationOppositeClusterId": AnnotationOppositeClusterId,
	}
	for name, a := range annotations {
		if !strings.Contains(a, "middleware.cn/") {
			t.Errorf("%s = %q should contain 'middleware.cn/' domain prefix", name, a)
		}
	}
}

// TestFinalizerConstants_NonEmpty verifies all finalizer constants are non-empty.
// 验证所有 finalizer 常量都是非空的。
func TestFinalizerConstants_NonEmpty(t *testing.T) {
	t.Parallel()
	finalizers := map[string]string{
		"FinalizerMiddleware":         FinalizerMiddleware,
		"FinalizerMiddlewareOperator": FinalizerMiddlewareOperator,
	}
	for name, f := range finalizers {
		if f == "" {
			t.Errorf("%s should not be empty", name)
		}
	}
}

// TestFinalizerConstants_Distinct verifies all finalizer constants have distinct values.
// 验证所有 finalizer 常量的值互不相同。
func TestFinalizerConstants_Distinct(t *testing.T) {
	t.Parallel()
	if FinalizerMiddleware == FinalizerMiddlewareOperator {
		t.Error("FinalizerMiddleware and FinalizerMiddlewareOperator should be distinct")
	}
}

// TestFieldOwner_IsOpenSaola verifies FieldOwner is set to "opensaola".
// 验证 FieldOwner 设置为 "opensaola"。
func TestFieldOwner_IsOpenSaola(t *testing.T) {
	t.Parallel()
	if FieldOwner != "opensaola" {
		t.Errorf("FieldOwner should be 'opensaola', got %q", FieldOwner)
	}
}

// TestSecretConstant verifies the Secret constant value.
// 验证 Secret 常量的值。
func TestSecretConstant(t *testing.T) {
	t.Parallel()
	if Secret != "secret" {
		t.Errorf("Secret should be 'secret', got %q", Secret)
	}
}

// TestConditionTypeConstants_Distinct verifies all CondType constants are distinct non-empty strings.
// 验证所有 CondType 常量都是互不相同的非空字符串。
func TestConditionTypeConstants_Distinct(t *testing.T) {
	t.Parallel()
	types := map[string]string{
		"CondTypeChecked":                   CondTypeChecked,
		"CondTypeBuildPreResource":          CondTypeBuildPreResource,
		"CondTypeBuildExtraResource":        CondTypeBuildExtraResource,
		"CondTypeApplyRBAC":                 CondTypeApplyRBAC,
		"CondTypeApplyOperator":             CondTypeApplyOperator,
		"CondTypeApplyCluster":              CondTypeApplyCluster,
		"CondTypeMapCueFields":              CondTypeMapCueFields,
		"CondTypeExecuteAction":             CondTypeExecuteAction,
		"CondTypeExecuteCue":                CondTypeExecuteCue,
		"CondTypeExecuteCmd":                CondTypeExecuteCmd,
		"CondTypeExecuteHttp":               CondTypeExecuteHttp,
		"CondTypeRunning":                   CondTypeRunning,
		"CondTypeTemplateParseWithBaseline": CondTypeTemplateParseWithBaseline,
		"CondTypeUpdating":                  CondTypeUpdating,
	}
	seen := make(map[string]bool)
	for name, ct := range types {
		if ct == "" {
			t.Errorf("%s should not be empty", name)
		}
		if seen[ct] {
			t.Errorf("duplicate condition type value %q from %s", ct, name)
		}
		seen[ct] = true
	}
}

// TestConditionReasonConstants_NonEmpty verifies all CondReason constants are non-empty.
// 验证所有 CondReason 常量都是非空的。
func TestConditionReasonConstants_NonEmpty(t *testing.T) {
	t.Parallel()
	reasons := map[string]string{
		"CondReasonUnknown":                          CondReasonUnknown,
		"CondReasonIniting":                          CondReasonIniting,
		"CondReasonCheckedFailed":                    CondReasonCheckedFailed,
		"CondReasonCheckedSuccess":                   CondReasonCheckedSuccess,
		"CondReasonBuildExtraResourceSuccess":        CondReasonBuildExtraResourceSuccess,
		"CondReasonBuildExtraResourceFailed":         CondReasonBuildExtraResourceFailed,
		"CondReasonApplyRBACSuccess":                 CondReasonApplyRBACSuccess,
		"CondReasonApplyRBACFailed":                  CondReasonApplyRBACFailed,
		"CondReasonApplyOperatorSuccess":             CondReasonApplyOperatorSuccess,
		"CondReasonApplyOperatorFailed":              CondReasonApplyOperatorFailed,
		"CondReasonApplyClusterSuccess":              CondReasonApplyClusterSuccess,
		"CondReasonApplyClusterFailed":               CondReasonApplyClusterFailed,
		"CondReasonMapCueFieldsSuccess":              CondReasonMapCueFieldsSuccess,
		"CondReasonMapCueFieldsFailed":               CondReasonMapCueFieldsFailed,
		"CondReasonExecuteActionSuccess":             CondReasonExecuteActionSuccess,
		"CondReasonExecuteActionFailed":              CondReasonExecuteActionFailed,
		"CondReasonExecuteCueSuccess":                CondReasonExecuteCueSuccess,
		"CondReasonExecuteCueFailed":                 CondReasonExecuteCueFailed,
		"CondReasonExecuteCmdSuccess":                CondReasonExecuteCmdSuccess,
		"CondReasonExecuteCmdFailed":                 CondReasonExecuteCmdFailed,
		"CondReasonRunningSuccess":                   CondReasonRunningSuccess,
		"CondReasonRunningFailed":                    CondReasonRunningFailed,
		"CondReasonUpdatingSuccess":                  CondReasonUpdatingSuccess,
		"CondReasonUpdatingFailed":                   CondReasonUpdatingFailed,
		"CondReasonTemplateParseWithBaselineSuccess": CondReasonTemplateParseWithBaselineSuccess,
		"CondReasonTemplateParseWithBaselineFailed":  CondReasonTemplateParseWithBaselineFailed,
	}
	for name, r := range reasons {
		if r == "" {
			t.Errorf("%s should not be empty", name)
		}
	}
}

// TestPermissionScopeConstants_Distinct verifies all PermissionScope constants are distinct non-empty strings.
// 验证所有 PermissionScope 常量都是互不相同的非空字符串。
func TestPermissionScopeConstants_Distinct(t *testing.T) {
	t.Parallel()
	scopes := []PermissionScope{
		PermissionScopeUnknown,
		PermissionScopeCluster,
		PermissionScopeNamespace,
	}
	seen := make(map[PermissionScope]bool)
	for _, s := range scopes {
		if s == "" {
			t.Errorf("PermissionScope should not be empty")
		}
		if seen[s] {
			t.Errorf("duplicate PermissionScope value: %s", s)
		}
		seen[s] = true
	}
}

// TestConfigurationType_CfgTypeList verifies CfgTypeList contains all expected configuration types.
// 验证 CfgTypeList 包含所有预期的配置类型。
func TestConfigurationType_CfgTypeList(t *testing.T) {
	t.Parallel()
	expected := []ConfigurationType{
		CfgTypeUnknown,
		CfgTypeConfigmap,
		CfgTypeServiceAccount,
		CfgTypeRole,
		CfgTypeRoleBinding,
		CfgTypeClusterRole,
		CfgTypeClusterRoleBinding,
		CfgTypeCustomResource,
		CfgTypeCustomResourceBaseline,
	}
	if len(CfgTypeList) != len(expected) {
		t.Fatalf("CfgTypeList has %d entries, expected %d", len(CfgTypeList), len(expected))
	}
	for i, ct := range expected {
		if CfgTypeList[i] != ct {
			t.Errorf("CfgTypeList[%d] = %q, expected %q", i, CfgTypeList[i], ct)
		}
	}
}

// TestConfigurationType_Distinct verifies all non-unknown ConfigurationType constants are distinct.
// 验证所有非 Unknown 的 ConfigurationType 常量值互不相同。
func TestConfigurationType_Distinct(t *testing.T) {
	t.Parallel()
	types := []ConfigurationType{
		CfgTypeConfigmap,
		CfgTypeServiceAccount,
		CfgTypeRole,
		CfgTypeRoleBinding,
		CfgTypeClusterRole,
		CfgTypeClusterRoleBinding,
		CfgTypeCustomResource,
		CfgTypeCustomResourceBaseline,
	}
	seen := make(map[ConfigurationType]bool)
	for _, ct := range types {
		if ct == "" {
			t.Errorf("non-unknown ConfigurationType should not be empty")
		}
		if seen[ct] {
			t.Errorf("duplicate ConfigurationType value: %s", ct)
		}
		seen[ct] = true
	}
}

// TestMiddlewareCustomResourceConstants verifies middleware custom resource type constants.
// 验证中间件自定义资源类型常量的值。
func TestMiddlewareCustomResourceConstants(t *testing.T) {
	t.Parallel()
	if MiddlewareCustomResourceCluster == "" {
		t.Error("MiddlewareCustomResourceCluster should not be empty")
	}
	if MiddlewareCustomResourceResources == "" {
		t.Error("MiddlewareCustomResourceResources should not be empty")
	}
	if MiddlewareCustomResourceCluster == MiddlewareCustomResourceResources {
		t.Error("MiddlewareCustomResourceCluster and MiddlewareCustomResourceResources should be distinct")
	}
}

// TestBaselineTypeConstants_Distinct verifies all BaselineType constants are distinct non-empty strings.
// 验证所有 BaselineType 常量都是互不相同的非空字符串。
func TestBaselineTypeConstants_Distinct(t *testing.T) {
	t.Parallel()
	if WorkflowPreAction == "" {
		t.Error("WorkflowPreAction should not be empty")
	}
	if WorkflowNormalAction == "" {
		t.Error("WorkflowNormalAction should not be empty")
	}
	if WorkflowPreAction == WorkflowNormalAction {
		t.Error("WorkflowPreAction and WorkflowNormalAction should be distinct")
	}
}

// TestNecessaryKeywords_ContainsImage verifies NecessaryKeywords contains the "image" keyword.
// 验证 NecessaryKeywords 包含 "image" 关键字。
func TestNecessaryKeywords_ContainsImage(t *testing.T) {
	t.Parallel()
	count, ok := NecessaryKeywords["image"]
	if !ok {
		t.Fatal("NecessaryKeywords should contain 'image' key")
	}
	if count != 1 {
		t.Errorf("NecessaryKeywords['image'] should be 1, got %d", count)
	}
}
