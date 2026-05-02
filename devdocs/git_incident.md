# Git History Cleanup Procedure

For commits that must be removed from history (large blobs, sensitive data) rather than reverted.

The risk: after a force push, anyone who already fetched the bad commit can accidentally re-introduce it on their next push.

## Steps

0. Tell the team.
1. Fix main locally.
2. Add a placeholder commit noting the cleanup (`git commit --allow-empty`).
3. Force push the fixed main branch.
4. Update `.github/queue-gate-ancestor` to the SHA of your placeholder commit.
5. Add the bad commit's subject to `.github/queue-gate-bad-subjects`.
6. Force push to main.
7. Tell the team.

## Gate Files

Both are fetched live from `origin/main` by CI (`.buildkite/steps/commit-validation.sh`), so updates take effect immediately for all queue pushes.

| File | Purpose |
|------|---------|
| `.github/queue-gate-ancestor` | SHA that all queue branches must include as an ancestor |
| `.github/queue-gate-bad-subjects` | Commit subjects that are blocked (one per line) |
