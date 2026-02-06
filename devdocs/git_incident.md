# Git Industrial Accident Recovery

Most bad commits can be fixed by adding an additional revert commit.

Sometimes, like committing giant blobs or sensitive data, we want to actually force push to main to remove that commit from history.

The trouble is that it's really easy to accidentally re-introduce that commit, if others have already fetched it.

If you need to remove a commit from history, follow these steps.

0. Tell the team.
1. Fix main locally.
2. Add a new placeholder commit that says that a cleanup happened (`git commit --allow-empty`).
3. Force push that fixed main branch.
4. Change the fix SHA in `queue-gate-ancestor` to the SHA of your placeholder commit.
5. Add the bad commit's subject to `queue-gate-bad-subjects`.
6. Force push to main.
7. Tell the team.

The gate files you updated prevent accidental reintroduction. Both are fetched live from `origin/main` by CI, so updates take effect immediately for all queue pushes.

- `.github/queue-gate-ancestor` — SHA that all queue branches must include as an ancestor.
- `.github/queue-gate-bad-subjects` — Commit subjects that are blocked (one per line).
