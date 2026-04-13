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

// MiddlewareActionBaselineSpec defines the desired state of MiddlewareActionBaseline.
type MiddlewareActionBaselineSpec struct {
	BaselineType       BaselineType         `json:"baselineType"`
	ActionType         string               `json:"actionType"`
	SupportedBaselines []string             `json:"supportedBaselines,omitempty"`
	Necessary          runtime.RawExtension `json:"necessary,omitempty"`
	Steps              []Step               `json:"steps"`
}

type Step struct {
	Name   string `json:"name"`
	Type   string `json:"type,omitempty"`
	Output Output `json:"output,omitempty"`
	CUE    string `json:"cue,omitempty"`
	CMD    CMD    `json:"cmd,omitempty"`
	HTTP   Http   `json:"http,omitempty"`
}

type Output struct {
	Expose bool   `json:"expose,omitempty"`
	Type   string `json:"type,omitempty"`
}

type CMD struct {
	Command []string `json:"command,omitempty"`
}

type Http struct {
	Method string            `json:"method"`
	URL    string            `json:"url"`
	Header map[string]string `json:"header,omitempty"`
	Body   string            `json:"body,omitempty"`
}

// MiddlewareActionBaselineStatus defines the observed state of MiddlewareActionBaseline
type MiddlewareActionBaselineStatus struct {
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
// +kubebuilder:resource:scope=Cluster,shortName=mab
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.metadata.labels['middleware\.cn/component']`
// +kubebuilder:printcolumn:name="Package",type=string,JSONPath=`.metadata.labels['middleware\.cn/packagename']`
// +kubebuilder:printcolumn:name="Status",type=string,JSONPath=`.status.state`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// MiddlewareActionBaseline is the Schema for the middlewareactionbaselines API.
type MiddlewareActionBaseline struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MiddlewareActionBaselineSpec   `json:"spec,omitempty"`
	Status MiddlewareActionBaselineStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// MiddlewareActionBaselineList contains a list of MiddlewareActionBaseline.
type MiddlewareActionBaselineList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MiddlewareActionBaseline `json:"items"`
}

func init() {
	SchemeBuilder.Register(&MiddlewareActionBaseline{}, &MiddlewareActionBaselineList{})
}
