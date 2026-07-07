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

package predicate_test

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/event"

	"github.com/scality/node-warden-operator/internal/predicate"
)

// nodeWithCondition builds a *corev1.Node named "n1" with a single condition of the given
// type/status. lastHeartbeat/lastTransition let tests vary only the churn fields while
// keeping type/status fixed.
func nodeWithCondition(condType corev1.NodeConditionType, status corev1.ConditionStatus, lastHeartbeat, lastTransition time.Time) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "n1"},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{
				{
					Type:               condType,
					Status:             status,
					LastHeartbeatTime:  metav1.NewTime(lastHeartbeat),
					LastTransitionTime: metav1.NewTime(lastTransition),
				},
			},
		},
	}
}

var _ = Describe("ConditionStatusChanged", func() {
	var t0, t1 time.Time

	BeforeEach(func() {
		t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		t1 = t0.Add(10 * time.Second)
	})

	It("filters a heartbeat-only change", func() {
		oldNode := nodeWithCondition(corev1.NodeReady, corev1.ConditionTrue, t0, t0)
		newNode := nodeWithCondition(corev1.NodeReady, corev1.ConditionTrue, t1, t0)

		result := predicate.ConditionStatusChanged().Update(event.UpdateEvent{
			ObjectOld: oldNode,
			ObjectNew: newNode,
		})

		Expect(result).To(BeFalse())
	})

	It("passes a status change", func() {
		oldNode := nodeWithCondition(corev1.NodeReady, corev1.ConditionFalse, t0, t0)
		newNode := nodeWithCondition(corev1.NodeReady, corev1.ConditionTrue, t1, t1)

		result := predicate.ConditionStatusChanged().Update(event.UpdateEvent{
			ObjectOld: oldNode,
			ObjectNew: newNode,
		})

		Expect(result).To(BeTrue())
	})

	It("passes an added condition", func() {
		oldNode := &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: "n1"},
			Status:     corev1.NodeStatus{Conditions: []corev1.NodeCondition{}},
		}
		newNode := nodeWithCondition(corev1.NodeMemoryPressure, corev1.ConditionTrue, t0, t0)

		result := predicate.ConditionStatusChanged().Update(event.UpdateEvent{
			ObjectOld: oldNode,
			ObjectNew: newNode,
		})

		Expect(result).To(BeTrue())
	})

	It("filters a lastTransitionTime bump between two set values", func() {
		oldNode := nodeWithCondition(corev1.NodeReady, corev1.ConditionTrue, t0, t0)
		newNode := nodeWithCondition(corev1.NodeReady, corev1.ConditionTrue, t0, t1)

		result := predicate.ConditionStatusChanged().Update(event.UpdateEvent{
			ObjectOld: oldNode,
			ObjectNew: newNode,
		})

		Expect(result).To(BeFalse())
	})

	It("passes when a condition first gains a lastTransitionTime", func() {
		oldNode := nodeWithCondition(corev1.NodeReady, corev1.ConditionTrue, t0, time.Time{})
		newNode := nodeWithCondition(corev1.NodeReady, corev1.ConditionTrue, t0, t1)

		result := predicate.ConditionStatusChanged().Update(event.UpdateEvent{
			ObjectOld: oldNode,
			ObjectNew: newNode,
		})

		Expect(result).To(BeTrue())
	})

	It("filters non-Node objects", func() {
		oldPod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "p1"}}
		newPod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "p1"}}

		result := predicate.ConditionStatusChanged().Update(event.UpdateEvent{
			ObjectOld: oldPod,
			ObjectNew: newPod,
		})

		Expect(result).To(BeFalse())
	})
})

var _ = Describe("ChangedConditionTypes", func() {
	const condWP corev1.NodeConditionType = "WPUnavailable"

	It("returns only the types whose status changed, not an unrelated one", func() {
		oldConds := []corev1.NodeCondition{
			{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
			{Type: condWP, Status: corev1.ConditionFalse},
		}
		newConds := []corev1.NodeCondition{
			{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
			{Type: condWP, Status: corev1.ConditionTrue},
		}

		Expect(predicate.ChangedConditionTypes(oldConds, newConds)).To(Equal(map[string]bool{string(condWP): true}))
	})

	It("includes an added and a removed condition type", func() {
		oldConds := []corev1.NodeCondition{{Type: "Gone", Status: corev1.ConditionTrue}}
		newConds := []corev1.NodeCondition{{Type: "New", Status: corev1.ConditionTrue}}

		Expect(predicate.ChangedConditionTypes(oldConds, newConds)).To(Equal(map[string]bool{"Gone": true, "New": true}))
	})

	It("is empty when only timestamps changed", func() {
		conds := []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}

		Expect(predicate.ChangedConditionTypes(conds, conds)).To(BeEmpty())
	})

	It("includes a type that gained a lastTransitionTime at the same status", func() {
		oldConds := []corev1.NodeCondition{{Type: condWP, Status: corev1.ConditionTrue}}
		newConds := []corev1.NodeCondition{{Type: condWP, Status: corev1.ConditionTrue, LastTransitionTime: metav1.NewTime(time.Unix(1, 0))}}

		Expect(predicate.ChangedConditionTypes(oldConds, newConds)).To(Equal(map[string]bool{string(condWP): true}))
	})
})
