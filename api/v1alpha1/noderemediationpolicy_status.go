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
	"fmt"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ConditionRemediating is the single condition type the policy publishes; its reason tells which
// state the policy is in.
const ConditionRemediating = "Remediating"

// Reasons for the Remediating condition, one per state the policy can be in.
const (
	ReasonInvalidSpec     = "InvalidSpec"
	ReasonGuardTripped    = "GuardTripped"
	ReasonNodesRemediated = "NodesRemediated"
	ReasonPending         = "Pending"
	ReasonHeld            = "Held"
	ReasonNoRemediation   = "NoRemediation"
)

// MarkInvalidSpec records that the spec cannot be used; the operator holds off until it is fixed.
func (p *NodeRemediationPolicy) MarkInvalidSpec(message string) {
	p.setRemediatingCondition(metav1.ConditionFalse, ReasonInvalidSpec, message)
}

// MarkGuardTripped records that too many selected nodes matched at once, so the guard holds back
// new remediation. Nodes remediated on earlier passes keep their taint (the guard only blocks new
// ones), so the message reports how many currently carry it rather than claiming none do.
func (p *NodeRemediationPolicy) MarkGuardTripped(remediated int) {
	p.setRemediatingCondition(metav1.ConditionFalse, ReasonGuardTripped,
		fmt.Sprintf("guard tripped: too many matching nodes; holding new remediations, %d node(s) currently remediated", remediated))
}

// MarkNodesRemediated records that count nodes currently carry the remediation.
func (p *NodeRemediationPolicy) MarkNodesRemediated(count int) {
	p.setRemediatingCondition(metav1.ConditionTrue, ReasonNodesRemediated,
		fmt.Sprintf("%d node(s) remediated", count))
}

// MarkPending records that count matched nodes are awaiting the debounce window before their
// taint is applied; no node is remediated yet, so the condition stays False but the reason
// distinguishes this from an idle policy.
func (p *NodeRemediationPolicy) MarkPending(count int) {
	p.setRemediatingCondition(metav1.ConditionFalse, ReasonPending,
		fmt.Sprintf("%d node(s) awaiting the debounce window before remediation", count))
}

// MarkHeld records that count nodes are held pending a usable condition signal - the condition is
// Unknown/unreported, or it matches but carries no lastTransitionTime to debounce against - so the
// policy neither remediates nor reports them as healthy.
func (p *NodeRemediationPolicy) MarkHeld(count int) {
	p.setRemediatingCondition(metav1.ConditionFalse, ReasonHeld,
		fmt.Sprintf("%d node(s) held pending a usable condition signal (missing lastTransitionTime or unknown status)", count))
}

// MarkNoRemediation records that no selected node currently requires remediation.
func (p *NodeRemediationPolicy) MarkNoRemediation() {
	p.setRemediatingCondition(metav1.ConditionFalse, ReasonNoRemediation,
		"no nodes require remediation")
}

// setRemediatingCondition upserts the single Remediating condition, stamping the current
// generation as observedGeneration. It is the one place that condition is written.
func (p *NodeRemediationPolicy) setRemediatingCondition(status metav1.ConditionStatus, reason, message string) {
	apimeta.SetStatusCondition(&p.Status.Conditions, metav1.Condition{
		Type:               ConditionRemediating,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: p.Generation,
	})
}
