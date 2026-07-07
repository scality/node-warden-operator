# CLAUDE.md

Working guide for node-warden-operator. The project's documentation is imported below so it
is always in context -- read it and follow it instead of rediscovering the conventions each
time.

- What the operator does, the `NodeRemediationPolicy` CRD, and how to run it: @README.md
- Architecture, the reconcile model, and design decisions: @DESIGN.md
- Workflow and coding conventions: @CONTRIBUTING.md

## Keep the docs in sync

The docs are part of every change, not an afterthought. In the same PR, update the doc that
owns what you changed:

- behavior, the CRD, flags, or output -> @README.md
- architecture or a design decision -> @DESIGN.md
- conventions or workflow -> @CONTRIBUTING.md

If a change contradicts something written in these docs, fix the docs rather than leaving
them stale.
