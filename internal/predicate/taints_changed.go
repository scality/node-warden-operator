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

package predicate

import (
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

// TaintsChanged passes a Node update when the set of taint keys changed (a key added or
// removed), so the operator re-asserts a taint removed out of band and re-observes after its own
// writes. Taints are stable across kubelet heartbeats, so this does not cause a reconcile storm.
func TaintsChanged() predicate.Predicate {
	return predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldNode, ok1 := e.ObjectOld.(*corev1.Node)
			newNode, ok2 := e.ObjectNew.(*corev1.Node)
			if !ok1 || !ok2 {
				return false
			}
			return len(ChangedTaintKeys(oldNode.Spec.Taints, newNode.Spec.Taints)) > 0
		},
	}
}

// ChangedTaintKeys returns the set of taint keys whose presence differs between old and new (a
// key added or removed), ignoring ordering. The operator applies a taint only when its key is
// absent and removes it by key, so only the presence of a key -- not its value or effect --
// matters for deciding which policies to enqueue.
func ChangedTaintKeys(oldTaints, newTaints []corev1.Taint) map[string]bool {
	oldKeys := taintKeySet(oldTaints)
	newKeys := taintKeySet(newTaints)
	changed := make(map[string]bool)
	for key := range oldKeys {
		if !newKeys[key] {
			changed[key] = true
		}
	}
	for key := range newKeys {
		if !oldKeys[key] {
			changed[key] = true
		}
	}
	return changed
}

func taintKeySet(taints []corev1.Taint) map[string]bool {
	m := make(map[string]bool, len(taints))
	for i := range taints {
		m[taints[i].Key] = true
	}
	return m
}
