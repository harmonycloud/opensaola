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

// MiddlewareSpec defines the desired state of Middleware.
type MiddlewareSpec struct {
	// OperatorBaseline references the MiddlewareOperatorBaseline that manages the operator for this middleware.
	OperatorBaseline OperatorBaseline `json:"operatorBaseline,omitempty"`
	// Baseline is the name of the MiddlewareBaseline to use as the default template.
	Baseline string `json:"baseline,omitempty"`
	// Necessary holds required parameters for the middleware instance as raw JSON.
	// The exact schema depends on the middleware type defined in the MiddlewareBaseline.
	// Common fields include image, replicas, storage, and resource requirements.
	// Example: {"image": "redis:7.2", "replicas": 3, "storage": "10Gi"}
	Necessary runtime.RawExtension `json:"necessary,omitempty"`
	// PreActions is the list of pre-actions to execute before the main reconciliation workflow.
	PreActions []PreAction `json:"preActions,omitempty"`
	// +kubebuilder:pruning:PreserveUnknownFields
	// Parameters holds user-configurable parameters for the middleware instance as raw JSON.
	// These values are merged with defaults from the MiddlewareBaseline during reconciliation.
	// The schema varies by middleware type; common fields include port, password, and tuning knobs.
	// Example: {"port": 6379, "maxmemory": "256mb", "databases": 16}
	Parameters runtime.RawExtension `json:"parameters,omitempty"`
	// Configurations is the list of additional configuration resources to create alongside the middleware.
	Configurations []Configuration `json:"configurations,omitempty"`
}

// MiddlewareStatus defines the observed state of Middleware.
type MiddlewareStatus struct {
	// The generation observed by the deployment controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty" protobuf:"varint,1,opt,name=observedGeneration"`

	// Conditions represent the latest available observations of the middleware's current state.
	// +patchMergeKey=type
	// +patchStrategy=merge
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// CustomResources holds the status of the underlying custom resources managed by the middleware operator.
	CustomResources CustomResources `json:"customResources,omitempty"`

	// State is the high-level state of the middleware. Valid values: Available, Unavailable, Updating.
	// +optional
	State State `json:"state,omitempty"`
	// Reason provides a human-readable explanation of the current state.
	Reason string `json:"reason,omitempty"`
}

// CustomResources holds the status of custom sub-resources created by the middleware operator.
type CustomResources struct {
	// CreationTimestamp is the time when the custom resources were first created.
	CreationTimestamp metav1.Time `json:"creationTimestamp,omitempty"`
	// Phase is the current lifecycle phase of the custom resources.
	Phase Phase `json:"phase,omitempty"`
	// Resources holds the compute resource requirements (CPU, memory) of the custom resources.
	Resources v1.ResourceRequirements `json:"resources,omitempty"`
	// Replicas is the number of replicas reported by the custom resources.
	Replicas int `json:"replicas,omitempty"`
	// Reason provides a human-readable explanation of the current phase.
	Reason string `json:"reason,omitempty"`
	// Type is the kind identifier of the custom resource (e.g., "cluster", "resources").
	Type string `json:"type,omitempty"`
	// Include holds references to sub-resources (pods, PVCs, services, etc.) associated with this middleware.
	Include Include `json:"include,omitempty"`
	// Disaster holds the optional disaster recovery configuration and status.
	Disaster *Disaster `json:"disaster,omitempty"`
}

// Disaster holds disaster recovery configuration and status for cross-cluster replication.
type Disaster struct {
	// Gossip holds the gossip protocol discovery configuration for cluster membership.
	Gossip *Gossip `json:"gossip,omitempty"`
	// Data holds the data replication configuration and status.
	Data *Data `json:"data,omitempty"`
}

// Gossip holds gossip protocol configuration for cluster node discovery and membership.
type Gossip struct {
	// AdvertiseAddress is the address this node advertises to peers for gossip communication.
	AdvertiseAddress string `json:"advertiseAddress,omitempty"`
	// AdvertisePort is the port this node advertises to peers for gossip communication.
	AdvertisePort int64 `json:"advertisePort,omitempty"`
	// Phase is the current phase of the gossip protocol.
	Phase string `json:"phase,omitempty"`
	// ClusterRole is the role of this node within the cluster (e.g., primary, secondary).
	ClusterRole string `json:"clusterRole,omitempty"`
	// ClusterPhase is the current phase of the cluster as a whole.
	ClusterPhase string `json:"clusterPhase,omitempty"`
	// Role is the replication role of this node.
	Role string `json:"role,omitempty"`
	// GossipPhase is the current phase of the gossip subsystem specifically.
	GossipPhase string `json:"gossipPhase,omitempty"`
}

// Data holds data replication configuration for disaster recovery.
type Data struct {
	// Phase is the current phase of the data replication process.
	Phase string `json:"phase,omitempty"`
	// Address is the target address for data replication.
	Address string `json:"targetAddress,omitempty"`
	// OppositeAddress is the address of the peer cluster for bidirectional replication.
	OppositeAddress string `json:"oppositeAddress,omitempty"`
	// OppositeClusterId is the unique identifier of the peer cluster.
	OppositeClusterId string `json:"oppositeClusterId,omitempty"`
	// OppositeClusterName is the name of the peer cluster.
	OppositeClusterName string `json:"oppositeClusterName,omitempty"`
	// OppositeClusterNamespace is the namespace in the peer cluster.
	OppositeClusterNamespace string `json:"oppositeClusterNamespace,omitempty"`
}

// Include holds references to Kubernetes sub-resources associated with a middleware instance.
type Include struct {
	// Pods is the list of associated Pod resources.
	Pods []IncludeModel `json:"pods,omitempty"`
	// Pvcs is the list of associated PersistentVolumeClaim resources.
	Pvcs []IncludeModel `json:"pvcs,omitempty"`
	// Services is the list of associated Service resources.
	Services []IncludeModel `json:"services,omitempty"`
	// Statefulsets is the list of associated StatefulSet resources.
	Statefulsets []IncludeModel `json:"statefulsets,omitempty"`
	// Deployments is the list of associated Deployment resources.
	Deployments []IncludeModel `json:"deployments,omitempty"`
	// Daemonsets is the list of associated DaemonSet resources.
	Daemonsets []IncludeModel `json:"daemonsets,omitempty"`
}

// IncludeModel represents a reference to a Kubernetes sub-resource.
type IncludeModel struct {
	// Name is the name of the sub-resource.
	Name string `json:"name,omitempty"`
	// Type is the kind of the sub-resource.
	Type string `json:"type,omitempty"`
	// Source is the origin of this sub-resource reference.
	Source string `json:"source,omitempty"`
	// SourceName is the name of the source that created this sub-resource.
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
// It represents a deployed middleware instance (e.g., MySQL, Redis) managed by the operator.
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
