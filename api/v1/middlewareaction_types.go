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

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// MiddlewareActionSpec defines the desired state of MiddlewareAction.
type MiddlewareActionSpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file
	Baseline       string               `json:"baseline"`
	Necessary      runtime.RawExtension `json:"necessary,omitempty"`
	MiddlewareName string               `json:"middlewareName"`
}

// MiddlewareActionStatus defines the observed state of MiddlewareAction
type MiddlewareActionStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// The generation observed by the deployment controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty" protobuf:"varint,1,opt,name=observedGeneration"`

	// Represents the latest available observations of a deployment's current state.
	// +patchMergeKey=type
	// +patchStrategy=merge
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// The state of the middleware.
	// +optional
	State State `json:"state,omitempty"`

	Reason metav1.StatusReason `json:"reason,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=ma
// +kubebuilder:printcolumn:name="Status",type=string,JSONPath=`.status.state`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:printcolumn:name="Reason",type=string,JSONPath=`.status.reason`

// MiddlewareAction is the Schema for the middlewareactions API.
type MiddlewareAction struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MiddlewareActionSpec   `json:"spec,omitempty"`
	Status MiddlewareActionStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// MiddlewareActionList contains a list of MiddlewareAction.
type MiddlewareActionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MiddlewareAction `json:"items"`
}

func init() {
	SchemeBuilder.Register(&MiddlewareAction{}, &MiddlewareActionList{})
}

func (m *MiddlewareAction) GetConfigurations() []Configuration {
	return nil
}

func (m *MiddlewareAction) GetUnified() *runtime.RawExtension {
	return &m.Spec.Necessary
}

func (m *MiddlewareAction) GetPreActions() []PreAction {
	return []PreAction{}
}

func (m *MiddlewareAction) GetMiddlewareName() string {
	return m.Spec.MiddlewareName
}
