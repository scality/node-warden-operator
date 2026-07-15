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
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	wardenv1alpha1 "github.com/scality/node-warden-operator/api/v1alpha1"
)

// makePolicy builds a minimal, CEL-valid NodeRemediationPolicy with the given taint key so the
// specs reach the webhook rather than being rejected by the schema first.
func makePolicy(name, taintKey string) *wardenv1alpha1.NodeRemediationPolicy {
	return &wardenv1alpha1.NodeRemediationPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: wardenv1alpha1.NodeRemediationPolicySpec{
			Condition: wardenv1alpha1.ConditionMatch{Type: "WebhookTest", Status: "True"},
			Remediations: wardenv1alpha1.Remediations{
				Taint: &corev1.Taint{Key: taintKey, Effect: corev1.TaintEffectNoExecute},
			},
		},
	}
}

var _ = Describe("NodeRemediationPolicy Webhook", func() {
	Context("taint.key uniqueness across policies", func() {
		It("admits a policy claiming a taint key", func() {
			p := makePolicy("wh-solo", "warden.scality.com/wh-solo")
			Expect(k8sClient.Create(ctx, p)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, p) })
		})

		It("rejects a second policy reusing the same taint key", func() {
			owner := makePolicy("wh-owner", "warden.scality.com/wh-dup")
			Expect(k8sClient.Create(ctx, owner)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, owner) })

			dup := makePolicy("wh-dup", "warden.scality.com/wh-dup")
			err := k8sClient.Create(ctx, dup)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("already used by NodeRemediationPolicy"))
			Expect(err.Error()).To(ContainSubstring("wh-owner"))
		})

		It("admits policies with distinct taint keys", func() {
			a := makePolicy("wh-a", "warden.scality.com/wh-a")
			b := makePolicy("wh-b", "warden.scality.com/wh-b")
			Expect(k8sClient.Create(ctx, a)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, a) })
			Expect(k8sClient.Create(ctx, b)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, b) })
		})

		It("admits updating a policy (its own key is not a conflict)", func() {
			p := makePolicy("wh-self", "warden.scality.com/wh-self")
			Expect(k8sClient.Create(ctx, p)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, p) })

			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "wh-self"}, p)).To(Succeed())
			p.Spec.Condition.Type = "WebhookTestChanged"
			Expect(k8sClient.Update(ctx, p)).To(Succeed())
		})
	})
})
