Please test exed. Doing so requires operating interactively with SSH, so read
tmux.md for information on how to do so.

When you are done, create a file called "result.txt" with either SUCCESS or FAILURE
in it, and commit. Please also create "report/report.md" with details about what you did
including "screenshots". If testing a web thing, that can be real screenshots. If testing
interactive terminal things, just include the text from the tmux output!

To speed things up in terms of discovering how things work, amend
this file with <hint>...</hint> blocks. For example, if you've discovered
how to build or start something, indicate that in <hint> blocks so future
test runs don't need to rediscover the whole thing.

<hint>
Key Discovery:
- exed runs on port 8080 (HTTP) and 2223 (SSH direct)
- sshpiper runs on port 2222 (SSH proxy) 
- Email verification requires browser interaction at http://localhost:8080/verify-email?token=...
- SSH to containers: `ssh -p 2222 machinename@localhost` (wait ~15s after creation)
- Build times: exed ~30 seconds first time, sshpiper ~10 seconds
- Check logs: `tmux capture-pane -p -t testing:exed` or `testing:sshpiper`
</hint>

Test Setup:

<hint>Use tmux windows: `tmux new-session -d -s testing && tmux new-window -t testing -n exed && tmux new-window -t testing -n sshpiper && tmux new-window -t testing -n client`</hint>

1. Verify that "docker ps" works before you start. (Docker should already
   be running; just verify that it's working.)
   <hint>Docker should already be running in the environment. Do NOT start dockerd manually - this can cause conflicts and system issues. Just verify with `docker ps`.</hint>

2. Build and start exed (in dev mode).
   <hint>Build: `cd /app && make build`. Start: `cd /app && make run-dev` (in exed window). Server starts on :8080 (HTTP) and :2223 (SSH). Look for "SSH server starting" in output.</hint>

3. Build and start sshpiper.
   <hint>Build: `cd sshpiper && go build -o sshpiperd ./cmd/sshpiperd && go build -o metrics ./plugin/metrics && cd ..`. Start: `./sshpiper.sh` (in sshpiper window). Listens on :2222.</hint>

4. Create yourself an ssh key with ssh-keygen.
   <hint>Command: `ssh-keygen -t rsa -b 2048 -f ~/.ssh/id_rsa -N ""`. Creates RSA key needed for authentication.</hint>

Test Steps:

1. Register with exed by running "ssh -p 2222 localhost" 
   <hint>Use: `ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -p 2222 localhost`. Prompts for email and team name.</hint>

The e-mail thing will require doing a web call; use the browser to handle that.
<hint>After entering email/team, get verification URL like http://localhost:8080/verify-email?token=... Copy this URL to browser and click "Confirm Email Verification"</hint>

2. Test the "whoami" command.
   <hint>After email verification, press any key to continue, then type `whoami` at exe.dev prompt. Shows email and SSH key fingerprint.</hint>

3. Test the "list" command (should have no machines yet!)
   <hint>Type `list` at exe.dev prompt. Should show "No machines found. Create one with 'new'."</hint>

4. Create a new container using "ssh -p 2222 localhost"
   <hint>Type `new` at exe.dev prompt. Creates machine with random name like "able-yankee". Shows command like "ssh -p 2222 able-yankee@localhost"</hint>

5. SSH into that container (by using ssh -p 2222 name@localhost) and run "id"
  (docker exec is not the right way to do this)
  <hint>Use `ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -p 2222 machinename@localhost`. Wait ~10-15 seconds after container creation for SSH daemon to be fully ready. If connection fails, check sshpiper logs with `tmux capture-pane -p -t testing:sshpiper` for connectivity issues.</hint>

6. Open up a shell to the new container and check the hostname.
   <hint>In container shell, run `hostname`. Should show "machine-name.team-name.exe.dev"</hint>

7. Use scp with the new container to test that SSH subsystems work.
   <hint>Use `scp -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -P 2222 localfile machinename@localhost:/remote/path`. Ensure SSH connection works first before testing SCP.</hint>
