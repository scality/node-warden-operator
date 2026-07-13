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

package predicate

import (
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

// ConditionStatusChanged passes a Node update when the status of some condition type changed
// (added, removed, or flipped), or when a condition first gains a lastTransitionTime it lacked.
// Plain lastHeartbeatTime churn and lastTransitionTime bumps that keep the same status and
// presence are ignored. The lastTransitionTime-appears case matters because a remediation held
// for a missing timestamp can only proceed once the timestamp shows up, and that change would
// otherwise be filtered out. Create/Delete/Generic events are not filtered (the default Funcs
// behavior), so a node is still evaluated when it appears or disappears.
func ConditionStatusChanged() predicate.Predicate {
	return predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldNode, ok1 := e.ObjectOld.(*corev1.Node)
			newNode, ok2 := e.ObjectNew.(*corev1.Node)
			if !ok1 || !ok2 {
				return false
			}
			return len(ChangedConditionTypes(oldNode.Status.Conditions, newNode.Status.Conditions)) > 0
		},
	}
}

// ChangedConditionTypes returns the set of condition types that changed between old and new in a
// way that can affect a decision: the status flipped (or the type was added/removed), or the
// condition gained/lost its lastTransitionTime. Plain timestamp bumps and ordering are ignored.
// The Node watch uses it to enqueue only the policies that reference an actually-changed
// condition, so an unrelated condition flipping on the node does not wake every policy.
func ChangedConditionTypes(oldConds, newConds []corev1.NodeCondition) map[string]bool {
	oldState := conditionStateByType(oldConds)
	newState := conditionStateByType(newConds)
	changed := make(map[string]bool)
	for condType, state := range oldState {
		if newState[condType] != state {
			changed[condType] = true
		}
	}
	for condType, state := range newState {
		if oldState[condType] != state {
			changed[condType] = true
		}
	}
	return changed
}

// conditionState is the part of a condition that matters for waking the loop: its status and
// whether it carries a lastTransitionTime (the exact time is churn and ignored).
type conditionState struct {
	status       corev1.ConditionStatus
	hasTimestamp bool
}

// conditionStateByType indexes conditions by type; an absent type reads back as the zero state,
// so an added or removed condition is detected as a change.
func conditionStateByType(conds []corev1.NodeCondition) map[string]conditionState {
	m := make(map[string]conditionState, len(conds))
	for i := range conds {
		m[string(conds[i].Type)] = conditionState{
			status:       conds[i].Status,
			hasTimestamp: !conds[i].LastTransitionTime.IsZero(),
		}
	}
	return m
}
