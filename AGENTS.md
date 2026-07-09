# AGENTS.md

**Before you change anything, open and read these three docs in full.** They define what
the operator is and the conventions to follow, so you work from them instead of guessing.
This file is deliberately short and does not duplicate them:

- `README.md` -- what the operator does, the `NodeRemediationPolicy` CRD, and how to run it.
- `DESIGN.md` -- the architecture, the reconcile model, and the decisions behind them.
- `CONTRIBUTING.md` -- the development workflow and the coding conventions.

`CLAUDE.md` is the same guide for Claude Code -- it imports these docs into context directly
(`@file`); this file is the equivalent for every other agent, pointing at them explicitly.
Keep the two in sync.

## Keep the docs in sync

The docs are part of every change, not an afterthought. In the same PR, update the doc that
owns what you changed:

- behavior, the CRD, flags, or output -> `README.md`
- architecture or a design decision -> `DESIGN.md`
- conventions or workflow -> `CONTRIBUTING.md`

If a change contradicts something written in these docs, fix the docs rather than leaving
them stale.
