[![Post Merge](https://github.com/scality/node-warden-operator/actions/workflows/post-merge.yaml/badge.svg)](https://github.com/scality/node-warden-operator/actions/workflows/post-merge.yaml)
[![GitHub release](https://img.shields.io/github/v/release/scality/node-warden-operator)](https://github.com/scality/node-warden-operator/releases/latest)
[![Go version](https://img.shields.io/github/go-mod/go-version/scality/node-warden-operator)](go.mod)
[![License](https://img.shields.io/github/license/scality/node-warden-operator)](LICENSE)

# node-warden-operator

A cluster-scoped Kubernetes operator that reacts to node conditions and remediates them.

## Description

node-warden watches Kubernetes Node conditions and remediates affected nodes. It is driven by
a generic `NodeRemediationPolicy` custom resource (cluster-scoped) that maps a node condition
to one or more remediations. For v1, the only remediation is a reversible taint whose effect the
policy chooses: `NoExecute` evicts non-tolerating pods and drops the node from Service endpoints,
so traffic stops being routed to it, while `NoSchedule`/`PreferNoSchedule` only keep new pods off
the node. The taint's identity (key, value, effect) is immutable once the policy exists; delete and
recreate the policy to change it. Each policy owns its taint by `key`, so different policies need
different `taint.key`s -- a validating webhook rejects a policy whose key another policy already
uses (two policies sharing a key would fight over the same taint). The taint is removed
automatically once the condition clears, and when the policy is deleted -- a finalizer removes it
from the affected nodes first.

Every action is observable: the policy `status` reports the selected, matched, pending, held and
remediated nodes, and the operator emits Kubernetes `Events` -- `TaintApplied`/`TaintRemoved`
on both the affected node (`kubectl describe node`) and the policy (`kubectl describe nrp`), and
`GuardTripped`/`InvalidSpec`/`MissingTransitionTime` on the policy -- alongside structured logs.

## Deploy

The operator image is built and published to `ghcr.io/scality/node-warden-operator` by CI. The
validating webhook is served with a cert-manager-issued certificate, so
[cert-manager](https://cert-manager.io/) must already be installed in the cluster. Deploy the
operator into the cluster your `kubectl` currently targets:

```sh
make install   # install the CRD
make deploy    # deploy the controller (image: ghcr.io/scality/node-warden-operator:latest)
```

Then create a `NodeRemediationPolicy`. A commented example lives in
[`config/samples/`](config/samples/warden_v1alpha1_noderemediationpolicy.yaml); apply it (and edit
the condition, taint and thresholds for your environment) with:

```sh
kubectl apply -k config/samples/
```

## Uninstall

```sh
kubectl delete -k config/samples/   # remove the policies
make undeploy                       # remove the controller
make uninstall                      # remove the CRD
```

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for the development workflow (building and testing) and
[DESIGN.md](DESIGN.md) for how the operator is built.

## License

See [LICENSE](LICENSE).
