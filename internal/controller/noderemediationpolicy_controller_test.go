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
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	wardenv1alpha1 "github.com/scality/node-warden-operator/api/v1alpha1"
	"github.com/scality/node-warden-operator/internal/remediation"
)

const (
	testConditionType   = "WPUnavailable"
	testConditionStatus = "True"
	testTaintKey        = "node.scality.io/wp-unreachable"
	testTaintEffect     = "NoExecute"
	testLabelValue      = "yes"
)

// findTaint returns the remediation taint (testTaintKey) and whether it was present. It is used
// instead of asserting on the full taint list because envtest's TaintNodesByCondition
// admission plugin auto-adds a node.kubernetes.io/not-ready taint to fresh Nodes.
func findTaint(node *corev1.Node) (corev1.Taint, bool) {
	for _, t := range node.Spec.Taints {
		if t.Key == testTaintKey {
			return t, true
		}
	}
	return corev1.Taint{}, false
}

// invalidNodeSelector returns a selector that passes the CRD's structural validation but fails
// metav1.LabelSelectorAsSelector (an unknown operator), used to exercise the invalid-spec paths.
func invalidNodeSelector() *metav1.LabelSelector {
	return &metav1.LabelSelector{
		MatchExpressions: []metav1.LabelSelectorRequirement{{Key: "example.com/role", Operator: "Foo"}},
	}
}

// drainQueue removes every request from the queue and returns the policy names, so a handler
// spec can assert exactly which policies a Node event enqueued.
func drainQueue(q workqueue.TypedRateLimitingInterface[reconcile.Request]) []string {
	var names []string
	for q.Len() > 0 {
		item, _ := q.Get()
		names = append(names, item.Name)
		q.Done(item)
	}
	return names
}

// drainEvents non-blockingly collects every event the fake recorder holds, so a spec can assert
// on what was recorded. Each entry is formatted "<type> <reason> <note>".
func drainEvents(rec *events.FakeRecorder) []string {
	var got []string
	for {
		select {
		case e := <-rec.Events:
			got = append(got, e)
		default:
			return got
		}
	}
}

var _ = Describe("NodeRemediationPolicy Controller", func() {
	var reconciler *NodeRemediationPolicyReconciler
	var recorder *events.FakeRecorder

	BeforeEach(func() {
		recorder = events.NewFakeRecorder(64)
		reconciler = &NodeRemediationPolicyReconciler{
			Client:    k8sClient,
			APIReader: k8sClient,
			Scheme:    k8sClient.Scheme(),
			Recorder:  recorder,
		}
	})

	// createNode creates a Node carrying labelKey=labelVal and, via the status subresource, a
	// single WPUnavailable condition with the given status and last-transition time.
	createNode := func(name, labelKey, labelVal string, status corev1.ConditionStatus, transition time.Time) *corev1.Node {
		node := &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name:   name,
				Labels: map[string]string{labelKey: labelVal},
			},
		}
		Expect(k8sClient.Create(ctx, node)).To(Succeed())
		DeferCleanup(func() {
			_ = k8sClient.Delete(ctx, node)
		})

		node.Status.Conditions = []corev1.NodeCondition{{
			Type:               corev1.NodeConditionType(testConditionType),
			Status:             status,
			LastTransitionTime: metav1.NewTime(transition),
		}}
		Expect(k8sClient.Status().Update(ctx, node)).To(Succeed())
		return node
	}

	// createPolicy creates a NodeRemediationPolicy selecting labelKey=yes. Debounce is set
	// explicitly (60s enter / 30s exit) so the timing is deterministic and independent of the
	// CRD defaults; the specs drive elapsed time via each node condition's LastTransitionTime.
	createPolicy := func(name, labelKey string) {
		policy := &wardenv1alpha1.NodeRemediationPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: wardenv1alpha1.NodeRemediationPolicySpec{
				NodeSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{labelKey: testLabelValue},
				},
				Condition: wardenv1alpha1.ConditionMatch{Type: testConditionType, Status: testConditionStatus},
				Remediations: wardenv1alpha1.Remediations{
					Taint: &corev1.Taint{Key: testTaintKey, Effect: corev1.TaintEffectNoExecute},
				},
				Debounce: wardenv1alpha1.Debounce{
					Enter: &metav1.Duration{Duration: 60 * time.Second},
					Exit:  &metav1.Duration{Duration: 30 * time.Second},
				},
			},
		}
		Expect(k8sClient.Create(ctx, policy)).To(Succeed())
		DeferCleanup(func() {
			_ = k8sClient.Delete(ctx, policy)
		})
	}

	reconcilePolicy := func(name string) reconcile.Result {
		res, err := reconciler.Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: name},
		})
		Expect(err).NotTo(HaveOccurred())
		return res
	}

	// reconcileUntilStable reconciles a few times so the observational status converges
	// (apply/remove happen on one pass; remediatedNodes reflects the taint on the next).
	reconcileUntilStable := func(name string) {
		for range 3 {
			reconcilePolicy(name)
		}
	}

	// newFailingReconciler returns a reconciler whose Client fails every Node taint patch for
	// failNode (simulating a node that cannot be written -- deleted mid-reconcile, retries
	// exhausted) while every other write succeeds. APIReader stays the real uncached client, and
	// status/finalizer writes (subresource patch / update) are not intercepted, so they still land.
	newFailingReconciler := func(failNode string) *NodeRemediationPolicyReconciler {
		base, err := client.NewWithWatch(cfg, client.Options{Scheme: k8sClient.Scheme()})
		Expect(err).NotTo(HaveOccurred())
		failing := interceptor.NewClient(base, interceptor.Funcs{
			Patch: func(fctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				if obj.GetName() == failNode {
					return fmt.Errorf("simulated write failure on %s", failNode)
				}
				return c.Patch(fctx, obj, patch, opts...)
			},
		})
		return &NodeRemediationPolicyReconciler{
			Client:    failing,
			APIReader: k8sClient,
			Scheme:    k8sClient.Scheme(),
			Recorder:  recorder,
		}
	}

	It("applies the taint when the condition holds past the enter debounce", func() {
		const nodeName, policyName, labelKey = "node-apply", "policy-apply", "test/apply"

		createNode(nodeName, labelKey, testLabelValue, corev1.ConditionTrue, time.Now().Add(-2*time.Minute))
		createPolicy(policyName, labelKey)

		By("marking the node pending on the first pass, before the taint is observed")
		reconcilePolicy(policyName)
		var pending wardenv1alpha1.NodeRemediationPolicy
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: policyName}, &pending)).To(Succeed())
		Expect(pending.Status.SelectedNodes).To(ContainElement(nodeName))
		Expect(pending.Status.PendingNodes).To(ContainElement(nodeName))
		Expect(pending.Status.RemediatedNodes).NotTo(ContainElement(nodeName))

		reconcileUntilStable(policyName)

		By("adding our taint with NoExecute effect")
		var node corev1.Node
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nodeName}, &node)).To(Succeed())
		taint, ok := findTaint(&node)
		Expect(ok).To(BeTrue(), "expected node to carry the remediation taint")
		Expect(taint.Effect).To(Equal(corev1.TaintEffectNoExecute))

		By("reflecting the node in the policy status")
		var policy wardenv1alpha1.NodeRemediationPolicy
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: policyName}, &policy)).To(Succeed())
		Expect(policy.Status.SelectedNodes).To(ContainElement(nodeName))
		Expect(policy.Status.MatchedNodes).To(ContainElement(nodeName))
		Expect(policy.Status.RemediatedNodes).To(ContainElement(nodeName))

		By("clearing pendingNodes once the node is remediated")
		Expect(policy.Status.PendingNodes).To(BeEmpty())

		cond := apimeta.FindStatusCondition(policy.Status.Conditions, wardenv1alpha1.ConditionRemediating)
		Expect(cond).NotTo(BeNil())
		Expect(cond.Status).To(Equal(metav1.ConditionTrue))
		Expect(cond.ObservedGeneration).To(Equal(policy.Generation))

		By("recording a TaintApplied event naming the node")
		Expect(drainEvents(recorder)).To(ContainElement(SatisfyAll(
			ContainSubstring(reasonTaintApplied),
			ContainSubstring(nodeName),
		)))
	})

	It("enqueues only the policies whose watched condition actually changed", func() {
		const labelKey = "handlertest"
		mkPolicy := func(name, condType string) {
			policy := &wardenv1alpha1.NodeRemediationPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: name},
				Spec: wardenv1alpha1.NodeRemediationPolicySpec{
					NodeSelector: &metav1.LabelSelector{MatchLabels: map[string]string{labelKey: testLabelValue}},
					Condition:    wardenv1alpha1.ConditionMatch{Type: condType, Status: testConditionStatus},
					Remediations: wardenv1alpha1.Remediations{Taint: &corev1.Taint{Key: testTaintKey, Effect: corev1.TaintEffectNoExecute}},
				},
			}
			Expect(k8sClient.Create(ctx, policy)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, policy) })
		}
		mkPolicy("handler-wp", testConditionType)
		mkPolicy("handler-disk", string(corev1.NodeDiskPressure))

		nodeLabels := map[string]string{labelKey: testLabelValue}
		oldNode := &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: "handler-node", Labels: nodeLabels},
			Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{
				{Type: corev1.NodeConditionType(testConditionType), Status: corev1.ConditionTrue},
				{Type: corev1.NodeDiskPressure, Status: corev1.ConditionFalse},
			}},
		}
		newNode := oldNode.DeepCopy()
		newNode.Status.Conditions[1].Status = corev1.ConditionTrue // only DiskPressure flips

		queue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[reconcile.Request]())
		reconciler.nodeEventHandler().Update(ctx, event.UpdateEvent{ObjectOld: oldNode, ObjectNew: newNode}, queue)

		enqueued := drainQueue(queue)
		Expect(enqueued).To(ContainElement("handler-disk"))
		Expect(enqueued).NotTo(ContainElement("handler-wp"))
	})

	It("defaults the debounce windows to 60s/30s when omitted", func() {
		policy := &wardenv1alpha1.NodeRemediationPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "policy-debounce-default"},
			Spec: wardenv1alpha1.NodeRemediationPolicySpec{
				Condition: wardenv1alpha1.ConditionMatch{Type: testConditionType, Status: testConditionStatus},
				Remediations: wardenv1alpha1.Remediations{
					Taint: &corev1.Taint{Key: testTaintKey, Effect: corev1.TaintEffectNoExecute},
				},
			},
		}
		Expect(k8sClient.Create(ctx, policy)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, policy) })

		var got wardenv1alpha1.NodeRemediationPolicy
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: policy.Name}, &got)).To(Succeed())
		Expect(got.Spec.Debounce.Enter).NotTo(BeNil())
		Expect(got.Spec.Debounce.Enter.Duration).To(Equal(60 * time.Second))
		Expect(got.Spec.Debounce.Exit).NotTo(BeNil())
		Expect(got.Spec.Debounce.Exit.Duration).To(Equal(30 * time.Second))
	})

	It("removes the taint when the condition clears past the exit debounce", func() {
		const nodeName, policyName, labelKey = "node-remove", "policy-remove", "test/remove"

		node := createNode(nodeName, labelKey, testLabelValue, corev1.ConditionFalse, time.Now().Add(-2*time.Minute))
		createPolicy(policyName, labelKey)

		By("seeding the node with the remediation taint")
		_, seedErr := EnsureTaint(ctx, k8sClient, k8sClient, nodeName, remediation.Taint{Key: testTaintKey, Effect: testTaintEffect})
		Expect(seedErr).NotTo(HaveOccurred())
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nodeName}, node)).To(Succeed())
		_, ok := findTaint(node)
		Expect(ok).To(BeTrue(), "precondition: node should start tainted")

		reconcileUntilStable(policyName)

		By("removing our taint")
		var got corev1.Node
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nodeName}, &got)).To(Succeed())
		_, ok = findTaint(&got)
		Expect(ok).To(BeFalse(), "expected the remediation taint to be removed")

		By("dropping the node from the policy status and reporting NoRemediation")
		var policy wardenv1alpha1.NodeRemediationPolicy
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: policyName}, &policy)).To(Succeed())
		Expect(policy.Status.RemediatedNodes).NotTo(ContainElement(nodeName))

		cond := apimeta.FindStatusCondition(policy.Status.Conditions, wardenv1alpha1.ConditionRemediating)
		Expect(cond).NotTo(BeNil())
		Expect(cond.Status).To(Equal(metav1.ConditionFalse))
		Expect(cond.Reason).To(Equal(wardenv1alpha1.ReasonNoRemediation))
	})

	It("holds the taint when the condition goes Unknown instead of removing it", func() {
		const nodeName, policyName, labelKey = "node-unknown", "policy-unknown", "test/unknown"

		createNode(nodeName, labelKey, testLabelValue, corev1.ConditionUnknown, time.Now().Add(-2*time.Minute))
		createPolicy(policyName, labelKey)

		By("seeding the node with the remediation taint")
		_, seedErr := EnsureTaint(ctx, k8sClient, k8sClient, nodeName, remediation.Taint{Key: testTaintKey, Effect: testTaintEffect})
		Expect(seedErr).NotTo(HaveOccurred())
		var seeded corev1.Node
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nodeName}, &seeded)).To(Succeed())
		_, ok := findTaint(&seeded)
		Expect(ok).To(BeTrue(), "precondition: node should start tainted")

		reconcileUntilStable(policyName)

		By("keeping the taint present (held, not removed)")
		var got corev1.Node
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nodeName}, &got)).To(Succeed())
		_, ok = findTaint(&got)
		Expect(ok).To(BeTrue(), "expected the remediation taint to be held while the condition is Unknown")

		By("listing the node as unknown and still remediated")
		var policy wardenv1alpha1.NodeRemediationPolicy
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: policyName}, &policy)).To(Succeed())
		Expect(policy.Status.UnknownNodes).To(ContainElement(nodeName))
		Expect(policy.Status.RemediatedNodes).To(ContainElement(nodeName))
		Expect(policy.Status.MatchedNodes).NotTo(ContainElement(nodeName))
		Expect(policy.Status.PendingNodes).NotTo(ContainElement(nodeName))
	})

	It("does not taint and requeues while the condition is still within the enter debounce", func() {
		const nodeName, policyName, labelKey = "node-pending", "policy-pending", "test/pending"

		createNode(nodeName, labelKey, testLabelValue, corev1.ConditionTrue, time.Now())
		createPolicy(policyName, labelKey)

		res := reconcilePolicy(policyName)

		By("requeuing for the remaining debounce window")
		Expect(res.RequeueAfter).To(BeNumerically(">", time.Duration(0)))

		By("leaving the node untainted")
		var node corev1.Node
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nodeName}, &node)).To(Succeed())
		_, ok := findTaint(&node)
		Expect(ok).To(BeFalse(), "expected no remediation taint within the enter debounce")

		By("reporting Pending (not NoRemediation) while the node awaits the debounce")
		var policy wardenv1alpha1.NodeRemediationPolicy
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: policyName}, &policy)).To(Succeed())
		Expect(policy.Status.PendingNodes).To(ContainElement(nodeName))
		cond := apimeta.FindStatusCondition(policy.Status.Conditions, wardenv1alpha1.ConditionRemediating)
		Expect(cond).NotTo(BeNil())
		Expect(cond.Reason).To(Equal(wardenv1alpha1.ReasonPending))
	})

	It("only remediates nodes matched by the nodeSelector", func() {
		const policyName, labelKey = "policy-scope", "test/scope"
		const matchNode, otherNode = "node-scope-match", "node-scope-other"

		createNode(matchNode, labelKey, testLabelValue, corev1.ConditionTrue, time.Now().Add(-2*time.Minute))
		createNode(otherNode, labelKey, "no", corev1.ConditionTrue, time.Now().Add(-2*time.Minute))
		createPolicy(policyName, labelKey)

		reconcileUntilStable(policyName)

		By("tainting only the matching node")
		var matched corev1.Node
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: matchNode}, &matched)).To(Succeed())
		_, ok := findTaint(&matched)
		Expect(ok).To(BeTrue(), "matching node should be tainted")

		var other corev1.Node
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: otherNode}, &other)).To(Succeed())
		_, ok = findTaint(&other)
		Expect(ok).To(BeFalse(), "non-matching node should not be tainted")

		By("listing only the matching node in status")
		var policy wardenv1alpha1.NodeRemediationPolicy
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: policyName}, &policy)).To(Succeed())
		Expect(policy.Status.SelectedNodes).To(ConsistOf(matchNode))
		Expect(policy.Status.MatchedNodes).To(ConsistOf(matchNode))
		Expect(policy.Status.RemediatedNodes).To(ConsistOf(matchNode))
	})

	It("trips the guard and applies no taint when too many nodes match", func() {
		const policyName, labelKey = "policy-guard", "test/guard"
		nodeNames := []string{"node-guard-1", "node-guard-2", "node-guard-3"}
		for _, n := range nodeNames {
			createNode(n, labelKey, testLabelValue, corev1.ConditionTrue, time.Now().Add(-2*time.Minute))
		}

		policy := &wardenv1alpha1.NodeRemediationPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: policyName},
			Spec: wardenv1alpha1.NodeRemediationPolicySpec{
				NodeSelector: &metav1.LabelSelector{MatchLabels: map[string]string{labelKey: testLabelValue}},
				Condition:    wardenv1alpha1.ConditionMatch{Type: testConditionType, Status: testConditionStatus},
				Remediations: wardenv1alpha1.Remediations{
					Taint: &corev1.Taint{Key: testTaintKey, Effect: corev1.TaintEffectNoExecute},
				},
				Guard: wardenv1alpha1.Guard{MaxAffectedPercent: ptr.To(int32(50))},
			},
		}
		Expect(k8sClient.Create(ctx, policy)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, policy) })

		reconcilePolicy(policyName)

		By("leaving every matching node untainted")
		for _, n := range nodeNames {
			var node corev1.Node
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: n}, &node)).To(Succeed())
			_, ok := findTaint(&node)
			Expect(ok).To(BeFalse(), "guard should prevent tainting")
		}

		By("reporting GuardTripped in status")
		var got wardenv1alpha1.NodeRemediationPolicy
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: policyName}, &got)).To(Succeed())
		cond := apimeta.FindStatusCondition(got.Status.Conditions, wardenv1alpha1.ConditionRemediating)
		Expect(cond).NotTo(BeNil())
		Expect(cond.Status).To(Equal(metav1.ConditionFalse))
		Expect(cond.Reason).To(Equal(wardenv1alpha1.ReasonGuardTripped))
	})

	It("trips the guard with two matching nodes at 50% and taints neither", func() {
		const policyName, labelKey = "policy-guard2", "test/guard2"
		nodeNames := []string{"node-g2-1", "node-g2-2"}
		for _, n := range nodeNames {
			createNode(n, labelKey, testLabelValue, corev1.ConditionTrue, time.Now().Add(-2*time.Minute))
		}

		policy := &wardenv1alpha1.NodeRemediationPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: policyName},
			Spec: wardenv1alpha1.NodeRemediationPolicySpec{
				NodeSelector: &metav1.LabelSelector{MatchLabels: map[string]string{labelKey: testLabelValue}},
				Condition:    wardenv1alpha1.ConditionMatch{Type: testConditionType, Status: testConditionStatus},
				Remediations: wardenv1alpha1.Remediations{
					Taint: &corev1.Taint{Key: testTaintKey, Effect: corev1.TaintEffectNoExecute},
				},
				// 2 of 2 nodes match = 100% > 50% -> guard trips, nothing is applied.
				Guard: wardenv1alpha1.Guard{MaxAffectedPercent: ptr.To(int32(50))},
			},
		}
		Expect(k8sClient.Create(ctx, policy)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, policy) })

		reconcileUntilStable(policyName)

		By("tainting neither node")
		for _, n := range nodeNames {
			var node corev1.Node
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: n}, &node)).To(Succeed())
			_, ok := findTaint(&node)
			Expect(ok).To(BeFalse(), "guard should prevent tainting either node")
		}

		By("reporting GuardTripped")
		var got wardenv1alpha1.NodeRemediationPolicy
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: policyName}, &got)).To(Succeed())
		cond := apimeta.FindStatusCondition(got.Status.Conditions, wardenv1alpha1.ConditionRemediating)
		Expect(cond).NotTo(BeNil())
		Expect(cond.Status).To(Equal(metav1.ConditionFalse))
		Expect(cond.Reason).To(Equal(wardenv1alpha1.ReasonGuardTripped))

		By("recording a GuardTripped warning event on the policy")
		Expect(drainEvents(recorder)).To(ContainElement(SatisfyAll(
			ContainSubstring("Warning"),
			ContainSubstring(wardenv1alpha1.ReasonGuardTripped),
		)))
	})

	It("keeps remediating the other nodes and requeues when one node's taint write fails", func() {
		const goodNode, badNode = "node-act-good", "node-act-bad"
		const policyName, labelKey = "policy-act-partial", "test/actpartial"

		createNode(goodNode, labelKey, testLabelValue, corev1.ConditionTrue, time.Now().Add(-2*time.Minute))
		createNode(badNode, labelKey, testLabelValue, corev1.ConditionTrue, time.Now().Add(-2*time.Minute))
		createPolicy(policyName, labelKey)

		By("reconciling with the bad node's taint write failing")
		r := newFailingReconciler(badNode)
		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: policyName}})
		Expect(err).To(HaveOccurred(), "a per-node failure must surface as an aggregate error so the reconcile requeues")

		By("still tainting the healthy node despite the other node's failure")
		var good corev1.Node
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: goodNode}, &good)).To(Succeed())
		_, ok := findTaint(&good)
		Expect(ok).To(BeTrue(), "the healthy node must be remediated even though another node failed")
		var bad corev1.Node
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: badNode}, &bad)).To(Succeed())
		_, ok = findTaint(&bad)
		Expect(ok).To(BeFalse(), "the failing node must not be tainted")

		By("writing the status best-effort despite the error")
		var policy wardenv1alpha1.NodeRemediationPolicy
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: policyName}, &policy)).To(Succeed())
		Expect(policy.Status.MatchedNodes).To(ContainElements(goodNode, badNode))
	})

	It("marks the policy InvalidSpec and stops requeuing on an invalid nodeSelector", func() {
		policy := &wardenv1alpha1.NodeRemediationPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "policy-invalid"},
			Spec: wardenv1alpha1.NodeRemediationPolicySpec{
				NodeSelector: invalidNodeSelector(),
				Condition:    wardenv1alpha1.ConditionMatch{Type: testConditionType, Status: testConditionStatus},
				Remediations: wardenv1alpha1.Remediations{
					Taint: &corev1.Taint{Key: testTaintKey, Effect: corev1.TaintEffectNoExecute},
				},
			},
		}
		Expect(k8sClient.Create(ctx, policy)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, policy) })

		res := reconcilePolicy(policy.Name)

		By("returning no error and no requeue")
		Expect(res).To(Equal(reconcile.Result{}))

		By("surfacing InvalidSpec in status")
		var got wardenv1alpha1.NodeRemediationPolicy
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: policy.Name}, &got)).To(Succeed())
		cond := apimeta.FindStatusCondition(got.Status.Conditions, wardenv1alpha1.ConditionRemediating)
		Expect(cond).NotTo(BeNil())
		Expect(cond.Status).To(Equal(metav1.ConditionFalse))
		Expect(cond.Reason).To(Equal(wardenv1alpha1.ReasonInvalidSpec))

		By("recording an InvalidSpec warning event on the policy")
		Expect(drainEvents(recorder)).To(ContainElement(SatisfyAll(
			ContainSubstring("Warning"),
			ContainSubstring(wardenv1alpha1.ReasonInvalidSpec),
		)))
	})

	It("holds and warns when the matching condition has no lastTransitionTime", func() {
		const nodeName, policyName, labelKey = "node-nots", "policy-nots", "test/nots"

		node := &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: nodeName, Labels: map[string]string{labelKey: testLabelValue}},
		}
		Expect(k8sClient.Create(ctx, node)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, node) })
		node.Status.Conditions = []corev1.NodeCondition{{
			Type:   corev1.NodeConditionType(testConditionType),
			Status: corev1.ConditionTrue,
			// LastTransitionTime intentionally left zero (some detectors omit it).
		}}
		Expect(k8sClient.Status().Update(ctx, node)).To(Succeed())
		createPolicy(policyName, labelKey)

		reconcileUntilStable(policyName)

		By("not tainting the node, so the debounce is not bypassed")
		var got corev1.Node
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nodeName}, &got)).To(Succeed())
		_, ok := findTaint(&got)
		Expect(ok).To(BeFalse(), "node must not be tainted without a lastTransitionTime")

		By("reporting the node as matched-but-Held, listed in heldNodes, not NoRemediation")
		var policy wardenv1alpha1.NodeRemediationPolicy
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: policyName}, &policy)).To(Succeed())
		Expect(policy.Status.MatchedNodes).To(ContainElement(nodeName))
		Expect(policy.Status.PendingNodes).NotTo(ContainElement(nodeName))
		Expect(policy.Status.HeldNodes).To(ContainElement(nodeName))
		Expect(policy.Status.HeldCount).To(Equal(int32(1)))
		cond := apimeta.FindStatusCondition(policy.Status.Conditions, wardenv1alpha1.ConditionRemediating)
		Expect(cond).NotTo(BeNil())
		Expect(cond.Reason).To(Equal(wardenv1alpha1.ReasonHeld))

		By("recording a MissingTransitionTime warning on the policy")
		Expect(drainEvents(recorder)).To(ContainElement(SatisfyAll(
			ContainSubstring("Warning"),
			ContainSubstring(reasonMissingTransitionTime),
		)))
	})

	It("does not re-emit policy-level events on the settle reconcile", func() {
		const nodeName, policyName, labelKey = "node-settle", "policy-settle", "test/settle"

		node := &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: nodeName, Labels: map[string]string{labelKey: testLabelValue}},
		}
		Expect(k8sClient.Create(ctx, node)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, node) })
		node.Status.Conditions = []corev1.NodeCondition{{
			Type:   corev1.NodeConditionType(testConditionType),
			Status: corev1.ConditionTrue,
			// no LastTransitionTime -> held -> MissingTransitionTime warning
		}}
		Expect(k8sClient.Status().Update(ctx, node)).To(Succeed())
		createPolicy(policyName, labelKey)

		By("emitting the warning on the pass that changes the status")
		reconcilePolicy(policyName)
		Expect(drainEvents(recorder)).To(ContainElement(ContainSubstring(reasonMissingTransitionTime)))

		By("not re-emitting it on the settle pass where nothing changed")
		reconcilePolicy(policyName)
		Expect(drainEvents(recorder)).NotTo(ContainElement(ContainSubstring(reasonMissingTransitionTime)))
	})

	It("keeps the last-known node status when a working policy's spec becomes invalid", func() {
		const nodeName, policyName, labelKey = "node-stale", "policy-stale", "test/stale"

		createNode(nodeName, labelKey, testLabelValue, corev1.ConditionTrue, time.Now().Add(-2*time.Minute))
		createPolicy(policyName, labelKey)
		reconcileUntilStable(policyName)

		By("first populating the status with a remediated node")
		var populated wardenv1alpha1.NodeRemediationPolicy
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: policyName}, &populated)).To(Succeed())
		Expect(populated.Status.RemediatedCount).To(BeNumerically(">", 0))

		By("making the spec invalid")
		populated.Spec.NodeSelector = invalidNodeSelector()
		Expect(k8sClient.Update(ctx, &populated)).To(Succeed())
		reconcilePolicy(policyName)

		By("preserving the node lists so the orphaned taint stays visible under an InvalidSpec condition")
		var got wardenv1alpha1.NodeRemediationPolicy
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: policyName}, &got)).To(Succeed())
		Expect(got.Status.RemediatedNodes).To(ContainElement(nodeName))
		Expect(got.Status.RemediatedCount).To(BeNumerically(">", 0))
		cond := apimeta.FindStatusCondition(got.Status.Conditions, wardenv1alpha1.ConditionRemediating)
		Expect(cond).NotTo(BeNil())
		Expect(cond.Reason).To(Equal(wardenv1alpha1.ReasonInvalidSpec))
	})

	It("removes the applied taints and the finalizer when the policy is deleted", func() {
		const nodeName, policyName, labelKey = "node-final", "policy-final", "test/final"

		createNode(nodeName, labelKey, testLabelValue, corev1.ConditionTrue, time.Now().Add(-2*time.Minute))
		createPolicy(policyName, labelKey)
		reconcileUntilStable(policyName)

		By("tainting the node and installing the finalizer")
		var node corev1.Node
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nodeName}, &node)).To(Succeed())
		_, ok := findTaint(&node)
		Expect(ok).To(BeTrue(), "node should be tainted before deletion")
		var got wardenv1alpha1.NodeRemediationPolicy
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: policyName}, &got)).To(Succeed())
		Expect(got.Finalizers).To(ContainElement(taintCleanupFinalizer))

		By("deleting the policy, which only sets a deletionTimestamp while the finalizer is held")
		Expect(k8sClient.Delete(ctx, &got)).To(Succeed())
		reconcilePolicy(policyName)

		By("removing the remediation taint from the node")
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nodeName}, &node)).To(Succeed())
		_, ok = findTaint(&node)
		Expect(ok).To(BeFalse(), "taint must be cleaned up on deletion")

		By("letting the object be garbage-collected once the finalizer is gone")
		err := k8sClient.Get(ctx, types.NamespacedName{Name: policyName}, &got)
		Expect(apierrors.IsNotFound(err)).To(BeTrue(), "policy should be gone after finalizer removal")
	})

	It("still removes the taint on deletion when the nodeSelector has become invalid", func() {
		const nodeName, policyName, labelKey = "node-final-inval", "policy-final-inval", "test/finalinval"

		createNode(nodeName, labelKey, testLabelValue, corev1.ConditionTrue, time.Now().Add(-2*time.Minute))
		createPolicy(policyName, labelKey)
		reconcileUntilStable(policyName)

		By("tainting the node")
		var node corev1.Node
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nodeName}, &node)).To(Succeed())
		_, ok := findTaint(&node)
		Expect(ok).To(BeTrue())

		By("breaking the nodeSelector (parses at admission, fails at LabelSelectorAsSelector)")
		var policy wardenv1alpha1.NodeRemediationPolicy
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: policyName}, &policy)).To(Succeed())
		policy.Spec.NodeSelector = invalidNodeSelector()
		Expect(k8sClient.Update(ctx, &policy)).To(Succeed())

		By("deleting the policy and reconciling")
		Expect(k8sClient.Delete(ctx, &policy)).To(Succeed())
		reconcilePolicy(policyName)

		By("cleaning up the taint by key despite the unparseable selector")
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nodeName}, &node)).To(Succeed())
		_, ok = findTaint(&node)
		Expect(ok).To(BeFalse(), "finalizer must clean up by key, not depend on the selector")
		err := k8sClient.Get(ctx, types.NamespacedName{Name: policyName}, &policy)
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
	})

	It("keeps the finalizer and retries when a node's cleanup fails on deletion", func() {
		const goodNode, badNode = "node-fin-good", "node-fin-bad"
		const policyName, labelKey = "policy-fin-partial", "test/finpartial"

		createNode(goodNode, labelKey, testLabelValue, corev1.ConditionTrue, time.Now().Add(-2*time.Minute))
		createNode(badNode, labelKey, testLabelValue, corev1.ConditionTrue, time.Now().Add(-2*time.Minute))
		createPolicy(policyName, labelKey)
		reconcileUntilStable(policyName)

		By("tainting both nodes and installing the finalizer")
		for _, n := range []string{goodNode, badNode} {
			var node corev1.Node
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: n}, &node)).To(Succeed())
			_, ok := findTaint(&node)
			Expect(ok).To(BeTrue())
		}

		By("deleting the policy, then reconciling with one node's cleanup failing")
		var got wardenv1alpha1.NodeRemediationPolicy
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: policyName}, &got)).To(Succeed())
		Expect(k8sClient.Delete(ctx, &got)).To(Succeed())
		r := newFailingReconciler(badNode)
		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: policyName}})
		Expect(err).To(HaveOccurred(), "a failed cleanup must surface so the deletion is retried")

		By("cleaning up the healthy node but keeping the policy (finalizer held) until cleanup completes")
		var good corev1.Node
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: goodNode}, &good)).To(Succeed())
		_, ok := findTaint(&good)
		Expect(ok).To(BeFalse(), "the healthy node must be cleaned up despite the other failing")
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: policyName}, &got)).To(Succeed())
		Expect(got.Finalizers).To(ContainElement(taintCleanupFinalizer), "finalizer must stay until every taint is gone")

		By("completing the cleanup on a later pass once the failure clears")
		reconcilePolicy(policyName)
		var bad corev1.Node
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: badNode}, &bad)).To(Succeed())
		_, ok = findTaint(&bad)
		Expect(ok).To(BeFalse())
		err = k8sClient.Get(ctx, types.NamespacedName{Name: policyName}, &got)
		Expect(apierrors.IsNotFound(err)).To(BeTrue(), "policy should be gone once cleanup fully succeeds")
	})
})
