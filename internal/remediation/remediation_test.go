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

package remediation_test

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"k8s.io/apimachinery/pkg/labels"

	"github.com/scality/node-warden-operator/internal/remediation"
)

// now is the fixed reference time used across the table; conditions are expressed
// relative to it via the "since" duration passed to newNode.
var now = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

const (
	conditionType = "WPUnavailable"
	roleLabelKey  = "role"
	statusTrue    = "True"
	statusFalse   = "False"
	statusUnknown = "Unknown"
)

// newNode builds a NodeFact selected by role=wp. When condStatus is non-empty, it sets the
// watched condition whose LastTransition is now.Add(-since). When condStatus is empty, Condition
// is left at its zero value (the node does not report the condition).
func newNode(name, condStatus string, since time.Duration, hasTaint bool) remediation.NodeFact {
	n := remediation.NodeFact{
		Name:     name,
		Labels:   map[string]string{roleLabelKey: "wp"},
		HasTaint: hasTaint,
	}
	if condStatus != "" {
		n.Condition = remediation.ConditionFact{
			Status:         condStatus,
			LastTransition: now.Add(-since),
		}
	}
	return n
}

// newNodeNoTimestamp builds a role=wp node whose watched condition has the given status but no
// lastTransitionTime (some detectors omit it), to exercise the debounce fail-safe.
func newNodeNoTimestamp(name, condStatus string, hasTaint bool) remediation.NodeFact {
	return remediation.NodeFact{
		Name:      name,
		Labels:    map[string]string{roleLabelKey: "wp"},
		Condition: remediation.ConditionFact{Status: condStatus}, // LastTransition left zero
		HasTaint:  hasTaint,
	}
}

// basePolicy is the default policy shared by all table entries; entries that need a
// different guard, selector, etc. mutate the returned value.
func basePolicy() remediation.Policy {
	return remediation.Policy{
		Selector:        labels.Everything(),
		ConditionType:   conditionType,
		ConditionStatus: statusTrue,
		Taint: remediation.Taint{
			Key:    "node.scality.io/wp-unreachable",
			Effect: "NoExecute",
		},
		DebounceEnter:      60 * time.Second,
		DebounceExit:       30 * time.Second,
		MaxAffectedPercent: 100,
	}
}

// decideCase is one DescribeTable row: a policy, a set of node facts, and the
// expectations to assert on the resulting Decision.
type decideCase struct {
	policy remediation.Policy
	nodes  []remediation.NodeFact

	expectApply   []string // nil/empty -> Decision.ApplyTaint must be empty
	expectRemove  []string // nil/empty -> Decision.RemoveTaint must be empty
	expectHeld    []string // nil/empty -> Decision.HeldMissingTimestamp must be empty
	expectGuard   bool
	expectRequeue bool // true -> RequeueAfter > 0; false -> RequeueAfter == 0

	// Node sets: set on every row. empty -> the set must be empty.
	expectSelected []string // selected nodes (pass the selector)
	expectPending  []string // matched nodes not yet remediated (match && !HasTaint)
	expectUnknown  []string // selected nodes held: condition Unknown/absent and not matching

	// Optional extra assertions used by specific rows.
	expectRemediated   []string       // when non-nil, asserted against Status.RemediatedNodes
	expectMatched      []string       // when non-nil, asserted against Status.MatchedNodes
	expectMatchedNone  bool           // when true, Status.MatchedNodes must be empty
	expectRequeueValue *time.Duration // when non-nil, RequeueAfter must equal exactly this (overrides expectRequeue)
	expectDeterminate  *int           // when non-nil, asserted against Status.DeterminateCount (the guard denominator)
}

func dur(d time.Duration) *time.Duration { return &d }

func countPtr(i int) *int { return &i }

var _ = Describe("Decide", func() {
	DescribeTable("remediation decisions",
		func(c decideCase) {
			d := remediation.Decide(c.policy, c.nodes, now)

			if len(c.expectApply) == 0 {
				Expect(d.ApplyTaint).To(BeEmpty())
			} else {
				Expect(d.ApplyTaint).To(ConsistOf(c.expectApply))
			}

			if len(c.expectRemove) == 0 {
				Expect(d.RemoveTaint).To(BeEmpty())
			} else {
				Expect(d.RemoveTaint).To(ConsistOf(c.expectRemove))
			}

			if len(c.expectHeld) == 0 {
				Expect(d.HeldMissingTimestamp).To(BeEmpty())
			} else {
				Expect(d.HeldMissingTimestamp).To(ConsistOf(c.expectHeld))
			}

			Expect(d.Status.GuardTripped).To(Equal(c.expectGuard))

			if len(c.expectSelected) == 0 {
				Expect(d.Status.SelectedNodes).To(BeEmpty())
			} else {
				Expect(d.Status.SelectedNodes).To(ConsistOf(c.expectSelected))
			}

			if len(c.expectPending) == 0 {
				Expect(d.Status.PendingNodes).To(BeEmpty())
			} else {
				Expect(d.Status.PendingNodes).To(ConsistOf(c.expectPending))
			}

			if len(c.expectUnknown) == 0 {
				Expect(d.Status.UnknownNodes).To(BeEmpty())
			} else {
				Expect(d.Status.UnknownNodes).To(ConsistOf(c.expectUnknown))
			}

			switch {
			case c.expectRequeueValue != nil:
				Expect(d.RequeueAfter).To(Equal(*c.expectRequeueValue))
			case c.expectRequeue:
				Expect(d.RequeueAfter).To(BeNumerically(">", 0))
			default:
				Expect(d.RequeueAfter).To(Equal(time.Duration(0)))
			}

			if c.expectRemediated != nil {
				Expect(d.Status.RemediatedNodes).To(ConsistOf(c.expectRemediated))
			}

			if c.expectMatched != nil {
				Expect(d.Status.MatchedNodes).To(ConsistOf(c.expectMatched))
			}

			if c.expectMatchedNone {
				Expect(d.Status.MatchedNodes).To(BeEmpty())
			}

			if c.expectDeterminate != nil {
				Expect(d.Status.DeterminateCount).To(Equal(*c.expectDeterminate))
			}
		},

		// remediatedNodes is purely observational: it is exactly the SELECTED nodes whose
		// HasTaint is true at input, regardless of the apply/remove intent. A node about to be
		// tainted (HasTaint=false) is not remediated yet; a node about to lose its taint
		// (HasTaint=true) is still remediated until the removal lands and the next reconcile
		// (woken by the TaintsChanged predicate) re-observes it.

		Entry("matching, stable past enter debounce, untainted -> apply taint", decideCase{
			policy:           basePolicy(),
			nodes:            []remediation.NodeFact{newNode("n1", statusTrue, 90*time.Second, false)},
			expectApply:      []string{"n1"},
			expectGuard:      false,
			expectRequeue:    false,
			expectSelected:   []string{"n1"},
			expectPending:    []string{"n1"},
			expectMatched:    []string{"n1"},
			expectRemediated: []string{},
		}),

		Entry("matching, stable below enter debounce, untainted -> pending, requeue", decideCase{
			policy:           basePolicy(),
			nodes:            []remediation.NodeFact{newNode("n1", statusTrue, 10*time.Second, false)},
			expectGuard:      false,
			expectRequeue:    true,
			expectSelected:   []string{"n1"},
			expectPending:    []string{"n1"},
			expectMatched:    []string{"n1"},
			expectRemediated: []string{},
		}),

		Entry("matching, already tainted -> steady state, no-op", decideCase{
			policy:           basePolicy(),
			nodes:            []remediation.NodeFact{newNode("n1", statusTrue, 5*time.Minute, true)},
			expectGuard:      false,
			expectRequeue:    false,
			expectSelected:   []string{"n1"},
			expectMatched:    []string{"n1"},
			expectRemediated: []string{"n1"},
		}),

		Entry("cleared, stable past exit debounce, tainted -> remove taint", decideCase{
			policy:           basePolicy(),
			nodes:            []remediation.NodeFact{newNode("n1", statusFalse, 60*time.Second, true)},
			expectRemove:     []string{"n1"},
			expectGuard:      false,
			expectRequeue:    false,
			expectSelected:   []string{"n1"},
			expectRemediated: []string{"n1"},
		}),

		Entry("cleared, stable below exit debounce, tainted -> pending removal, requeue", decideCase{
			policy:           basePolicy(),
			nodes:            []remediation.NodeFact{newNode("n1", statusFalse, 5*time.Second, true)},
			expectGuard:      false,
			expectRequeue:    true,
			expectSelected:   []string{"n1"},
			expectRemediated: []string{"n1"},
		}),

		Entry("unknown status, untainted -> hold, no-op", decideCase{
			policy:           basePolicy(),
			nodes:            []remediation.NodeFact{newNode("n1", statusUnknown, 5*time.Minute, false)},
			expectGuard:      false,
			expectRequeue:    false,
			expectSelected:   []string{"n1"},
			expectUnknown:    []string{"n1"},
			expectRemediated: []string{},
		}),

		// Unknown must never un-remediate: the detector is down or the node is unreachable,
		// so the taint is HELD regardless of the exit debounce (no remove, no requeue).
		Entry("unknown status, tainted, 1min -> hold (no remove)", decideCase{
			policy:           basePolicy(),
			nodes:            []remediation.NodeFact{newNode("n1", statusUnknown, 60*time.Second, true)},
			expectGuard:      false,
			expectRequeue:    false,
			expectSelected:   []string{"n1"},
			expectUnknown:    []string{"n1"},
			expectRemediated: []string{"n1"},
		}),

		Entry("guard exceeded -> blocks new taints", decideCase{
			policy: func() remediation.Policy {
				p := basePolicy()
				p.MaxAffectedPercent = 50
				return p
			}(),
			nodes: []remediation.NodeFact{
				newNode("n1", statusTrue, 5*time.Minute, false),
				newNode("n2", statusTrue, 5*time.Minute, false),
				newNode("n3", statusFalse, 5*time.Minute, false),
			},
			expectGuard:      true,
			expectRequeue:    false,
			expectSelected:   []string{"n1", "n2", "n3"},
			expectPending:    []string{"n1", "n2"},
			expectMatched:    []string{"n1", "n2"},
			expectRemediated: []string{},
		}),

		Entry("guard tripped: 2 of 2 matching at 50% -> taint neither", decideCase{
			policy: func() remediation.Policy {
				p := basePolicy()
				p.MaxAffectedPercent = 50
				return p
			}(),
			nodes: []remediation.NodeFact{
				newNode("n1", statusTrue, 5*time.Minute, false),
				newNode("n2", statusTrue, 5*time.Minute, false),
			},
			expectGuard:      true,
			expectRequeue:    false,
			expectSelected:   []string{"n1", "n2"},
			expectPending:    []string{"n1", "n2"},
			expectMatched:    []string{"n1", "n2"},
			expectRemediated: []string{},
		}),

		Entry("guard within budget -> allows taint", decideCase{
			policy: func() remediation.Policy {
				p := basePolicy()
				p.MaxAffectedPercent = 50
				return p
			}(),
			nodes: []remediation.NodeFact{
				newNode("n1", statusTrue, 5*time.Minute, false),
				newNode("n2", statusFalse, 5*time.Minute, false),
				newNode("n3", statusFalse, 5*time.Minute, false),
			},
			expectApply:      []string{"n1"},
			expectGuard:      false,
			expectRequeue:    false,
			expectSelected:   []string{"n1", "n2", "n3"},
			expectPending:    []string{"n1"},
			expectMatched:    []string{"n1"},
			expectRemediated: []string{},
		}),

		// Guard denominator counts only nodes with a determinate condition: Unknown/unreported
		// nodes carry no signal and must not dilute it during a partial detector outage.
		Entry("guard denominator excludes Unknown nodes", decideCase{
			policy: func() remediation.Policy {
				p := basePolicy()
				p.MaxAffectedPercent = 50
				return p
			}(),
			nodes: []remediation.NodeFact{
				newNode("n1", statusTrue, 5*time.Minute, false),    // matched
				newNode("n2", statusTrue, 5*time.Minute, false),    // matched
				newNode("n3", statusUnknown, 5*time.Minute, false), // no signal -> out of the denominator
				newNode("n4", statusFalse, 5*time.Minute, false),   // determinately healthy
			},
			// 2 matched of 3 determinate (n1,n2,n4) = 67% > 50% -> tripped; 2 of 4 selected would be
			// exactly 50% and NOT trip if the Unknown node diluted the denominator.
			expectGuard:       true,
			expectRequeue:     false,
			expectSelected:    []string{"n1", "n2", "n3", "n4"},
			expectMatched:     []string{"n1", "n2"},
			expectPending:     []string{"n1", "n2"},
			expectUnknown:     []string{"n3"},
			expectRemediated:  []string{},
			expectDeterminate: countPtr(3), // n1,n2,n4 -- the Unknown n3 is excluded
		}),

		// Boundary: the guard is strict (matched*100 > percent*determinate), so a fraction exactly
		// at the limit must NOT trip. 1 matched of 2 determinate at 50% is exactly 50% -> apply.
		Entry("guard exactly at the limit -> does not trip", decideCase{
			policy: func() remediation.Policy {
				p := basePolicy()
				p.MaxAffectedPercent = 50
				return p
			}(),
			nodes: []remediation.NodeFact{
				newNode("n1", statusTrue, 5*time.Minute, false),  // matched
				newNode("n2", statusFalse, 5*time.Minute, false), // determinately healthy
			},
			expectApply:       []string{"n1"},
			expectGuard:       false,
			expectRequeue:     false,
			expectSelected:    []string{"n1", "n2"},
			expectPending:     []string{"n1"},
			expectMatched:     []string{"n1"},
			expectRemediated:  []string{},
			expectDeterminate: countPtr(2),
		}),

		// A policy can watch for the Unknown status itself (e.g. "detector lost the node"). Such a
		// node matches and is remediated normally -- it must land in MatchedNodes, not UnknownNodes,
		// and count toward the guard denominator.
		Entry("policy triggers on Unknown -> matched and applied, not held", decideCase{
			policy: func() remediation.Policy {
				p := basePolicy()
				p.ConditionStatus = statusUnknown
				return p
			}(),
			nodes: []remediation.NodeFact{
				newNode("n1", statusUnknown, 90*time.Second, false),
			},
			expectApply:       []string{"n1"},
			expectGuard:       false,
			expectRequeue:     false,
			expectSelected:    []string{"n1"},
			expectPending:     []string{"n1"},
			expectMatched:     []string{"n1"},
			expectUnknown:     []string{},
			expectRemediated:  []string{},
			expectDeterminate: countPtr(1),
		}),

		Entry("selector scopes node out of policy -> no match", decideCase{
			policy: func() remediation.Policy {
				p := basePolicy()
				p.Selector = labels.SelectorFromSet(labels.Set{roleLabelKey: "wp"})
				return p
			}(),
			nodes: []remediation.NodeFact{
				{
					Name:   "n1",
					Labels: map[string]string{roleLabelKey: "cp"},
					Condition: remediation.ConditionFact{
						Status:         statusTrue,
						LastTransition: now.Add(-5 * time.Minute),
					},
					HasTaint: false,
				},
			},
			expectGuard:       false,
			expectRequeue:     false,
			expectSelected:    []string{},
			expectPending:     []string{},
			expectMatchedNone: true,
			expectRemediated:  []string{},
		}),

		Entry("condition absent, tainted -> hold (not a recovery)", decideCase{
			policy:           basePolicy(),
			nodes:            []remediation.NodeFact{newNode("n1", "", 0, true)},
			expectGuard:      false,
			expectRequeue:    false,
			expectSelected:   []string{"n1"},
			expectUnknown:    []string{"n1"},
			expectRemediated: []string{"n1"},
		}),

		// Debounce fail-safe: with no lastTransitionTime we cannot measure how long the condition
		// has held, so we must NOT apply (a saturated stableFor would evict on the first pass).
		// The node is held, not pending: it is not a normal debounce wait, so it stays out of
		// PendingNodes (the shell surfaces it as a warning instead).
		Entry("matching, no lastTransitionTime, untainted -> hold, not pending, no apply, no requeue", decideCase{
			policy:           basePolicy(),
			nodes:            []remediation.NodeFact{newNodeNoTimestamp("n1", statusTrue, false)},
			expectGuard:      false,
			expectRequeue:    false,
			expectSelected:   []string{"n1"},
			expectMatched:    []string{"n1"},
			expectPending:    []string{},
			expectHeld:       []string{"n1"},
			expectRemediated: []string{},
		}),

		// Symmetric fail-safe on exit: no lastTransitionTime -> keep the taint, do not remove it.
		Entry("cleared, tainted, no lastTransitionTime -> hold, keep taint", decideCase{
			policy:           basePolicy(),
			nodes:            []remediation.NodeFact{newNodeNoTimestamp("n1", statusFalse, true)},
			expectGuard:      false,
			expectRequeue:    false,
			expectSelected:   []string{"n1"},
			expectHeld:       []string{"n1"},
			expectRemediated: []string{"n1"},
		}),

		// Flap regression: a matching, already-tainted node inside the enter-debounce
		// window is already remediated. It must NOT be dropped from RemediatedNodes and
		// must NOT emit a requeue (the bug produced a pointless requeue and lost status).
		Entry("matching, tainted, inside enter window -> stays remediated, no requeue", decideCase{
			policy:           basePolicy(),
			nodes:            []remediation.NodeFact{newNode("n1", statusTrue, 20*time.Second, true)},
			expectGuard:      false,
			expectRequeue:    false,
			expectSelected:   []string{"n1"},
			expectMatched:    []string{"n1"},
			expectRemediated: []string{"n1"},
		}),

		// Requeue value: two pending windows, the soonest wins. Enter-pending n1 has a
		// 40s window (60s enter - 20s stable); exit-pending n2 has a 25s window (30s exit
		// - 5s cleared). RequeueAfter must be exactly 25s.
		Entry("two pending nodes -> requeue equals the soonest window", decideCase{
			policy: basePolicy(),
			nodes: []remediation.NodeFact{
				newNode("n1", statusTrue, 20*time.Second, false),
				newNode("n2", statusFalse, 5*time.Second, true),
			},
			expectGuard:        false,
			expectRequeueValue: dur(25 * time.Second),
			expectSelected:     []string{"n1", "n2"},
			expectPending:      []string{"n1"},
			expectMatched:      []string{"n1"},
			expectRemediated:   []string{"n2"},
		}),

		// Guard tripped with mixed taints: new taints are blocked, but a recovered node
		// (non-matching + tainted) is still removed, and matching+tainted nodes stay
		// remediated. remediatedNodes observes every selected tainted node (n1 and n3),
		// including the one whose taint is about to be removed.
		Entry("guard tripped, mixed taints -> block apply, still remove recovered", decideCase{
			policy: func() remediation.Policy {
				p := basePolicy()
				p.MaxAffectedPercent = 50
				return p
			}(),
			nodes: []remediation.NodeFact{
				newNode("n1", statusTrue, 5*time.Minute, true),
				newNode("n2", statusTrue, 5*time.Minute, false),
				newNode("n3", statusFalse, 5*time.Minute, true),
			},
			expectRemove:     []string{"n3"},
			expectGuard:      true,
			expectRequeue:    false,
			expectSelected:   []string{"n1", "n2", "n3"},
			expectPending:    []string{"n2"},
			expectMatched:    []string{"n1", "n2"},
			expectRemediated: []string{"n1", "n3"},
		}),

		Entry("matching, untainted, stable exactly at enter boundary -> apply taint", decideCase{
			policy:           basePolicy(),
			nodes:            []remediation.NodeFact{newNode("n1", statusTrue, 60*time.Second, false)},
			expectApply:      []string{"n1"},
			expectGuard:      false,
			expectRequeue:    false,
			expectSelected:   []string{"n1"},
			expectPending:    []string{"n1"},
			expectMatched:    []string{"n1"},
			expectRemediated: []string{},
		}),

		Entry("cleared, tainted, cleared exactly at exit boundary -> remove taint", decideCase{
			policy:           basePolicy(),
			nodes:            []remediation.NodeFact{newNode("n1", statusFalse, 30*time.Second, true)},
			expectRemove:     []string{"n1"},
			expectGuard:      false,
			expectRequeue:    false,
			expectSelected:   []string{"n1"},
			expectRemediated: []string{"n1"},
		}),

		Entry("MaxAffectedPercent=100 -> all matching nodes applied, guard off", decideCase{
			policy: basePolicy(),
			nodes: []remediation.NodeFact{
				newNode("n1", statusTrue, 5*time.Minute, false),
				newNode("n2", statusTrue, 5*time.Minute, false),
			},
			expectApply:      []string{"n1", "n2"},
			expectGuard:      false,
			expectRequeue:    false,
			expectSelected:   []string{"n1", "n2"},
			expectPending:    []string{"n1", "n2"},
			expectMatched:    []string{"n1", "n2"},
			expectRemediated: []string{},
		}),

		Entry("MaxAffectedPercent=0 -> guard trips on any matching node", decideCase{
			policy: func() remediation.Policy {
				p := basePolicy()
				p.MaxAffectedPercent = 0
				return p
			}(),
			nodes:            []remediation.NodeFact{newNode("n1", statusTrue, 5*time.Minute, false)},
			expectGuard:      true,
			expectRequeue:    false,
			expectSelected:   []string{"n1"},
			expectPending:    []string{"n1"},
			expectMatched:    []string{"n1"},
			expectRemediated: []string{},
		}),

		// A node relabeled out of scope while still carrying our taint must be cleaned up, or it
		// stays tainted (and unusable) forever. It is not selected, so it appears in no status list.
		Entry("scoped-out node still carrying our taint -> remove it (cleanup)", decideCase{
			policy: func() remediation.Policy {
				p := basePolicy()
				p.Selector = labels.SelectorFromSet(labels.Set{roleLabelKey: "wp"})
				return p
			}(),
			nodes: []remediation.NodeFact{
				{
					Name:   "n1",
					Labels: map[string]string{roleLabelKey: "cp"},
					Condition: remediation.ConditionFact{
						Status:         statusTrue,
						LastTransition: now.Add(-5 * time.Minute),
					},
					HasTaint: true,
				},
			},
			expectRemove:      []string{"n1"},
			expectGuard:       false,
			expectRequeue:     false,
			expectSelected:    []string{},
			expectPending:     []string{},
			expectMatchedNone: true,
			expectRemediated:  []string{},
		}),

		Entry("non-matching, untainted -> no-op, no requeue", decideCase{
			policy:           basePolicy(),
			nodes:            []remediation.NodeFact{newNode("n1", statusFalse, 5*time.Minute, false)},
			expectGuard:      false,
			expectRequeue:    false,
			expectSelected:   []string{"n1"},
			expectRemediated: []string{},
		}),
	)
})
