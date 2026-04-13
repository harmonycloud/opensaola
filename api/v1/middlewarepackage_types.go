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

// MiddlewarePackageSpec defines the desired state of MiddlewarePackage.
// It describes the metadata and contents of a middleware distribution package.
type MiddlewarePackageSpec struct {
	// Name is the display name of the middleware package.
	Name string `json:"name,omitempty"`
	// Catalog lists the resources included in this package (CRDs, baselines, configurations, actions).
	Catalog Catalog `json:"catalog,omitempty"`
	// Version is the semantic version of this package.
	Version string `json:"version,omitempty"`
	// Owner is the entity or team that maintains this package.
	Owner string `json:"owner,omitempty"`
	// Type is the middleware type identifier (e.g., "mysql", "redis", "kafka").
	Type string `json:"type,omitempty"`
	// Description is a human-readable description of this middleware package.
	Description string `json:"description,omitempty"`
}

// Catalog lists the resources included in a MiddlewarePackage.
type Catalog struct {
	// Crds is the list of CustomResourceDefinition names bundled in this package.
	Crds []string `json:"crds,omitempty"`
	// Baselines is the list of baseline resource names (MiddlewareBaseline and MiddlewareOperatorBaseline) in this package.
	Baselines []string `json:"definitions,omitempty"`
	// Configurations is the list of MiddlewareConfiguration resource names in this package.
	Configurations []string `json:"configurations,omitempty"`
	// Actions is the list of MiddlewareActionBaseline resource names in this package.
	Actions []string `json:"actions,omitempty"`
}

// MiddlewarePackageStatus defines the observed state of MiddlewarePackage.
type MiddlewarePackageStatus struct {
	// The generation observed by the deployment controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty" protobuf:"varint,1,opt,name=observedGeneration"`

	// Conditions represent the latest available observations of the package's current state.
	// +patchMergeKey=type
	// +patchStrategy=merge
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// State is the high-level state of the package. Valid values: Available, Unavailable, Updating.
	// +optional
	State State `json:"state,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=mp,scope=Cluster
// +kubebuilder:printcolumn:name="Version",type=string,JSONPath=`.spec.version`
// +kubebuilder:printcolumn:name="Status",type=string,JSONPath=`.status.state`
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.metadata.labels['middleware\.cn/component']`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// MiddlewarePackage is the Schema for the middlewarepackages API.
// It is a cluster-scoped resource that represents an installable middleware distribution package.
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
