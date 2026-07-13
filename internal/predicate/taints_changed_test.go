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

// nodeWithTaints builds a *corev1.Node named "n1" carrying the given taints.
func nodeWithTaints(taints ...corev1.Taint) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "n1"},
		Spec:       corev1.NodeSpec{Taints: taints},
	}
}

var _ = Describe("TaintsChanged", func() {
	taintA := corev1.Taint{Key: "node.scality.io/wp-unreachable", Value: "", Effect: corev1.TaintEffectNoExecute}
	taintB := corev1.Taint{Key: "node.scality.io/cp-unreachable", Value: "", Effect: corev1.TaintEffectNoSchedule}

	It("passes when a taint is added", func() {
		oldNode := nodeWithTaints()
		newNode := nodeWithTaints(taintA)

		result := predicate.TaintsChanged().Update(event.UpdateEvent{
			ObjectOld: oldNode,
			ObjectNew: newNode,
		})

		Expect(result).To(BeTrue())
	})

	It("passes when a taint is removed", func() {
		oldNode := nodeWithTaints(taintA)
		newNode := nodeWithTaints()

		result := predicate.TaintsChanged().Update(event.UpdateEvent{
			ObjectOld: oldNode,
			ObjectNew: newNode,
		})

		Expect(result).To(BeTrue())
	})

	It("filters a reorder of the same taint set", func() {
		oldNode := nodeWithTaints(taintA, taintB)
		newNode := nodeWithTaints(taintB, taintA)

		result := predicate.TaintsChanged().Update(event.UpdateEvent{
			ObjectOld: oldNode,
			ObjectNew: newNode,
		})

		Expect(result).To(BeFalse())
	})

	It("filters an update that changes only a condition/heartbeat field", func() {
		t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		t1 := t0.Add(10 * time.Second)
		oldNode := nodeWithTaints(taintA)
		oldNode.Status.Conditions = []corev1.NodeCondition{{
			Type:              corev1.NodeReady,
			Status:            corev1.ConditionTrue,
			LastHeartbeatTime: metav1.NewTime(t0),
		}}
		newNode := nodeWithTaints(taintA)
		newNode.Status.Conditions = []corev1.NodeCondition{{
			Type:              corev1.NodeReady,
			Status:            corev1.ConditionTrue,
			LastHeartbeatTime: metav1.NewTime(t1),
		}}

		result := predicate.TaintsChanged().Update(event.UpdateEvent{
			ObjectOld: oldNode,
			ObjectNew: newNode,
		})

		Expect(result).To(BeFalse())
	})

	It("filters non-Node objects", func() {
		oldPod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "p1"}}
		newPod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "p1"}}

		result := predicate.TaintsChanged().Update(event.UpdateEvent{
			ObjectOld: oldPod,
			ObjectNew: newPod,
		})

		Expect(result).To(BeFalse())
	})
})

var _ = Describe("ChangedTaintKeys", func() {
	keyA := "node.scality.io/wp-unreachable"
	keyB := "node.scality.io/cp-unreachable"

	It("returns only the keys whose presence changed, not an unrelated one", func() {
		oldTaints := []corev1.Taint{{Key: keyA, Effect: corev1.TaintEffectNoExecute}}
		newTaints := []corev1.Taint{
			{Key: keyA, Effect: corev1.TaintEffectNoExecute},
			{Key: keyB, Effect: corev1.TaintEffectNoSchedule},
		}

		Expect(predicate.ChangedTaintKeys(oldTaints, newTaints)).To(Equal(map[string]bool{keyB: true}))
	})

	It("ignores a value or effect change on the same key", func() {
		oldTaints := []corev1.Taint{{Key: keyA, Value: "old", Effect: corev1.TaintEffectNoExecute}}
		newTaints := []corev1.Taint{{Key: keyA, Value: "new", Effect: corev1.TaintEffectNoSchedule}}

		Expect(predicate.ChangedTaintKeys(oldTaints, newTaints)).To(BeEmpty())
	})

	It("includes a removed key", func() {
		oldTaints := []corev1.Taint{{Key: keyA, Effect: corev1.TaintEffectNoExecute}}

		Expect(predicate.ChangedTaintKeys(oldTaints, nil)).To(Equal(map[string]bool{keyA: true}))
	})
})
