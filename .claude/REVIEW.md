# Review criteria

Read by the `/review-pr` skill (Scality agent hub) and by anyone reviewing by hand.
Flag problems only — see "What not to flag" at the end.

## What this repo is

`node-warden-operator` is a cluster-scoped Kubernetes operator that watches Node
conditions and remediates the affected nodes, driven by a generic
`NodeRemediationPolicy` custom resource. The decision logic is a pure functional
core in `internal/remediation` (`Decide(facts, now) -> Plan`); the reconciler only
gathers facts and applies the plan. Architecture: `DESIGN.md`.

## Criteria

| Area | What to check |
|------|---------------|
| Functional core purity | The decision logic in `internal/remediation` (`Decide(facts, now) -> Plan`) must stay I/O-free: no client/API calls, no clock reads (time enters via `now`), no logging or other side effects. Decisions belong here, not inlined into `Reconcile`. |
| Reconcile idempotency | `Reconcile` must be safe to run repeatedly for the same object, hold no state across calls, and converge to the desired state regardless of the starting point. |
| Taint remediation safety | Taint apply/remove is read-modify-write with conflict retry (not Server-Side Apply); touches only the policy's own taint key; stays `NoExecute` and reversible; must not evict kubelet-managed static pods (apiserver, etcd, scheduler, controller-manager). |
| Debounce & guard | Debounce derived from the condition's `lastTransitionTime`; `guard.maxAffectedFraction` respected and within `[0,1]`; `Unknown`/stale conditions handled explicitly, never treated as healthy. |
| Watch predicates | Predicates must filter noise (kubelet heartbeats, lease/heartbeat-only condition churn, the controller's own status writes) to avoid self-trigger loops and needless reconciles. |
| RBAC scoping | `+kubebuilder:rbac` markers grant least privilege and match what the code actually reads/writes; `config/rbac` regenerated after changes. |
| Status subresource | Status written via the status subresource, once per reconcile, using standard `metav1.Condition` conventions (type/status/reason/lastTransitionTime). |
| CRD / API compatibility | `v1alpha1` changes stay backward compatible where possible; invariants enforced by CEL validation markers (e.g. `taint.effect` restricted to `NoExecute`, fraction in `[0,1]`, at least one remediation set) rather than only in Go. |
| Generated code in sync | After editing `api/` types or kubebuilder markers, `zz_generated.deepcopy.go` and `config/crd` must be regenerated (`make generate manifests`) and committed in the same PR. |
| Error wrapping | Wrap with `fmt.Errorf("...: %w", err)` (not `%v`); don't swallow errors; return them so controller-runtime can requeue. |
| Context propagation | Thread the `ctx` from `Reconcile` through every client call; respect cancellation; don't spawn detached background contexts. |
| Logging | Use the `logr` logger from `logf.FromContext(ctx)` with structured key/values (logcheck enforces the k8s logging conventions); no `fmt.Print*` or stdlib `log`. |
| Concurrency | Any goroutines have clear exit conditions and no leaks; shared state is guarded. |
| Docs sync | Behavior / CRD / flags / output -> `README.md`; architecture or a design decision -> `DESIGN.md`; conventions or workflow -> `CONTRIBUTING.md`. Flag docs left stale by the change. |
| Security | No secrets, tokens, or keys in code or samples; HTTP/2 stays disabled unless intentionally enabled; the metrics endpoint stays behind authn/authz. |
| Breaking changes | Anything that changes the CRD schema, public Go APIs, flags, or the manager's behavior in a non-additive way. |

## What not to flag

- Anything the linters already own: `golangci-lint` (errcheck, gocyclo, revive,
  staticcheck, logcheck, depguard, misspell…), `gofmt`, `goimports`.
- Generated files (`zz_generated.*`, `config/crd`) except when they are stale with
  respect to the sources changed in the same PR.
- Markdown or comment wording preferences.
- Refactors unrelated to the PR's purpose.
