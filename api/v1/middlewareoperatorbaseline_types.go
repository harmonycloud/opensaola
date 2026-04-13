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

// MiddlewareOperatorBaselineSpec defines the desired state of MiddlewareOperatorBaseline.
type MiddlewareOperatorBaselineSpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	GVKs            []GVK                 `json:"gvks,omitempty"`
	Globe           *runtime.RawExtension `json:"globe,omitempty"`
	PreActions      []PreAction           `json:"preActions,omitempty"`
	PermissionScope PermissionScope       `json:"permissionScope,omitempty"`
	Permissions     []Permission          `json:"permissions,omitempty"`
	Deployment      *runtime.RawExtension `json:"deployment,omitempty"`
	Configurations  []Configuration       `json:"configurations,omitempty"`
}

// MiddlewareOperatorBaselineStatus defines the observed state of MiddlewareOperatorBaseline.
type MiddlewareOperatorBaselineStatus struct {
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
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=mob
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.metadata.labels['middleware\.cn/component']`
// +kubebuilder:printcolumn:name="Package",type=string,JSONPath=`.metadata.labels['middleware\.cn/packagename']`
// +kubebuilder:printcolumn:name="Status",type=string,JSONPath=`.status.state`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// MiddlewareOperatorBaseline is the Schema for the middlewareoperatorbaselines API.
type MiddlewareOperatorBaseline struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MiddlewareOperatorBaselineSpec   `json:"spec,omitempty"`
	Status MiddlewareOperatorBaselineStatus `json:"status,omitempty"`
}

func (m *MiddlewareOperatorBaseline) GetConfigurations() []Configuration {
	return m.Spec.Configurations
}

func (m *MiddlewareOperatorBaseline) GetUnified() *runtime.RawExtension {
	return m.Spec.Globe
}

// +kubebuilder:object:root=true

// MiddlewareOperatorBaselineList contains a list of MiddlewareOperatorBaseline.
type MiddlewareOperatorBaselineList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MiddlewareOperatorBaseline `json:"items"`
}

func init() {
	SchemeBuilder.Register(&MiddlewareOperatorBaseline{}, &MiddlewareOperatorBaselineList{})
}
