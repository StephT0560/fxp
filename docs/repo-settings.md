# Repository settings checklist (manual)

Round 4 Low #3 remains an operator action until this ruleset is active. Merging
CI configuration alone does not prevent merging a red PR. No settings are
changed by this branch, and auto-merge is not enabled.

1. After this PR has produced a successful `ci-success` check, open
   **Settings → Rules → Rulesets → New ruleset → New branch ruleset**.
2. Name it `main-required-ci`, set **Enforcement status: Active**, leave the
   bypass list empty, and under **Target branches → Add target → Include by
   pattern**, enter `main` (not all branches).
3. Under **Branch rules**, enable **Require a pull request before merging**.
   The minimum required approval count may remain zero for a single maintainer;
   the PR and CI gate are still required.
4. Enable **Require status checks to pass**, click **Add checks**, select exactly
   `ci-success`, and select **GitHub Actions** as its expected source. Enable
   **Require branches to be up to date before merging**. Do not require each
   matrix leg separately, benchmarks, or release checks. Save/create the ruleset.
5. Enable **Block force pushes** and **Restrict deletions** in the same ruleset.
   Confirm the ruleset is Active and targets `main` with no bypass actors.
6. In **Settings → General → Pull Requests**, enable **Automatically delete head
   branches**. This affects future merges; inspect and manually delete these
   already-merged Dependabot branches in **Code → Branches**:
   - `dependabot/cargo/fxp_core/criterion-0.8.2` (PR #6)
   - `dependabot/cargo/fxp_core/prost-build-0.14.4` (PR #7)
   - `dependabot/github_actions/actions/setup-go-7` (PR #8)
   Confirm each full branch name and merged status before deletion.

Verification: open a small PR, confirm `ci-success` is required and pending until
all CI jobs finish, and confirm an out-of-date branch must be updated. A failed,
cancelled, or skipped dependency makes the gate fail. The `go` dependency is the
combined matrix result; both legs have `fail-fast: false` and neither may use
`continue-on-error`. Keep the gate's `needs` list synchronized with every other
job; `tests/ci/test_gate.py` checks this invariant in CI. Keep its check name stable.

CI uses `pull_request` without actor/path filters, `contents: read`, no private
secrets, no PR comments, and no `pull_request_target`. Dependabot can use the
automatically supplied read-only `GITHUB_TOKEN` for public tool downloads.
