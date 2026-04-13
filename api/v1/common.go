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
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// Permission defines the RBAC permission for a service account.
type Permission struct {
	ServiceAccountName string              `json:"serviceAccountName,omitempty"`
	Rules              []rbacv1.PolicyRule `json:"rules,omitempty"`
}

type OperatorBaseline struct {
	Name    string `json:"name,omitempty"`
	GvkName string `json:"gvkName,omitempty"`
}

// Configuration defines a named configuration with raw values.
type Configuration struct {
	Name   string               `json:"name,omitempty"`
	Values runtime.RawExtension `json:"values,omitempty"`
}

// PermissionScope defines the scope of a permission (Cluster or Namespace).
type PermissionScope string

const (
	PermissionScopeUnknown   PermissionScope = "Unknown"
	PermissionScopeCluster   PermissionScope = "Cluster"
	PermissionScopeNamespace PermissionScope = "Namespace"
)

// State represents the current state of a resource.
type State string

const (
	StateAvailable   State = "Available"
	StateUnavailable State = "Unavailable"
	StateUpdating    State = "Updating"
)

const (
	MiddlewareCustomResourceCluster   = "cluster"
	MiddlewareCustomResourceResources = "resources"
)

// Phase represents the current phase of a resource lifecycle.
type Phase string

const (
	PhaseUnknown                 Phase = ""
	PhaseChecking                Phase = "Checking"
	PhaseChecked                 Phase = "Checked"
	PhaseCreating                Phase = "Creating"
	PhaseUpdating                Phase = "Updating"
	PhaseRunning                 Phase = "Running"
	PhaseFailed                  Phase = "Failed"
	PhaseUpdatingCustomResources Phase = "UpdatingCustomResources"
	PhaseBuildingRBAC            Phase = "BuildingRBAC"
	PhaseBuildingDeployment      Phase = "BuildingDeployment"
	PhaseFinished                Phase = "Finished"
	PhaseMappingFields           Phase = "MappingFields"
	PhaseExecuting               Phase = "Executing"
)

// ConfigurationType defines the type of a configuration resource.
type ConfigurationType string

var CfgTypeList = []ConfigurationType{
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

const (
	CfgTypeUnknown                ConfigurationType = ""
	CfgTypeConfigmap              ConfigurationType = "configmap"
	CfgTypeServiceAccount         ConfigurationType = "serviceAccount"
	CfgTypeRole                   ConfigurationType = "role"
	CfgTypeRoleBinding            ConfigurationType = "roleBinding"
	CfgTypeClusterRole            ConfigurationType = "clusterRole"
	CfgTypeClusterRoleBinding     ConfigurationType = "clusterRoleBinding"
	CfgTypeCustomResource         ConfigurationType = "customResource"
	CfgTypeCustomResourceBaseline ConfigurationType = "customResourceBaseline"
)

const (
	// CondTypeUnknown                 string = "Unknown"
	// CondTypeAvailable               string = "Available"
	// CondTypeExtraResourceAvailable  string = "ExtraResourceAvailable"
	// CondTypeRBACAvailable           string = "RBACAvailable"
	// CondTypeDeploymentAvailable     string = "DeploymentAvailable"
	// CondTypeCustomResourceAvailable string = "CustomResourceAvailable"
	// CondTypeMapCueFieldsSuccess     string = "MapCueFieldsSuccess"
	// CondTypeExecuteCueSuccess       string = "ExecuteCueSuccess"
	// CondTypeExecuteSuccess          string = "ExecuteSuccess"

	CondTypeChecked                   = "Checked"
	CondTypeBuildPreResource          = "BuildPreResource"
	CondTypeBuildExtraResource        = "BuildExtraResource"
	CondTypeApplyRBAC                 = "ApplyRBAC"
	CondTypeApplyOperator             = "ApplyOperator"
	CondTypeApplyCluster              = "ApplyCluster"
	CondTypeMapCueFields              = "MapCueFields"
	CondTypeExecuteAction             = "ExecuteAction"
	CondTypeExecuteCue                = "ExecuteCue"
	CondTypeExecuteCmd                = "ExecuteCmd"
	CondTypeExecuteHttp               = "ExecuteHttp"
	CondTypeRunning                   = "Running"
	CondTypeTemplateParseWithBaseline = "TemplateParseWithBaseline"

	CondTypeUpdating = "Updating"
)

const (
	CondReasonUnknown                          string = "Unknown"
	CondReasonIniting                          string = "Initing"
	CondReasonCheckedFailed                    string = "CheckedFailed"
	CondReasonCheckedSuccess                   string = "CheckedSuccess"
	CondReasonBuildExtraResourceSuccess        string = "BuildExtraResourceSuccess"
	CondReasonBuildExtraResourceFailed         string = "BuildExtraResourceFailed"
	CondReasonApplyRBACSuccess                 string = "ApplyRBACSuccess"
	CondReasonApplyRBACFailed                  string = "ApplyRBACFailed"
	CondReasonApplyOperatorSuccess             string = "ApplyOperatorSuccess"
	CondReasonApplyOperatorFailed              string = "ApplyOperatorFailed"
	CondReasonApplyClusterSuccess              string = "ApplyClusterSuccess"
	CondReasonApplyClusterFailed               string = "ApplyClusterFailed"
	CondReasonMapCueFieldsSuccess              string = "MapCueFieldsSuccess"
	CondReasonMapCueFieldsFailed               string = "MapCueFieldsFailed"
	CondReasonExecuteActionSuccess             string = "ExecuteActionSuccess"
	CondReasonExecuteActionFailed              string = "ExecuteActionFailed"
	CondReasonExecuteCueSuccess                string = "ExecuteCueSuccess"
	CondReasonExecuteCueFailed                 string = "ExecuteCueFailed"
	CondReasonExecuteCmdSuccess                string = "ExecuteCmdSuccess"
	CondReasonExecuteCmdFailed                 string = "ExecuteCmdFailed"
	CondReasonRunningSuccess                   string = "RunningSuccess"
	CondReasonRunningFailed                    string = "RunningFailed"
	CondReasonUpdatingSuccess                  string = "UpdatingSuccess"
	CondReasonUpdatingFailed                   string = "UpdatingFailed"
	CondReasonTemplateParseWithBaselineSuccess        = "TemplateParseWithBaselineSuccess"
	CondReasonTemplateParseWithBaselineFailed          = "TemplateParseWithBaselineFailed"
)

var NecessaryKeywords = map[string]int{
	"image": 1,
}

type Globe struct {
	Repository string `json:"repository,omitempty"`
}

type BaselineType string

const (
	WorkflowPreAction    BaselineType = "PreAction"
	WorkflowNormalAction BaselineType = "NormalAction"
)

type PreAction struct {
	Name       string               `json:"name,omitempty"`
	Fixed      bool                 `json:"fixed,omitempty"`
	Exposed    bool                 `json:"exposed,omitempty"`
	Parameters runtime.RawExtension `json:"parameters,omitempty"`
}

// GVK defines the GroupVersionKind of a Kubernetes object.
type GVK struct {
	Name    string `json:"name,omitempty"`
	Group   string `json:"group,omitempty"`
	Version string `json:"version,omitempty"`
	Kind    string `json:"kind,omitempty"`
}
