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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/scality/node-warden-operator/internal/remediation"
)

var _ = Describe("Taint", func() {
	var nodeCounter int
	var nodeNames []string

	newNodeName := func() string {
		nodeCounter++
		return fmt.Sprintf("taint-test-node-%d", nodeCounter)
	}

	createNode := func(name string, taints ...corev1.Taint) {
		node := &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name: name,
			},
			Spec: corev1.NodeSpec{
				Taints: taints,
			},
		}
		Expect(k8sClient.Create(ctx, node)).To(Succeed())
		nodeNames = append(nodeNames, name)
	}

	getNode := func(name string) *corev1.Node {
		var node corev1.Node
		Expect(k8sClient.Get(ctx, client.ObjectKey{Name: name}, &node)).To(Succeed())
		return &node
	}

	BeforeEach(func() {
		nodeNames = nil
	})

	AfterEach(func() {
		for _, name := range nodeNames {
			node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name}}
			_ = k8sClient.Delete(ctx, node)
		}
	})

	ourTaint := remediation.Taint{Key: "node.scality.io/wp-unreachable", Effect: "NoExecute"}

	It("adds the taint and is idempotent on repeated apply", func() {
		name := newNodeName()
		createNode(name)

		changed, err := EnsureTaint(ctx, k8sClient, k8sClient, name, ourTaint)
		Expect(err).NotTo(HaveOccurred())
		Expect(changed).To(BeTrue(), "first apply must change the node")

		changed, err = EnsureTaint(ctx, k8sClient, k8sClient, name, ourTaint)
		Expect(err).NotTo(HaveOccurred())
		Expect(changed).To(BeFalse(), "re-applying an already-present taint must be a no-op")

		node := getNode(name)
		matching := 0
		for _, tnt := range node.Spec.Taints {
			if tnt.Key == ourTaint.Key {
				matching++
				Expect(tnt.Effect).To(Equal(corev1.TaintEffectNoExecute))
			}
		}
		Expect(matching).To(Equal(1))
	})

	It("removes the taint and is idempotent on repeated remove", func() {
		name := newNodeName()
		createNode(name, corev1.Taint{
			Key:    ourTaint.Key,
			Effect: corev1.TaintEffectNoExecute,
		})

		changed, err := RemoveTaint(ctx, k8sClient, k8sClient, name, ourTaint.Key)
		Expect(err).NotTo(HaveOccurred())
		Expect(changed).To(BeTrue(), "removing a present taint must change the node")

		node := getNode(name)
		for _, tnt := range node.Spec.Taints {
			Expect(tnt.Key).NotTo(Equal(ourTaint.Key))
		}

		// idempotent: calling again on an already-absent taint is a no-op.
		changed, err = RemoveTaint(ctx, k8sClient, k8sClient, name, ourTaint.Key)
		Expect(err).NotTo(HaveOccurred())
		Expect(changed).To(BeFalse(), "removing an absent taint must be a no-op")
	})

	It("preserves foreign taints when applying and removing our taint", func() {
		name := newNodeName()
		foreign := corev1.Taint{
			Key:    "other.example.com/x",
			Effect: corev1.TaintEffectNoSchedule,
		}
		createNode(name, foreign)

		_, err := EnsureTaint(ctx, k8sClient, k8sClient, name, ourTaint)
		Expect(err).NotTo(HaveOccurred())

		// Note: envtest's API server may inject its own taints (e.g. the
		// TaintNodesByCondition admission plugin adds node.kubernetes.io/not-ready on
		// creation), so assert on presence of the specific taints we care about rather
		// than the exact full set.
		node := getNode(name)
		Expect(node.Spec.Taints).To(ContainElement(foreign))
		Expect(node.Spec.Taints).To(ContainElement(corev1.Taint{Key: ourTaint.Key, Effect: corev1.TaintEffectNoExecute}))

		_, err = RemoveTaint(ctx, k8sClient, k8sClient, name, ourTaint.Key)
		Expect(err).NotTo(HaveOccurred())

		node = getNode(name)
		Expect(node.Spec.Taints).To(ContainElement(foreign))
		for _, tnt := range node.Spec.Taints {
			Expect(tnt.Key).NotTo(Equal(ourTaint.Key))
		}
	})

	It("retries on a conflicting write and converges", func() {
		name := newNodeName()
		createNode(name)

		// Wrap the real client so the first Patch fails with a 409; RetryOnConflict must re-read
		// (uncached, via apiReader) and re-apply, so the taint still lands and no update is lost.
		base, err := client.NewWithWatch(cfg, client.Options{Scheme: k8sClient.Scheme()})
		Expect(err).NotTo(HaveOccurred())
		var patchCalls int
		flaky := interceptor.NewClient(base, interceptor.Funcs{
			Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				patchCalls++
				if patchCalls == 1 {
					return apierrors.NewConflict(corev1.Resource("nodes"), obj.GetName(), fmt.Errorf("simulated concurrent update"))
				}
				return c.Patch(ctx, obj, patch, opts...)
			},
		})

		changed, err := EnsureTaint(ctx, flaky, k8sClient, name, ourTaint)
		Expect(err).NotTo(HaveOccurred())
		Expect(changed).To(BeTrue())
		Expect(patchCalls).To(BeNumerically(">=", 2), "must have retried after the conflict")

		node := getNode(name)
		Expect(node.Spec.Taints).To(ContainElement(corev1.Taint{Key: ourTaint.Key, Effect: corev1.TaintEffectNoExecute}))
	})
})
