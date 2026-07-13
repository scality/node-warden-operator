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

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var _ = Describe("NodeRemediationPolicy status conditions", func() {
	var policy *NodeRemediationPolicy

	BeforeEach(func() {
		policy = &NodeRemediationPolicy{}
		policy.Generation = 7
	})

	remediating := func() *metav1.Condition {
		return apimeta.FindStatusCondition(policy.Status.Conditions, ConditionRemediating)
	}

	It("marks InvalidSpec False, carrying the message and the observed generation", func() {
		policy.MarkInvalidSpec("bad selector")

		cond := remediating()
		Expect(cond).NotTo(BeNil())
		Expect(cond.Status).To(Equal(metav1.ConditionFalse))
		Expect(cond.Reason).To(Equal(ReasonInvalidSpec))
		Expect(cond.Message).To(Equal("bad selector"))
		Expect(cond.ObservedGeneration).To(Equal(int64(7)))
	})

	It("marks GuardTripped False and reports how many are still remediated", func() {
		policy.MarkGuardTripped(2)

		Expect(remediating().Status).To(Equal(metav1.ConditionFalse))
		Expect(remediating().Reason).To(Equal(ReasonGuardTripped))
		Expect(remediating().Message).To(ContainSubstring("2 node(s) currently remediated"))
	})

	It("marks NodesRemediated True and names the count", func() {
		policy.MarkNodesRemediated(3)

		Expect(remediating().Status).To(Equal(metav1.ConditionTrue))
		Expect(remediating().Reason).To(Equal(ReasonNodesRemediated))
		Expect(remediating().Message).To(ContainSubstring("3"))
	})

	It("marks Pending False and names the count, distinct from NoRemediation", func() {
		policy.MarkPending(2)

		Expect(remediating().Status).To(Equal(metav1.ConditionFalse))
		Expect(remediating().Reason).To(Equal(ReasonPending))
		Expect(remediating().Message).To(ContainSubstring("2"))
	})

	It("marks Held False and names the count", func() {
		policy.MarkHeld(2)

		Expect(remediating().Status).To(Equal(metav1.ConditionFalse))
		Expect(remediating().Reason).To(Equal(ReasonHeld))
		Expect(remediating().Message).To(ContainSubstring("2"))
	})

	It("keeps a single Remediating condition as the state changes", func() {
		policy.MarkGuardTripped(0)
		policy.MarkNoRemediation()

		count := 0
		for _, c := range policy.Status.Conditions {
			if c.Type == ConditionRemediating {
				count++
			}
		}
		Expect(count).To(Equal(1))
		Expect(remediating().Reason).To(Equal(ReasonNoRemediation))
	})
})
