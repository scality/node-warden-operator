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

package controller

import (
	"context"
	"maps"
	"slices"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	errors "github.com/scality/go-errors"

	wardenv1alpha1 "github.com/scality/node-warden-operator/api/v1alpha1"
	intpredicate "github.com/scality/node-warden-operator/internal/predicate"
	"github.com/scality/node-warden-operator/internal/remediation"
)

// NodeRemediationPolicyReconciler reconciles a NodeRemediationPolicy object
type NodeRemediationPolicyReconciler struct {
	client.Client
	// APIReader reads directly from the API server (uncached). Taint writes use it on a
	// conflict retry so they see the current resourceVersion, not a stale cached one.
	APIReader client.Reader
	Scheme    *runtime.Scheme
}

var (
	ErrProjectPolicy = errors.New("project NodeRemediationPolicy")
	ErrListNodes     = errors.New("list nodes")
	ErrWriteStatus   = errors.New("write NodeRemediationPolicy status")
)

// +kubebuilder:rbac:groups=warden.scality.com,resources=noderemediationpolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=warden.scality.com,resources=noderemediationpolicies/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=warden.scality.com,resources=noderemediationpolicies/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch;update;patch

// Reconcile runs the observe/decide/act loop for one NodeRemediationPolicy: it observes the
// selected nodes, delegates the decision to the pure core, enacts the taint changes and writes
// back the projected status.
func (r *NodeRemediationPolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (_ ctrl.Result, retErr error) {
	var policy wardenv1alpha1.NodeRemediationPolicy
	if err := r.Get(ctx, req.NamespacedName, &policy); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	original := policy.DeepCopy()
	defer func() {
		if equality.Semantic.DeepEqual(original.Status, policy.Status) {
			return
		}
		if err := client.IgnoreNotFound(r.Status().Patch(ctx, &policy, client.MergeFrom(original))); err != nil && retErr == nil {
			retErr = errors.Wrap(ErrWriteStatus, errors.WithProperty("policy", policy.Name), errors.CausedBy(err))
		}
	}()

	// OBSERVE
	pure, err := projectPolicy(&policy)
	if err != nil {
		policy.MarkInvalidSpec(err.Error())
		// Keep the last observed node lists: the spec can no longer be projected, so any taint
		// already applied cannot be reconciled or removed, and blanking the status would hide the
		// nodes still carrying it. The InvalidSpec condition explains why they are stuck.
		// Permanent configuration error: do not requeue; a spec change re-triggers reconcile.
		return ctrl.Result{}, nil
	}
	var nodeList corev1.NodeList
	if err := r.List(ctx, &nodeList); err != nil {
		return ctrl.Result{}, errors.Wrap(ErrListNodes, errors.CausedBy(err))
	}
	facts := observeNodes(nodeList.Items, pure)

	// DECIDE (pure)
	decision := remediation.Decide(pure, facts, time.Now())

	// ACT. A per-node taint error (node deleted mid-reconcile, conflict retries exhausted) must
	// not abort the other nodes: collect the errors, keep applying/removing on every other node,
	// and return an aggregate at the end so the reconcile still requeues. Otherwise one churning
	// node would strand a healthy node's taint (leaving it cordoned/evicting).
	var taintErrs []error
	for _, name := range decision.ApplyTaint {
		if _, err := EnsureTaint(ctx, r.Client, r.APIReader, name, pure.Taint); err != nil {
			taintErrs = append(taintErrs, err)
		}
	}
	for _, name := range decision.RemoveTaint {
		if _, err := RemoveTaint(ctx, r.Client, r.APIReader, name, pure.Taint.Key); err != nil {
			taintErrs = append(taintErrs, err)
		}
	}

	writeStatus(&policy, decision)

	// A taint op failed on some node(s): status is still written above (best effort) and the
	// aggregate error requeues so the failed nodes are retried.
	if err := utilerrors.NewAggregate(taintErrs); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: decision.RequeueAfter}, nil
}

// projectPolicy converts the CRD spec into the pure remediation.Policy. Field defaults are
// resolved by the spec's own ...OrDefault getters (which mirror the CRD defaults), so a nil
// selector or taint is the only thing defended here.
func projectPolicy(p *wardenv1alpha1.NodeRemediationPolicy) (remediation.Policy, error) {
	selector, err := nodeSelector(p)
	if err != nil {
		return remediation.Policy{}, errors.Wrap(ErrProjectPolicy,
			errors.WithProperty("policy", p.Name), errors.CausedBy(err))
	}
	var taint remediation.Taint
	if p.Spec.Remediations.Taint != nil {
		taint = taintFromSpec(p.Spec.Remediations.Taint)
	}
	return remediation.Policy{
		Selector:           selector,
		ConditionType:      p.Spec.Condition.Type,
		ConditionStatus:    p.Spec.Condition.StatusOrDefault(),
		Taint:              taint,
		DebounceEnter:      p.Spec.Debounce.EnterOrDefault(),
		DebounceExit:       p.Spec.Debounce.ExitOrDefault(),
		MaxAffectedPercent: p.Spec.Guard.MaxAffectedPercentOrDefault(),
	}, nil
}

// taintFromSpec maps the CRD taint (key/value/effect) to the pure remediation.Taint; timeAdded is
// ignored on apply. Shared by projectPolicy and the finalizer cleanup so the two cannot drift.
func taintFromSpec(t *corev1.Taint) remediation.Taint {
	return remediation.Taint{Key: t.Key, Value: t.Value, Effect: string(t.Effect)}
}

// observeNodes builds the read-only facts the pure core consumes, keeping only the one condition
// the policy watches (absent -> zero ConditionFact, which the core holds on).
func observeNodes(nodes []corev1.Node, p remediation.Policy) []remediation.NodeFact {
	facts := make([]remediation.NodeFact, 0, len(nodes))
	for i := range nodes {
		n := &nodes[i]
		var condition remediation.ConditionFact
		for _, c := range n.Status.Conditions {
			if string(c.Type) == p.ConditionType {
				condition = remediation.ConditionFact{
					Status:         string(c.Status),
					LastTransition: c.LastTransitionTime.Time,
				}
				break
			}
		}
		facts = append(facts, remediation.NodeFact{
			Name:      n.Name,
			Labels:    n.Labels,
			Condition: condition,
			HasTaint:  hasTaintKey(n, p.Taint.Key),
		})
	}
	return facts
}

// hasTaintKey reports whether the node carries a taint with the given key.
func hasTaintKey(node *corev1.Node, key string) bool {
	for _, t := range node.Spec.Taints {
		if t.Key == key {
			return true
		}
	}
	return false
}

// writeStatus projects the decision onto the policy status (sorted for stable output) and
// sets a single Remediating condition.
func writeStatus(p *wardenv1alpha1.NodeRemediationPolicy, d remediation.Decision) {
	// The decision is built fresh each reconcile and discarded after this call, so its slices are
	// sorted in place for stable output rather than copied first.
	slices.Sort(d.Status.SelectedNodes)
	slices.Sort(d.Status.MatchedNodes)
	slices.Sort(d.Status.PendingNodes)
	slices.Sort(d.Status.UnknownNodes)
	slices.Sort(d.HeldMissingTimestamp)
	slices.Sort(d.Status.RemediatedNodes)
	p.Status.SelectedNodes = d.Status.SelectedNodes
	p.Status.MatchedNodes = d.Status.MatchedNodes
	p.Status.PendingNodes = d.Status.PendingNodes
	p.Status.UnknownNodes = d.Status.UnknownNodes
	p.Status.HeldNodes = d.HeldMissingTimestamp
	p.Status.RemediatedNodes = d.Status.RemediatedNodes
	p.Status.SelectedCount = int32(len(d.Status.SelectedNodes))
	p.Status.MatchedCount = int32(len(d.Status.MatchedNodes))
	p.Status.PendingCount = int32(len(d.Status.PendingNodes))
	p.Status.RemediatedCount = int32(len(d.Status.RemediatedNodes))
	p.Status.UnknownCount = int32(len(d.Status.UnknownNodes))
	p.Status.HeldCount = int32(len(d.HeldMissingTimestamp))

	switch {
	case d.Status.GuardTripped:
		p.MarkGuardTripped(len(d.Status.RemediatedNodes))
	case len(d.Status.RemediatedNodes) > 0:
		p.MarkNodesRemediated(len(d.Status.RemediatedNodes))
	case len(d.Status.PendingNodes) > 0:
		p.MarkPending(len(d.Status.PendingNodes))
	case len(d.HeldMissingTimestamp) > 0 || len(d.Status.UnknownNodes) > 0:
		// Matched-but-held (no lastTransitionTime) or Unknown/unreported nodes are neither
		// remediated nor healthy; surface that instead of claiming no remediation is required.
		p.MarkHeld(len(d.HeldMissingTimestamp) + len(d.Status.UnknownNodes))
	default:
		p.MarkNoRemediation()
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *NodeRemediationPolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&wardenv1alpha1.NodeRemediationPolicy{}).
		Watches(&corev1.Node{},
			r.nodeEventHandler(),
			builder.WithPredicates(predicate.Or(
				intpredicate.ConditionStatusChanged(),
				intpredicate.TaintsChanged(),
				predicate.LabelChangedPredicate{},
			)),
		).
		Named("noderemediationpolicy").
		Complete(r)
}

// nodeEventHandler enqueues, for a Node event, only the policies actually affected by it. On an
// update it diffs old vs new: it enqueues a policy when the node's labels changed (its scope may
// differ) or when the specific condition type or taint key that policy cares about changed, so an
// unrelated condition or taint flipping on the node does not wake every policy. On create/delete
// there is no diff, so every policy the node selects is enqueued.
func (r *NodeRemediationPolicyReconciler) nodeEventHandler() handler.EventHandler {
	return handler.Funcs{
		CreateFunc: func(ctx context.Context, e event.CreateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			if node, ok := e.Object.(*corev1.Node); ok {
				r.enqueueSelectingPolicies(ctx, node, q)
			}
		},
		DeleteFunc: func(ctx context.Context, e event.DeleteEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			if node, ok := e.Object.(*corev1.Node); ok {
				r.enqueueSelectingPolicies(ctx, node, q)
			}
		},
		UpdateFunc: func(ctx context.Context, e event.UpdateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			oldNode, ok1 := e.ObjectOld.(*corev1.Node)
			newNode, ok2 := e.ObjectNew.(*corev1.Node)
			if !ok1 || !ok2 {
				return
			}
			changedConditions := intpredicate.ChangedConditionTypes(oldNode.Status.Conditions, newNode.Status.Conditions)
			changedTaints := intpredicate.ChangedTaintKeys(oldNode.Spec.Taints, newNode.Spec.Taints)
			labelsChanged := !maps.Equal(oldNode.Labels, newNode.Labels)
			r.enqueuePolicies(ctx, q, func(p *wardenv1alpha1.NodeRemediationPolicy) bool {
				sel, err := nodeSelector(p)
				if err != nil {
					return false
				}
				// A label change can move the node in or out of a policy's scope, so re-evaluate
				// every policy the node selects under either its old or its new labels.
				if labelsChanged {
					return sel.Matches(labels.Set(newNode.Labels)) || sel.Matches(labels.Set(oldNode.Labels))
				}
				if !sel.Matches(labels.Set(newNode.Labels)) {
					return false
				}
				if changedConditions[p.Spec.Condition.Type] {
					return true
				}
				return p.Spec.Remediations.Taint != nil && changedTaints[p.Spec.Remediations.Taint.Key]
			})
		},
	}
}

// enqueueSelectingPolicies enqueues every policy whose selector matches the node, used for
// create/delete events where there is no old/new diff to narrow the set down.
func (r *NodeRemediationPolicyReconciler) enqueueSelectingPolicies(ctx context.Context, node *corev1.Node, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	r.enqueuePolicies(ctx, q, func(p *wardenv1alpha1.NodeRemediationPolicy) bool {
		sel, err := nodeSelector(p)
		return err == nil && sel.Matches(labels.Set(node.Labels))
	})
}

// enqueuePolicies lists the policies and enqueues those for which want reports true.
func (r *NodeRemediationPolicyReconciler) enqueuePolicies(ctx context.Context, q workqueue.TypedRateLimitingInterface[reconcile.Request], want func(*wardenv1alpha1.NodeRemediationPolicy) bool) {
	var policies wardenv1alpha1.NodeRemediationPolicyList
	if err := r.List(ctx, &policies); err != nil {
		// A handler cannot requeue; log so a dropped Node event is at least traceable.
		ctrl.Log.WithName("noderemediationpolicy").Error(err, "listing policies to enqueue for a Node event")
		return
	}
	for i := range policies.Items {
		p := &policies.Items[i]
		if want(p) {
			q.Add(reconcile.Request{NamespacedName: client.ObjectKey{Name: p.Name}})
		}
	}
}

// nodeSelector parses the policy's nodeSelector once (a nil selector matches every node). Both
// projectPolicy (the decision scope) and the Node-watch enqueue path use it, so the two cannot
// drift, and the enqueue path parses each policy's selector once per event rather than per label.
func nodeSelector(p *wardenv1alpha1.NodeRemediationPolicy) (labels.Selector, error) {
	if p.Spec.NodeSelector == nil {
		return labels.Everything(), nil
	}
	return metav1.LabelSelectorAsSelector(p.Spec.NodeSelector)
}
