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
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// MiddlewareOperatorSpec defines the desired state of MiddlewareOperator.
type MiddlewareOperatorSpec struct {
	// Baseline is the name of the MiddlewareOperatorBaseline to use as the default template.
	Baseline string `json:"baseline,omitempty"`
	// Globe holds global configuration for the operator as raw JSON.
	// Typically includes image repository overrides and other cluster-wide settings.
	// These values are available to all middleware instances managed by this operator.
	// Example: {"repository": "registry.example.com/middleware"}
	Globe *runtime.RawExtension `json:"globe,omitempty"`
	// PreActions is the list of pre-actions to execute before the main operator reconciliation.
	PreActions []PreAction `json:"preActions,omitempty"`
	// PermissionScope defines whether RBAC resources are created at Cluster or Namespace scope.
	PermissionScope PermissionScope `json:"permissionScope,omitempty"`
	// Permissions is the list of RBAC permission definitions for the operator's service accounts.
	Permissions []Permission `json:"permissions,omitempty"`
	// Deployment holds the operator Deployment specification as raw JSON.
	// Values are merged with defaults from the MiddlewareOperatorBaseline during reconciliation.
	// Common fields include image, replicas, resource limits, and environment variables.
	// Example: {"image": "mysql-operator:0.6.3", "replicas": 1, "resources": {"limits": {"cpu": "500m"}}}
	Deployment *runtime.RawExtension `json:"deployment,omitempty"`
	// Configurations is the list of additional configuration resources to create alongside the operator.
	Configurations []Configuration `json:"configurations,omitempty"`
}

// MiddlewareOperatorStatus defines the observed state of MiddlewareOperator.
type MiddlewareOperatorStatus struct {
	// The generation observed by the deployment controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty" protobuf:"varint,1,opt,name=observedGeneration"`

	// Conditions represent the latest available observations of the operator's current state.
	// +patchMergeKey=type
	// +patchStrategy=merge
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// State is the high-level state of the operator. Valid values: Available, Unavailable, Updating.
	// +optional
	State State `json:"state,omitempty"`

	// OperatorStatus holds the deployment status for each operator deployment managed by this resource.
	OperatorStatus map[string]appsv1.DeploymentStatus `json:"operators,omitempty"`
	// OperatorAvailable is a human-readable summary of operator availability (e.g., "1/1").
	OperatorAvailable string `json:"operatorAvailable,omitempty"`
	// Reason provides a human-readable explanation of the current state.
	Reason string `json:"reason,omitempty"`

	// Ready indicates whether all operator deployments are available and healthy.
	// +optional
	Ready bool `json:"ready,omitempty"`
	// Runtime is an optional summary field for kubectl display (forward compatible).
	// +optional
	Runtime string `json:"runtime,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=mo
// +kubebuilder:printcolumn:name="State",type=string,JSONPath=`.status.state`
// +kubebuilder:printcolumn:name="Ready",type=boolean,JSONPath=`.status.ready`
// +kubebuilder:printcolumn:name="Avail",type=string,JSONPath=`.status.operatorAvailable`
// +kubebuilder:printcolumn:name="Baseline",type=string,JSONPath=`.spec.baseline`
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.metadata.labels['middleware\.cn/component']`
// +kubebuilder:printcolumn:name="Package",type=string,JSONPath=`.metadata.labels['middleware\.cn/packagename']`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:printcolumn:name="Runtime",type=string,JSONPath=`.status.runtime`,priority=1
// +kubebuilder:printcolumn:name="Reason",type=string,JSONPath=`.status.reason`,priority=1
// +kubebuilder:printcolumn:name="ObsGen",type=integer,JSONPath=`.status.observedGeneration`,priority=1

// MiddlewareOperator is the Schema for the middlewareoperators API.
// It represents a deployed middleware operator (e.g., MySQL Operator) that manages Middleware instances.
type MiddlewareOperator struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MiddlewareOperatorSpec   `json:"spec,omitempty"`
	Status MiddlewareOperatorStatus `json:"status,omitempty"`
}

func (m *MiddlewareOperator) GetConfigurations() []Configuration {
	return m.Spec.Configurations
}

func (m *MiddlewareOperator) GetUnified() *runtime.RawExtension {
	return m.Spec.Globe
}

func (m *MiddlewareOperator) GetPreActions() []PreAction {
	return m.Spec.PreActions
}

func (m *MiddlewareOperator) GetMiddlewareName() string {
	return ""
}

// +kubebuilder:object:root=true

// MiddlewareOperatorList contains a list of MiddlewareOperator.
type MiddlewareOperatorList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MiddlewareOperator `json:"items"`
}

func init() {
	SchemeBuilder.Register(&MiddlewareOperator{}, &MiddlewareOperatorList{})
}
