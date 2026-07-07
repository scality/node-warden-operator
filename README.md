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
recreate the policy to change it. The taint is removed automatically once the condition clears.

## Deploy

The operator image is built and published to `ghcr.io/scality/node-warden-operator` by CI.
Deploy the latest release into the cluster your `kubectl` currently targets:

```sh
make install   # install the CRD
make deploy    # deploy the controller (image: ghcr.io/scality/node-warden-operator:latest)
```

Then create a `NodeRemediationPolicy`. The CRD and sample manifests land in a later pull
request; once available, the samples can be applied with:

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
