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
)

// MiddlewareConfigurationSpec defines the desired state of MiddlewareConfiguration.
type MiddlewareConfigurationSpec struct {
	// +kubebuilder:pruning:PreserveUnknownFields
	// Template is the CUE or Go template string used to generate Kubernetes resources from configuration values.
	Template string `json:"template"`
}

// MiddlewareConfigurationStatus defines the observed state of MiddlewareConfiguration.
type MiddlewareConfigurationStatus struct {
	// The generation observed by the deployment controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty" protobuf:"varint,1,opt,name=observedGeneration"`

	// Conditions represent the latest available observations of the configuration's current state.
	// +patchMergeKey=type
	// +patchStrategy=merge
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// Users is the list of GroupVersionKind resources that consume this configuration.
	Users []metav1.GroupVersionKind `json:"gvks,omitempty"`

	// State is the high-level state of the configuration. Valid values: Available, Unavailable, Updating.
	// +optional
	State State `json:"state,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=mcf
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.metadata.labels['middleware\.cn/component']`
// +kubebuilder:printcolumn:name="Package",type=string,JSONPath=`.metadata.labels['middleware\.cn/packagename']`
// +kubebuilder:printcolumn:name="Status",type=string,JSONPath=`.status.state`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// MiddlewareConfiguration is the Schema for the middlewareconfigurations API.
// It is a cluster-scoped resource that holds templates for generating Kubernetes resources from configuration values.
type MiddlewareConfiguration struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MiddlewareConfigurationSpec   `json:"spec,omitempty"`
	Status MiddlewareConfigurationStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// MiddlewareConfigurationList contains a list of MiddlewareConfiguration.
type MiddlewareConfigurationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MiddlewareConfiguration `json:"items"`
}

func init() {
	SchemeBuilder.Register(&MiddlewareConfiguration{}, &MiddlewareConfigurationList{})
}
