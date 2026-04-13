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

// MiddlewareActionBaselineSpec defines the desired state of MiddlewareActionBaseline.
// It serves as the cluster-scoped template that defines execution steps for MiddlewareAction instances.
type MiddlewareActionBaselineSpec struct {
	// BaselineType indicates whether this baseline is a pre-action or normal action.
	// Valid values: PreAction, NormalAction.
	BaselineType BaselineType `json:"baselineType"`
	// ActionType is the type identifier for this action (e.g., "backup", "restore", "healthcheck").
	ActionType string `json:"actionType"`
	// SupportedBaselines is the optional list of MiddlewareBaseline names that this action supports.
	SupportedBaselines []string `json:"supportedBaselines,omitempty"`
	// Necessary holds required default parameters for this action type.
	Necessary runtime.RawExtension `json:"necessary,omitempty"`
	// Steps is the ordered list of execution steps that make up this action.
	Steps []Step `json:"steps"`
}

// Step defines a single execution step within a MiddlewareActionBaseline.
type Step struct {
	// Name is the unique name identifying this step within the action.
	Name string `json:"name"`
	// Type is the optional step type classifier for conditional logic.
	Type string `json:"type,omitempty"`
	// Output defines how the step's output is handled and exposed.
	Output Output `json:"output,omitempty"`
	// CUE holds the CUE expression to evaluate for this step. Mutually exclusive with CMD and HTTP.
	CUE string `json:"cue,omitempty"`
	// CMD holds the command to execute for this step. Mutually exclusive with CUE and HTTP.
	CMD CMD `json:"cmd,omitempty"`
	// HTTP holds the HTTP request to execute for this step. Mutually exclusive with CUE and CMD.
	HTTP Http `json:"http,omitempty"`
}

// Output defines how a step's execution output is handled.
type Output struct {
	// Expose indicates whether the step output should be exposed to the parent MiddlewareAction status.
	Expose bool `json:"expose,omitempty"`
	// Type is the format type of the output (e.g., for serialization or display).
	Type string `json:"type,omitempty"`
}

// CMD defines a command execution step.
type CMD struct {
	// Command is the command and arguments to execute as a subprocess.
	Command []string `json:"command,omitempty"`
}

// Http defines an HTTP request execution step.
type Http struct {
	// Method is the HTTP method to use (e.g., GET, POST, PUT, DELETE).
	Method string `json:"method"`
	// URL is the target URL for the HTTP request. Supports template variable substitution.
	URL string `json:"url"`
	// Header is the optional map of HTTP headers to include in the request.
	Header map[string]string `json:"header,omitempty"`
	// Body is the optional HTTP request body content. Supports template variable substitution.
	Body string `json:"body,omitempty"`
}

// MiddlewareActionBaselineStatus defines the observed state of MiddlewareActionBaseline.
type MiddlewareActionBaselineStatus struct {
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
// +kubebuilder:resource:scope=Cluster,shortName=mab
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.metadata.labels['middleware\.cn/component']`
// +kubebuilder:printcolumn:name="Package",type=string,JSONPath=`.metadata.labels['middleware\.cn/packagename']`
// +kubebuilder:printcolumn:name="Status",type=string,JSONPath=`.status.state`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// MiddlewareActionBaseline is the Schema for the middlewareactionbaselines API.
// It is a cluster-scoped resource that defines reusable action step templates for MiddlewareAction instances.
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
