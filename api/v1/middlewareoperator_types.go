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

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// MiddlewareOperatorSpec defines the desired state of MiddlewareOperator.
type MiddlewareOperatorSpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	Baseline        string                `json:"baseline,omitempty"`
	Globe           *runtime.RawExtension `json:"globe,omitempty"`
	PreActions      []PreAction           `json:"preActions,omitempty"`
	PermissionScope PermissionScope       `json:"permissionScope,omitempty"`
	Permissions     []Permission          `json:"permissions,omitempty"`
	Deployment      *runtime.RawExtension `json:"deployment,omitempty"`
	Configurations  []Configuration       `json:"configurations,omitempty"`
}

// MiddlewareOperatorStatus defines the observed state of MiddlewareOperator.
type MiddlewareOperatorStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// The generation observed by the deployment controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty" protobuf:"varint,1,opt,name=observedGeneration"`

	// Represents the latest available observations of a deployment's current state.
	// +patchMergeKey=type
	// +patchStrategy=merge
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// The status of the middlewareoperator.
	// +optional
	State State `json:"state,omitempty"`

	OperatorStatus    map[string]appsv1.DeploymentStatus `json:"operators,omitempty"`
	OperatorAvailable string                             `json:"operatorAvailable,omitempty"`
	Reason            string                             `json:"reason,omitempty"`

	// Summary fields for kubectl get display (forward compatible)
	// +optional
	Ready bool `json:"ready,omitempty"`
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
