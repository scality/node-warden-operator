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

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	wardenv1alpha1 "github.com/scality/node-warden-operator/api/v1alpha1"
)

// NodeRemediationPolicyReconciler reconciles a NodeRemediationPolicy object
type NodeRemediationPolicyReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=warden.scality.com,resources=noderemediationpolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=warden.scality.com,resources=noderemediationpolicies/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=warden.scality.com,resources=noderemediationpolicies/finalizers,verbs=update

// Reconcile is intentionally a no-op for now: this is the scaffolding PR. The
// observe/decide/act logic (condition matching, debounce, guard, taint apply/remove)
// lands in a follow-up PR alongside the NodeRemediationPolicy API.
func (r *NodeRemediationPolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	_ = logf.FromContext(ctx)
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *NodeRemediationPolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&wardenv1alpha1.NodeRemediationPolicy{}).
		Named("noderemediationpolicy").
		Complete(r)
}
