/*
Copyright 2026 Scality.

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

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// NodeRemediationPolicySpec defines the desired state of NodeRemediationPolicy
type NodeRemediationPolicySpec struct {
	// nodeSelector scopes the policy to a subset of nodes (empty = all nodes).
	// +optional
	NodeSelector *metav1.LabelSelector `json:"nodeSelector,omitempty"`
	// condition is the node condition that triggers remediation.
	Condition ConditionMatch `json:"condition"`
	// remediations lists the remediations to apply to a matched node; v1 supports taint.
	Remediations Remediations `json:"remediations"`
	// debounce sets how long the condition must be stable before applying or removing the remediation.
	// +optional
	Debounce Debounce `json:"debounce,omitempty"`
	// guard limits how many nodes a single policy may remediate at once.
	// +optional
	Guard Guard `json:"guard,omitempty"`
}

// ConditionMatch identifies the node condition that triggers remediation.
type ConditionMatch struct {
	// type is the node condition type to watch (for example, one set by a node problem detector).
	// +kubebuilder:validation:MinLength=1
	Type string `json:"type"`
	// status is the condition status that triggers remediation.
	// +kubebuilder:validation:Enum=True;False;Unknown
	// +kubebuilder:default="True"
	Status string `json:"status,omitempty"`
}

// Remediations is an additive set of remediations to apply; v1 supports only taint.
// +kubebuilder:validation:XValidation:rule="has(self.taint)",message="at least one remediation must be set (v1 supports 'taint')"
type Remediations struct {
	// taint applied to matched nodes; effect must be a real Kubernetes taint effect. NoExecute
	// also evicts non-tolerating pods and drops the node from Service endpoints; NoSchedule and
	// PreferNoSchedule only keep new pods off the node. The taint is reversible and removed when
	// the condition clears.
	// +kubebuilder:validation:XValidation:rule="self.effect in ['NoSchedule','PreferNoSchedule','NoExecute']",message="taint.effect must be one of NoSchedule, PreferNoSchedule, NoExecute"
	// key is a valid Kubernetes qualified name: a name of at most 63 characters, optionally prefixed
	// with a DNS subdomain and a slash (for example, node.example.com/unreachable).
	// +kubebuilder:validation:XValidation:rule="self.key.matches('^([a-z0-9]([-a-z0-9]*[a-z0-9])?([.][a-z0-9]([-a-z0-9]*[a-z0-9])?)*/)?[A-Za-z0-9]([-A-Za-z0-9_.]{0,61}[A-Za-z0-9])?$') && self.key.size() <= 317",message="taint.key must be a valid Kubernetes qualified name (name <=63 chars, optional DNS-subdomain prefix)"
	// value, when set, is a valid Kubernetes label value (at most 63 characters).
	// +kubebuilder:validation:XValidation:rule="!has(self.value) || self.value.matches('^([A-Za-z0-9]([-A-Za-z0-9_.]{0,61}[A-Za-z0-9])?)?$')",message="taint.value must be empty or a valid Kubernetes label value (<=63 chars)"
	// The taint is immutable once set; delete and recreate the policy to change it.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="taint is immutable; delete and recreate the policy to change it"
	// +optional
	Taint *corev1.Taint `json:"taint,omitempty"`
}

// Debounce controls how long the condition must be stable before the remediation is applied or
// removed. enter and exit are each a non-negative Go duration (for example, 60s, 1h30m, 500ms).
// +kubebuilder:validation:XValidation:rule="(!has(self.enter) || self.enter.matches('^(([0-9]{1,6}([.][0-9]+)?|[.][0-9]+)(ns|us|µs|ms|s|m|h))+$')) && (!has(self.exit) || self.exit.matches('^(([0-9]{1,6}([.][0-9]+)?|[.][0-9]+)(ns|us|µs|ms|s|m|h))+$'))",message="debounce.enter and debounce.exit must be a non-negative Go duration (e.g. 60s, 1h30m, 500ms)"
type Debounce struct {
	// enter is how long the condition must hold before the remediation is applied.
	// +kubebuilder:default="60s"
	// +optional
	Enter *metav1.Duration `json:"enter,omitempty"`
	// exit is how long the condition must be clear before the remediation is removed.
	// +kubebuilder:default="30s"
	// +optional
	Exit *metav1.Duration `json:"exit,omitempty"`
}

// Guard limits the blast radius of a policy.
type Guard struct {
	// maxAffectedPercent is evaluated over the selected nodes that report a determinate condition
	// (Unknown or unreported nodes are excluded); if the percentage of matched nodes exceeds it, no
	// node is remediated. Defaults to 100 (guard off).
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=100
	// +kubebuilder:default=100
	// +optional
	MaxAffectedPercent *int32 `json:"maxAffectedPercent,omitempty"`
}

// NodeRemediationPolicyStatus defines the observed state of NodeRemediationPolicy.
type NodeRemediationPolicyStatus struct {
	// selectedNodes are the nodes in scope, matching spec.nodeSelector (empty selector = all nodes).
	// +optional
	SelectedNodes []string `json:"selectedNodes,omitempty"`
	// matchedNodes are the selected nodes whose condition currently matches.
	// +optional
	MatchedNodes []string `json:"matchedNodes,omitempty"`
	// pendingNodes are the matched nodes not yet remediated (awaiting the debounce window,
	// blocked by the guard, or being applied).
	// +optional
	PendingNodes []string `json:"pendingNodes,omitempty"`
	// unknownNodes are the selected nodes whose condition is Unknown or unreported (held:
	// neither remediated nor cleared).
	// +optional
	UnknownNodes []string `json:"unknownNodes,omitempty"`
	// heldNodes are the matched nodes held because their condition carries no lastTransitionTime,
	// so the debounce cannot be evaluated; they stay stuck until a timestamp appears.
	// +optional
	HeldNodes []string `json:"heldNodes,omitempty"`
	// remediatedNodes are the selected nodes that currently carry the remediation taint.
	// +optional
	RemediatedNodes []string `json:"remediatedNodes,omitempty"`
	// selectedCount is the number of nodes in scope of the policy.
	// +optional
	SelectedCount int32 `json:"selectedCount,omitempty"`
	// matchedCount is the number of nodes currently matching the policy condition.
	// +optional
	MatchedCount int32 `json:"matchedCount,omitempty"`
	// pendingCount is the number of matched nodes not yet remediated.
	// +optional
	PendingCount int32 `json:"pendingCount,omitempty"`
	// unknownCount is the number of selected nodes whose condition is Unknown or unreported.
	// +optional
	UnknownCount int32 `json:"unknownCount,omitempty"`
	// heldCount is the number of matched nodes held for a missing lastTransitionTime.
	// +optional
	HeldCount int32 `json:"heldCount,omitempty"`
	// remediatedCount is the number of nodes currently carrying the remediation.
	// +optional
	RemediatedCount int32 `json:"remediatedCount,omitempty"`

	// conditions represents the observations of the policy state (e.g. the Remediating condition).
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=nrp
// +kubebuilder:printcolumn:name="Condition",type=string,JSONPath=`.spec.condition.type`
// +kubebuilder:printcolumn:name="Matched",type=integer,JSONPath=`.status.matchedCount`
// +kubebuilder:printcolumn:name="Remediated",type=integer,JSONPath=`.status.remediatedCount`
// +kubebuilder:printcolumn:name="Unknown",type=integer,JSONPath=`.status.unknownCount`
// +kubebuilder:printcolumn:name="Selected",type=integer,JSONPath=`.status.selectedCount`,priority=1
// +kubebuilder:printcolumn:name="Pending",type=integer,JSONPath=`.status.pendingCount`,priority=1
// +kubebuilder:printcolumn:name="Held",type=integer,JSONPath=`.status.heldCount`,priority=1
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// NodeRemediationPolicy is the Schema for the noderemediationpolicies API
type NodeRemediationPolicy struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of NodeRemediationPolicy
	// +required
	Spec NodeRemediationPolicySpec `json:"spec"`

	// status defines the observed state of NodeRemediationPolicy
	// +optional
	Status NodeRemediationPolicyStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// NodeRemediationPolicyList contains a list of NodeRemediationPolicy
type NodeRemediationPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []NodeRemediationPolicy `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &NodeRemediationPolicy{}, &NodeRemediationPolicyList{})
		return nil
	})
}
