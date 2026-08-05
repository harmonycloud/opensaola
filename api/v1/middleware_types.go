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
	// ReconcileOverrides is an OpenSaola-managed RFC 7396 JSON Merge Patch
	// applied to the rendered primary custom-resource spec after Baseline and
	// template rendering. It persists accepted changes to the live primary CR
	// made during a reconciliation pause when they cannot safely be represented
	// by Parameters alone, such as removal of a
	// Baseline default.
	ReconcileOverrides *ReconcileOverrides `json:"reconcileOverrides,omitempty"`
	// Configurations is the list of additional configuration resources to create alongside the middleware.
	Configurations []Configuration `json:"configurations,omitempty"`
}

// ReconcileOverrides holds the generated merge patch that is applied to the
// rendered primary custom-resource spec. Users normally do not edit it: the
// Middleware controller writes it after a successful resume-policy=merge.
type ReconcileOverrides struct {
	// SpecPatch is an RFC 7396 JSON Merge Patch rooted at the target CR spec.
	// +kubebuilder:pruning:PreserveUnknownFields
	SpecPatch runtime.RawExtension `json:"specPatch,omitempty"`
	// BaseSpec is the rendered primary-CR spec that SpecPatch was calculated
	// against. It lets later explicit MID/Baseline changes win at the same
	// path while preserving unrelated adopted changes from the pause.
	// +kubebuilder:pruning:PreserveUnknownFields
	BaseSpec runtime.RawExtension `json:"baseSpec,omitempty"`
	// GVK, Namespace and Name bind an override to the primary CR it was
	// adopted from. A Baseline/package target change must not apply an old
	// schema-specific patch to a different CR.
	GVK       GVK    `json:"gvk,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name,omitempty"`
}

// ReconcilePauseStatus is a durable, one-shot baseline for a paused
// Middleware reconciliation session. It is not an in-memory cache: it is
// retained only until the session resumes successfully or the resource is
// deleted.
type ReconcilePauseStatus struct {
	// DesiredSpec is the effective primary custom-resource spec rendered when
	// the pause first became active. It is the B input of the B/L/D merge.
	// +kubebuilder:pruning:PreserveUnknownFields
	DesiredSpec runtime.RawExtension `json:"desiredSpec,omitempty"`
	// GVK identifies the primary resource rendered for this session.
	GVK GVK `json:"gvk,omitempty"`
	// Namespace and Name identify the target primary custom resource.
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name,omitempty"`
	// ResourceUID protects against automatically adopting a deleted and
	// recreated actual resource during the pause.
	ResourceUID string `json:"resourceUID,omitempty"`
	// Generation is the Middleware generation at snapshot capture time.
	Generation int64 `json:"generation,omitempty"`
	// Hash is the SHA-256 of DesiredSpec and makes the snapshot identity
	// visible to operators and conflict-safe updates.
	Hash       string      `json:"hash,omitempty"`
	CapturedAt metav1.Time `json:"capturedAt,omitempty"`
	// AdoptedLiveSpecHash is the hash of the live primary CR spec used to
	// calculate the most recent merge. It is checked again immediately before
	// the resumed SSA write so a second live change during the pause is never overwritten using an
	// older L input.
	AdoptedLiveSpecHash string `json:"adoptedLiveSpecHash,omitempty"`
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

	// RenderedConfigurationResources is the lifecycle-managed inventory of resources rendered from MiddlewareConfigurations.
	// It enables kind-aware cleanup when a configuration template becomes empty after a feature is disabled.
	RenderedConfigurationResources []RenderedConfigurationResource `json:"renderedConfigurationResources,omitempty"`
	// RenderedConfigurationResourcesGeneration is the generation that last successfully reconciled the inventory.
	// It prevents an older status writer from overwriting a newer inventory snapshot.
	RenderedConfigurationResourcesGeneration int64 `json:"renderedConfigurationResourcesGeneration,omitempty"`

	// ReconcilePause stores the durable baseline used to safely merge primary
	// custom-resource changes made during a suspend-reconcile window.
	ReconcilePause *ReconcilePauseStatus `json:"reconcilePause,omitempty"`
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
