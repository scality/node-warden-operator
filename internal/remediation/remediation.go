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

package remediation

import (
	"time"

	"k8s.io/apimachinery/pkg/labels"
)

// statusUnknown is the condition status that marks a node as indeterminate: the detector is
// down or the node is unreachable. It is treated as a hold, never as a recovery.
const statusUnknown = "Unknown"

// Taint is the desired taint identity, from a policy's remediations.taint.
type Taint struct {
	Key    string
	Value  string
	Effect string
}

// ConditionFact is one observed node condition: its status and when it last changed.
type ConditionFact struct {
	Status         string // "True" | "False" | "Unknown" | "" (absent)
	LastTransition time.Time
}

// NodeFact is the observed state of one node relevant to a policy. Condition is the single
// condition the policy watches (its zero value, Status "", means the node does not report it);
// the shell picks it out so the core never deals with the node's other conditions.
type NodeFact struct {
	Name      string
	Labels    map[string]string
	Condition ConditionFact
	HasTaint  bool // the policy's taint key is currently present on the node
}

// Policy is the pure projection of a NodeRemediationPolicy spec.
type Policy struct {
	Selector           labels.Selector // parsed by the shell; labels.Everything() if no selector
	ConditionType      string
	ConditionStatus    string // the status that triggers remediation
	Taint              Taint
	DebounceEnter      time.Duration
	DebounceExit       time.Duration
	MaxAffectedPercent int32 // over SELECTED nodes; 100 = guard disabled
}

// Decision is what the shell must enact for one policy.
type Decision struct {
	ApplyTaint  []string // node names that must gain the taint now
	RemoveTaint []string // node names that must lose the taint now
	// HeldMissingTimestamp lists selected nodes whose remediation was held because their
	// condition carries no lastTransitionTime, so the debounce cannot be evaluated. The shell
	// surfaces these as a warning; they are not applied or removed.
	HeldMissingTimestamp []string
	RequeueAfter         time.Duration
	Status               Status
}

// Status is the projected policy status.
type Status struct {
	SelectedNodes   []string // nodes in scope (match the selector)
	MatchedNodes    []string // selected nodes whose condition matches
	PendingNodes    []string // matched nodes not yet remediated (debounce/guard/mid-apply)
	UnknownNodes    []string // selected nodes whose condition is Unknown or unreported (held)
	RemediatedNodes []string // selected nodes observed to currently carry the taint
	GuardTripped    bool
	// DeterminateCount is the number of selected nodes with a determinate condition (matched or
	// cleared, i.e. not Unknown/unreported): the denominator the guard percentage is evaluated
	// over. Surfaced so the guard event reports the same fraction the guard actually used.
	DeterminateCount int
}

// Decide is the pure core: given a policy, the observed node facts and the current time,
// it returns the taints to add/remove, the requeue delay for a pending debounce window, and
// the projected status. It performs no I/O.
func Decide(p Policy, nodes []NodeFact, now time.Time) Decision {
	if p.Selector == nil {
		p.Selector = labels.Everything()
	}

	// Select the in-scope nodes, evaluating the condition match once per node (and counting the
	// matches) so the action loop below can reuse it instead of re-checking.
	type evaluated struct {
		NodeFact
		matches bool
	}
	var selected []evaluated
	var scopedOutTainted []string
	matchedCount := 0
	determinateCount := 0
	for _, n := range nodes {
		if !p.Selector.Matches(labels.Set(n.Labels)) {
			// Out of scope. If it still carries our taint (e.g. relabeled out of the selector
			// after being remediated), remove it so the node is not left tainted forever.
			if n.HasTaint {
				scopedOutTainted = append(scopedOutTainted, n.Name)
			}
			continue
		}
		matches := n.Condition.Status == p.ConditionStatus
		if matches {
			matchedCount++
		}
		// A node contributes to the guard denominator only when we have a determinate signal for
		// it: it matches, or its condition is present and not Unknown. Unknown/unreported nodes
		// carry no signal, so counting them would dilute the guard exactly during a partial
		// detector outage. (matches is checked first, so a policy that triggers on Unknown still
		// counts its matched nodes.)
		if matches || (n.Condition.Status != statusUnknown && n.Condition.Status != "") {
			determinateCount++
		}
		selected = append(selected, evaluated{NodeFact: n, matches: matches})
	}

	// guard: trip when the matched fraction exceeds MaxAffectedPercent over the nodes with a
	// determinate condition, using integer math
	// (matched/determinate > percent/100  <=>  matched*100 > percent*determinate).
	guardTripped := determinateCount > 0 && matchedCount*100 > int(p.MaxAffectedPercent)*determinateCount

	d := Decision{Status: Status{GuardTripped: guardTripped, DeterminateCount: determinateCount}}
	d.RemoveTaint = append(d.RemoveTaint, scopedOutTainted...)
	var requeue time.Duration

	for _, n := range selected {
		d.Status.SelectedNodes = append(d.Status.SelectedNodes, n.Name)
		if n.HasTaint {
			// remediatedNodes reflects the taint actually observed on the node, not the
			// intent to apply it; it converges on the next reconcile (a taint change wakes
			// the controller via the TaintsChanged predicate).
			d.Status.RemediatedNodes = append(d.Status.RemediatedNodes, n.Name)
		}
		// stableFor is only meaningful when the condition has a lastTransitionTime; when it does
		// not, now.Sub(zero) saturates to a huge value, so the debounce branches below must guard
		// on noTimestamp first rather than trust stableFor.
		noTimestamp := n.Condition.LastTransition.IsZero()
		stableFor := now.Sub(n.Condition.LastTransition)

		switch {
		case n.matches:
			d.Status.MatchedNodes = append(d.Status.MatchedNodes, n.Name)
			if n.HasTaint {
				continue // already remediated
			}
			if noTimestamp {
				// No lastTransitionTime: we cannot tell how long the condition has held, so we
				// hold instead of bypassing the enter debounce and evicting immediately. This is
				// not a normal debounce wait, so it is not reported as pending; the shell surfaces
				// it as a warning instead.
				d.HeldMissingTimestamp = append(d.HeldMissingTimestamp, n.Name)
				continue
			}
			d.Status.PendingNodes = append(d.Status.PendingNodes, n.Name)
			if guardTripped {
				continue // mass event: the guard blocks applying new taints
			}
			if stableFor >= p.DebounceEnter {
				d.ApplyTaint = append(d.ApplyTaint, n.Name)
			} else {
				requeue = minPositive(requeue, p.DebounceEnter-stableFor)
			}
		case n.Condition.Status == statusUnknown || n.Condition.Status == "":
			// Indeterminate (condition Unknown or unreported): hold - never apply, never
			// remove; the node keeps its current taint. We do NOT assume recovery.
			d.Status.UnknownNodes = append(d.Status.UnknownNodes, n.Name)
		default:
			// Condition determinately cleared (present, not the trigger, not Unknown):
			// remove the taint after the exit debounce.
			if n.HasTaint {
				if noTimestamp {
					// No lastTransitionTime: keep the taint rather than removing it on a
					// saturated stableFor that would bypass the exit debounce.
					d.HeldMissingTimestamp = append(d.HeldMissingTimestamp, n.Name)
					continue
				}
				if stableFor >= p.DebounceExit {
					d.RemoveTaint = append(d.RemoveTaint, n.Name)
				} else {
					requeue = minPositive(requeue, p.DebounceExit-stableFor)
				}
			}
		}
	}

	d.RequeueAfter = requeue
	return d
}

// minPositive folds a new requeue candidate into the running minimum: cur is the smallest
// strictly-positive delay so far (0 = none yet). A non-positive candidate is not a valid requeue,
// so it is ignored rather than treated as the new minimum (which would cancel the requeue).
func minPositive(cur, next time.Duration) time.Duration {
	if next <= 0 {
		return cur
	}
	if cur == 0 || next < cur {
		return next
	}
	return cur
}
