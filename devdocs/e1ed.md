# e1ed: Fast e1e Tests via Git Push

Run e1e tests on edric by pushing a commit. Tests run against a pre-warmed VM pool.

## Usage

```bash
bin/e1e              # test HEAD
bin/e1e my-branch    # test tip of a branch
bin/e1e abc123       # test a specific commit
bin/e1e-wip          # test your working tree (uncommitted + untracked files)
```

## Output

```
All tests passed.
scp root@edric:/data/e1ed/runs/abc12345-1740000000.jsonl .
```

```
FAIL: TestFoo, TestBar
scp root@edric:/data/e1ed/runs/abc12345-1740000000.jsonl .
```

The scp command copies the full NDJSON test log (same format as `go test -json`).

## Changing e1ed Infra

These scripts test your *exe* code on edric's existing e1ed deployment. If you're
changing e1ed itself (cmd/e1ed, cmd/e1ed-hook, exelet, execore, etc.), you need to
redeploy before your changes take effect:

```bash
./ops/deploy/deploy-e1ed.sh            # redeploy the e1ed service
./ops/deploy/deploy-e1ed-hook.sh       # redeploy the post-receive hook
./ops/deploy/setup-push-repo.sh        # first-time bare repo setup (run on edric)
```

## How It Works

1. `git push` sends objects to `/data/e1ed/push.git` (a bare repo on edric)
2. A `post-receive` hook (`cmd/e1ed-hook`) POSTs `{"commit": "<sha>"}` to the local e1ed service
3. e1ed creates a worktree, claims a VM from the pool, and runs `go test -json`
4. The hook saves the full NDJSON output to `/data/e1ed/runs/` and prints a summary
