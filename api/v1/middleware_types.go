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
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// MiddlewareSpec defines the desired state of Middleware.
type MiddlewareSpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	OperatorBaseline OperatorBaseline     `json:"operatorBaseline,omitempty"`
	Baseline         string               `json:"baseline,omitempty"`
	Necessary        runtime.RawExtension `json:"necessary,omitempty"`
	PreActions       []PreAction          `json:"preActions,omitempty"`
	// +kubebuilder:pruning:PreserveUnknownFields
	Parameters     runtime.RawExtension `json:"parameters,omitempty"`
	Configurations []Configuration      `json:"configurations,omitempty"`
}

// MiddlewareStatus defines the observed state of Middleware.
type MiddlewareStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// The generation observed by the deployment controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty" protobuf:"varint,1,opt,name=observedGeneration"`

	// Represents the latest available observations of a deployment's current state.
	// +patchMergeKey=type
	// +patchStrategy=merge
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	CustomResources CustomResources `json:"customResources,omitempty"`

	// The state of the middleware.
	// +optional
	State  State  `json:"state,omitempty"`
	Reason string `json:"reason,omitempty"`
}

type CustomResources struct {
	CreationTimestamp metav1.Time             `json:"creationTimestamp,omitempty"`
	Phase             Phase                   `json:"phase,omitempty"`
	Resources         v1.ResourceRequirements `json:"resources,omitempty"`
	Replicas          int                     `json:"replicas,omitempty"`
	Reason            string                  `json:"reason,omitempty"`
	Type              string                  `json:"type,omitempty"`
	Include           Include                 `json:"include,omitempty"`
	Disaster          *Disaster               `json:"disaster,omitempty"`
}

type Disaster struct {
	Gossip *Gossip `json:"gossip,omitempty"`
	Data   *Data   `json:"data,omitempty"`
}

type Gossip struct {
	AdvertiseAddress string `json:"advertiseAddress,omitempty"`
	AdvertisePort    int64  `json:"advertisePort,omitempty"`
	Phase            string `json:"phase,omitempty"`
	ClusterRole      string `json:"clusterRole,omitempty"`
	ClusterPhase     string `json:"clusterPhase,omitempty"`
	Role             string `json:"role,omitempty"`
	GossipPhase      string `json:"gossipPhase,omitempty"`
}

type Data struct {
	Phase                    string `json:"phase,omitempty"`
	Address                  string `json:"targetAddress,omitempty"`
	OppositeAddress          string `json:"oppositeAddress,omitempty"`
	OppositeClusterId        string `json:"oppositeClusterId,omitempty"`
	OppositeClusterName      string `json:"oppositeClusterName,omitempty"`
	OppositeClusterNamespace string `json:"oppositeClusterNamespace,omitempty"`
}

type Include struct {
	Pods         []IncludeModel `json:"pods,omitempty"`
	Pvcs         []IncludeModel `json:"pvcs,omitempty"`
	Services     []IncludeModel `json:"services,omitempty"`
	Statefulsets []IncludeModel `json:"statefulsets,omitempty"`
	Deployments  []IncludeModel `json:"deployments,omitempty"`
	Daemonsets   []IncludeModel `json:"daemonsets,omitempty"`
}

type IncludeModel struct {
	Name       string `json:"name,omitempty"`
	Type       string `json:"type,omitempty"`
	Source     string `json:"source,omitempty"`
	SourceName string `json:"sourceName,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=mid
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.metadata.labels['middleware\.cn/component']`
// +kubebuilder:printcolumn:name="Package",type=string,JSONPath=`.metadata.labels['middleware\.cn/packagename']`
// +kubebuilder:printcolumn:name="Baseline",type=string,JSONPath=`.spec.baseline`
// +kubebuilder:printcolumn:name="Status",type=string,JSONPath=`.status.state`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Middleware is the Schema for the middlewares API.
type Middleware struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MiddlewareSpec   `json:"spec,omitempty"`
	Status MiddlewareStatus `json:"status,omitempty"`
}

func (m *Middleware) GetConfigurations() []Configuration {
	return m.Spec.Configurations
}

func (m *Middleware) GetUnified() *runtime.RawExtension {
	return &m.Spec.Necessary
}

func (m *Middleware) GetPreActions() []PreAction {
	return m.Spec.PreActions
}

func (m *Middleware) GetMiddlewareName() string {
	return m.Name
}

// +kubebuilder:object:root=true

// MiddlewareList contains a list of Middleware.
type MiddlewareList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Middleware `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Middleware{}, &MiddlewareList{})
}
