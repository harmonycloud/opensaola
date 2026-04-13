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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// MiddlewareBaselineSpec defines the desired state of MiddlewareBaseline.
// It serves as the cluster-scoped default template for creating Middleware instances.
type MiddlewareBaselineSpec struct {
	// OperatorBaseline references the MiddlewareOperatorBaseline that manages the operator for this middleware type.
	OperatorBaseline OperatorBaseline `json:"operatorBaseline,omitempty"`
	// GVK defines the GroupVersionKind of the custom resource that this baseline produces.
	GVK GVK `json:"gvk,omitempty"`
	// Necessary holds required default parameters (e.g., image) for this middleware type.
	Necessary runtime.RawExtension `json:"necessary,omitempty"`
	// PreActions is the list of pre-actions to execute before the main reconciliation workflow.
	PreActions []PreAction `json:"preActions,omitempty"`
	// +kubebuilder:pruning:PreserveUnknownFields
	// Parameters holds optional default parameters that Middleware instances inherit.
	Parameters runtime.RawExtension `json:"parameters,omitempty"`
	// Configurations is the list of default configuration resources included in this baseline.
	Configurations []Configuration `json:"configurations,omitempty"`
}

// MiddlewareBaselineStatus defines the observed state of MiddlewareBaseline.
type MiddlewareBaselineStatus struct {
	// The generation observed by the deployment controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty" protobuf:"varint,1,opt,name=observedGeneration"`

	// Conditions represent the latest available observations of the baseline's current state.
	// +patchMergeKey=type
	// +patchStrategy=merge
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// State is the high-level state of the baseline. Valid values: Available, Unavailable, Updating.
	// +optional
	State State `json:"state,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=mb
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.metadata.labels['middleware\.cn/component']`
// +kubebuilder:printcolumn:name="Package",type=string,JSONPath=`.metadata.labels['middleware\.cn/packagename']`
// +kubebuilder:printcolumn:name="Status",type=string,JSONPath=`.status.state`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// MiddlewareBaseline is the Schema for the middlewarebaselines API.
// It is a cluster-scoped resource that provides default templates for Middleware instances.
type MiddlewareBaseline struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MiddlewareBaselineSpec   `json:"spec,omitempty"`
	Status MiddlewareBaselineStatus `json:"status,omitempty"`
}

func (m *MiddlewareBaseline) GetConfigurations() []Configuration {
	return m.Spec.Configurations
}

func (m *MiddlewareBaseline) GetUnified() *runtime.RawExtension {
	return nil
}

// +kubebuilder:object:root=true

// MiddlewareBaselineList contains a list of MiddlewareBaseline.
type MiddlewareBaselineList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MiddlewareBaseline `json:"items"`
}

func init() {
	SchemeBuilder.Register(&MiddlewareBaseline{}, &MiddlewareBaselineList{})
}
