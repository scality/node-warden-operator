# DESIGN.md

Design overview for node-warden-operator. This is the root design
doc: goals, architecture, and the decisions behind them. The full reconcile mechanics
(predicate specifics, the debounce state machine, the guard-fraction math) are written up in
detail once the behavior lands; this doc keeps that part high-level on purpose.

## Goals

- React to node conditions and remediate automatically, so an unhealthy node is dealt with
  instead of silently degrading service.
- Provide a generic, declarative `NodeRemediationPolicy` CRD that maps any node condition to
  remediation(s), so a new condition or use case is covered by adding a CR, not by changing
  code.
- Ship a v1 remediation (a reversible `NoExecute` taint) that is safe enough to run against
  production nodes: debounced against flapping, guarded against over-triggering on a
  cluster-wide event, and self-healing once the condition clears.

## Non-goals

- Detecting node conditions. Conditions are produced by other components (node-problem-detector,
  the kubelet, custom probes, ...); node-warden only reacts to conditions that already exist on
  the `Node` object.
- Adding tolerations for a remediation taint to the infra components that must keep running on
  a remediated node. Which components need the toleration is deployment-specific, so that also
  lives with the deployment.
- Non-taint remediations (`drain`, cordon-only, ...). The `spec.remediations` object is
  designed to grow additively, but v1 implements only `taint`.
- Prometheus alerting on remediation events.

## Architecture: functional core, imperative shell

node-warden separates the decision from the I/O:

- A pure `Decide(facts, now) -> Plan` function holds all the decision logic (condition
  matching, `nodeSelector` matching, debounce, guard fraction). It performs no I/O.
- A thin controller shell wraps it: gather facts from the cluster (OBSERVE), call `Decide`
  (DECIDE), and apply the resulting plan (ACT).

Keeping the decision pure is what makes the tricky parts testable: the debounce state machine,
the guard fraction, and `Unknown`/stale handling are all covered by plain table-driven unit
tests that construct facts and assert on the returned plan, with no cluster or envtest needed.
The shell stays small, so a handful of integration tests cover the wiring.

Directory sketch:

```
api/v1alpha1/          NodeRemediationPolicy types + CEL validation + deepcopy
cmd/main.go            composition root: scheme, manager, watches wiring
config/                CRD, RBAC, manager, samples (example NodeRemediationPolicy manifests)
internal/
  remediation/         pure core: Decide(facts, now) -> Plan
                        (condition match, nodeSelector, debounce state machine, guard fraction)
  controller/          thin shell: OBSERVE -> DECIDE -> ACT (taint apply/remove, status once)
  predicate/           conditionStatusChanged, label-changed, generation-changed
hack/
test/                  envtest + e2e (kind)
```

## Reconcile model: OBSERVE -> DECIDE -> ACT

High level only here; a fuller write-up of the mechanics (predicate specifics, the
debounce-via-`lastTransitionTime` state machine, the guard-fraction math) lands with the
behavior itself.

- **OBSERVE**: the controller watches `Node` and `NodeRemediationPolicy` objects through
  predicates that filter out noise -- kubelet heartbeats, lease/heartbeat-only condition
  churn, and the controller's own status writes -- so the loop only wakes up on changes that
  can actually affect a decision. It then gathers read-only facts: the policies, and per node
  its conditions (with `lastTransitionTime`), labels, and current taints.
- **DECIDE**: the facts and the current time are passed to the pure `Decide` function, which
  evaluates each policy against the nodes it selects and returns a `Plan` -- which taints to
  add or remove, the status to write per policy, and when to requeue.
- **ACT**: the shell applies the plan (taint add/remove on the matched nodes) and writes each
  policy's status exactly once, then requeues if the plan asks for it (e.g. to re-check a
  pending debounce window later).

Because the decision is pure, edge cases -- flapping, a partial outage over the guard,
`Unknown` conditions, recovery, relabeling, self-trigger loops -- are covered by table-driven
unit tests against `Decide`, not by cluster-dependent tests against the controller.

## Notable decisions

- **Validation via CEL.** The CRD's invariants (e.g. `taint.effect` restricted to
  `NoExecute`, `guard.maxAffectedFraction` in `[0,1]`, at least one remediation set) are
  expressed as CEL validation rules on the schema, so no admission webhook is needed today.
  One can still be added later if a check outgrows what CEL can express.
- **Read-modify-write, not Server-Side Apply, for taints.** `node.spec.taints` is a plain
  list on the `Node` object, which node-warden does not own. To avoid clobbering taints set by
  anything else, the controller reads the node, adds or removes only the taint(s) whose key
  matches its own policy, and writes back with conflict retry, rather than server-side-applying
  a list it does not fully own.
- **The remediation taint is `NoExecute` and reversible.** It evicts only pods that do not
  tolerate it; kubelet-managed static pods are unaffected, and workloads that must keep
  running on a remediated node can be given a matching toleration. Removing the taint restores
  normal scheduling.
