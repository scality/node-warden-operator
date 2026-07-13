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
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	errors "github.com/scality/go-errors"

	"github.com/scality/node-warden-operator/internal/remediation"
)

var (
	ErrApplyTaint  = errors.New("apply node taint")
	ErrRemoveTaint = errors.New("remove node taint")
)

// taintBackoff mirrors the retry backoff Kubernetes' own AddOrUpdateTaintOnNode uses: a handful
// of attempts with a generous, jittered delay so the read/write settles under Node churn.
var taintBackoff = wait.Backoff{Steps: 5, Duration: 100 * time.Millisecond, Jitter: 1.0}

// EnsureTaint adds the taint to the node if it is absent and reports whether the node changed. It
// is a no-op if a taint with the same key is already present.
func EnsureTaint(ctx context.Context, c client.Client, apiReader client.Reader, nodeName string, t remediation.Taint) (bool, error) {
	changed, err := patchTaintList(ctx, c, apiReader, nodeName, func(taints []corev1.Taint) ([]corev1.Taint, bool) {
		for _, existing := range taints {
			if existing.Key == t.Key {
				return nil, false // already present, nothing to do
			}
		}
		return append(taints, corev1.Taint{
			Key:    t.Key,
			Value:  t.Value,
			Effect: corev1.TaintEffect(t.Effect),
		}), true
	})
	if err != nil {
		return false, errors.Wrap(ErrApplyTaint,
			errors.WithProperty("node", nodeName),
			errors.WithProperty("key", t.Key),
			errors.CausedBy(err))
	}
	return changed, nil
}

// RemoveTaint removes any taint with the given key from the node and reports whether the node
// changed. It is a no-op if no such taint is present.
func RemoveTaint(ctx context.Context, c client.Client, apiReader client.Reader, nodeName, key string) (bool, error) {
	changed, err := patchTaintList(ctx, c, apiReader, nodeName, func(taints []corev1.Taint) ([]corev1.Taint, bool) {
		kept := make([]corev1.Taint, 0, len(taints))
		for _, existing := range taints {
			if existing.Key != key {
				kept = append(kept, existing)
			}
		}
		if len(kept) == len(taints) {
			return nil, false // not present, nothing to do
		}
		return kept, true
	})
	if err != nil {
		return false, errors.Wrap(ErrRemoveTaint,
			errors.WithProperty("node", nodeName),
			errors.WithProperty("key", key),
			errors.CausedBy(err))
	}
	return changed, nil
}

// patchTaintList applies mutate to the node's taint list under read-modify-write with conflict
// retry. It always reads the node from the API server (apiReader, uncached), not the cache: this
// is a read-modify-write on an object the operator does not own, and a stale cache could turn a
// needed apply/remove into a silent no-op -- and a no-op is not a conflict, so RetryOnConflict
// would never re-read to correct it. Taint writes happen only on a remediation transition (rare),
// so the extra read is cheap. The write is a taints-scoped strategic merge patch with an optimistic
// lock, so it touches only spec.taints and 409s on a concurrent change (node.spec.taints is an
// atomic list the operator does not own, so a targeted patch is used rather than server-side apply).
// mutate takes the node's current taints and returns the updated list plus whether it changed
// anything; when it reports no change the node is left untouched and no patch is sent.
func patchTaintList(ctx context.Context, c client.Client, apiReader client.Reader, nodeName string, mutate func(taints []corev1.Taint) ([]corev1.Taint, bool)) (bool, error) {
	changed := false
	err := retry.RetryOnConflict(taintBackoff, func() error {
		changed = false
		var node corev1.Node
		if err := apiReader.Get(ctx, client.ObjectKey{Name: nodeName}, &node); err != nil {
			return err
		}
		updated, ok := mutate(node.Spec.Taints)
		if !ok {
			return nil // nothing to change; skip the deep copy and the write
		}
		patch := client.StrategicMergeFrom(node.DeepCopy(), client.MergeFromWithOptimisticLock{})
		node.Spec.Taints = updated
		if err := c.Patch(ctx, &node, patch); err != nil {
			return err
		}
		changed = true
		return nil
	})
	return changed, err
}
