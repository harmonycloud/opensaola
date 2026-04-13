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

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// MiddlewarePackageSpec defines the desired state of MiddlewarePackage.
type MiddlewarePackageSpec struct {
	Name        string  `json:"name,omitempty"`
	Catalog     Catalog `json:"catalog,omitempty"`
	Version     string  `json:"version,omitempty"`
	Owner       string  `json:"owner,omitempty"`
	Type        string  `json:"type,omitempty"`
	Description string  `json:"description,omitempty"`
}

type Catalog struct {
	Crds           []string `json:"crds,omitempty"`
	Baselines      []string `json:"definitions,omitempty"`
	Configurations []string `json:"configurations,omitempty"`
	Actions        []string `json:"actions,omitempty"`
}

// MiddlewarePackageStatus defines the observed state of MiddlewarePackage.
type MiddlewarePackageStatus struct {
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
// +kubebuilder:resource:shortName=mp,scope=Cluster
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.metadata.labels['middleware\.cn/component']`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// MiddlewarePackage is the Schema for the middlewarepackages API.
type MiddlewarePackage struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MiddlewarePackageSpec   `json:"spec,omitempty"`
	Status MiddlewarePackageStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// MiddlewarePackageList contains a list of MiddlewarePackage.
type MiddlewarePackageList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MiddlewarePackage `json:"items"`
}

func init() {
	SchemeBuilder.Register(&MiddlewarePackage{}, &MiddlewarePackageList{})
}
