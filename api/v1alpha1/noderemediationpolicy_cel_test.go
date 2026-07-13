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
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const (
	exampleCondType = "Example"
	exampleTaintKey = "example.com/unreachable"
)

var _ = Describe("NodeRemediationPolicy CEL validation", func() {
	It("rejects a policy with no remediation", func() {
		p := &NodeRemediationPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "no-remediation"},
			Spec: NodeRemediationPolicySpec{
				Condition:    ConditionMatch{Type: exampleCondType, Status: DefaultConditionStatus},
				Remediations: Remediations{}, // no taint
			},
		}
		err := k8sClient.Create(ctx, p)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("at least one remediation"))
	})

	It("accepts a policy with a taint remediation", func() {
		p := &NodeRemediationPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "with-taint"},
			Spec: NodeRemediationPolicySpec{
				Condition: ConditionMatch{Type: exampleCondType, Status: DefaultConditionStatus},
				Remediations: Remediations{
					Taint: &corev1.Taint{Key: exampleTaintKey, Effect: corev1.TaintEffectNoExecute},
				},
			},
		}
		Expect(k8sClient.Create(ctx, p)).To(Succeed())
	})

	It("accepts a taint with the NoSchedule effect (scheduling-only remediation)", func() {
		p := &NodeRemediationPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "noschedule-taint"},
			Spec: NodeRemediationPolicySpec{
				Condition: ConditionMatch{Type: exampleCondType, Status: DefaultConditionStatus},
				Remediations: Remediations{
					Taint: &corev1.Taint{Key: exampleTaintKey, Effect: corev1.TaintEffectNoSchedule},
				},
			},
		}
		Expect(k8sClient.Create(ctx, p)).To(Succeed())
	})

	It("rejects a taint whose effect is not a real Kubernetes taint effect", func() {
		p := &NodeRemediationPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "bad-effect"},
			Spec: NodeRemediationPolicySpec{
				Condition: ConditionMatch{Type: exampleCondType, Status: DefaultConditionStatus},
				Remediations: Remediations{
					Taint: &corev1.Taint{Key: exampleTaintKey, Effect: corev1.TaintEffect("Evict")},
				},
			},
		}
		err := k8sClient.Create(ctx, p)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("taint.effect must be"))
	})

	It("rejects changing the taint of an existing policy", func() {
		p := &NodeRemediationPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "immutable-taint"},
			Spec: NodeRemediationPolicySpec{
				Condition: ConditionMatch{Type: exampleCondType, Status: DefaultConditionStatus},
				Remediations: Remediations{
					Taint: &corev1.Taint{Key: exampleTaintKey, Effect: corev1.TaintEffectNoSchedule},
				},
			},
		}
		Expect(k8sClient.Create(ctx, p)).To(Succeed())

		p.Spec.Remediations.Taint.Effect = corev1.TaintEffectNoExecute
		err := k8sClient.Update(ctx, p)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("taint is immutable"))
	})

	It("rejects a negative debounce duration", func() {
		p := &NodeRemediationPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "negative-debounce"},
			Spec: NodeRemediationPolicySpec{
				Condition: ConditionMatch{Type: exampleCondType, Status: DefaultConditionStatus},
				Remediations: Remediations{
					Taint: &corev1.Taint{Key: exampleTaintKey, Effect: corev1.TaintEffectNoExecute},
				},
				Debounce: Debounce{Enter: &metav1.Duration{Duration: -1 * time.Second}},
			},
		}
		err := k8sClient.Create(ctx, p)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("non-negative Go duration"))
	})

	It("rejects an unparseable debounce duration (would stall decode)", func() {
		// Set the raw string via unstructured: a typed metav1.Duration can only hold a valid
		// duration, but the API accepts any string absent validation, and "5min" fails to decode.
		u := &unstructured.Unstructured{}
		u.SetGroupVersionKind(GroupVersion.WithKind("NodeRemediationPolicy"))
		u.SetName("unparseable-debounce")
		Expect(unstructured.SetNestedField(u.Object, exampleCondType, "spec", "condition", "type")).To(Succeed())
		Expect(unstructured.SetNestedField(u.Object, exampleTaintKey, "spec", "remediations", "taint", "key")).To(Succeed())
		Expect(unstructured.SetNestedField(u.Object, "NoExecute", "spec", "remediations", "taint", "effect")).To(Succeed())
		Expect(unstructured.SetNestedField(u.Object, "5min", "spec", "debounce", "enter")).To(Succeed())
		err := k8sClient.Create(ctx, u)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("non-negative Go duration"))
	})

	It("rejects a taint key that is not a valid qualified name", func() {
		p := &NodeRemediationPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "bad-taint-key"},
			Spec: NodeRemediationPolicySpec{
				Condition: ConditionMatch{Type: exampleCondType, Status: DefaultConditionStatus},
				Remediations: Remediations{
					Taint: &corev1.Taint{Key: "not a qualified name", Effect: corev1.TaintEffectNoExecute},
				},
			},
		}
		err := k8sClient.Create(ctx, p)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("qualified name"))
	})

	It("rejects a taint key whose name segment exceeds the 63-char limit", func() {
		// Well-formed characters but one over the length limit: it passes the character-class
		// regex but Kubernetes' own IsQualifiedName would 422 every node patch, so reject at admission.
		p := &NodeRemediationPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "overlong-taint-key"},
			Spec: NodeRemediationPolicySpec{
				Condition: ConditionMatch{Type: exampleCondType, Status: DefaultConditionStatus},
				Remediations: Remediations{
					Taint: &corev1.Taint{Key: strings.Repeat("a", 64), Effect: corev1.TaintEffectNoExecute},
				},
			},
		}
		err := k8sClient.Create(ctx, p)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("qualified name"))
	})

	It("rejects a taint value that is not a valid label value", func() {
		p := &NodeRemediationPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "bad-taint-value"},
			Spec: NodeRemediationPolicySpec{
				Condition: ConditionMatch{Type: exampleCondType, Status: DefaultConditionStatus},
				Remediations: Remediations{
					Taint: &corev1.Taint{Key: exampleTaintKey, Value: "not a valid value!", Effect: corev1.TaintEffectNoExecute},
				},
			},
		}
		err := k8sClient.Create(ctx, p)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("label value"))
	})

	It("rejects a debounce duration whose magnitude is out of range (would overflow decode)", func() {
		// A syntactically unit-correct but absurdly large value passes character validation yet
		// overflows time.Duration on decode; the digit cap rejects it at admission instead.
		u := &unstructured.Unstructured{}
		u.SetGroupVersionKind(GroupVersion.WithKind("NodeRemediationPolicy"))
		u.SetName("overflow-debounce")
		Expect(unstructured.SetNestedField(u.Object, exampleCondType, "spec", "condition", "type")).To(Succeed())
		Expect(unstructured.SetNestedField(u.Object, exampleTaintKey, "spec", "remediations", "taint", "key")).To(Succeed())
		Expect(unstructured.SetNestedField(u.Object, "NoExecute", "spec", "remediations", "taint", "effect")).To(Succeed())
		Expect(unstructured.SetNestedField(u.Object, "100000000000s", "spec", "debounce", "enter")).To(Succeed())
		err := k8sClient.Create(ctx, u)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("non-negative Go duration"))
	})

	It("accepts a fractional debounce duration written with a leading dot", func() {
		// ".5s" is a valid Go duration (500ms); the rule must not reject it for lacking a leading digit.
		u := &unstructured.Unstructured{}
		u.SetGroupVersionKind(GroupVersion.WithKind("NodeRemediationPolicy"))
		u.SetName("leading-dot-debounce")
		Expect(unstructured.SetNestedField(u.Object, exampleCondType, "spec", "condition", "type")).To(Succeed())
		Expect(unstructured.SetNestedField(u.Object, exampleTaintKey, "spec", "remediations", "taint", "key")).To(Succeed())
		Expect(unstructured.SetNestedField(u.Object, "NoExecute", "spec", "remediations", "taint", "effect")).To(Succeed())
		Expect(unstructured.SetNestedField(u.Object, ".5s", "spec", "debounce", "enter")).To(Succeed())
		Expect(k8sClient.Create(ctx, u)).To(Succeed())
	})

	It("allows updating a policy without touching the taint", func() {
		p := &NodeRemediationPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "mutable-elsewhere"},
			Spec: NodeRemediationPolicySpec{
				Condition: ConditionMatch{Type: exampleCondType, Status: DefaultConditionStatus},
				Remediations: Remediations{
					Taint: &corev1.Taint{Key: exampleTaintKey, Effect: corev1.TaintEffectNoSchedule},
				},
			},
		}
		Expect(k8sClient.Create(ctx, p)).To(Succeed())

		p.Spec.Condition.Type = "AnotherCondition"
		Expect(k8sClient.Update(ctx, p)).To(Succeed())
	})
})
