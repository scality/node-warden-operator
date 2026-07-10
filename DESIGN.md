# DESIGN.md

Design overview for node-warden-operator. This is the root design
doc: the goals, the architecture, the reconcile mechanics (the debounce state machine, the
guard-percentage math, the watch predicates), and the decisions behind them.

## Goals

- React to node conditions and remediate automatically, so an unhealthy node is dealt with
  instead of silently degrading service.
- Provide a generic, declarative `NodeRemediationPolicy` CRD that maps any node condition to
  remediation(s), so a new condition or use case is covered by adding a CR, not by changing
  code.
- Ship a v1 remediation (a reversible taint, effect chosen by the policy) that is safe enough to
  run against production nodes: debounced against flapping, guarded against over-triggering on a
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

- A pure `Decide(facts, now) -> Decision` function holds all the decision logic (condition
  matching, `nodeSelector` matching, debounce, guard percentage). It performs no I/O.
- A thin controller shell wraps it: gather facts from the cluster (OBSERVE), call `Decide`
  (DECIDE), and apply the resulting decision (ACT).

Keeping the decision pure is what makes the tricky parts testable: the debounce state machine,
the guard percentage, and `Unknown`/stale handling are all covered by plain table-driven unit
tests that construct facts and assert on the returned decision, with no cluster or envtest needed.
The shell stays small, so a handful of integration tests cover the wiring.

Directory sketch:

```
api/v1alpha1/          NodeRemediationPolicy types + CEL validation + deepcopy
cmd/main.go            composition root: scheme, manager, watches wiring
config/                CRD, RBAC, manager, samples (example NodeRemediationPolicy manifests)
internal/
  remediation/         pure core: Decide(facts, now) -> Decision
                        (condition match, nodeSelector, debounce state machine, guard percentage)
  controller/          thin shell: OBSERVE -> DECIDE -> ACT (taint apply/remove, status once)
  predicate/           conditionStatusChanged, taintsChanged
hack/
test/                  envtest + e2e (kind)
```

## Reconcile model: OBSERVE -> DECIDE -> ACT

- **OBSERVE**: the controller watches `Node` and `NodeRemediationPolicy` objects. `Node` updates
  go through predicates that drop noise -- kubelet heartbeats and lease/heartbeat-only condition
  churn -- so the loop only wakes on changes that can affect a decision. Policy updates are not
  filtered: the loop is idempotent (status is written only when it changed), so the controller's
  own status writes settle in a no-op pass instead of looping, and a hand-edited status is
  reconciled back. It then gathers read-only facts: the policies, and per node the one condition
  its policy watches (with `lastTransitionTime`), labels, and current taints.
- **DECIDE**: the facts and the current time are passed to the pure `Decide` function, which
  evaluates each policy against the nodes it selects and returns a `Decision` -- which taints to
  add or remove, the status to write per policy, and when to requeue.
- **ACT**: the shell applies the decision (taint add/remove on the matched nodes) and writes each
  policy's status exactly once, then requeues if it asks for it (e.g. to re-check a pending
  debounce window later). A per-node taint write that fails does not abort the others: the errors
  are aggregated and returned so the failed nodes are retried on the requeue, while the healthy
  nodes are still remediated. It also emits structured logs and Kubernetes `Events` for what
  happened (see below), so an operator can follow the decisions without reading logs.

Because the decision is pure, edge cases -- flapping, a partial outage over the guard,
`Unknown` conditions, recovery, relabeling, self-trigger loops -- are covered by table-driven
unit tests against `Decide`, not by cluster-dependent tests against the controller.

### The debounce state machine

Debounce is measured from the watched condition's `lastTransitionTime`, not from a timer the
operator keeps: `stableFor = now - lastTransitionTime`. For a selected node whose condition matches:

- if it is not yet tainted and `stableFor >= debounce.enter`, the taint is applied; otherwise the
  node is *pending* and the reconcile is requeued after `enter - stableFor`;
- once tainted, it stays tainted while the condition holds (steady state, a no-op pass).

For a selected node whose condition has determinately cleared (present, not the trigger status, not
`Unknown`) and still carries the taint: if `stableFor >= debounce.exit` the taint is removed,
otherwise the reconcile is requeued after `exit - stableFor`.

Two situations are *held* rather than acted on:

- **`Unknown` or unreported.** The detector is down or the node is unreachable; recovery is never
  assumed, so the node keeps whatever taint it has and is surfaced in `status.unknownNodes`.
- **No `lastTransitionTime`.** Some detectors omit it, so stability cannot be measured. Rather than
  bypass the debounce on a saturated `now - zero`, the node is held and surfaced in
  `status.heldNodes` alongside a `MissingTransitionTime` event.

The requeue the pass returns is the smallest positive per-node wait, so a single reconcile re-checks
every pending window at the right time.

### The guard percentage

The guard bounds the blast radius, so a cluster-wide event (a detector bug or a network partition
tripping the condition on many nodes at once) does not remediate the whole fleet. It is evaluated
over the *determinate* nodes only -- the selected nodes whose condition is matched or determinately
cleared; `Unknown`/unreported nodes carry no signal and are excluded, so a partial detector outage
cannot dilute the denominator exactly when it matters.

The comparison is exact-rational integer math (no float): with `matched` matching nodes out of
`determinate` determinate ones, the guard trips when

```
matched * 100 > maxAffectedPercent * determinate
```

The `>` is strict, so a fraction exactly at the limit does not trip. A tripped guard blocks only
*new* taint applications (matching-but-untainted nodes stay pending); it never blocks removal or
recovery, and never disturbs nodes already remediated. `maxAffectedPercent: 100` disables it.

### The watch predicates

`Node` objects change constantly (kubelet heartbeats every few seconds), so an unfiltered watch
would wake the loop for nothing. Two predicates gate `Node` events:

- **`conditionStatusChanged`** passes an update only when the *watched* condition's status changed,
  or that condition appeared or disappeared. Heartbeat-only churn (a bumped `lastHeartbeatTime`),
  lease renewals, and changes to conditions the policy does not watch are all dropped.
- **`taintsChanged`** passes an update when the node's taint set changed, so the loop re-observes
  after its own (or an external) taint write and converges `status.remediatedNodes`.

A `Node` event is mapped to precisely the policies the change can affect -- the diff is computed in
the enqueue handler, not by re-listing every policy. The `NodeRemediationPolicy` watch, by
contrast, carries no generation-changed predicate: a deletion sets `deletionTimestamp` without
bumping the generation, and the loop must still wake to run the finalizer cleanup.

## Notable decisions

- **Validation via CEL.** The CRD's invariants -- `taint.effect` restricted to a real Kubernetes
  taint effect, `taint.key` a valid qualified name (otherwise every node patch is rejected),
  `debounce.enter`/`exit` a non-negative Go duration (otherwise decode stalls the reconcile), and
  at least one remediation set -- are expressed as CEL validation rules on the schema, so no
  admission webhook is needed today. One can still be added later if a check outgrows what CEL can
  express.
- **Read-modify-write, not Server-Side Apply, for taints.** `node.spec.taints` is a plain
  list on the `Node` object, which node-warden does not own. To avoid clobbering taints set by
  anything else, the controller reads the node, adds or removes only the taint(s) whose key
  matches its own policy, and writes back with conflict retry, rather than server-side-applying
  a list it does not fully own.
- **A policy's `taint.key` is its identity, and must be unique across policies.** node-warden
  tracks and cleans up its taint purely by `key` -- it does not tag ownership -- so two policies
  that share a `taint.key` interfere: each removes that key from every node it does not select, so
  one policy strips the taint another legitimately applied, and the node flaps. CEL cannot enforce
  uniqueness across objects, so for v1 this is a documented operator constraint; a future
  validating webhook can reject a duplicate key (and the exact key/duration limits CEL only
  approximates) at admission.
- **The remediation taint is reversible; its effect is the policy's choice.** `effect` is
  validated to a real Kubernetes taint effect (`NoSchedule`, `PreferNoSchedule`, `NoExecute`), so
  an invalid value is rejected at admission instead of failing every `Node` update. `NoExecute`
  evicts pods that do not tolerate it (kubelet-managed static pods are unaffected; workloads that
  must keep running can carry a matching toleration); the softer effects only stop new
  scheduling. Removing the taint restores normal scheduling.
- **The taint is immutable once set.** A CEL transition rule (`self == oldSelf`) rejects any
  change to `remediations.taint` on an existing policy. The operator tracks and removes the taint
  by its identity, so allowing the key, value or effect to change would orphan the taint already
  applied; keeping it immutable means observe/apply/remove stay keyed on a single, stable
  identity. Changing the remediation means deleting and recreating the policy (the finalizer
  cleans up first).
- **A finalizer removes the taints on deletion.** Deleting a policy must not strand its taints on
  the nodes, so the operator sets a finalizer and, on deletion, removes every taint carrying the
  policy's key before letting the object go. This is also why the policy watch carries no
  generation-changed predicate: a deletion sets `deletionTimestamp` without bumping generation, and
  the loop must still wake to run the cleanup.
- **Events go on the object each fact is about.** A per-node action (`TaintApplied`,
  `TaintRemoved`) is recorded on both the affected `Node` -- so `kubectl describe node` explains
  why the node is tainted, like the node-lifecycle controllers do -- and on the policy. A
  policy-level decision (`GuardTripped`, `InvalidSpec`, `MissingTransitionTime`) is recorded only
  on the policy, since it is not about any single node. Node events carry the node's UID so they
  surface under `kubectl describe node`.
