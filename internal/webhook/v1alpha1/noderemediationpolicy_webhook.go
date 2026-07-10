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
	"context"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	wardenv1alpha1 "github.com/scality/node-warden-operator/api/v1alpha1"
)

// nolint:unused
// log is for logging in this package.
var noderemediationpolicylog = logf.Log.WithName("noderemediationpolicy-resource")

// SetupNodeRemediationPolicyWebhookWithManager registers the webhook for NodeRemediationPolicy in the manager.
func SetupNodeRemediationPolicyWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &wardenv1alpha1.NodeRemediationPolicy{}).
		WithValidator(&NodeRemediationPolicyCustomValidator{reader: mgr.GetAPIReader()}).
		Complete()
}

// +kubebuilder:webhook:path=/validate-warden-scality-com-v1alpha1-noderemediationpolicy,mutating=false,failurePolicy=fail,sideEffects=None,groups=warden.scality.com,resources=noderemediationpolicies,verbs=create;update,versions=v1alpha1,name=vnoderemediationpolicy-v1alpha1.kb.io,admissionReviewVersions=v1

// NodeRemediationPolicyCustomValidator validates a NodeRemediationPolicy on create and update. Its
// only job is the one invariant CEL cannot express because it spans objects: a policy's taint.key
// must be unique across all policies. node-warden tracks and removes its taint by key alone, so two
// policies sharing a key fight over the same taint (each strips it from nodes the other owns).
type NodeRemediationPolicyCustomValidator struct {
	// reader lists the existing policies to check for a key collision. It is the manager's uncached
	// API reader, so the check sees policies created moments earlier that are not yet in the cache.
	reader client.Reader
}

// ValidateCreate rejects a new policy whose taint.key is already owned by another policy.
func (v *NodeRemediationPolicyCustomValidator) ValidateCreate(ctx context.Context, obj *wardenv1alpha1.NodeRemediationPolicy) (admission.Warnings, error) {
	noderemediationpolicylog.Info("validating NodeRemediationPolicy on create", "name", obj.GetName())
	return nil, v.validateUniqueTaintKey(ctx, obj)
}

// ValidateUpdate re-checks uniqueness on update. The taint is immutable (a CEL rule), so its key
// cannot actually change, but validating anyway keeps the guarantee independent of that rule.
func (v *NodeRemediationPolicyCustomValidator) ValidateUpdate(ctx context.Context, _, newObj *wardenv1alpha1.NodeRemediationPolicy) (admission.Warnings, error) {
	noderemediationpolicylog.Info("validating NodeRemediationPolicy on update", "name", newObj.GetName())
	return nil, v.validateUniqueTaintKey(ctx, newObj)
}

// ValidateDelete is a no-op: deleting a policy never breaks another policy's key ownership.
func (v *NodeRemediationPolicyCustomValidator) ValidateDelete(_ context.Context, _ *wardenv1alpha1.NodeRemediationPolicy) (admission.Warnings, error) {
	return nil, nil
}

// validateUniqueTaintKey rejects policy if any other NodeRemediationPolicy already declares a taint
// with the same key. A TOCTOU race (two policies with the same key admitted in the same instant)
// cannot be fully closed by a webhook; the controller's key-based cleanup remains the backstop.
func (v *NodeRemediationPolicyCustomValidator) validateUniqueTaintKey(ctx context.Context, policy *wardenv1alpha1.NodeRemediationPolicy) error {
	if policy.Spec.Remediations.Taint == nil {
		return nil // no taint to collide with; CEL owns the "at least one remediation" rule
	}
	key := policy.Spec.Remediations.Taint.Key

	var policies wardenv1alpha1.NodeRemediationPolicyList
	if err := v.reader.List(ctx, &policies); err != nil {
		return apierrors.NewInternalError(err)
	}

	for i := range policies.Items {
		other := &policies.Items[i]
		if other.Name == policy.Name {
			continue // the policy itself (on update); names are unique for a cluster-scoped resource
		}
		if other.Spec.Remediations.Taint != nil && other.Spec.Remediations.Taint.Key == key {
			return apierrors.NewInvalid(
				wardenv1alpha1.GroupVersion.WithKind("NodeRemediationPolicy").GroupKind(),
				policy.Name,
				field.ErrorList{field.Forbidden(
					field.NewPath("spec", "remediations", "taint", "key"),
					"taint.key "+key+" is already used by NodeRemediationPolicy "+other.Name+
						"; each policy must own a unique taint key",
				)},
			)
		}
	}
	return nil
}
