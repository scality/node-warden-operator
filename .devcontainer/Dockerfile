FROM mcr.microsoft.com/devcontainers/go:1.26-trixie@sha256:c249440b024e82a569726ab41e60400237665d1f259fb575fd707b04b1b44e55

USER vscode

# Each CLI below drops its zsh completion here; the dir is on zsh's default $fpath.
RUN sudo mkdir -p /usr/local/share/zsh/site-functions

# renovate: datasource=github-releases depName=golangci/golangci-lint
ARG GOLANGCI_VERSION=v2.12.2
# Fetch the install script pinned to the release tag (not HEAD) before piping to sh.
RUN curl -sSfL "https://raw.githubusercontent.com/golangci/golangci-lint/${GOLANGCI_VERSION}/install.sh" \
  | sudo sh -s -- -b /usr/local/bin ${GOLANGCI_VERSION} \
  && golangci-lint completion zsh | sudo tee /usr/local/share/zsh/site-functions/_golangci-lint >/dev/null

# renovate: datasource=github-releases depName=kubernetes-sigs/kind
ARG KIND_VERSION=v0.32.0
RUN curl -sSfLo /tmp/kind "https://kind.sigs.k8s.io/dl/${KIND_VERSION}/kind-linux-amd64" \
  && sudo install -m 0755 /tmp/kind /usr/local/bin/kind \
  && rm /tmp/kind \
  && kind completion zsh | sudo tee /usr/local/share/zsh/site-functions/_kind >/dev/null

# renovate: datasource=github-releases depName=kubernetes-sigs/kubebuilder
ARG KUBEBUILDER_VERSION=v4.15.0
RUN curl -sSfLo /tmp/kubebuilder "https://github.com/kubernetes-sigs/kubebuilder/releases/download/${KUBEBUILDER_VERSION}/kubebuilder_linux_amd64" \
  && sudo install -m 0755 /tmp/kubebuilder /usr/local/bin/kubebuilder \
  && rm /tmp/kubebuilder \
  && kubebuilder completion zsh | sudo tee /usr/local/share/zsh/site-functions/_kubebuilder >/dev/null

# renovate: datasource=github-releases depName=kubernetes/kubernetes
ARG KUBECTL_VERSION=v1.36.2
RUN curl -sSfLo /tmp/kubectl "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/amd64/kubectl" \
  && sudo install -m 0755 /tmp/kubectl /usr/local/bin/kubectl \
  && rm /tmp/kubectl \
  && kubectl completion zsh | sudo tee /usr/local/share/zsh/site-functions/_kubectl >/dev/null

# renovate: datasource=github-releases depName=kubernetes-sigs/kustomize extractVersion=^kustomize/(?<version>v.+)$
ARG KUSTOMIZE_VERSION=v5.8.1
RUN curl -sSfLo /tmp/kustomize.tar.gz "https://github.com/kubernetes-sigs/kustomize/releases/download/kustomize%2F${KUSTOMIZE_VERSION}/kustomize_${KUSTOMIZE_VERSION}_linux_amd64.tar.gz" \
  && tar -C /tmp -xzf /tmp/kustomize.tar.gz kustomize \
  && sudo install -m 0755 /tmp/kustomize /usr/local/bin/kustomize \
  && rm /tmp/kustomize.tar.gz /tmp/kustomize \
  && kustomize completion zsh | sudo tee /usr/local/share/zsh/site-functions/_kustomize >/dev/null
