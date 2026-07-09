---
name: review-pr
description: Review a PR on node-warden-operator (a cluster-scoped Kubernetes operator that remediates unhealthy node conditions via a NodeRemediationPolicy CRD)
argument-hint: <pr-number-or-url>
disable-model-invocation: true
allowed-tools: Read, Bash(gh repo view *), Bash(gh pr view *), Bash(gh pr diff *), Bash(gh pr comment *), Bash(gh api *), Bash(git diff *), Bash(git log *), Bash(git show *)
---

# Review GitHub PR

You are an expert code reviewer. Review this PR: $ARGUMENTS

## Determine PR target

Parse `$ARGUMENTS` to extract the repo and PR number:

- If arguments contain `REPO:` and `PR_NUMBER:` (CI mode), use those values directly.
- If the argument is a GitHub URL (starts with `https://github.com/`), extract `owner/repo` and the PR number from it.
- If the argument is just a number, use the current repo from `gh repo view --json nameWithOwner -q .nameWithOwner`.

## Output mode

- **CI mode** (arguments contain `REPO:` and `PR_NUMBER:`): post inline comments and summary to GitHub.
- **Local mode** (all other cases): output the review as text directly. Do NOT post anything to GitHub.

## Steps

1. **Fetch PR details:**

```bash
gh pr view <number> --repo <owner/repo> --json title,body,headRefOid,author,files
gh pr diff <number> --repo <owner/repo>
```

2. **Read changed files** to understand the full context around each change (not just the diff hunks).

3. **Analyze the changes** against these criteria:

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

4. **Deliver your review:**

### If CI mode: post to GitHub

#### Part A: Inline file comments

For each issue, post a comment on the exact file and line. Keep comments short (1-3 sentences), end with `— Claude Code`. Use line numbers from the **new version** of the file.

**Without suggestion block** — single-line command, `<br>` for line breaks:
```bash
gh api -X POST -H "Accept: application/vnd.github+json" "repos/<owner/repo>/pulls/<number>/comments" -f body="Issue description.<br><br>— Claude Code" -f path="file" -F line=42 -f side="RIGHT" -f commit_id="<headRefOid>"
```

**With suggestion block** — use a heredoc (`-F body=@-`) so code renders correctly:
```bash
gh api -X POST -H "Accept: application/vnd.github+json" "repos/<owner/repo>/pulls/<number>/comments" -F body=@- -f path="file" -F line=42 -f side="RIGHT" -f commit_id="<headRefOid>" <<'COMMENT_BODY'
Issue description.

```suggestion
first line of suggested code
second line of suggested code
```

— Claude Code
COMMENT_BODY
```

Only suggest when you can show the exact replacement. For architectural or design issues, just describe the problem.

#### Part B: Summary comment

Single-line command, `<br>` for line breaks. No markdown headings — they render as giant bold text. Flat bullet list only:

```bash
gh pr comment <number> --repo <owner/repo> --body "- file:line — issue<br>- file:line — issue<br><br>Review by Claude Code"
```

If no issues: just say "LGTM". End with: `Review by Claude Code`

### If local mode: output the review as text

Do NOT post anything to GitHub. Instead, output the review directly as text.

For each issue found, output:

```
**<file_path>:<line_number>** — <what's wrong and how to fix it>
```

When the fix is a concrete line change, include a fenced code block showing the suggested replacement.

At the end, output a summary section listing all issues. If no issues: just say "LGTM".

End with: `Review by Claude Code`

## What NOT to do

- Do not comment on markdown formatting preferences
- Do not suggest refactors unrelated to the PR's purpose
- Do not praise code — only flag problems or stay silent
- If no issues are found, post only a summary saying "LGTM"
- Do not flag style issues already covered by the project's linter (golangci-lint: errcheck, gocyclo, revive, staticcheck, logcheck, depguard, misspell, ...)
