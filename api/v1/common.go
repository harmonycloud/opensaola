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
	// ServiceAccountName is the name of the service account to bind these RBAC rules to.
	ServiceAccountName string `json:"serviceAccountName,omitempty"`
	// Rules is the list of RBAC policy rules to apply to the service account.
	Rules []rbacv1.PolicyRule `json:"rules,omitempty"`
}

// OperatorBaseline references a MiddlewareOperatorBaseline by name and GVK identifier.
type OperatorBaseline struct {
	// Name is the name of the MiddlewareOperatorBaseline resource.
	Name string `json:"name,omitempty"`
	// GvkName is the GroupVersionKind identifier used to match the operator baseline.
	GvkName string `json:"gvkName,omitempty"`
}

// Configuration defines a named configuration with raw values.
type Configuration struct {
	// Name is the unique name identifying this configuration entry.
	Name string `json:"name,omitempty"`
	// Values holds configuration values as raw JSON, rendered from CUE/Go templates.
	// The structure depends on the Configuration name and target resource type.
	// For ConfigMap targets, this typically contains key-value pairs; for RBAC targets,
	// it contains role/binding specifications.
	// Example (ConfigMap): {"my.cnf": "max_connections=200\ninnodb_buffer_pool_size=1G"}
	// Example (ServiceAccount): {"automountServiceAccountToken": true}
	Values runtime.RawExtension `json:"values,omitempty"`
}

// PermissionScope defines the scope of a permission.
// Valid values: Unknown, Cluster, Namespace.
type PermissionScope string

const (
	// PermissionScopeUnknown indicates the permission scope is not specified.
	PermissionScopeUnknown PermissionScope = "Unknown"
	// PermissionScopeCluster indicates cluster-wide permission scope.
	PermissionScopeCluster PermissionScope = "Cluster"
	// PermissionScopeNamespace indicates namespace-scoped permission scope.
	PermissionScopeNamespace PermissionScope = "Namespace"
)

// State represents the current state of a resource.
// Valid values: Available, Unavailable, Updating.
type State string

const (
	// StateAvailable indicates the resource is healthy and available.
	StateAvailable State = "Available"
	// StateUnavailable indicates the resource is not available.
	StateUnavailable State = "Unavailable"
	// StateUpdating indicates the resource is being updated.
	StateUpdating State = "Updating"
)

const (
	// MiddlewareCustomResourceCluster is the custom resource type for cluster-scoped resources.
	MiddlewareCustomResourceCluster = "cluster"
	// MiddlewareCustomResourceResources is the custom resource type for namespace-scoped resources.
	MiddlewareCustomResourceResources = "resources"
)

// Phase represents the current phase of a resource lifecycle.
type Phase string

const (
	// PhaseUnknown indicates the phase has not been determined.
	PhaseUnknown Phase = ""
	// PhaseChecking indicates the resource is being validated.
	PhaseChecking Phase = "Checking"
	// PhaseChecked indicates validation has completed.
	PhaseChecked Phase = "Checked"
	// PhaseCreating indicates the resource is being created.
	PhaseCreating Phase = "Creating"
	// PhaseUpdating indicates the resource is being updated.
	PhaseUpdating Phase = "Updating"
	// PhaseRunning indicates the resource is running normally.
	PhaseRunning Phase = "Running"
	// PhaseFailed indicates the resource has encountered an error.
	PhaseFailed Phase = "Failed"
	// PhaseUpdatingCustomResources indicates custom resources are being updated.
	PhaseUpdatingCustomResources Phase = "UpdatingCustomResources"
	// PhaseBuildingRBAC indicates RBAC resources are being created.
	PhaseBuildingRBAC Phase = "BuildingRBAC"
	// PhaseBuildingDeployment indicates the deployment is being created.
	PhaseBuildingDeployment Phase = "BuildingDeployment"
	// PhaseFinished indicates the lifecycle operation has completed.
	PhaseFinished Phase = "Finished"
	// PhaseMappingFields indicates CUE field mapping is in progress.
	PhaseMappingFields Phase = "MappingFields"
	// PhaseExecuting indicates an action is being executed.
	PhaseExecuting Phase = "Executing"
)

// ConfigurationType defines the Kubernetes resource type that a configuration targets.
type ConfigurationType string

// CfgTypeList enumerates all valid ConfigurationType values.
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
	// CfgTypeUnknown indicates the configuration type is not specified.
	CfgTypeUnknown ConfigurationType = ""
	// CfgTypeConfigmap targets a ConfigMap resource.
	CfgTypeConfigmap ConfigurationType = "configmap"
	// CfgTypeServiceAccount targets a ServiceAccount resource.
	CfgTypeServiceAccount ConfigurationType = "serviceAccount"
	// CfgTypeRole targets a Role resource.
	CfgTypeRole ConfigurationType = "role"
	// CfgTypeRoleBinding targets a RoleBinding resource.
	CfgTypeRoleBinding ConfigurationType = "roleBinding"
	// CfgTypeClusterRole targets a ClusterRole resource.
	CfgTypeClusterRole ConfigurationType = "clusterRole"
	// CfgTypeClusterRoleBinding targets a ClusterRoleBinding resource.
	CfgTypeClusterRoleBinding ConfigurationType = "clusterRoleBinding"
	// CfgTypeCustomResource targets a custom resource.
	CfgTypeCustomResource ConfigurationType = "customResource"
	// CfgTypeCustomResourceBaseline targets a custom resource baseline.
	CfgTypeCustomResourceBaseline ConfigurationType = "customResourceBaseline"
)

// Condition type constants used in status conditions across all CRD types.
const (
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

// Condition reason constants used in status conditions to provide detail about the condition.
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
	CondReasonTemplateParseWithBaselineFailed         = "TemplateParseWithBaselineFailed"
)

// NecessaryKeywords defines required keywords and their expected occurrence count
// used during spec validation.
var NecessaryKeywords = map[string]int{
	"image": 1,
}

// Globe defines global configuration settings shared across middleware resources.
type Globe struct {
	// Repository is the optional global container image repository override.
	Repository string `json:"repository,omitempty"`
}

// BaselineType indicates whether an action is a pre-action or a normal action.
// Valid values: PreAction, NormalAction.
type BaselineType string

const (
	// WorkflowPreAction indicates the action runs before the main reconciliation workflow.
	WorkflowPreAction BaselineType = "PreAction"
	// WorkflowNormalAction indicates the action runs as part of the normal workflow.
	WorkflowNormalAction BaselineType = "NormalAction"
)

// PreAction defines a pre-action to execute before the main reconciliation workflow.
type PreAction struct {
	// Name is the name of the MiddlewareActionBaseline to execute.
	Name string `json:"name,omitempty"`
	// Fixed indicates whether this pre-action uses fixed parameters that cannot be overridden.
	Fixed bool `json:"fixed,omitempty"`
	// Exposed indicates whether this pre-action's output is exposed to the parent resource.
	Exposed bool `json:"exposed,omitempty"`
	// Parameters holds input parameters for the pre-action as raw JSON.
	// The schema is defined by the corresponding MiddlewareActionBaseline.
	// Example: {"backupPath": "/data/backup", "compress": true}
	Parameters runtime.RawExtension `json:"parameters,omitempty"`
}

// GVK defines the GroupVersionKind of a Kubernetes object.
type GVK struct {
	// Name is the name identifier for this GVK entry.
	Name string `json:"name,omitempty"`
	// Group is the API group of the Kubernetes resource.
	Group string `json:"group,omitempty"`
	// Version is the API version of the Kubernetes resource.
	Version string `json:"version,omitempty"`
	// Kind is the kind of the Kubernetes resource.
	Kind string `json:"kind,omitempty"`
}
